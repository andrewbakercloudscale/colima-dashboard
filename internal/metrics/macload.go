package metrics

import (
	"bufio"
	"bytes"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// "Why is it slow?": what the whole Mac is busy with over the last few minutes, so a slow
// container can be told apart from a Mac that is flat out on something else. It samples only
// while someone is looking (the Monitor page asks every few seconds) and stops on its own.

// MacApp is one app or command and the share of the Mac's CPU it used over the window.
type MacApp struct {
	Name      string   `json:"name"`
	CPU       float64  `json:"cpu"` // percent of the whole Mac (all cores)
	Processes int      `json:"processes"`
	Commands  []string `json:"commands"` // a few process names, for the detail line
	VM        bool     `json:"vm"`       // a Colima or Lima VM process
}

// MacLoad is the answer to "why is it slow?".
type MacLoad struct {
	WindowSec   float64  `json:"windowSec"` // how much history the figures cover (up to 5 min)
	Busy        float64  `json:"busy"`      // percent of all cores in use
	Kernel      float64  `json:"kernel"`    // percent of all cores in system (kernel) time
	SpawnPerSec float64  `json:"spawnPerSec"`
	Cores       int      `json:"cores"`
	Apps        []MacApp `json:"apps"`
	Ready       bool     `json:"ready"` // false until two samples exist
}

const (
	macEvery  = 5 * time.Second
	macWindow = 5 * time.Minute
	macIdle   = 30 * time.Second
	pidMax    = 99999 // macOS hands out PIDs in order and wraps here
)

type macSnap struct {
	t       time.Time
	cpu     map[int]float64 // pid -> cumulative CPU seconds
	comm    map[int]string
	lastPID int // PID of the newest process
	busy    float64
	kernel  float64
}

// MacWatcher samples the Mac's processes while it is being asked for.
type MacWatcher struct {
	mu      sync.Mutex
	snaps   []macSnap
	seen    time.Time
	running bool
	// sample is swapped in tests.
	sample func() (macSnap, error)
}

// NewMacWatcher returns a watcher that runs ps and iostat.
func NewMacWatcher() *MacWatcher { return &MacWatcher{sample: sampleMac} }

// Get returns the load over the window and keeps sampling for a little while.
func (w *MacWatcher) Get() MacLoad {
	w.mu.Lock()
	w.seen = time.Now()
	if !w.running {
		w.running = true
		go w.run()
	}
	snaps := append([]macSnap(nil), w.snaps...)
	w.mu.Unlock()
	return summarise(snaps, runtime.NumCPU())
}

func (w *MacWatcher) run() {
	for {
		s, err := w.sample()
		w.mu.Lock()
		if err == nil {
			w.snaps = append(w.snaps, s)
			cut := s.t.Add(-macWindow - macEvery)
			for len(w.snaps) > 2 && w.snaps[0].t.Before(cut) {
				w.snaps = w.snaps[1:]
			}
		}
		if time.Since(w.seen) > macIdle {
			w.running = false
			w.snaps = nil // stale by the time anyone asks again
			w.mu.Unlock()
			return
		}
		w.mu.Unlock()
		time.Sleep(macEvery)
	}
}

func summarise(snaps []macSnap, cores int) MacLoad {
	out := MacLoad{Cores: cores, Apps: []MacApp{}}
	if len(snaps) < 2 || cores < 1 {
		return out
	}
	first, last := snaps[0], snaps[len(snaps)-1]
	dt := last.t.Sub(first.t).Seconds()
	if dt <= 0 {
		return out
	}
	out.Ready = true
	out.WindowSec = dt
	// Busy and kernel: average of the per-sample readings (each is iostat over one second).
	n := 0
	for _, s := range snaps[1:] {
		out.Busy += s.busy
		out.Kernel += s.kernel
		n++
	}
	out.Busy /= float64(n)
	out.Kernel /= float64(n)
	// New processes: how far the PID counter moved, wrapping at pidMax.
	var spawned int
	for i := 1; i < len(snaps); i++ {
		d := snaps[i].lastPID - snaps[i-1].lastPID
		if d < 0 {
			d += pidMax
		}
		spawned += d
	}
	out.SpawnPerSec = float64(spawned) / dt

	// Per app: CPU time used by processes alive at both ends of each step, summed over the steps,
	// so processes that came and went part way through still count for the time they were seen.
	type agg struct {
		secs  float64
		pids  map[int]bool
		comms map[string]bool
	}
	apps := map[string]*agg{}
	for i := 1; i < len(snaps); i++ {
		a, b := snaps[i-1], snaps[i]
		for pid, cb := range b.cpu {
			ca, ok := a.cpu[pid]
			if !ok || b.comm[pid] != a.comm[pid] || cb < ca {
				continue
			}
			name := appName(b.comm[pid])
			g := apps[name]
			if g == nil {
				g = &agg{pids: map[int]bool{}, comms: map[string]bool{}}
				apps[name] = g
			}
			g.secs += cb - ca
			g.pids[pid] = true
			g.comms[filepath.Base(b.comm[pid])] = true
		}
	}
	for name, g := range apps {
		pct := g.secs / dt / float64(cores) * 100
		if pct < 0.05 {
			continue
		}
		cmds := make([]string, 0, len(g.comms))
		for c := range g.comms {
			cmds = append(cmds, c)
		}
		sort.Strings(cmds)
		if len(cmds) > 4 {
			cmds = cmds[:4]
		}
		out.Apps = append(out.Apps, MacApp{Name: name, CPU: pct, Processes: len(g.pids), Commands: cmds, VM: isVMProcess(name)})
	}
	sort.Slice(out.Apps, func(i, j int) bool { return out.Apps[i].CPU > out.Apps[j].CPU })
	if len(out.Apps) > 8 {
		out.Apps = out.Apps[:8]
	}
	return out
}

// appName groups helpers under their app: "/Applications/Google Chrome.app/Contents/Frameworks/
// .../Google Chrome Helper (Renderer)" is "Google Chrome". Plain commands keep their own name.
func appName(comm string) string {
	if i := strings.Index(comm, ".app/"); i >= 0 {
		return filepath.Base(comm[:i])
	}
	if strings.HasSuffix(comm, ".app") {
		return strings.TrimSuffix(filepath.Base(comm), ".app")
	}
	if i := strings.Index(comm, ".xpc/"); i >= 0 {
		return filepath.Base(comm[:i])
	}
	return filepath.Base(comm)
}

func isVMProcess(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "virtualization") || strings.HasPrefix(n, "limactl") || strings.HasPrefix(n, "qemu") || n == "colima"
}

func sampleMac() (macSnap, error) {
	s := macSnap{t: time.Now(), cpu: map[int]float64{}, comm: map[int]string{}}
	out, err := exec.Command("ps", "-A", "-o", "pid=,etime=,time=,comm=").Output()
	if err != nil {
		return s, err
	}
	parsePS(out, &s)
	// iostat: the second line is the last second; "us sy id" are its last three columns.
	if io, err := exec.Command("iostat", "-n", "0", "-c", "2", "-w", "1").Output(); err == nil {
		s.busy, s.kernel = parseIostat(io)
	}
	s.t = time.Now()
	return s, nil
}

func parsePS(out []byte, s *macSnap) {
	newest := -1.0
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		// comm may contain spaces: it is everything after the third field.
		rest := strings.TrimSpace(sc.Text())
		for i := 0; i < 3; i++ {
			rest = strings.TrimSpace(rest[strings.Index(rest, f[i])+len(f[i]):])
		}
		s.cpu[pid] = parseCPUTime(f[2])
		s.comm[pid] = rest
		if age := parseCPUTime(f[1]); newest < 0 || age < newest || (age == newest && pid > s.lastPID) {
			newest, s.lastPID = age, pid
		}
	}
}

func parseIostat(out []byte) (busy, kernel float64) {
	// With -n 0 each data line is "us sy id 1m 5m 15m"; the last line is the last second.
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	f := strings.Fields(lines[len(lines)-1])
	if len(f) < 3 {
		return 0, 0
	}
	sys, err1 := strconv.ParseFloat(f[1], 64)
	idle, err2 := strconv.ParseFloat(f[2], 64)
	if err1 != nil || err2 != nil {
		return 0, 0
	}
	return 100 - idle, sys
}
