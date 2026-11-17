package history

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
)

// How often the recorder looks for running profiles, samples them, and reads build history.
const (
	scanEvery   = 30 * time.Second
	pointEvery  = 5 * time.Minute
	dfEvery     = 15 * time.Minute
	buildsEvery = 2 * time.Minute
)

// GuestAverages, when set, returns the VM's own CPU % and memory use averaged since a time, if
// the live Monitor sampled them (it only samples while someone is watching).
type GuestAverages func(profile string, since time.Time) (cpu float64, mem uint64, ok bool)

// Recorder watches every running Colima profile with the Docker runtime and writes to a Store.
// It costs one open event stream per profile and a few cheap reads every few minutes.
type Recorder struct {
	Store      *Store
	ColimaHome string
	Guest      GuestAverages
	Logf       func(format string, a ...any)
	// OnRun and OnBuild, when set, hear about each run and each build as they are recorded
	// (builds finished before the recorder started are stored but not passed on).
	OnRun   func(profile string, run Run)
	OnBuild func(profile string, b Build)

	mu       sync.Mutex
	watching map[string]context.CancelFunc
	buildsAt map[string]time.Time
	seenRefs map[string]map[string]bool
	buildErr map[string]string
	began    time.Time
}

// Run watches until ctx ends.
func (r *Recorder) Run(ctx context.Context) {
	r.mu.Lock()
	r.watching = map[string]context.CancelFunc{}
	r.buildsAt = map[string]time.Time{}
	r.seenRefs = map[string]map[string]bool{}
	r.buildErr = map[string]string{}
	r.began = time.Now()
	r.mu.Unlock()
	r.Store.Prune(time.Now())
	scan := time.NewTicker(scanEvery)
	defer scan.Stop()
	lastPrune := time.Now()
	for {
		r.scan(ctx)
		select {
		case <-ctx.Done():
			return
		case <-scan.C:
		}
		if time.Since(lastPrune) > 24*time.Hour {
			r.Store.Prune(time.Now())
			lastPrune = time.Now()
		}
	}
}

func (r *Recorder) logf(format string, a ...any) {
	if r.Logf != nil {
		r.Logf(format, a...)
	}
}

// scan starts a watcher for each newly running profile and stops those whose engine went away.
func (r *Recorder) scan(ctx context.Context) {
	running := map[string]bool{}
	entries, _ := os.ReadDir(r.ColimaHome)
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, "_") || !colima.ValidProfile(name) {
			continue
		}
		sock := filepath.Join(r.ColimaHome, name, "docker.sock")
		if _, err := os.Stat(sock); err != nil {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := docker.New(sock).Ping(pctx)
		cancel()
		if err == nil {
			running[name] = true
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, stop := range r.watching {
		if !running[name] {
			stop()
			delete(r.watching, name)
		}
	}
	for name := range running {
		if _, ok := r.watching[name]; !ok {
			wctx, cancel := context.WithCancel(ctx)
			r.watching[name] = cancel
			go r.watch(wctx, name)
		}
	}
}

// Watching lists the profiles being recorded now.
func (r *Recorder) Watching() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for name := range r.watching {
		out = append(out, name)
	}
	return out
}

func (r *Recorder) watch(ctx context.Context, profile string) {
	dc := docker.New(filepath.Join(r.ColimaHome, profile, "docker.sock"))
	go r.events(ctx, profile, dc)
	go r.points(ctx, profile, dc)
	r.PollBuilds(ctx, profile)
	t := time.NewTicker(buildsEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.PollBuilds(ctx, profile)
		}
	}
}

// ---------- container runs, from Docker's event stream ----------

type event struct {
	Type     string `json:"Type"`
	Action   string `json:"Action"`
	TimeNano int64  `json:"timeNano"`
	Actor    struct {
		ID         string            `json:"ID"`
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
}

// tracker turns a container's events into Run records.
type tracker struct {
	live map[string]*liveRun
	// inspect finds when a container started, for one already running when watching began.
	inspect func(id string) time.Time
}

type liveRun struct {
	started   time.Time
	stopped   bool // a kill or stop came first: someone stopped it
	oom       bool
	unhealthy bool
	runs      int // starts seen for this container
}

func newTracker(inspect func(string) time.Time) *tracker {
	return &tracker{live: map[string]*liveRun{}, inspect: inspect}
}

// handle returns a finished Run when ev ends one.
func (t *tracker) handle(ev event) (Run, bool) {
	if ev.Type != "container" {
		return Run{}, false
	}
	id := ev.Actor.ID
	at := time.Unix(0, ev.TimeNano)
	lr := t.live[id]
	if lr == nil {
		lr = &liveRun{}
		t.live[id] = lr
	}
	switch action := ev.Action; {
	case action == "start":
		lr.started, lr.stopped, lr.oom, lr.unhealthy = at, false, false, false
		lr.runs++
	case action == "kill" || action == "stop":
		lr.stopped = true
	case action == "oom":
		lr.oom = true
	case strings.HasPrefix(action, "health_status") && strings.Contains(action, "unhealthy"):
		lr.unhealthy = true
	case action == "destroy":
		delete(t.live, id)
	case action == "die":
		a := ev.Actor.Attributes
		exit, _ := strconv.Atoi(a["exitCode"])
		started := lr.started
		if started.IsZero() && t.inspect != nil {
			started = t.inspect(id)
		}
		run := Run{T: at, Started: started, ID: short(id), Name: a["name"], Image: a["image"],
			Project: a["com.docker.compose.project"], Service: a["com.docker.compose.service"],
			Exit: exit, Restarted: lr.runs > 1, Unhealthy: lr.unhealthy}
		if !started.IsZero() {
			run.Seconds = at.Sub(started).Seconds()
		}
		switch {
		case lr.oom || exit == 137 && !lr.stopped:
			run.Outcome = "oom"
		case lr.stopped:
			run.Outcome = "stopped"
		case exit == 0:
			run.Outcome = "pass"
		default:
			run.Outcome = "fail"
		}
		run.Job = jobOf(run)
		lr.started, lr.stopped, lr.oom, lr.unhealthy = time.Time{}, false, false, false
		return run, true
	}
	return Run{}, false
}

// jobOf is what runs are grouped by: the Compose service, else the image without its digest.
func jobOf(r Run) string {
	if r.Project != "" && r.Service != "" {
		return r.Project + "/" + r.Service
	}
	img := r.Image
	if i := strings.Index(img, "@"); i > 0 {
		img = img[:i]
	}
	if img == "" || strings.HasPrefix(img, "sha256:") {
		return r.Name
	}
	return img
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func (r *Recorder) events(ctx context.Context, profile string, dc *docker.Client) {
	inspect := func(id string) time.Time {
		ictx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		doc, err := dc.Inspect(ictx, id)
		if err != nil {
			return time.Time{}
		}
		st, _ := doc["State"].(map[string]any)
		s, _ := st["StartedAt"].(string)
		at, _ := time.Parse(time.RFC3339Nano, s)
		return at
	}
	tr := newTracker(inspect)
	filters, _ := json.Marshal(map[string][]string{"type": {"container"}})
	for backoff := time.Second; ctx.Err() == nil; {
		body, err := dc.Raw(ctx, "/events", url.Values{"filters": {string(filters)}})
		if err == nil {
			backoff = time.Second
			sc := bufio.NewScanner(body)
			sc.Buffer(make([]byte, 64<<10), 1<<20)
			for sc.Scan() {
				var ev event
				if json.Unmarshal(sc.Bytes(), &ev) != nil {
					continue
				}
				if run, ok := tr.handle(ev); ok {
					if err := r.Store.AddRun(profile, run); err != nil {
						r.logf("history: %v", err)
					}
					if r.OnRun != nil {
						r.OnRun(profile, run)
					}
				}
			}
			body.Close()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

// ---------- points: cost and disk every five minutes ----------

func (r *Recorder) points(ctx context.Context, profile string, dc *docker.Client) {
	hr := &hostCPU{dir: filepath.Join(r.ColimaHome, "_lima", colima.LimaInstance(profile))}
	hr.sample() // first reading: the CPU figure needs two
	var lastDF time.Time
	t := time.NewTicker(pointEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		p := Point{T: time.Now()}
		if cpu, mem, ok := hr.sample(); ok {
			p.HostCPU, p.HostMem = &cpu, mem
		}
		if r.Guest != nil {
			if cpu, mem, ok := r.Guest(profile, p.T.Add(-pointEvery)); ok {
				p.GuestCPU, p.GuestMem = &cpu, mem
			}
		}
		p.DiskMac, p.DiskSize = diskImage(r.ColimaHome, profile)
		qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if cs, err := dc.Containers(qctx); err == nil {
			for _, c := range cs {
				if c.State == "running" {
					p.Running++
				}
			}
		}
		if time.Since(lastDF) >= dfEvery {
			if du, err := dc.DiskUsage(qctx); err == nil {
				lastDF = time.Now()
				for _, s := range du.Summary {
					v := s.Size
					switch s.Type {
					case "Images":
						p.Images = &v
					case "Containers":
						p.Contain = &v
					case "Local Volumes", "Volumes":
						p.Volumes = &v
					case "Build Cache":
						p.Cache = &v
					}
				}
			}
		}
		cancel()
		if err := r.Store.AddPoint(profile, p); err != nil {
			r.logf("history: %v", err)
		}
	}
}

// diskImage reports how much of the Mac the VM's data disk takes, and its size.
func diskImage(colimaHome, profile string) (onMac, size uint64) {
	for _, name := range []string{"datadisk", "diffdisk"} {
		var st syscall.Stat_t
		p := filepath.Join(colimaHome, "_lima", "_disks", colima.LimaInstance(profile), name)
		if syscall.Stat(p, &st) == nil {
			return uint64(st.Blocks) * 512, uint64(st.Size)
		}
	}
	return 0, 0
}

// hostCPU measures the VM's processes on the Mac between two readings.
type hostCPU struct {
	dir   string
	lastS float64
	lastT time.Time
}

func (h *hostCPU) sample() (cpu float64, mem uint64, ok bool) {
	var pids []string
	if b, err := os.ReadFile(filepath.Join(h.dir, "ha.pid")); err == nil {
		pids = append(pids, strings.TrimSpace(string(b)))
	}
	if out, err := exec.Command("pgrep", "-f", "com.apple.Virtualization.VirtualMachine").Output(); err == nil {
		pids = append(pids, strings.Fields(string(out))...)
	}
	if len(pids) == 0 {
		return 0, 0, false
	}
	out, err := exec.Command("ps", "-o", "time=", "-p", strings.Join(pids, ",")).Output()
	if err != nil {
		return 0, 0, false
	}
	var secs float64
	for _, f := range strings.Fields(string(out)) {
		secs += cpuTime(f)
	}
	now := time.Now()
	first := h.lastT.IsZero()
	if !first && secs >= h.lastS {
		cpu = (secs - h.lastS) / now.Sub(h.lastT).Seconds() * 100
	}
	h.lastS, h.lastT = secs, now
	args := []string{"-f", "bytes", "--noCategories"}
	for _, p := range pids {
		args = append(args, "-p", p)
	}
	if fp, err := exec.Command("footprint", args...).Output(); err == nil {
		for _, line := range strings.Split(string(fp), "\n") {
			if i := strings.Index(line, "phys_footprint:"); i >= 0 {
				v, _ := strconv.ParseUint(strings.Fields(line[i+len("phys_footprint:"):])[0], 10, 64)
				mem += v
			}
		}
	}
	return cpu, mem, !first
}

// cpuTime reads ps TIME: [[dd-]hh:]mm:ss.cc
func cpuTime(s string) float64 {
	days := 0.0
	if i := strings.IndexByte(s, '-'); i >= 0 {
		d, _ := strconv.ParseFloat(s[:i], 64)
		days, s = d, s[i+1:]
	}
	var total float64
	for _, part := range strings.Split(s, ":") {
		v, _ := strconv.ParseFloat(strings.Replace(part, ",", ".", 1), 64)
		total = total*60 + v
	}
	return total + days*86400
}

// ---------- builds, from BuildKit's history through docker buildx ----------

// ErrNoBuildx means docker buildx is not installed, so builds cannot be read.
var ErrNoBuildx = errors.New("docker buildx is not installed")

// BuildError says why builds could not be read for a profile, if they could not.
func (r *Recorder) BuildError(profile string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buildErr[profile]
}

// PollBuilds reads BuildKit's history for profile and stores builds not seen before. It runs at
// most every 10 seconds per profile, so the page may call it when opened.
func (r *Recorder) PollBuilds(ctx context.Context, profile string) {
	r.mu.Lock()
	if r.buildsAt == nil {
		r.buildsAt, r.seenRefs, r.buildErr = map[string]time.Time{}, map[string]map[string]bool{}, map[string]string{}
	}
	if time.Since(r.buildsAt[profile]) < 10*time.Second {
		r.mu.Unlock()
		return
	}
	r.buildsAt[profile] = time.Now()
	seen := r.seenRefs[profile]
	r.mu.Unlock()
	if seen == nil {
		seen = map[string]bool{}
		for _, b := range r.Store.Builds(profile, time.Time{}) {
			seen[b.Ref] = true
		}
	}
	builds, err := readBuilds(ctx, filepath.Join(r.ColimaHome, profile, "docker.sock"))
	for _, b := range builds {
		if seen[b.Ref] || b.T.IsZero() {
			continue // already stored, or still running
		}
		seen[b.Ref] = true
		if b.Status == "Error" && b.Error == "" {
			b.Error = buildError(ctx, filepath.Join(r.ColimaHome, profile, "docker.sock"), b.Ref)
		}
		if err := r.Store.AddBuild(profile, b); err != nil {
			r.logf("history: %v", err)
		}
		r.mu.Lock()
		began := r.began
		r.mu.Unlock()
		if r.OnBuild != nil && b.T.After(began) {
			r.OnBuild(profile, b)
		}
	}
	r.mu.Lock()
	r.seenRefs[profile] = seen
	if err != nil {
		r.buildErr[profile] = err.Error()
	} else {
		delete(r.buildErr, profile)
	}
	r.mu.Unlock()
}

func readBuilds(ctx context.Context, sock string) ([]Build, error) {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return nil, errors.New("the docker CLI is not installed")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := buildxCmd(ctx, bin, sock, "history", "ls", "--format", "json").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && strings.Contains(string(ee.Stderr), "unknown command") {
			return nil, ErrNoBuildx
		}
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, errors.New(strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	return parseBuilds(out), nil
}

// buildxCmd runs docker buildx against the profile's engine. It goes through Colima's docker
// context when there is one ("colima", or "colima-<profile>"): buildx keeps each build's name
// (the image or folder) in local state filed under the builder, and that builder is named after
// the context. Through a bare DOCKER_HOST the builder is "default" and every name comes back empty.
func buildxCmd(ctx context.Context, bin, sock string, args ...string) *exec.Cmd {
	name := contextName(filepath.Base(filepath.Dir(sock)))
	if exec.CommandContext(ctx, bin, "context", "inspect", name).Run() == nil {
		return exec.CommandContext(ctx, bin, append([]string{"--context", name, "buildx"}, args...)...)
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"buildx"}, args...)...)
	cmd.Env = append(os.Environ(), "DOCKER_HOST=unix://"+sock)
	return cmd
}

// contextName is the docker context Colima creates for a profile.
func contextName(profile string) string {
	if profile == "default" || profile == "" {
		return "colima"
	}
	return "colima-" + profile
}

// buildError asks BuildKit why a build failed: the failing step and its message, or "" when
// buildx cannot say.
func buildError(ctx context.Context, sock, ref string) string {
	bin, err := exec.LookPath("docker")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := buildxCmd(ctx, bin, sock, "history", "inspect", ref, "--format", "json").Output()
	if err != nil {
		return ""
	}
	return parseBuildError(out)
}

func parseBuildError(out []byte) string {
	var v struct {
		Error *struct{ Name, Message string }
	}
	if json.Unmarshal(out, &v) != nil || v.Error == nil {
		return ""
	}
	msg := strings.TrimSpace(v.Error.Message)
	if n := strings.TrimSpace(v.Error.Name); n != "" {
		msg = n + ": " + msg
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}

// parseBuilds reads `docker buildx history ls --format json`: one object per line (newer buildx)
// or one array. Field names have varied between buildx versions, so several are tried.
func parseBuilds(out []byte) []Build {
	var rows []map[string]any
	trim := strings.TrimSpace(string(out))
	if strings.HasPrefix(trim, "[") {
		_ = json.Unmarshal([]byte(trim), &rows)
	} else {
		for _, line := range strings.Split(trim, "\n") {
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) == nil {
				rows = append(rows, m)
			}
		}
	}
	str := func(m map[string]any, keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	num := func(m map[string]any, keys ...string) int {
		for _, k := range keys {
			if v, ok := m[k].(float64); ok {
				return int(v)
			}
		}
		return 0
	}
	tm := func(m map[string]any, keys ...string) time.Time {
		t, _ := time.Parse(time.RFC3339Nano, str(m, keys...))
		return t
	}
	var builds []Build
	for _, m := range rows {
		b := Build{
			Ref:     str(m, "ref", "Ref", "ID", "id"),
			Name:    str(m, "name", "Name"),
			Status:  str(m, "status", "Status"),
			Started: tm(m, "created_at", "CreatedAt", "createdAt"),
			T:       tm(m, "completed_at", "CompletedAt", "completedAt"),
			Steps:   num(m, "total_steps", "TotalSteps", "totalSteps"),
			Cached:  num(m, "cached_steps", "CachedSteps", "cachedSteps"),
			Error:   str(m, "error", "Error"),
		}
		if b.Ref == "" {
			continue
		}
		if i := strings.LastIndex(b.Ref, "/"); i >= 0 { // builder/node/ref: keep the ref
			b.Ref = b.Ref[i+1:]
		}
		if !b.T.IsZero() && !b.Started.IsZero() {
			b.Seconds = b.T.Sub(b.Started).Seconds()
		}
		builds = append(builds, b)
	}
	return builds
}
