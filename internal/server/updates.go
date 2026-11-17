package server

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Routes for version checks and updates. Every one needs the session like the rest of the API.
func (s *Server) updateRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/updates", s.auth(s.apiUpdates))
	m.HandleFunc("POST /api/updates/check", s.auth(s.apiUpdatesCheck))
	m.HandleFunc("POST /api/updates/settings", s.auth(s.apiUpdatesSettings))
	m.HandleFunc("POST /api/updates/install", s.auth(s.apiUpdatesInstall))
	m.HandleFunc("POST /api/updates/colima", s.auth(s.apiUpdateColima))
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

// apiUpdateColima runs brew upgrade colima as a job. Running VMs keep running on the old
// version until they are restarted.
func (s *Server) apiUpdateColima(w http.ResponseWriter, r *http.Request) {
	brew, err := exec.LookPath("brew")
	if err != nil {
		writeError(w, http.StatusConflict, errors.New("Homebrew is not installed"))
		return
	}
	real, err := filepath.EvalSymlinks(s.opt.Colima.Bin)
	if err != nil || !strings.Contains(real, "/Cellar/") {
		writeError(w, http.StatusConflict, errors.New("colima was not installed with Homebrew"))
		return
	}
	const key = "_upgrade"
	if err := s.opt.Colima.Claim(key, "upgrade"); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	job := s.jobs.Start("brew upgrade colima", "", func(j *Job) error {
		defer s.opt.Colima.Release(key)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, brew, "upgrade", "colima")
		cmd.Stdout, cmd.Stderr = j, j
		cmd.Env = append(cmd.Environ(), "HOMEBREW_NO_AUTO_UPDATE=", "HOMEBREW_NO_ENV_HINTS=1")
		if err := cmd.Run(); err != nil {
			return err
		}
		j.Line("Done. Running profiles keep the old version until you restart them.")
		if s.opt.Updater != nil {
			s.opt.Updater.Check(context.Background(), true)
		}
		return nil
	})
	writeJSON(w, http.StatusAccepted, job.snapshot())
}
