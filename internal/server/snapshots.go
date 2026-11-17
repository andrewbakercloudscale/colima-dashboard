package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
)

// snapshotProfile checks the profile name, that snapshots are on, and that the VM is stopped.
func (s *Server) snapshotProfile(w http.ResponseWriter, r *http.Request, needStopped bool) (string, bool) {
	p := r.PathValue("p")
	if !colima.ValidProfile(p) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid profile name %q", p))
		return "", false
	}
	if s.opt.Snapshots == nil {
		writeError(w, http.StatusNotFound, errors.New("snapshots are off"))
		return "", false
	}
	if needStopped {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		list, err := s.opt.Colima.List(ctx)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return "", false
		}
		for _, pr := range list {
			if pr.Name == p && !strings.EqualFold(pr.Status, "Stopped") {
				writeError(w, http.StatusConflict, fmt.Errorf("stop profile %s first: a snapshot of a running VM would not be consistent", p))
				return "", false
			}
		}
	}
	return p, true
}

func (s *Server) apiSnapshots(w http.ResponseWriter, r *http.Request) {
	p, ok := s.snapshotProfile(w, r, false)
	if !ok {
		return
	}
	list, err := s.opt.Snapshots.List(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) apiSnapshotTake(w http.ResponseWriter, r *http.Request) {
	p, ok := s.snapshotProfile(w, r, true)
	if !ok {
		return
	}
	var req struct{ Name, Note string }
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Snapshot " + time.Now().Format("2 Jan 15:04")
	}
	if utf8.RuneCountInString(name) > 80 {
		writeError(w, http.StatusBadRequest, errors.New("name is longer than 80 characters"))
		return
	}
	note := strings.TrimSpace(req.Note)
	if utf8.RuneCountInString(note) > 500 {
		writeError(w, http.StatusBadRequest, errors.New("description is longer than 500 characters"))
		return
	}
	s.snapshotJob(w, p, "snapshot", func(ctx context.Context, j *Job) error {
		_, err := s.opt.Snapshots.Take(ctx, j, p, name, note, false)
		return err
	})
}

func (s *Server) apiSnapshotRestore(w http.ResponseWriter, r *http.Request) {
	p, ok := s.snapshotProfile(w, r, true)
	if !ok {
		return
	}
	id := r.PathValue("id")
	s.snapshotJob(w, p, "restore", func(ctx context.Context, j *Job) error {
		return s.opt.Snapshots.Restore(ctx, j, p, id)
	})
}

func (s *Server) apiSnapshotDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.snapshotProfile(w, r, false)
	if !ok {
		return
	}
	if err := s.opt.Snapshots.Delete(p, r.PathValue("id")); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// snapshotJob runs fn as a job while holding the profile, so nothing starts the VM meanwhile.
func (s *Server) snapshotJob(w http.ResponseWriter, p, op string, fn func(context.Context, *Job) error) {
	if err := s.opt.Colima.Claim(p, op); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	job := s.jobs.Start(fmt.Sprintf("colima-dashboard %s (%s)", op, p), p, func(j *Job) error {
		defer s.opt.Colima.Release(p)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		return fn(ctx, j)
	})
	writeJSON(w, http.StatusAccepted, job.snapshot())
}
