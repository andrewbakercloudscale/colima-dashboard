package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/history"
)

// The optional Prometheus endpoint. Off by default; while off, /metrics needs the session cookie
// like any API call. Turned on in Preferences, it is served without the cookie so a local
// Prometheus can scrape it (still only on 127.0.0.1, still behind the Host check).
//
// A scrape never starts anything: profile state comes from Colima's files, VM and container
// figures from the Monitor's latest sample when it is sampling, and run and build counts from
// the history on disk, read at most once a minute.

type promState struct {
	mu      sync.Mutex
	loaded  bool
	on      bool
	histAt  time.Time
	history map[string]promHistory
}

type promHistory struct {
	runs      map[string]int // by outcome
	builds    map[string]int // by status
	buildSecs float64
	last      *history.Point
}

func (s *Server) promFile() string {
	if s.opt.DataDir == "" {
		return ""
	}
	return filepath.Join(s.opt.DataDir, "prometheus.json")
}

func (s *Server) promEnabled() bool {
	s.prom.mu.Lock()
	defer s.prom.mu.Unlock()
	if !s.prom.loaded {
		s.prom.loaded = true
		if f := s.promFile(); f != "" {
			var v struct {
				Enabled bool `json:"enabled"`
			}
			if b, err := os.ReadFile(f); err == nil && json.Unmarshal(b, &v) == nil {
				s.prom.on = v.Enabled
			}
		}
	}
	return s.prom.on
}

func (s *Server) promRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /metrics", s.handleMetrics)
	m.HandleFunc("GET /api/prometheus", s.auth(s.apiPrometheus))
	m.HandleFunc("POST /api/prometheus", s.auth(s.apiSetPrometheus))
}

func (s *Server) apiPrometheus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"enabled": s.promEnabled(), "url": fmt.Sprintf("http://127.0.0.1:%d/metrics", s.opt.Port), "available": s.promFile() != ""})
}

func (s *Server) apiSetPrometheus(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	f := s.promFile()
	if f == "" {
		writeError(w, http.StatusNotFound, fmt.Errorf("no settings folder in this build"))
		return
	}
	b, _ := json.Marshal(body)
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := os.WriteFile(f, append(b, '\n'), 0o600); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.promEnabled() // load first, so the write below is not overwritten by a later lazy load
	s.prom.mu.Lock()
	s.prom.on = body.Enabled
	s.prom.mu.Unlock()
	s.apiPrometheus(w, r)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.promEnabled() && !s.authed(r) {
		http.Error(w, "the Prometheus endpoint is off: turn it on in Colima Dashboard's Preferences", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(s.promText()))
}

// profilesOnDisk lists Colima's profiles from its folder, without running colima.
func profilesOnDisk(home string) []string {
	var out []string
	ents, _ := os.ReadDir(home)
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") || !colima.ValidProfile(e.Name()) {
			continue
		}
		if _, err := os.Stat(filepath.Join(home, e.Name(), "colima.yaml")); err == nil {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func (s *Server) promHistory(profiles []string) map[string]promHistory {
	s.prom.mu.Lock()
	defer s.prom.mu.Unlock()
	if s.prom.history != nil && time.Since(s.prom.histAt) < time.Minute {
		return s.prom.history
	}
	out := map[string]promHistory{}
	if s.opt.History != nil {
		since := time.Now().Add(-history.Keep)
		for _, p := range profiles {
			h := promHistory{runs: map[string]int{}, builds: map[string]int{}}
			for _, r := range s.opt.History.Store.Runs(p, since) {
				h.runs[r.Outcome]++
			}
			for _, b := range s.opt.History.Store.Builds(p, since) {
				h.builds[b.Status]++
				if b.Status == "Completed" {
					h.buildSecs += b.Seconds
				}
			}
			if pts := s.opt.History.Store.Points(p, time.Now().Add(-15*time.Minute)); len(pts) > 0 {
				last := pts[len(pts)-1]
				h.last = &last
			}
			out[p] = h
		}
	}
	s.prom.history, s.prom.histAt = out, time.Now()
	return out
}

// promText renders the exposition format.
func (s *Server) promText() string {
	var b strings.Builder
	type metric struct{ name, help, kind string }
	written := map[string]bool{}
	gauge := func(m metric, labels map[string]string, v float64) {
		if !written[m.name] {
			written[m.name] = true
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", m.name, m.help, m.name, m.kind)
		}
		b.WriteString(m.name)
		if len(labels) > 0 {
			keys := make([]string, 0, len(labels))
			for k := range labels {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			b.WriteByte('{')
			for i, k := range keys {
				if i > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, "%s=%q", k, promEscape(labels[k]))
			}
			b.WriteByte('}')
		}
		b.WriteByte(' ')
		b.WriteString(strconv.FormatFloat(v, 'g', -1, 64))
		b.WriteByte('\n')
	}
	var (
		info       = metric{"colima_dashboard_info", "Colima Dashboard version.", "gauge"}
		up         = metric{"colima_profile_up", "1 when the profile's VM is running and its Docker socket exists.", "gauge"}
		hostCPU    = metric{"colima_vm_host_cpu_percent", "Mac CPU used by the VM, percent of one core.", "gauge"}
		hostMem    = metric{"colima_vm_host_memory_bytes", "Physical memory the VM takes on the Mac.", "gauge"}
		guestCPU   = metric{"colima_vm_cpu_percent", "CPU in use inside the VM, percent of all its vCPUs (only while the Monitor samples).", "gauge"}
		guestMem   = metric{"colima_vm_memory_used_bytes", "Memory in use inside the VM (only while the Monitor samples).", "gauge"}
		guestTotal = metric{"colima_vm_memory_total_bytes", "Memory the VM has.", "gauge"}
		diskMac    = metric{"colima_vm_disk_on_mac_bytes", "Space the VM's disk image takes on the Mac (5 minute samples).", "gauge"}
		running    = metric{"colima_containers_running", "Running containers (5 minute samples).", "gauge"}
		cCPU       = metric{"colima_container_cpu_percent", "Container CPU, percent of the whole VM (only while the Monitor samples).", "gauge"}
		cMem       = metric{"colima_container_memory_bytes", "Container memory (only while the Monitor samples).", "gauge"}
		runs       = metric{"colima_history_runs", "Container runs recorded in the last 30 days, by outcome.", "gauge"}
		builds     = metric{"colima_history_builds", "Image builds recorded in the last 30 days, by status.", "gauge"}
		buildSecs  = metric{"colima_history_build_seconds", "Seconds spent on completed builds in the last 30 days.", "gauge"}
	)
	gauge(info, map[string]string{"version": s.opt.Version}, 1)
	profiles := profilesOnDisk(s.opt.ColimaHome)
	hist := s.promHistory(profiles)
	for _, p := range profiles {
		lp := map[string]string{"profile": p}
		isUp := colima.Running(s.opt.ColimaHome, p)
		gauge(up, lp, boolNum(isUp))
		h := hist[p]
		sample, names, images, live := s.opt.Metrics.Latest(p, 30*time.Second)
		switch {
		case live && sample.Host != nil:
			gauge(hostCPU, lp, sample.Host.CPU)
			gauge(hostMem, lp, float64(sample.Host.Memory))
		case isUp && h.last != nil && h.last.HostCPU != nil:
			gauge(hostCPU, lp, *h.last.HostCPU)
			gauge(hostMem, lp, float64(h.last.HostMem))
		}
		if live && sample.Guest != nil {
			gauge(guestCPU, lp, sample.Guest.CPU)
			gauge(guestMem, lp, float64(sample.Guest.MemUsed))
			gauge(guestTotal, lp, float64(sample.Guest.MemTotal))
		}
		if isUp && h.last != nil {
			if h.last.DiskMac > 0 {
				gauge(diskMac, lp, float64(h.last.DiskMac))
			}
			gauge(running, lp, float64(h.last.Running))
		}
		if live {
			ids := make([]string, 0, len(sample.Containers))
			for id := range sample.Containers {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				c := sample.Containers[id]
				name := strings.TrimPrefix(names[id], "/")
				if name == "" {
					name = id[:min(12, len(id))]
				}
				l := map[string]string{"profile": p, "container": name, "image": images[id]}
				gauge(cCPU, l, c.CPU)
				gauge(cMem, l, float64(c.Memory))
			}
		}
		for _, o := range []string{"pass", "fail", "oom", "stopped"} {
			gauge(runs, map[string]string{"profile": p, "outcome": o}, float64(h.runs[o]))
		}
		for _, st := range []string{"Completed", "Error", "Canceled"} {
			gauge(builds, map[string]string{"profile": p, "status": strings.ToLower(st)}, float64(h.builds[st]))
		}
		gauge(buildSecs, lp, h.buildSecs)
	}
	return b.String()
}

func boolNum(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// promEscape keeps label values printable; %q then adds the quotes and escapes.
func promEscape(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 {
			return ' '
		}
		return r
	}, s)
}
