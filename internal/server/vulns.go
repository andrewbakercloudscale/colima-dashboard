package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/vuln"
)

// Security: a CVE scan with Trivy of every image the profile's containers use (or every image).
// Scans run one image at a time in the background; each answer is kept per image ID for a day,
// in memory and in the data folder, so the page opens with the last results straight away.

func (s *Server) vulnRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/profiles/{p}/vulns", s.auth(s.apiVulns))
	m.HandleFunc("POST /api/profiles/{p}/vulns/scan", s.auth(s.apiVulnScan))
	m.HandleFunc("GET /api/profiles/{p}/vulns/{id}", s.auth(s.apiVulnImage))
}

const vulnTTL = 24 * time.Hour

type vulnEntry struct {
	Report    *vuln.Report `json:"report"`
	ScannedAt time.Time    `json:"scannedAt"`
	Ref       string       `json:"ref"`
}

// VulnScan is the state of the background scan for one profile.
type VulnScan struct {
	Running  bool      `json:"running"`
	Phase    string    `json:"phase,omitempty"` // "db" while Trivy updates its database, then "images"
	Done     int       `json:"done"`
	Total    int       `json:"total"`
	Current  string    `json:"current,omitempty"`
	Started  time.Time `json:"started,omitempty"`
	Finished time.Time `json:"finished,omitempty"`
	Error    string    `json:"error,omitempty"`
	Failed   []string  `json:"failed,omitempty"` // "ref: why"
}

var vulnState = struct {
	sync.Mutex
	cache  map[string]*vulnEntry // profile|imageID
	scans  map[string]*VulnScan  // profile
	dbAt   time.Time             // when Trivy's database was last brought up to date
	loaded map[string]bool       // profiles whose saved results were read
}{cache: map[string]*vulnEntry{}, scans: map[string]*VulnScan{}, loaded: map[string]bool{}}

var imageIDRe = regexp.MustCompile(`^(sha256:)?[a-f0-9]{12,64}$`)

// findTrivy looks on PATH and in Homebrew's folders: a LaunchAgent's PATH is short.
func findTrivy() string {
	if p, err := exec.LookPath("trivy"); err == nil {
		return p
	}
	for _, p := range []string{"/opt/homebrew/bin/trivy", "/usr/local/bin/trivy"} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

func (s *Server) vulnFile(p string) string {
	if s.opt.DataDir == "" {
		return ""
	}
	return filepath.Join(s.opt.DataDir, "vulns", p+".json")
}

// loadVulns reads a profile's saved results once.
func (s *Server) loadVulns(p string) {
	vulnState.Lock()
	defer vulnState.Unlock()
	if vulnState.loaded[p] {
		return
	}
	vulnState.loaded[p] = true
	f := s.vulnFile(p)
	if f == "" {
		return
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return
	}
	var saved map[string]*vulnEntry
	if json.Unmarshal(b, &saved) != nil {
		return
	}
	for id, e := range saved {
		if e != nil && e.Report != nil && time.Since(e.ScannedAt) < 7*vulnTTL {
			vulnState.cache[p+"|"+id] = e
		}
	}
}

// saveVulns writes a profile's results; call with vulnState held.
func (s *Server) saveVulns(p string) {
	f := s.vulnFile(p)
	if f == "" {
		return
	}
	out := map[string]*vulnEntry{}
	for k, e := range vulnState.cache {
		if id, ok := strings.CutPrefix(k, p+"|"); ok {
			out[id] = e
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(f), 0o700)
	tmp := f + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, f)
	}
}

type vulnTarget struct {
	ID         string          `json:"id"`
	Ref        string          `json:"ref"`
	Containers []vulnContainer `json:"containers"`
}

type vulnContainer struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
}

// vulnTargets lists the images to scan: those the containers use, or with all every image.
func vulnTargets(ctx context.Context, dc *docker.Client, all bool) ([]*vulnTarget, error) {
	cs, err := dc.Containers(ctx)
	if err != nil {
		return nil, err
	}
	imgs, err := dc.Images(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]*vulnTarget{}
	var order []*vulnTarget
	add := func(id, ref string) *vulnTarget {
		if t := byID[id]; t != nil {
			return t
		}
		t := &vulnTarget{ID: id, Ref: ref}
		byID[id] = t
		order = append(order, t)
		return t
	}
	tagOf := map[string]string{}
	for _, im := range imgs {
		for _, tag := range im.RepoTags {
			if tag != "<none>:<none>" {
				tagOf[im.ID] = tag
				break
			}
		}
	}
	for _, c := range cs {
		if c.Labels[docker.HelperLabel] != "" || c.ImageID == "" {
			continue
		}
		ref := tagOf[c.ImageID]
		if ref == "" {
			ref = c.Image
		}
		t := add(c.ImageID, ref)
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		t.Containers = append(t.Containers, vulnContainer{Name: name, Running: c.State == "running"})
	}
	if all {
		for _, im := range imgs {
			ref := tagOf[im.ID]
			if ref == "" {
				ref = im.ID
			}
			add(im.ID, ref)
		}
	}
	return order, nil
}

// GET /api/profiles/{p}/vulns[?all=1] -> {trivy, scan, images: [{id, ref, containers, summary, scannedAt}]}
func (s *Server) apiVulns(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	p := r.PathValue("p")
	s.loadVulns(p)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	targets, err := vulnTargets(ctx, dc, r.URL.Query().Get("all") == "1")
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	type row struct {
		*vulnTarget
		Summary   *vuln.Summary `json:"summary"`
		ScannedAt *time.Time    `json:"scannedAt"`
		Stale     bool          `json:"stale"`
	}
	rows := make([]row, 0, len(targets))
	vulnState.Lock()
	for _, t := range targets {
		rw := row{vulnTarget: t}
		if e := vulnState.cache[p+"|"+t.ID]; e != nil {
			sum := e.Report.Summarise()
			at := e.ScannedAt
			rw.Summary, rw.ScannedAt, rw.Stale = &sum, &at, time.Since(at) > vulnTTL
		}
		rows = append(rows, rw)
	}
	var scan VulnScan
	if st := vulnState.scans[p]; st != nil {
		scan = *st
		scan.Failed = append([]string(nil), st.Failed...)
	}
	vulnState.Unlock()
	sort.SliceStable(rows, func(i, j int) bool { return len(rows[i].Containers) > len(rows[j].Containers) })
	writeJSON(w, http.StatusOK, map[string]any{"trivy": findTrivy() != "", "scan": scan, "images": rows})
}

// GET /api/profiles/{p}/vulns/{id} -> the full report for one image.
func (s *Server) apiVulnImage(w http.ResponseWriter, r *http.Request) {
	p, id := r.PathValue("p"), r.PathValue("id")
	if _, err := s.dockerFor(p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.loadVulns(p)
	vulnState.Lock()
	e := vulnState.cache[p+"|"+id]
	vulnState.Unlock()
	if e == nil {
		writeError(w, http.StatusNotFound, errors.New("this image has not been scanned yet"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ref": e.Ref, "scannedAt": e.ScannedAt, "report": e.Report})
}

// POST /api/profiles/{p}/vulns/scan {all, force, ids} starts a scan in the background.
func (s *Server) apiVulnScan(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	var body struct {
		All   bool     `json:"all"`
		Force bool     `json:"force"`
		IDs   []string `json:"ids"` // just these images
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	trivy := findTrivy()
	if trivy == "" {
		writeError(w, http.StatusConflict, errors.New("Trivy is not installed; install it first (brew install trivy)"))
		return
	}
	p := r.PathValue("p")
	s.loadVulns(p)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	targets, err := vulnTargets(ctx, dc, body.All || len(body.IDs) > 0)
	cancel()
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	if len(body.IDs) > 0 {
		want := map[string]bool{}
		for _, id := range body.IDs {
			if !imageIDRe.MatchString(id) {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid image id %q", id))
				return
			}
			want[id] = true
		}
		var keep []*vulnTarget
		for _, t := range targets {
			if want[t.ID] {
				keep = append(keep, t)
			}
		}
		targets = keep
	}
	vulnState.Lock()
	if st := vulnState.scans[p]; st != nil && st.Running {
		vulnState.Unlock()
		writeError(w, http.StatusConflict, errors.New("a scan is already running"))
		return
	}
	var todo []*vulnTarget
	for _, t := range targets {
		e := vulnState.cache[p+"|"+t.ID]
		if body.Force || len(body.IDs) > 0 || e == nil || time.Since(e.ScannedAt) > vulnTTL {
			todo = append(todo, t)
		}
	}
	st := &VulnScan{Running: true, Phase: "db", Total: len(todo), Started: time.Now()}
	vulnState.scans[p] = st
	needDB := time.Since(vulnState.dbAt) > 6*time.Hour
	vulnState.Unlock()

	sock := filepath.Join(s.opt.ColimaHome, p, "docker.sock")
	go s.runVulnScan(p, trivy, sock, todo, needDB, st)
	writeJSON(w, http.StatusAccepted, map[string]any{"total": len(todo)})
}

func (s *Server) runVulnScan(p, trivy, sock string, todo []*vulnTarget, needDB bool, st *VulnScan) {
	set := func(f func()) { vulnState.Lock(); f(); vulnState.Unlock() }
	defer set(func() { st.Running, st.Current, st.Finished = false, "", time.Now() })
	env := append(os.Environ(), "DOCKER_HOST=unix://"+sock)
	if len(todo) == 0 {
		return
	}
	if needDB {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		cmd := exec.CommandContext(ctx, trivy, "image", "--download-db-only", "--quiet")
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			set(func() { st.Error = "Trivy could not update its vulnerability database: " + lastLine(out, err) })
			return
		}
		set(func() { vulnState.dbAt = time.Now() })
	}
	set(func() { st.Phase = "images" })
	for _, t := range todo {
		set(func() { st.Current = t.Ref })
		rep, err := trivyScan(trivy, env, t.Ref)
		set(func() {
			st.Done++
			if err != nil {
				st.Failed = append(st.Failed, t.Ref+": "+err.Error())
				return
			}
			vulnState.cache[p+"|"+t.ID] = &vulnEntry{Report: rep, ScannedAt: time.Now(), Ref: t.Ref}
			s.saveVulns(p)
		})
	}
}

func trivyScan(trivy string, env []string, ref string) (*vuln.Report, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, trivy, "image", "--scanners", "vuln", "--format", "json", "--quiet",
		"--image-src", "docker", "--skip-db-update", "--", ref)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, errors.New(lastLine(stderr.Bytes(), err))
	}
	return vuln.Parse(stdout.Bytes())
}

// lastLine is the last useful line of a tool's output, else the error.
func lastLine(out []byte, err error) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			if len(l) > 300 {
				l = l[:300] + "…"
			}
			return l
		}
	}
	return err.Error()
}
