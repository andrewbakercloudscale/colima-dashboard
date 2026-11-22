package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Compose from a folder: find the Compose projects on this Mac, show a folder's services and
// their state, and run up, down and the rest with the docker CLI, so the result is exactly
// what `docker compose` in that folder does in Terminal.

func (s *Server) composeRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/compose/find", s.auth(s.apiComposeFind))
	m.HandleFunc("GET /api/profiles/{p}/compose", s.auth(s.apiComposeShow))
	m.HandleFunc("POST /api/profiles/{p}/compose/{action}", s.auth(s.apiComposeAction))
}

// composeFiles are the names docker compose looks for, in its order.
var composeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// skipDirs are never worth descending into when looking for projects.
var skipDirs = map[string]bool{"node_modules": true, "vendor": true, "Library": true, "Applications": true,
	"Pictures": true, "Music": true, "Movies": true, "venv": true, "target": true, "dist": true, "build": true}

// ComposeProject is a folder holding a Compose file.
type ComposeProject struct {
	Dir      string `json:"dir"`
	File     string `json:"file"`
	Modified int64  `json:"modified"`
}

// findCompose walks root to depth levels for Compose files, skipping hidden and bulky folders,
// stopping at limit results or the deadline.
func findCompose(root string, depth, limit int, deadline time.Time) []ComposeProject {
	out := []ComposeProject{}
	base := strings.Count(filepath.Clean(root), string(os.PathSeparator))
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if len(out) >= limit || time.Now().After(deadline) {
			return fs.SkipAll
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if p != root && (strings.HasPrefix(name, ".") || skipDirs[name]) {
			return fs.SkipDir
		}
		if strings.Count(p, string(os.PathSeparator))-base > depth {
			return fs.SkipDir
		}
		for _, f := range composeFiles {
			if fi, err := os.Stat(filepath.Join(p, f)); err == nil && fi.Mode().IsRegular() {
				out = append(out, ComposeProject{Dir: p, File: f, Modified: fi.ModTime().Unix()})
				break
			}
		}
		return nil
	})
	return out
}

// GET /api/compose/find -> {projects, roots}: Compose projects under the usual code folders.
func (s *Server) apiComposeFind(w http.ResponseWriter, _ *http.Request) {
	home := homeDir()
	deadline := time.Now().Add(4 * time.Second)
	seen := map[string]bool{}
	all := []ComposeProject{}
	var roots []string
	// The home folder shallowly, then the usual places code lives more deeply.
	add := func(root string, depth int) {
		if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
			return
		}
		roots = append(roots, root)
		for _, p := range findCompose(root, depth, 300, deadline) {
			if !seen[p.Dir] {
				seen[p.Dir] = true
				all = append(all, p)
			}
		}
	}
	add(home, 1)
	for _, d := range []string{"code", "src", "dev", "Developer", "projects", "Projects", "repos", "git", "github", "work", "workspace", "Desktop", "Documents"} {
		add(filepath.Join(home, d), 4)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Modified > all[j].Modified })
	writeJSON(w, http.StatusOK, map[string]any{"projects": all, "roots": roots, "partial": time.Now().After(deadline)})
}

// composeDir checks a folder from a request: a full path (or ~/...) to a folder with a Compose file.
func composeDir(dir string) (string, string, error) {
	dir = strings.TrimSpace(dir)
	if strings.HasPrefix(dir, "~/") || dir == "~" {
		dir = filepath.Join(homeDir(), strings.TrimPrefix(dir, "~"))
	}
	if !filepath.IsAbs(dir) {
		return "", "", errors.New("give the folder as a full path, such as ~/code/app")
	}
	dir = filepath.Clean(dir)
	if fi, err := os.Stat(dir); err == nil && fi.Mode().IsRegular() { // a Compose file itself
		return filepath.Dir(dir), filepath.Base(dir), nil
	}
	for _, f := range composeFiles {
		if fi, err := os.Stat(filepath.Join(dir, f)); err == nil && fi.Mode().IsRegular() {
			return dir, f, nil
		}
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", "", fmt.Errorf("%s is not a folder on this Mac", dir)
	}
	return "", "", fmt.Errorf("no compose.yaml or docker-compose.yml in %s", dir)
}

// composeCmd is the docker CLI pointed at the profile's engine and the project's file.
func (s *Server) composeCmd(ctx context.Context, p, dir, file string, args ...string) (*exec.Cmd, error) {
	if _, err := s.dockerFor(p); err != nil {
		return nil, err
	}
	bin, err := exec.LookPath("docker")
	if err != nil {
		return nil, errors.New("the docker CLI is not installed: brew install docker docker-compose")
	}
	full := append([]string{"compose", "--project-directory", dir, "-f", filepath.Join(dir, file)}, args...)
	cmd := exec.CommandContext(ctx, bin, full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "DOCKER_HOST=unix://"+filepath.Join(s.opt.ColimaHome, p, "docker.sock"))
	return cmd, nil
}

// ComposeService is one service of a project, with its containers' state.
type ComposeService struct {
	Name       string   `json:"name"`
	Image      string   `json:"image"`
	Build      bool     `json:"build"`
	Ports      []string `json:"ports"`
	State      string   `json:"state"` // running, exited, created, partial or "" (never started)
	Health     string   `json:"health,omitempty"`
	Containers int      `json:"containers"`
	Running    int      `json:"running"`
}

// parseComposeConfig reads `docker compose config --format json`.
func parseComposeConfig(b []byte) (string, []ComposeService, error) {
	var cfg struct {
		Name     string `json:"name"`
		Services map[string]struct {
			Image string `json:"image"`
			Build any    `json:"build"`
			Ports []struct {
				Target    int    `json:"target"`
				Published string `json:"published"`
				HostIP    string `json:"host_ip"`
				Protocol  string `json:"protocol"`
			} `json:"ports"`
		} `json:"services"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", nil, fmt.Errorf("could not read the compose config: %w", err)
	}
	out := []ComposeService{}
	for name, sv := range cfg.Services {
		c := ComposeService{Name: name, Image: sv.Image, Build: sv.Build != nil, Ports: []string{}}
		for _, pt := range sv.Ports {
			if pt.Published == "" {
				c.Ports = append(c.Ports, fmt.Sprintf("%d", pt.Target))
				continue
			}
			c.Ports = append(c.Ports, fmt.Sprintf("%s→%d", pt.Published, pt.Target))
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return cfg.Name, out, nil
}

// applyComposePS adds container state from `docker compose ps -a --format json`, which prints
// either one JSON array or one object per line depending on the compose version.
func applyComposePS(services []ComposeService, b []byte) {
	type row struct {
		Service string `json:"Service"`
		State   string `json:"State"`
		Health  string `json:"Health"`
	}
	var rows []row
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '[' {
		_ = json.Unmarshal(b, &rows)
	} else {
		for _, line := range bytes.Split(b, []byte("\n")) {
			var r row
			if json.Unmarshal(line, &r) == nil && r.Service != "" {
				rows = append(rows, r)
			}
		}
	}
	idx := map[string]*ComposeService{}
	for i := range services {
		idx[services[i].Name] = &services[i]
	}
	for _, r := range rows {
		sv := idx[r.Service]
		if sv == nil {
			continue
		}
		sv.Containers++
		if r.State == "running" {
			sv.Running++
		}
		if r.Health != "" && sv.Health != "unhealthy" {
			sv.Health = r.Health
		}
		if sv.State == "" {
			sv.State = r.State
		}
	}
	for i := range services {
		sv := &services[i]
		switch {
		case sv.Containers == 0:
			sv.State = ""
		case sv.Running == sv.Containers:
			sv.State = "running"
		case sv.Running > 0:
			sv.State = "partial"
		}
	}
}

// GET /api/profiles/{p}/compose?dir=... -> {dir, file, project, services}
func (s *Server) apiComposeShow(w http.ResponseWriter, r *http.Request) {
	dir, file, err := composeDir(r.URL.Query().Get("dir"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	p := r.PathValue("p")
	cmd, err := s.composeCmd(ctx, p, dir, file, "config", "--format", "json")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		writeError(w, http.StatusUnprocessableEntity, errors.New(msg))
		return
	}
	project, services, err := parseComposeConfig(out)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	psErr := ""
	if ps, err := s.composeCmd(ctx, p, dir, file, "ps", "-a", "--format", "json"); err == nil {
		if out, err := ps.Output(); err == nil {
			applyComposePS(services, out)
		} else {
			psErr = "could not read container state: is the VM running?"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"dir": dir, "file": file, "project": project, "services": services, "psError": psErr})
}

// composeActions maps an action to its docker compose arguments.
var composeActions = map[string][]string{
	"up":      {"up", "-d"},
	"rebuild": {"up", "-d", "--build"},
	"pull":    {"pull"},
	"restart": {"restart"},
	"stop":    {"stop"},
	"down":    {"down"},
}

// POST /api/profiles/{p}/compose/{action} {dir, service?} -> job
func (s *Server) apiComposeAction(w http.ResponseWriter, r *http.Request) {
	base, ok := composeActions[r.PathValue("action")]
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown compose action %q", r.PathValue("action")))
		return
	}
	var body struct {
		Dir     string `json:"dir"`
		Service string `json:"service"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	dir, file, err := composeDir(body.Dir)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	args := append([]string{}, base...)
	if body.Service != "" {
		if !validServiceName(body.Service) || r.PathValue("action") == "down" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid service %q for %s", body.Service, r.PathValue("action")))
			return
		}
		args = append(args, body.Service)
	}
	p := r.PathValue("p")
	if _, err := s.composeCmd(r.Context(), p, dir, file); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	title := fmt.Sprintf("compose %s %s", r.PathValue("action"), filepath.Base(dir))
	if body.Service != "" {
		title += " " + body.Service
	}
	job := s.jobs.Start(title, p, func(j *Job) error {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		cmd, err := s.composeCmd(ctx, p, dir, file, append([]string{"--progress", "plain"}, args...)...)
		if err != nil {
			return err
		}
		cmd.Stdout, cmd.Stderr = j, j
		j.Line("$ cd " + dir + " && docker compose " + strings.Join(args, " "))
		return cmd.Run()
	})
	writeJSON(w, http.StatusAccepted, job.snapshot())
}

func validServiceName(s string) bool {
	if s == "" || len(s) > 128 || s[0] == '-' || s[0] == '.' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}
