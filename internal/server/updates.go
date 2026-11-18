package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/update"
)

// Routes for version checks and updates. Every one needs the session like the rest of the API.
func (s *Server) updateRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/updates", s.auth(s.apiUpdates))
	m.HandleFunc("POST /api/updates/check", s.auth(s.apiUpdatesCheck))
	m.HandleFunc("POST /api/updates/settings", s.auth(s.apiUpdatesSettings))
	m.HandleFunc("POST /api/updates/install", s.auth(s.apiUpdatesInstall))
	m.HandleFunc("POST /api/updates/colima", s.auth(s.apiUpdateColima))
	m.HandleFunc("POST /api/updates/brew", s.auth(s.apiUpdateBrew))
	m.HandleFunc("POST /api/install", s.auth(s.apiInstall))
	m.HandleFunc("POST /api/profiles/{p}/runtime-update", s.auth(s.apiUpdateRuntime))
}

func (s *Server) apiUpdates(w http.ResponseWriter, r *http.Request) {
	u := s.opt.Updater
	if u == nil {
		writeError(w, http.StatusNotFound, errors.New("updates are not available in this build"))
		return
	}
	st := u.Snapshot()
	if st.CheckedAt.IsZero() {
		st = u.Check(r.Context(), false)
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) apiUpdatesCheck(w http.ResponseWriter, r *http.Request) {
	if s.opt.Updater == nil {
		writeError(w, http.StatusNotFound, errors.New("updates are not available in this build"))
		return
	}
	writeJSON(w, http.StatusOK, s.opt.Updater.Check(r.Context(), true))
}

func (s *Server) apiUpdatesSettings(w http.ResponseWriter, r *http.Request) {
	if s.opt.Updater == nil {
		writeError(w, http.StatusNotFound, errors.New("updates are not available in this build"))
		return
	}
	var body struct {
		Auto *bool `json:"auto"`
	}
	if err := readJSON(r, &body); err != nil || body.Auto == nil {
		writeError(w, http.StatusBadRequest, errors.New(`send {"auto": true|false}`))
		return
	}
	if err := s.opt.Updater.SetAuto(*body.Auto); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.opt.Updater.Snapshot())
}

// apiUpdatesInstall starts a self-update in the background; the page polls GET /api/updates.
func (s *Server) apiUpdatesInstall(w http.ResponseWriter, r *http.Request) {
	u := s.opt.Updater
	if u == nil {
		writeError(w, http.StatusNotFound, errors.New("updates are not available in this build"))
		return
	}
	if st := u.Snapshot(); !st.Dashboard.CanUpdate {
		writeError(w, http.StatusConflict, errors.New(st.Dashboard.Reason))
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := u.Install(ctx); err != nil {
			s.opt.Logger.Printf("update: %v", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, u.Snapshot())
}

// apiUpdateColima runs brew upgrade colima as a job (kept for the menu bar and older pages).
func (s *Server) apiUpdateColima(w http.ResponseWriter, r *http.Request) {
	s.brewUpgrade(w, []string{"colima"})
}

// apiUpdateBrew runs brew upgrade for the formulae sent ({"formulae": ["docker", "lima"]}), each
// of which must be a known tool installed with Homebrew.
func (s *Server) apiUpdateBrew(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Formulae []string `json:"formulae"`
	}
	if err := readJSON(r, &body); err != nil || len(body.Formulae) == 0 {
		writeError(w, http.StatusBadRequest, errors.New(`send {"formulae": ["name", ...]}`))
		return
	}
	s.brewUpgrade(w, body.Formulae)
}

// installable is every formula a Diagnostics fix may install in place.
var installable = map[string]bool{
	"kubernetes-cli": true, "docker": true, "docker-buildx": true,
	"docker-compose": true, "docker-credential-helper": true,
}

// apiInstall runs brew install for one formula a Diagnostics check found missing.
func (s *Server) apiInstall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Formula string `json:"formula"`
	}
	if err := readJSON(r, &body); err != nil || !installable[body.Formula] {
		writeError(w, http.StatusBadRequest, errors.New("not a tool the dashboard installs"))
		return
	}
	brew, err := exec.LookPath("brew")
	if err != nil {
		writeError(w, http.StatusConflict, errors.New("Homebrew is not installed"))
		return
	}
	const key = "_upgrade"
	if err := s.opt.Colima.Claim(key, "install"); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	job := s.jobs.Start("brew install "+body.Formula, "", func(j *Job) error {
		defer s.opt.Colima.Release(key)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, brew, "install", body.Formula)
		cmd.Stdout, cmd.Stderr = j, j
		cmd.Env = append(cmd.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ENV_HINTS=1")
		if err := cmd.Run(); err != nil {
			return err
		}
		j.Line("Done.")
		return nil
	})
	writeJSON(w, http.StatusAccepted, job.snapshot())
}

func (s *Server) brewUpgrade(w http.ResponseWriter, formulae []string) {
	brew, err := exec.LookPath("brew")
	if err != nil {
		writeError(w, http.StatusConflict, errors.New("Homebrew is not installed"))
		return
	}
	seen := map[string]bool{}
	var list []string
	for _, f := range formulae {
		if !update.KnownFormula(f) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("%q is not a tool the dashboard updates", f))
			return
		}
		if !seen[f] {
			seen[f] = true
			list = append(list, f)
		}
	}
	if seen["colima"] {
		real, err := filepath.EvalSymlinks(s.opt.Colima.Bin)
		if err != nil || !strings.Contains(real, "/Cellar/") {
			writeError(w, http.StatusConflict, errors.New("colima was not installed with Homebrew"))
			return
		}
	}
	const key = "_upgrade"
	if err := s.opt.Colima.Claim(key, "upgrade"); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	job := s.jobs.Start("brew upgrade "+strings.Join(list, " "), "", func(j *Job) error {
		defer s.opt.Colima.Release(key)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, brew, append([]string{"upgrade"}, list...)...)
		cmd.Stdout, cmd.Stderr = j, j
		cmd.Env = append(cmd.Environ(), "HOMEBREW_NO_AUTO_UPDATE=", "HOMEBREW_NO_ENV_HINTS=1")
		if err := cmd.Run(); err != nil {
			return err
		}
		if seen["colima"] || seen["lima"] {
			j.Line("Done. Running profiles keep the old version until you restart them.")
		} else {
			j.Line("Done.")
		}
		if s.opt.Updater != nil {
			s.opt.Updater.Check(context.Background(), true)
		}
		return nil
	})
	writeJSON(w, http.StatusAccepted, job.snapshot())
}

// apiUpdateRuntime runs colima update for a profile: Colima updates the container runtime
// (Docker Engine) inside the VM. Containers stop while the engine restarts.
func (s *Server) apiUpdateRuntime(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("p")
	if !colima.ValidProfile(p) {
		writeError(w, http.StatusBadRequest, errors.New("invalid profile name"))
		return
	}
	if err := s.opt.Colima.Claim(p, "update"); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	job := s.jobs.Start("Update the container runtime in "+p, p, func(j *Job) error {
		defer s.opt.Colima.Release(p)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, s.opt.Colima.Bin, "update", "--profile", p)
		cmd.Stdout, cmd.Stderr = j, j
		if err := cmd.Run(); err != nil {
			return err
		}
		j.Line("Done.")
		return nil
	})
	writeJSON(w, http.StatusAccepted, job.snapshot())
}
