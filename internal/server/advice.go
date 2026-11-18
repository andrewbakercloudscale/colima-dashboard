package server

import (
	"context"
	"net/http"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/advice"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/history"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/metrics"
)

// Fix recommendations: GET /api/profiles/{p}/advice.
//
// Nothing here polls. The answer is worked out when a page asks, from what is already known:
// the container list and its cached inspect details, the Monitor's in-memory samples when it
// has been open in the last couple of minutes, and otherwise one docker stats reading per
// running container. It is cached for 30 seconds, so several pages asking cost one run.

const (
	adviceTTL = 30 * time.Second
	// samplesFresh: Monitor samples newer than this stand in for live use.
	samplesFresh = 2 * time.Minute
	// cpuWindow is how far back CPU is averaged; memory peaks look back the same distance for
	// running containers and the whole hour kept for stopped ones.
	cpuWindow = 3 * time.Minute
)

type adviceResponse struct {
	Profile string         `json:"profile"`
	Running bool           `json:"running"`
	Issues  []advice.Issue `json:"issues"`
	// Source says where the usage figures came from: "monitor" (its samples), "snapshot" (one
	// docker stats reading) or "" (none, for example no running containers).
	Source string `json:"source"`
	VMCPUs int    `json:"vmCpus"`
	VMMem  int64  `json:"vmMemory"`
	At     int64  `json:"at"` // unix ms
	Error  string `json:"error,omitempty"`
}

type adviceCache struct {
	run sync.Mutex // one computation at a time; the others wait and read the cache
	mu  sync.Mutex
	m   map[string]adviceResponse
	// raised remembers containers stopped for memory whose limit was raised here since, keyed
	// profile/id, valued with the container's FinishedAt then: the kernel's OOMKilled flag
	// stays set until the container starts again, and the advice should not repeat itself.
	raised map[string]string
	// spots holds each profile's last direct VM reading, the baseline for its CPU average.
	spots map[string]*metrics.Spot
}

func (ac *adviceCache) get(p string, maxAge time.Duration) (adviceResponse, bool) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	r, ok := ac.m[p]
	if !ok || time.Since(time.UnixMilli(r.At)) > maxAge {
		return adviceResponse{}, false
	}
	return r, true
}

func (ac *adviceCache) put(p string, r adviceResponse) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	if ac.m == nil {
		ac.m = map[string]adviceResponse{}
	}
	ac.m[p] = r
}

func (ac *adviceCache) drop(p string) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	delete(ac.m, p)
}

func (ac *adviceCache) markRaised(key, finishedAt string) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	if ac.raised == nil {
		ac.raised = map[string]string{}
	}
	ac.raised[key] = finishedAt
}

func (ac *adviceCache) wasRaised(key, finishedAt string) bool {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	v, ok := ac.raised[key]
	return ok && v == finishedAt
}

var macMemOnce = sync.OnceValue(func() int64 {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return n
})

func (s *Server) apiAdvice(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("p")
	if _, err := s.dockerFor(p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	maxAge := adviceTTL
	if r.URL.Query().Get("fresh") == "1" {
		maxAge = 3 * time.Second
	}
	if res, ok := s.advice.get(p, maxAge); ok {
		writeJSON(w, http.StatusOK, res)
		return
	}
	s.advice.run.Lock()
	defer s.advice.run.Unlock()
	if res, ok := s.advice.get(p, maxAge); ok {
		writeJSON(w, http.StatusOK, res)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res := s.computeAdvice(ctx, p)
	if res.Error == "" {
		s.advice.put(p, res)
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) computeAdvice(ctx context.Context, p string) adviceResponse {
	now := time.Now()
	res := adviceResponse{Profile: p, Issues: []advice.Issue{}, At: now.UnixMilli()}
	st, err := s.opt.Colima.Status(ctx, p)
	if err != nil {
		return res // not running: nothing to advise
	}
	res.Running, res.VMCPUs, res.VMMem = true, st.CPU, st.Memory
	in := advice.Input{
		Profile: p, Now: now,
		VM:  advice.VM{CPUs: st.CPU, Memory: st.Memory},
		Mac: advice.Mac{CPUs: runtime.NumCPU(), Memory: macMemOnce()},
	}
	dc, _ := s.dockerFor(p)
	var cs []docker.Container
	if st.Runtime == "docker" {
		cs, err = dc.Containers(ctx)
		if err != nil {
			res.Error = err.Error()
			return res
		}
	}
	details := s.containerDetails(ctx, p, dc, cs)
	samples := s.opt.Metrics.Window(p, now.Add(-time.Hour))
	use := usageFromSamples(samples, st.CPU, now)
	if use.vm != nil {
		in.VM.Sampled, in.VM.CPUPct, in.VM.MemUsed, in.VM.MemTotal, in.VM.Window = true, use.vm.cpuPct, use.vm.memUsed, use.vm.memTotal, use.vm.window
	} else if v := s.vmSpotUse(ctx, p, now); v != nil {
		in.VM.Sampled, in.VM.CPUPct, in.VM.MemUsed, in.VM.MemTotal, in.VM.Window = true, v.cpuPct, v.memUsed, v.memTotal, v.window
	}
	snap := map[string]*advice.Usage{}
	if !use.fresh {
		snap = s.snapshotUsage(ctx, dc, cs)
	}
	for i, c := range cs {
		d := details[i]
		ac := advice.Container{
			ID: c.ID, Name: strings.TrimPrefix(firstName(c.Names), "/"), Image: c.Image, State: c.State,
			Project: c.Labels["com.docker.compose.project"], Service: c.Labels["com.docker.compose.service"],
		}
		if ac.Name == "" {
			ac.Name = c.ID[:min(12, len(c.ID))]
		}
		if d != nil {
			ac.OOMKilled, ac.ExitCode, ac.MemLimit, ac.CPUs = d.OOMKilled, d.ExitCode, d.Memory, d.CPUs()
			if t, err := time.Parse(time.RFC3339Nano, d.FinishedAt); err == nil && t.Year() > 1 {
				ac.FinishedAt = t
			}
			if ac.OOMKilled && c.State != "running" && s.advice.wasRaised(p+"/"+c.ID, d.FinishedAt) {
				ac.OOMKilled, ac.OOMHandled = false, true // raised here since it stopped; it shows again if it runs out again
			}
		}
		switch {
		case c.State == "running" && use.fresh:
			ac.Usage = use.running[c.ID]
		case c.State == "running":
			ac.Usage = snap[c.ID]
		case use.peak[c.ID] > 0:
			ac.Usage = &advice.Usage{MemPeak: use.peak[c.ID]}
		}
		in.Containers = append(in.Containers, ac)
	}
	switch {
	case use.fresh:
		res.Source = "monitor"
	case len(snap) > 0:
		res.Source = "snapshot"
	}
	if s.opt.History != nil {
		in.JobOOMs = jobOOMs(s.opt.History.Store.Runs(p, now.Add(-advice.JobDays*24*time.Hour)))
	}
	res.Issues = advice.Analyse(in)
	return res
}

// spotKeep is how long an earlier VM reading stays the baseline for the CPU average.
const spotKeep = 15 * time.Minute

// vmSpotUse reads the VM's memory and CPU counters directly when the Monitor has no recent
// samples, so VM warnings show without the Monitor open. Memory is the reading now; CPU is the
// average since an earlier reading a minute or more ago (none on the first call).
func (s *Server) vmSpotUse(ctx context.Context, p string, now time.Time) *vmUse {
	if s.opt.Colima == nil {
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cur, err := metrics.ReadSpot(rctx, s.opt.Colima.Bin, p)
	if err != nil || cur.MemTotal == 0 {
		return nil
	}
	v := &vmUse{memUsed: int64(cur.MemUsed()), memTotal: int64(cur.MemTotal)}
	s.advice.mu.Lock()
	defer s.advice.mu.Unlock()
	if s.advice.spots == nil {
		s.advice.spots = map[string]*metrics.Spot{}
	}
	prev := s.advice.spots[p]
	age := now.Sub(func() time.Time {
		if prev == nil {
			return now
		}
		return prev.T
	}())
	switch {
	case prev == nil || age > spotKeep:
		s.advice.spots[p] = cur
	case age >= advice.MinWindow:
		if pct, ok := cur.CPUSince(prev); ok {
			v.cpuPct, v.window = pct, age
		}
		s.advice.spots[p] = cur
	}
	return v
}

type vmUse struct {
	cpuPct            float64
	memUsed, memTotal int64
	window            time.Duration
}

type sampledUse struct {
	fresh   bool
	vm      *vmUse
	running map[string]*advice.Usage // from the last few minutes, only when fresh
	peak    map[string]int64         // highest memory per container over the hour kept
}

// usageFromSamples turns the Monitor's samples into per-container use and the VM's averages.
// Container CPU in a sample is a percent of the whole VM; it becomes CPUs (cores) here.
func usageFromSamples(samples []metrics.Sample, vmCPUs int, now time.Time) sampledUse {
	out := sampledUse{running: map[string]*advice.Usage{}, peak: map[string]int64{}}
	for _, s := range samples {
		for id, c := range s.Containers {
			if int64(c.Memory) > out.peak[id] {
				out.peak[id] = int64(c.Memory)
			}
		}
	}
	if len(samples) == 0 {
		return out
	}
	last := samples[len(samples)-1]
	if now.Sub(time.UnixMilli(last.T)) > samplesFresh {
		return out
	}
	out.fresh = true
	from := last.T - cpuWindow.Milliseconds()
	type acc struct {
		cpu         float64
		n           int
		first, last int64
		memPeak     int64
	}
	accs := map[string]*acc{}
	var vmCPU, vmMem float64
	var vmN, vmMemN int
	var vmFirst, vmLast int64
	var memTotal int64
	for _, s := range samples {
		if s.T < from {
			continue
		}
		cpus := vmCPUs
		if s.Guest != nil {
			if s.Guest.CPUs > 0 {
				cpus = s.Guest.CPUs
			}
			if vmN == 0 {
				vmFirst = s.T
			}
			vmLast = s.T
			vmCPU += s.Guest.CPU
			vmN++
			if last.T-s.T <= 60_000 {
				vmMem += float64(s.Guest.MemUsed)
				vmMemN++
				memTotal = int64(s.Guest.MemTotal)
			}
		}
		for id, c := range s.Containers {
			a := accs[id]
			if a == nil {
				a = &acc{first: s.T}
				accs[id] = a
			}
			a.cpu += c.CPU / 100 * float64(cpus)
			a.n++
			a.last = s.T
			a.memPeak = max(a.memPeak, int64(c.Memory))
		}
	}
	step := metrics.Interval.Milliseconds()
	for id, a := range accs {
		c, ok := last.Containers[id]
		if !ok {
			continue // not running any more
		}
		out.running[id] = &advice.Usage{
			MemNow: int64(c.Memory), MemPeak: a.memPeak, CPU: a.cpu / float64(a.n),
			Window: time.Duration(a.last-a.first+step) * time.Millisecond,
		}
	}
	if vmN > 0 && vmMemN > 0 {
		out.vm = &vmUse{cpuPct: vmCPU / float64(vmN), memUsed: int64(vmMem / float64(vmMemN)), memTotal: memTotal,
			window: time.Duration(vmLast-vmFirst+step) * time.Millisecond}
	}
	return out
}

// jobOOMs groups the runs that ended out of memory by job, the most recent first.
func jobOOMs(runs []history.Run) []advice.JobOOM {
	idx := map[string]int{}
	var out []advice.JobOOM
	for _, r := range runs {
		if r.Outcome != "oom" || r.Job == "" {
			continue
		}
		i, ok := idx[r.Job]
		if !ok {
			i = len(out)
			idx[r.Job] = i
			out = append(out, advice.JobOOM{Job: r.Job})
		}
		j := &out[i]
		j.Count++
		if !r.T.Before(j.Last) {
			j.Last, j.Name, j.Image, j.Project, j.Service = r.T, r.Name, r.Image, r.Project, r.Service
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Last.After(out[b].Last) })
	return out
}

// snapshotUsage takes one docker stats reading per running container, four at a time.
func (s *Server) snapshotUsage(ctx context.Context, dc *docker.Client, cs []docker.Container) map[string]*advice.Usage {
	out := map[string]*advice.Usage{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	for _, c := range cs {
		if c.State != "running" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			st, err := dc.Stats(sctx, c.ID)
			if err != nil {
				return
			}
			mu.Lock()
			out[c.ID] = &advice.Usage{MemNow: int64(st.MemoryBytes), MemPeak: int64(st.MemoryBytes), CPU: st.CPUPercent / 100, Snapshot: true}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}
