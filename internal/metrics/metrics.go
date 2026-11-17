// Package metrics samples a running Colima profile every two seconds: what the VM costs the Mac,
// what is happening inside the VM, and what each container uses.
//
// Sampling only runs while someone is looking. A collector starts on the first request for a
// profile and stops after five minutes without one, so an idle Colima Dashboard costs nothing. History
// (up to an hour) is kept in memory across those pauses and shows the gap honestly.
package metrics

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
)

// Interval between samples.
const Interval = 2 * time.Second

const (
	historyLen = int(time.Hour / Interval)
	idleStop   = 5 * time.Minute
)

// Host is what the VM costs the Mac.
type Host struct {
	CPU    float64 `json:"cpu"`    // percent of one Mac core
	Memory uint64  `json:"memory"` // physical footprint, bytes (what Activity Monitor shows)
	VMs    int     `json:"vms"`    // VM processes counted (more than one: other profiles share the figure)
}

// Guest is the view from inside the VM.
type Guest struct {
	CPU       float64 `json:"cpu"` // percent of all vCPUs
	CPUs      int     `json:"cpus"`
	MemUsed   uint64  `json:"memUsed"`
	MemTotal  uint64  `json:"memTotal"`
	Load1     float64 `json:"load1"`
	NetRx     float64 `json:"netRx"` // bytes per second, VM to outside world
	NetTx     float64 `json:"netTx"`
	DiskRead  float64 `json:"diskRead"` // bytes per second
	DiskWrite float64 `json:"diskWrite"`
	DiskUsed  uint64  `json:"diskUsed"` // Docker's data disk
	DiskTotal uint64  `json:"diskTotal"`
}

// Container is one container's share.
type Container struct {
	CPU       float64 `json:"cpu"` // percent of the whole VM (all vCPUs), so containers stack to Guest.CPU
	Memory    uint64  `json:"memory"`
	NetRx     float64 `json:"netRx"`
	NetTx     float64 `json:"netTx"`
	DiskRead  float64 `json:"diskRead"`
	DiskWrite float64 `json:"diskWrite"`
}

// Sample is one point in time. A nil part could not be measured for that tick.
type Sample struct {
	T          int64                `json:"t"` // unix milliseconds
	Host       *Host                `json:"host,omitempty"`
	Guest      *Guest               `json:"guest,omitempty"`
	Containers map[string]Container `json:"containers"`
}

// Series is what the API returns.
type Series struct {
	Profile    string            `json:"profile"`
	IntervalMs int64             `json:"intervalMs"`
	Samples    []Sample          `json:"samples"`
	Names      map[string]string `json:"names"` // container id -> name
	Images     map[string]string `json:"images"`
	Error      string            `json:"error,omitempty"`
}

// Hub owns one collector per profile.
type Hub struct {
	ColimaBin  string
	ColimaHome string

	mu   sync.Mutex
	cols map[string]*collector
}

// NewHub returns an empty hub.
func NewHub(colimaBin, colimaHome string) *Hub {
	return &Hub{ColimaBin: colimaBin, ColimaHome: colimaHome, cols: map[string]*collector{}}
}

// Get returns samples newer than since (unix ms) and marks the profile as watched.
func (h *Hub) Get(profile string, since int64) Series {
	h.mu.Lock()
	c := h.cols[profile]
	if c == nil {
		c = &collector{hub: h, profile: profile, names: map[string]string{}, images: map[string]string{}}
		h.cols[profile] = c
	}
	h.mu.Unlock()
	c.touch()
	return c.series(since)
}

type collector struct {
	hub     *Hub
	profile string

	mu      sync.Mutex
	samples []Sample
	names   map[string]string
	images  map[string]string
	lastErr string
	seen    time.Time
	running bool
}

func (c *collector) touch() {
	c.mu.Lock()
	c.seen = time.Now()
	start := !c.running
	c.running = true
	c.mu.Unlock()
	if start {
		go c.run()
	}
}

func (c *collector) series(since int64) Series {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := Series{Profile: c.profile, IntervalMs: Interval.Milliseconds(), Error: c.lastErr,
		Names: map[string]string{}, Images: map[string]string{}}
	for _, s := range c.samples {
		if s.T > since {
			out.Samples = append(out.Samples, s)
		}
	}
	for k, v := range c.names {
		out.Names[k] = v
	}
	for k, v := range c.images {
		out.Images[k] = v
	}
	if out.Samples == nil {
		out.Samples = []Sample{}
	}
	return out
}

func (c *collector) setErr(err error) {
	c.mu.Lock()
	if err == nil {
		c.lastErr = ""
	} else {
		c.lastErr = err.Error()
	}
	c.mu.Unlock()
}

func (c *collector) idle() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.seen) > idleStop {
		c.running = false
		return true
	}
	return false
}

func (c *collector) run() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	guest := newGuestReader(c.hub.ColimaBin, c.profile)
	go guest.run(ctx)
	host := &hostReader{dir: filepath.Join(c.hub.ColimaHome, "_lima", colima.LimaInstance(c.profile))}
	conts := newContainerReader(docker.New(filepath.Join(c.hub.ColimaHome, c.profile, "docker.sock")))
	go conts.run(ctx)

	tick := time.NewTicker(Interval)
	defer tick.Stop()
	for range tick.C {
		if c.idle() {
			return
		}
		s := Sample{T: time.Now().UnixMilli(), Containers: map[string]Container{}}
		s.Host = host.sample()
		var gerr error
		s.Guest, gerr = guest.latest()
		names, images, stats := conts.latest()
		for id, st := range stats {
			if s.Guest != nil && s.Guest.CPUs > 0 {
				st.CPU /= float64(s.Guest.CPUs)
			}
			s.Containers[id] = st
		}
		c.mu.Lock()
		c.samples = append(c.samples, s)
		if len(c.samples) > historyLen {
			c.samples = c.samples[len(c.samples)-historyLen:]
		}
		for id, n := range names {
			c.names[id] = n
		}
		for id, n := range images {
			c.images[id] = n
		}
		c.mu.Unlock()
		c.setErr(gerr)
	}
}

// ---------- host: the VM process on the Mac ----------

type hostReader struct {
	dir     string
	lastCPU float64
	lastT   time.Time
}

func (h *hostReader) pids() []string {
	var pids []string
	if b, err := os.ReadFile(filepath.Join(h.dir, "ha.pid")); err == nil {
		pids = append(pids, strings.TrimSpace(string(b)))
	}
	// The VZ guest itself runs in an XPC service owned by launchd, not by Colima's process tree.
	if out, err := exec.Command("pgrep", "-f", "com.apple.Virtualization.VirtualMachine").Output(); err == nil {
		pids = append(pids, strings.Fields(string(out))...)
	}
	return pids
}

func (h *hostReader) sample() *Host {
	pids := h.pids()
	if len(pids) == 0 {
		return nil
	}
	out, err := exec.Command("ps", "-o", "time=", "-p", strings.Join(pids, ",")).Output()
	if err != nil {
		return nil
	}
	var cpu float64
	for _, f := range strings.Fields(string(out)) {
		cpu += parseCPUTime(f)
	}
	now := time.Now()
	res := &Host{VMs: len(pids) - 1}
	if !h.lastT.IsZero() && cpu >= h.lastCPU {
		res.CPU = (cpu - h.lastCPU) / now.Sub(h.lastT).Seconds() * 100
	}
	h.lastCPU, h.lastT = cpu, now
	args := []string{"-f", "bytes", "--noCategories"}
	for _, p := range pids {
		args = append(args, "-p", p)
	}
	if fp, err := exec.Command("footprint", args...).Output(); err == nil {
		res.Memory = sumFootprint(fp)
	}
	return res
}

// parseCPUTime reads ps TIME: [[dd-]hh:]mm:ss.cc
func parseCPUTime(s string) float64 {
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

var footprintRe = regexp.MustCompile(`phys_footprint:\s+(\d+)`)

func sumFootprint(out []byte) uint64 {
	var sum uint64
	for _, m := range footprintRe.FindAllSubmatch(out, -1) {
		v, _ := strconv.ParseUint(string(m[1]), 10, 64)
		sum += v
	}
	return sum
}

// ---------- guest: /proc inside the VM over one long-lived ssh session ----------

// The script prints one block every two seconds. One ssh session for the whole time the page is
// open: a new `colima ssh` per sample would cost more CPU than everything it measures.
const guestScript = `while :; do echo @@; grep '^cpu' /proc/stat; grep -E '^(MemTotal|MemAvailable):' /proc/meminfo; ` +
	`cat /proc/loadavg; cat /proc/net/dev; grep -E ' (vd[a-z]|sd[a-z]|nvme[0-9]+n[0-9]+) ' /proc/diskstats; ` +
	`echo df $(df -B1 --output=size,used /var/lib/docker 2>/dev/null | tail -1); sleep 2; done`

type guestReader struct {
	bin, profile string

	mu   sync.Mutex
	cur  *Guest
	at   time.Time
	err  error
	prev *rawGuest
}

type rawGuest struct {
	t                 time.Time
	busy, total       float64
	cpus              int
	memTotal, memAvl  uint64
	load1             float64
	rx, tx            uint64
	rd, wr            uint64
	diskSize, diskUse uint64
}

func newGuestReader(bin, profile string) *guestReader {
	return &guestReader{bin: bin, profile: profile}
}

func (g *guestReader) run(ctx context.Context) {
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, g.bin, "ssh", "-p", g.profile, "--", "sh", "-c", guestScript)
		out, err := cmd.StdoutPipe()
		if err == nil {
			err = cmd.Start()
		}
		if err != nil {
			g.fail(err)
		} else {
			g.read(out)
			err = cmd.Wait()
			if ctx.Err() == nil {
				g.fail(errorf("VM metrics stream ended: %v", err))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (g *guestReader) fail(err error) {
	g.mu.Lock()
	g.err = err
	g.cur = nil
	g.prev = nil
	g.mu.Unlock()
}

func (g *guestReader) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	var block []string
	for sc.Scan() {
		line := sc.Text()
		if line == "@@" {
			if len(block) > 0 {
				g.update(parseGuest(block))
			}
			block = block[:0]
			continue
		}
		block = append(block, line)
	}
}

func (g *guestReader) update(raw *rawGuest) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.err = nil
	prev := g.prev
	g.prev = raw
	if prev == nil {
		return
	}
	dt := raw.t.Sub(prev.t).Seconds()
	if dt <= 0 {
		return
	}
	cur := &Guest{
		CPUs: raw.cpus, MemTotal: raw.memTotal, Load1: raw.load1,
		DiskUsed: raw.diskUse, DiskTotal: raw.diskSize,
	}
	if raw.memTotal > raw.memAvl {
		cur.MemUsed = raw.memTotal - raw.memAvl
	}
	if dTotal := raw.total - prev.total; dTotal > 0 {
		cur.CPU = clamp((raw.busy-prev.busy)/dTotal*100, 0, 100)
	}
	cur.NetRx = rate(raw.rx, prev.rx, dt)
	cur.NetTx = rate(raw.tx, prev.tx, dt)
	cur.DiskRead = rate(raw.rd, prev.rd, dt)
	cur.DiskWrite = rate(raw.wr, prev.wr, dt)
	g.cur, g.at = cur, raw.t
}

func (g *guestReader) latest() (*Guest, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cur == nil || time.Since(g.at) > 3*Interval {
		if g.err != nil {
			return nil, g.err
		}
		return nil, nil
	}
	cp := *g.cur
	return &cp, nil
}

func rate(now, before uint64, dt float64) float64 {
	if now < before {
		return 0 // counter reset
	}
	return float64(now-before) / dt
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// parseGuest reads one block of the guest script's output.
func parseGuest(lines []string) *rawGuest {
	g := &rawGuest{t: time.Now()}
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch {
		case f[0] == "cpu":
			var vals []float64
			for _, x := range f[1:] {
				v, _ := strconv.ParseFloat(x, 64)
				vals = append(vals, v)
			}
			for i, v := range vals {
				if i >= 8 { // guest and guest_nice are already counted in user and nice
					break
				}
				g.total += v
				if i != 3 && i != 4 { // idle, iowait
					g.busy += v
				}
			}
		case strings.HasPrefix(f[0], "cpu"):
			g.cpus++
		case f[0] == "MemTotal:" && len(f) > 1:
			g.memTotal = kib(f[1])
		case f[0] == "MemAvailable:" && len(f) > 1:
			g.memAvl = kib(f[1])
		case f[0] == "df":
			if len(f) == 3 {
				g.diskSize, _ = strconv.ParseUint(f[1], 10, 64)
				g.diskUse, _ = strconv.ParseUint(f[2], 10, 64)
			}
		case strings.HasSuffix(f[0], ":") && len(f) >= 10:
			// /proc/net/dev: count only the VM's own interfaces, not Docker's bridges and veths,
			// which would count container traffic twice.
			name := strings.TrimSuffix(f[0], ":")
			if name == "lo" || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") ||
				strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "cni") || strings.HasPrefix(name, "flannel") {
				continue
			}
			rx, _ := strconv.ParseUint(f[1], 10, 64)
			tx, _ := strconv.ParseUint(f[9], 10, 64)
			g.rx += rx
			g.tx += tx
		case len(f) >= 10 && len(f) <= 20 && isNum(f[0]) && isNum(f[1]):
			// /proc/diskstats: sectors read (field 6) and written (field 10), 512 bytes each.
			rd, _ := strconv.ParseUint(f[5], 10, 64)
			wr, _ := strconv.ParseUint(f[9], 10, 64)
			g.rd += rd * 512
			g.wr += wr * 512
		case len(f) >= 3 && strings.Contains(f[0], "."):
			g.load1, _ = strconv.ParseFloat(f[0], 64) // /proc/loadavg
		}
	}
	return g
}

func kib(s string) uint64 {
	v, _ := strconv.ParseUint(s, 10, 64)
	return v * 1024
}

func isNum(s string) bool {
	_, err := strconv.ParseUint(s, 10, 64)
	return err == nil
}

// ---------- containers: one stats stream per running container ----------

type containerReader struct {
	dc *docker.Client

	mu      sync.Mutex
	names   map[string]string
	images  map[string]string
	latestS map[string]Container
	cancels map[string]context.CancelFunc
}

func newContainerReader(dc *docker.Client) *containerReader {
	return &containerReader{dc: dc, names: map[string]string{}, images: map[string]string{},
		latestS: map[string]Container{}, cancels: map[string]context.CancelFunc{}}
}

func (r *containerReader) run(ctx context.Context) {
	defer func() {
		r.mu.Lock()
		for _, cancel := range r.cancels {
			cancel()
		}
		r.mu.Unlock()
	}()
	for {
		r.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

// refresh starts streams for new running containers and stops those for containers that stopped.
func (r *containerReader) refresh(ctx context.Context) {
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	cs, err := r.dc.Containers(lctx)
	cancel()
	if err != nil {
		return
	}
	running := map[string]bool{}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range cs {
		if c.State != "running" {
			continue
		}
		running[c.ID] = true
		if len(c.Names) > 0 {
			r.names[c.ID] = strings.TrimPrefix(c.Names[0], "/")
		}
		r.images[c.ID] = c.Image
		if _, ok := r.cancels[c.ID]; !ok {
			sctx, scancel := context.WithCancel(ctx)
			r.cancels[c.ID] = scancel
			go r.stream(sctx, c.ID)
		}
	}
	for id, cancel := range r.cancels {
		if !running[id] {
			cancel()
			delete(r.cancels, id)
			delete(r.latestS, id)
		}
	}
}

type statsDoc struct {
	Read        time.Time `json:"read"`
	CPUStats    cpuDoc    `json:"cpu_stats"`
	PreCPUStats cpuDoc    `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		Rx uint64 `json:"rx_bytes"`
		Tx uint64 `json:"tx_bytes"`
	} `json:"networks"`
	Blkio struct {
		IO []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
}

type cpuDoc struct {
	Usage struct {
		Total uint64 `json:"total_usage"`
	} `json:"cpu_usage"`
	System uint64 `json:"system_cpu_usage"`
	CPUs   uint64 `json:"online_cpus"`
}

func (r *containerReader) stream(ctx context.Context, id string) {
	resp, err := r.dc.Raw(ctx, "/containers/"+url.PathEscape(id)+"/stats", url.Values{"stream": {"1"}})
	if err != nil {
		return
	}
	defer resp.Close()
	dec := json.NewDecoder(resp)
	var prev *statsDoc
	var prevRx, prevTx, prevRd, prevWr uint64
	for {
		var d statsDoc
		if err := dec.Decode(&d); err != nil {
			return
		}
		var rx, tx, rd, wr uint64
		for _, n := range d.Networks {
			rx += n.Rx
			tx += n.Tx
		}
		for _, e := range d.Blkio.IO {
			switch strings.ToLower(e.Op) {
			case "read":
				rd += e.Value
			case "write":
				wr += e.Value
			}
		}
		st := Container{Memory: d.MemoryStats.Usage}
		if v, ok := d.MemoryStats.Stats["inactive_file"]; ok && v < st.Memory {
			st.Memory -= v
		}
		cpuDelta := float64(d.CPUStats.Usage.Total) - float64(d.PreCPUStats.Usage.Total)
		sysDelta := float64(d.CPUStats.System) - float64(d.PreCPUStats.System)
		if cpuDelta > 0 && sysDelta > 0 {
			n := float64(d.CPUStats.CPUs)
			if n == 0 {
				n = 1
			}
			st.CPU = cpuDelta / sysDelta * n * 100 // docker stats' percent: 100 per vCPU
		}
		if prev != nil {
			if dt := d.Read.Sub(prev.Read).Seconds(); dt > 0 {
				st.NetRx, st.NetTx = rate(rx, prevRx, dt), rate(tx, prevTx, dt)
				st.DiskRead, st.DiskWrite = rate(rd, prevRd, dt), rate(wr, prevWr, dt)
			}
		}
		prev = &d
		prevRx, prevTx, prevRd, prevWr = rx, tx, rd, wr
		r.mu.Lock()
		if _, ok := r.cancels[id]; ok {
			r.latestS[id] = st
		}
		r.mu.Unlock()
	}
}

func (r *containerReader) latest() (names, images map[string]string, stats map[string]Container) {
	r.mu.Lock()
	defer r.mu.Unlock()
	names, images, stats = map[string]string{}, map[string]string{}, map[string]Container{}
	for id, s := range r.latestS {
		stats[id] = s
		names[id] = r.names[id]
		images[id] = r.images[id]
	}
	return names, images, stats
}

func errorf(format string, a ...any) error { return fmt.Errorf(format, a...) }
