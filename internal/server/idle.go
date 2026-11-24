package server

// Stopping idle VMs. A Colima VM holds on to the memory it has touched for as long as it runs,
// so one nobody is using still costs gigabytes. Once a minute the dashboard looks for signs that
// a running profile is in use; when it has seen none for the chosen time (an hour by default) it
// stops the profile, with a notification 5 minutes before.
//
// Signs of use:
//   - Docker events a person causes: creating, starting, stopping or removing containers, running
//     a command in one, pulling, tagging or removing images, creating volumes and networks. Health
//     checks and the restarts of a container that keeps crashing do not count.
//   - A new Kubernetes pod (a deploy or rollout). Pods restarting on their own do not count.
//   - Containers using more than a tenth of a CPU between them (serving requests, running tests).
//     Kubernetes' own system pods do not count: k3s keeps itself busy even when nothing happens.
//   - A docker, docker compose, kubectl, helm or k9s command still running on the Mac (docker
//     exec -it, logs -f, compose up), or a colima ssh session.
//   - A start, stop or other job the dashboard is running on the profile.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/alerts"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
)

// IdleSettings are kept in idle.json in the dashboard's folder.
type IdleSettings struct {
	Enabled bool     `json:"enabled"`
	Minutes int      `json:"minutes"`
	Keep    []string `json:"keep"` // profiles never stopped
}

const (
	idleEvery    = time.Minute
	idleWarn     = 5 * time.Minute
	idleCPUCores = 0.10            // containers using more than this between them are in use
	crashWindow  = 5 * time.Minute // a start this soon after a die is a restart policy, not a person
)

// idleProfile is what the watcher knows about one running profile.
type idleProfile struct {
	LastActive time.Time `json:"lastActive"`
	Reason     string    `json:"reason"`
	Snooze     time.Time `json:"snoozedUntil,omitzero"`
	Watching   bool      `json:"watching"`
	Note       string    `json:"note,omitempty"`

	since  time.Time            // events are read from here
	cpu    map[string]uint64    // container id -> CPU nanoseconds at cpuAt
	cpuAt  time.Time            //
	pods   map[string]bool      // Kubernetes pod uids seen
	died   map[string]time.Time // container id -> last die
	hc     map[string][]string  // container id -> health check command
	warned bool
	seeded bool
}

type idleWatch struct {
	mu       sync.Mutex
	settings IdleSettings
	loaded   bool
	profiles map[string]*idleProfile
}

func (s *Server) idlePath() string {
	if s.opt.DataDir == "" {
		return ""
	}
	return filepath.Join(s.opt.DataDir, "idle.json")
}

// idleSettings returns the settings, reading them the first time. A missing file means on, 1 hour.
func (s *Server) idleSettings() IdleSettings {
	w := &s.idle
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.loaded {
		w.loaded = true
		w.settings = IdleSettings{Enabled: true, Minutes: 60}
		if p := s.idlePath(); p != "" {
			if b, err := os.ReadFile(p); err == nil {
				_ = json.Unmarshal(b, &w.settings)
			}
		}
		w.settings = w.settings.clean()
	}
	return w.settings
}

func (st IdleSettings) clean() IdleSettings {
	st.Minutes = max(1, min(st.Minutes, 24*60))
	keep := []string{}
	for _, p := range st.Keep {
		if colima.ValidProfile(p) && !slices.Contains(keep, p) {
			keep = append(keep, p)
		}
	}
	st.Keep = keep
	return st
}

// RunIdle watches the running profiles until ctx ends.
func (s *Server) RunIdle(ctx context.Context) {
	if s.idlePath() == "" {
		return
	}
	t := time.NewTicker(idleEvery)
	defer t.Stop()
	for {
		s.idleTick(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) idleTick(ctx context.Context, now time.Time) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	list, err := s.opt.Colima.List(ctx)
	if err != nil {
		return
	}
	st := s.idleSettings()
	host := hostUse(ctx)
	busyJobs := map[string]bool{}
	for _, j := range s.jobs.List() {
		if !j.Done && j.Profile != "" {
			busyJobs[j.Profile] = true
		}
	}

	w := &s.idle
	w.mu.Lock()
	if w.profiles == nil {
		w.profiles = map[string]*idleProfile{}
	}
	running := map[string]bool{}
	for _, p := range list {
		if strings.EqualFold(p.Status, "Running") {
			running[p.Name] = true
			if w.profiles[p.Name] == nil {
				w.profiles[p.Name] = &idleProfile{LastActive: now, Reason: "the VM started", since: now,
					pods: map[string]bool{}, died: map[string]time.Time{}, hc: map[string][]string{}}
			}
			ip := w.profiles[p.Name]
			ip.Watching = strings.HasPrefix(p.Runtime, "docker")
			ip.Note = ""
			if !ip.Watching {
				ip.Note = "Only Docker profiles are watched; a containerd profile is never stopped for being idle."
			}
		}
	}
	for name := range w.profiles {
		if !running[name] {
			delete(w.profiles, name)
		}
	}
	w.mu.Unlock()

	for _, p := range list {
		if !running[p.Name] {
			continue
		}
		w.mu.Lock()
		ip := w.profiles[p.Name]
		w.mu.Unlock()
		if !ip.Watching {
			continue
		}
		reason := ""
		switch {
		case p.Busy != "" || busyJobs[p.Name]:
			reason = "the dashboard is running a job on it"
		case host[p.Name] != "":
			reason = host[p.Name]
		}
		if r := s.idleDocker(ctx, p.Name, ip, now); reason == "" {
			reason = r
		}
		w.mu.Lock()
		if reason != "" {
			ip.LastActive, ip.Reason, ip.warned = now, reason, false
		}
		stop, warn := idleDue(st, ip, p.Name, now)
		if warn {
			ip.warned = true
		}
		w.mu.Unlock()
		if warn {
			s.idleAlert(p.Name, fmt.Sprintf("%s stops in %d minutes", p.Name, int(idleWarn.Minutes())),
				fmt.Sprintf("It has been idle for %s. Use it, or press Keep running on the dashboard's General page.", minutesText(st.Minutes-int(idleWarn.Minutes()))))
		}
		if stop {
			s.idleStop(p.Name, st.Minutes)
		}
	}
}

// idleDue says whether to stop the profile now, or to warn that it will be.
func idleDue(st IdleSettings, ip *idleProfile, name string, now time.Time) (stop, warn bool) {
	if !st.Enabled || slices.Contains(st.Keep, name) || now.Before(ip.Snooze) {
		return false, false
	}
	since := ip.LastActive
	if ip.Snooze.After(since) {
		since = ip.Snooze
	}
	limit := time.Duration(st.Minutes) * time.Minute
	idle := now.Sub(since)
	if idle >= limit {
		return true, false
	}
	return false, limit >= 3*idleWarn && idle >= limit-idleWarn && !ip.warned
}

func (s *Server) idleStop(p string, minutes int) {
	if err := s.opt.Colima.Claim(p, string(colima.ActionStop)); err != nil {
		return // busy: something is using it after all
	}
	s.opt.Logger.Printf("stopping %s: idle for %d minutes", p, minutes)
	s.jobs.Start(fmt.Sprintf("colima stop (%s): idle for %s", p, minutesText(minutes)), p, func(j *Job) error {
		defer s.opt.Colima.Release(p)
		err := s.opt.Colima.RunClaimed(context.Background(), j, colima.ActionStop, p, nil)
		if err == nil {
			s.idleAlert(p, "Stopped "+p+" to free memory",
				fmt.Sprintf("Nothing used it for %s. Start it from the dashboard, or run colima start -p %s.", minutesText(minutes), p))
		}
		return err
	})
}

func (s *Server) idleAlert(p, title, body string) {
	if s.opt.Alerts != nil {
		s.opt.Alerts.Add(alerts.Alert{Profile: p, Kind: "idle", Title: title, Body: body})
	}
}

func minutesText(m int) string {
	switch {
	case m%60 == 0 && m >= 120:
		return fmt.Sprintf("%d hours", m/60)
	case m == 60:
		return "1 hour"
	case m == 1:
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", m)
}

// ---------- Docker ----------

type dockerEvent struct {
	Type   string `json:"Type"`
	Action string `json:"Action"`
	Actor  struct {
		ID         string            `json:"ID"`
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
	TimeNano int64 `json:"timeNano"`
}

// idleDocker reads the profile's Docker events and container CPU since the last look, and returns
// what shows it is in use, or "".
func (s *Server) idleDocker(ctx context.Context, p string, ip *idleProfile, now time.Time) string {
	dc, err := s.dockerFor(p)
	if err != nil {
		return ""
	}
	list, err := dc.Containers(ctx)
	if err != nil {
		return "" // the engine is not answering; leave it to the clock
	}
	reason := ""
	if !ip.seeded {
		for _, c := range list {
			if uid := c.Labels["io.kubernetes.pod.uid"]; uid != "" {
				ip.pods[uid] = true
			}
		}
	}
	if evs, err := readEvents(ctx, dc, ip.since, now); err == nil {
		ip.since = now
		for _, ev := range evs {
			if r := ip.classify(ev, func(id string) []string { return healthcheck(ctx, dc, ip, id) }); r != "" && reason == "" {
				reason = r
			}
		}
	}
	for _, c := range list {
		if c.State == "running" && c.Labels[docker.HelperLabel] != "" {
			if reason == "" {
				reason = "a shell from the dashboard is open"
			}
		}
	}
	if cores := ip.cpuUse(ctx, dc, list, now); cores > idleCPUCores && reason == "" {
		reason = fmt.Sprintf("its containers are busy (%.0f%% of a CPU)", cores*100)
	}
	ip.seeded = true
	return reason
}

func readEvents(ctx context.Context, dc *docker.Client, since, until time.Time) ([]dockerEvent, error) {
	if !until.After(since) {
		return nil, nil
	}
	q := url.Values{"since": {strconv.FormatInt(since.Unix(), 10)}, "until": {strconv.FormatInt(until.Unix(), 10)}}
	body, err := dc.Raw(ctx, "/events", q)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	var out []dockerEvent
	dec := json.NewDecoder(body)
	for {
		var ev dockerEvent
		if err := dec.Decode(&ev); err != nil {
			break
		}
		out = append(out, ev)
	}
	return out, nil
}

// classify returns why an event shows a person using the profile, or "". hc returns a container's
// health check command.
func (ip *idleProfile) classify(ev dockerEvent, hc func(id string) []string) string {
	action, cmd, _ := strings.Cut(ev.Action, ": ")
	attrs := ev.Actor.Attributes
	name := attrs["name"]
	switch ev.Type {
	case "container":
		if uid := attrs["io.kubernetes.pod.uid"]; uid != "" {
			// Pods: a new one means a deploy; the kubelet restarting one does not.
			if action != "create" || attrs["io.kubernetes.pod.namespace"] == "kube-system" || ip.pods[uid] {
				return ""
			}
			ip.pods[uid] = true
			return "a new Kubernetes pod " + attrs["io.kubernetes.pod.name"]
		}
		switch action {
		case "die":
			ip.died[ev.Actor.ID] = time.Unix(0, ev.TimeNano)
			return ""
		case "start":
			if t, ok := ip.died[ev.Actor.ID]; ok && time.Unix(0, ev.TimeNano).Sub(t) < crashWindow {
				return "" // its restart policy
			}
			return "container " + name + " started"
		case "exec_create":
			if isHealthcheck(cmd, hc(ev.Actor.ID)) {
				return ""
			}
			return "a command ran in " + name
		case "create", "attach", "commit", "copy", "rename", "update", "restart", "pause", "unpause", "kill", "stop", "destroy", "export":
			return "container " + name + ": " + action
		}
	case "image":
		switch action {
		case "pull", "push", "tag", "untag", "delete", "import", "load", "save":
			return "image " + action
		}
	case "volume", "network":
		if action == "create" || action == "destroy" {
			return ev.Type + " " + action
		}
	case "builder":
		return "build cache " + action
	}
	return ""
}

// isHealthcheck says whether an exec's command is the container's health check.
func isHealthcheck(cmd string, test []string) bool {
	if len(test) < 2 {
		return false
	}
	want := strings.Join(test[1:], " ")
	switch test[0] {
	case "CMD":
	case "CMD-SHELL":
		want = "/bin/sh -c " + want
	default:
		return false
	}
	return strings.TrimSpace(cmd) == want
}

func healthcheck(ctx context.Context, dc *docker.Client, ip *idleProfile, id string) []string {
	if t, ok := ip.hc[id]; ok {
		return t
	}
	var test []string
	if m, err := dc.Inspect(ctx, id); err == nil {
		if cfg, ok := m["Config"].(map[string]any); ok {
			if h, ok := cfg["Healthcheck"].(map[string]any); ok {
				if arr, ok := h["Test"].([]any); ok {
					for _, a := range arr {
						s, _ := a.(string)
						test = append(test, s)
					}
				}
			}
		}
	}
	ip.hc[id] = test
	return test
}

// cpuUse returns the CPUs the profile's own containers used since the last look (Kubernetes'
// system pods and pod sandboxes left out).
func (ip *idleProfile) cpuUse(ctx context.Context, dc *docker.Client, list []docker.Container, now time.Time) float64 {
	cur := map[string]uint64{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, c := range list {
		if c.State != "running" || c.Labels["io.kubernetes.pod.namespace"] == "kube-system" || c.Labels["io.kubernetes.docker.type"] == "podsandbox" {
			continue
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			body, err := dc.Raw(ctx, "/containers/"+id+"/stats", url.Values{"stream": {"false"}, "one-shot": {"true"}})
			if err != nil {
				return
			}
			defer body.Close()
			var st struct {
				CPU struct {
					Usage struct {
						Total uint64 `json:"total_usage"`
					} `json:"cpu_usage"`
				} `json:"cpu_stats"`
			}
			if json.NewDecoder(body).Decode(&st) == nil {
				mu.Lock()
				cur[id] = st.CPU.Usage.Total
				mu.Unlock()
			}
		}(c.ID)
	}
	wg.Wait()
	cores := cpuDelta(ip.cpu, cur, now.Sub(ip.cpuAt))
	ip.cpu, ip.cpuAt = cur, now
	return cores
}

// cpuDelta is the CPUs used between two samples of cumulative nanoseconds.
func cpuDelta(before, now map[string]uint64, dt time.Duration) float64 {
	if before == nil || dt <= 0 {
		return 0
	}
	var ns uint64
	for id, v := range now {
		if b, ok := before[id]; ok && v >= b {
			ns += v - b
		}
	}
	return float64(ns) / float64(dt.Nanoseconds())
}

// ---------- The Mac ----------

// hostUse looks for docker, kubectl and colima ssh commands still running on the Mac and returns,
// per profile, what it found.
func hostUse(ctx context.Context) map[string]string {
	out, err := exec.CommandContext(ctx, "ps", "-axww", "-o", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil
	}
	return parseHostUse(string(out), os.Getpid(), dockerContext, func() string {
		c, _ := kubeCurrentContext(ctx)
		return c
	})
}

// parseHostUse reads ps output. Our own children (the metrics' colima ssh) do not count.
// dockerCtx and kubeCtx return the current contexts, for commands that do not name one.
func parseHostUse(ps string, self int, dockerCtx, kubeCtx func() string) map[string]string {
	type proc struct {
		ppid int
		args []string
	}
	procs := map[int]proc{}
	sc := bufio.NewScanner(strings.NewReader(ps))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 == nil && err2 == nil {
			procs[pid] = proc{ppid, f[2:]}
		}
	}
	ours := func(pid int) bool {
		for i := 0; i < 64 && pid > 1; i++ {
			if pid == self {
				return true
			}
			pid = procs[pid].ppid
		}
		return false
	}
	use := map[string]string{}
	set := func(profile, why string) {
		if profile != "" && use[profile] == "" {
			use[profile] = why
		}
	}
	var dctx, kctx *string
	for pid, p := range procs {
		bin := filepath.Base(p.args[0])
		var kind string
		switch bin {
		case "docker", "com.docker.cli", "docker-compose", "docker-buildx":
			kind = "docker"
		case "kubectl", "helm", "k9s", "stern":
			kind = "kube"
		case "colima":
			if !slices.Contains(p.args, "ssh") {
				continue
			}
			kind = "ssh"
		case "limactl":
			if len(p.args) < 3 || p.args[1] != "shell" {
				continue
			}
			kind = "ssh"
		default:
			continue
		}
		if ours(pid) {
			continue
		}
		cmdline := strings.Join(p.args, " ")
		switch kind {
		case "ssh":
			if bin == "limactl" {
				set(profileFromInstance(p.args[2]), "a colima ssh session is open")
			} else {
				set(flagValue(p.args, "-p", "--profile", "default"), "a colima ssh session is open")
			}
		case "docker":
			ctxName := flagValue(p.args, "-c", "--context", "")
			if h := flagValue(p.args, "-H", "--host", ""); h != "" {
				ctxName = profileFromSocket(h)
			} else if ctxName == "" {
				if dctx == nil {
					v := dockerCtx()
					dctx = &v
				}
				ctxName = *dctx
			}
			set(profileFromInstance(ctxName), "a docker command is running on the Mac: "+shorten(cmdline, 60))
		case "kube":
			name := flagValue(p.args, "", "--context", "")
			if name == "" {
				if kctx == nil {
					v := kubeCtx()
					kctx = &v
				}
				name = *kctx
			}
			set(profileFromInstance(name), "a Kubernetes command is running on the Mac: "+shorten(cmdline, 60))
		}
	}
	delete(use, "")
	return use
}

// flagValue returns the value of -x v, --long v or --long=v, or def.
func flagValue(args []string, short, long, def string) string {
	for i, a := range args {
		if (a == short && short != "") || a == long {
			if i+1 < len(args) {
				return args[i+1]
			}
		}
		if v, ok := strings.CutPrefix(a, long+"="); ok {
			return v
		}
	}
	return def
}

// profileFromInstance maps a Docker or Kubernetes context or Lima instance name (colima,
// colima-bench) to the profile, or "" for something else.
func profileFromInstance(name string) string {
	switch {
	case name == "colima":
		return "default"
	case strings.HasPrefix(name, "colima-"):
		return strings.TrimPrefix(name, "colima-")
	}
	return ""
}

// profileFromSocket maps unix:///Users/x/.colima/bench/docker.sock to bench.
func profileFromSocket(h string) string {
	h = strings.TrimPrefix(h, "unix://")
	if filepath.Base(h) != "docker.sock" {
		return ""
	}
	return "colima-" + filepath.Base(filepath.Dir(h)) // so profileFromInstance maps it back
}

// dockerContext reads the current context from ~/.docker/config.json (DOCKER_CONTEXT wins).
func dockerContext() string {
	if c := os.Getenv("DOCKER_CONTEXT"); c != "" {
		return c
	}
	home, _ := os.UserHomeDir()
	dir := os.Getenv("DOCKER_CONFIG")
	if dir == "" {
		dir = filepath.Join(home, ".docker")
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return ""
	}
	var cfg struct {
		CurrentContext string `json:"currentContext"`
	}
	_ = json.Unmarshal(b, &cfg)
	return cfg.CurrentContext
}

// ---------- API ----------

type idleView struct {
	idleProfile
	IdleSeconds int64 `json:"idleSeconds"`
	StopsIn     int64 `json:"stopsInSeconds"` // -1: never
	Keep        bool  `json:"keep"`
}

// GET /api/idle -> {settings, profiles: {name: ...}}
func (s *Server) apiIdle(w http.ResponseWriter, r *http.Request) {
	st := s.idleSettings()
	now := time.Now()
	out := map[string]idleView{}
	s.idle.mu.Lock()
	for name, ip := range s.idle.profiles {
		v := idleView{idleProfile: *ip, Keep: slices.Contains(st.Keep, name), StopsIn: -1}
		since := ip.LastActive
		if ip.Snooze.After(since) {
			since = ip.Snooze
		}
		v.IdleSeconds = int64(max(0, now.Sub(since)).Seconds())
		if st.Enabled && !v.Keep && ip.Watching {
			v.StopsIn = int64(max(0, since.Add(time.Duration(st.Minutes)*time.Minute).Sub(now)).Seconds())
		}
		out[name] = v
	}
	s.idle.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"settings": st, "profiles": out, "available": s.idlePath() != ""})
}

// POST /api/idle/settings {enabled, minutes, keep}
func (s *Server) apiIdleSettings(w http.ResponseWriter, r *http.Request) {
	p := s.idlePath()
	if p == "" {
		writeError(w, http.StatusNotFound, errors.New("stopping idle VMs is off in this build"))
		return
	}
	var req IdleSettings
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req = req.clean()
	b, _ := json.MarshalIndent(req, "", "  ")
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.idleSettings() // loads once, so the write below is not overwritten
	s.idle.mu.Lock()
	s.idle.settings = req
	for _, ip := range s.idle.profiles {
		ip.warned = false
	}
	s.idle.mu.Unlock()
	writeJSON(w, http.StatusOK, req)
}

// POST /api/profiles/{p}/idle {minutes}: keep the profile running for that long (0 cancels).
func (s *Server) apiIdleSnooze(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("p")
	var req struct {
		Minutes int `json:"minutes"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Minutes < 0 || req.Minutes > 7*24*60 {
		writeError(w, http.StatusBadRequest, errors.New("minutes must be between 0 and a week"))
		return
	}
	s.idle.mu.Lock()
	defer s.idle.mu.Unlock()
	ip := s.idle.profiles[p]
	if ip == nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("%s is not running", p))
		return
	}
	now := time.Now()
	if req.Minutes == 0 {
		ip.Snooze = time.Time{}
		ip.LastActive, ip.Reason = now, "keep running was cancelled"
	} else {
		ip.Snooze = now.Add(time.Duration(req.Minutes) * time.Minute)
	}
	ip.warned = false
	writeJSON(w, http.StatusOK, map[string]any{"snoozedUntil": ip.Snooze})
}
