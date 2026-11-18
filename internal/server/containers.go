package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
)

// detailCache keeps each container's inspect summary (exit reason, limits) so the list, polled
// every few seconds, does not inspect every container every time. An entry is reused while the
// container's state and exit code are unchanged, for up to detailTTL (limits changed with
// `docker update` elsewhere show within that time; changes made here drop the entry at once).
type detailCache struct {
	mu sync.Mutex
	m  map[string]detailEntry // key: profile + "/" + container id
}

type detailEntry struct {
	stamp string
	at    time.Time
	d     *docker.Details
}

const detailTTL = 10 * time.Minute

func (dc *detailCache) get(key, stamp string) *docker.Details {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	e, ok := dc.m[key]
	if !ok || e.stamp != stamp || time.Since(e.at) > detailTTL {
		return nil
	}
	return e.d
}

func (dc *detailCache) put(key, stamp string, d *docker.Details) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if dc.m == nil {
		dc.m = map[string]detailEntry{}
	}
	dc.m[key] = detailEntry{stamp: stamp, at: time.Now(), d: d}
}

func (dc *detailCache) drop(key string) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	delete(dc.m, key)
}

// prune forgets containers of profile that are no longer listed.
func (dc *detailCache) prune(profile string, keep map[string]bool) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	for k := range dc.m {
		if strings.HasPrefix(k, profile+"/") && !keep[k] {
			delete(dc.m, k)
		}
	}
}

// containerRow is a list row with what the page shows beyond /containers/json.
type containerRow struct {
	docker.Container
	Health  string          `json:"health,omitempty"`
	CPUs    float64         `json:"cpus"`
	Details *docker.Details `json:"details,omitempty"`
}

func detailStamp(c docker.Container) string {
	return c.State + "|" + strconv.Itoa(docker.ExitCodeFromStatus(c.Status))
}

func (s *Server) apiContainers(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	cs, err := dc.Containers(r.Context())
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	p := r.PathValue("p")
	rows := make([]containerRow, len(cs))
	keep := map[string]bool{}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	for i, c := range cs {
		rows[i] = containerRow{Container: c, Health: docker.HealthFromStatus(c.Status)}
		key, stamp := p+"/"+c.ID, detailStamp(c)
		keep[key] = true
		if d := s.details.get(key, stamp); d != nil {
			rows[i].Details, rows[i].CPUs = d, d.CPUs()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			d, err := dc.Details(ctx, c.ID)
			if err != nil {
				return // removed meanwhile, or the engine is slow: the row shows without details
			}
			s.details.put(key, stamp, d)
			rows[i].Details, rows[i].CPUs = d, d.CPUs()
		}()
	}
	wg.Wait()
	s.details.prune(p, keep)
	writeJSON(w, http.StatusOK, rows)
}

// apiLimits changes a container's memory, CPU and restart policy in place.
func (s *Server) apiLimits(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	var req docker.LimitsRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	cur, err := dc.Details(ctx, id)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	body, err := docker.UpdateBody(cur, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	warnings, err := dc.Update(ctx, id, body)
	s.details.drop(r.PathValue("p") + "/" + id)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	if warnings == nil {
		warnings = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "warnings": warnings})
}

// logParams reads tail, since (seconds back) and follow from a logs request.
func logParams(r *http.Request) (tail int, since time.Time, follow bool) {
	q := r.URL.Query()
	tail, err := strconv.Atoi(q.Get("tail"))
	if err != nil || tail <= 0 || tail > 10000 {
		tail = 500
	}
	if secs, err := strconv.Atoi(q.Get("since")); err == nil && secs > 0 && secs <= 366*86400 {
		since = time.Now().Add(-time.Duration(secs) * time.Second)
	}
	return tail, since, q.Get("follow") == "1"
}

// logLine is one SSE "line" event: stream, text, UTC timestamp (StampFormat) and, for a
// project's combined logs, the service it came from.
type logLine struct {
	S  string `json:"s"`
	T  string `json:"t"`
	TS string `json:"ts,omitempty"`
	N  string `json:"n,omitempty"`
}

func ttyOf(ctx context.Context, dc *docker.Client, id string) (bool, error) {
	doc, err := dc.Inspect(ctx, id)
	if err != nil {
		return false, err
	}
	tty := false
	if cfg, ok := doc["Config"].(map[string]any); ok {
		tty, _ = cfg["Tty"].(bool)
	}
	return tty, nil
}

// apiLogs streams container logs as SSE "line" events (logLine). Lines always carry their
// timestamp; the page decides whether to show it.
func (s *Server) apiLogs(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	id := r.PathValue("id")
	tail, since, follow := logParams(r)
	tty, err := ttyOf(r.Context(), dc, id)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	ev, ok := newSSE(w)
	if !ok {
		return
	}
	var mu sync.Mutex
	err = dc.Logs(r.Context(), id, tail, since, follow, true, tty, func(stream, line string) error {
		ts, text, _ := docker.SplitStamp(line)
		mu.Lock()
		defer mu.Unlock()
		return ev.send("line", logLine{S: stream, T: text, TS: ts})
	})
	if err != nil && r.Context().Err() == nil {
		_ = ev.send("failed", err.Error())
		return
	}
	_ = ev.send("done", true)
}

var projectRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// projectMember is one container of a Compose project and the name its lines carry.
type projectMember struct {
	ID, Name string
}

// projectMembers picks a project's containers from the list and names each by its service,
// adding the replica number when a service has more than one container.
func projectMembers(cs []docker.Container, project string) []projectMember {
	var out []projectMember
	count := map[string]int{}
	for _, c := range cs {
		if c.Labels["com.docker.compose.project"] == project {
			count[serviceOf(c)]++
		}
	}
	for _, c := range cs {
		if c.Labels["com.docker.compose.project"] != project {
			continue
		}
		n := serviceOf(c)
		if count[n] > 1 {
			if num := c.Labels["com.docker.compose.container-number"]; num != "" {
				n += "-" + num
			} else {
				n += "-" + c.ID[:min(6, len(c.ID))]
			}
		}
		out = append(out, projectMember{ID: c.ID, Name: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func serviceOf(c docker.Container) string {
	if s := c.Labels["com.docker.compose.service"]; s != "" {
		return s
	}
	return strings.TrimPrefix(firstName(c.Names), "/")
}

// apiProjectLogs streams the logs of every container in a Compose project as one SSE stream.
// Lines from different containers arrive in the order they are read; each carries its
// timestamp and service name, and the page puts them in time order.
func (s *Server) apiProjectLogs(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	project := r.PathValue("project")
	if !projectRe.MatchString(project) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid project name %q", project))
		return
	}
	cs, err := dc.Containers(r.Context())
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	members := projectMembers(cs, project)
	if len(members) == 0 {
		writeError(w, http.StatusNotFound, fmt.Errorf("no containers in Compose project %q", project))
		return
	}
	tail, since, follow := logParams(r)
	ev, ok := newSSE(w)
	if !ok {
		return
	}
	var mu sync.Mutex
	names := make([]string, len(members))
	for i, m := range members {
		names[i] = m.Name
	}
	_ = ev.send("services", names)
	var wg sync.WaitGroup
	var errs []string
	for _, m := range members {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tty, err := ttyOf(r.Context(), dc, m.ID)
			if err == nil {
				err = dc.Logs(r.Context(), m.ID, tail, since, follow, true, tty, func(stream, line string) error {
					ts, text, _ := docker.SplitStamp(line)
					mu.Lock()
					defer mu.Unlock()
					return ev.send("line", logLine{S: stream, T: text, TS: ts, N: m.Name})
				})
			}
			if err != nil && r.Context().Err() == nil && !errors.Is(err, context.Canceled) {
				mu.Lock()
				errs = append(errs, m.Name+": "+err.Error())
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if r.Context().Err() != nil {
		return
	}
	if len(errs) > 0 {
		_ = ev.send("failed", strings.Join(errs, "; "))
		return
	}
	_ = ev.send("done", true)
}
