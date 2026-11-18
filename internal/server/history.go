package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/history"
)

// Routes for the 30 day history: runs and tests, builds, and long-term cost and disk use.
func (s *Server) historyRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/profiles/{p}/runs", s.auth(s.apiRuns))
	m.HandleFunc("GET /api/profiles/{p}/builds", s.auth(s.apiBuilds))
	m.HandleFunc("GET /api/profiles/{p}/history", s.auth(s.apiHistory))
	m.HandleFunc("GET /api/profiles/{p}/report", s.auth(s.apiReport))
}

// historyArgs validates the profile and reads ?days= (1 to 30, default 30).
func (s *Server) historyArgs(w http.ResponseWriter, r *http.Request) (string, time.Time, bool) {
	p := r.PathValue("p")
	if !colima.ValidProfile(p) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid profile name %q", p))
		return "", time.Time{}, false
	}
	if s.opt.History == nil {
		writeError(w, http.StatusNotFound, errors.New("history is not kept in this build"))
		return "", time.Time{}, false
	}
	days := 30
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && d >= 1 && d <= 30 {
		days = d
	}
	return p, time.Now().Add(-time.Duration(days) * 24 * time.Hour), true
}

func (s *Server) recording(p string) bool {
	for _, w := range s.opt.History.Watching() {
		if w == p {
			return true
		}
	}
	return false
}

func (s *Server) apiRuns(w http.ResponseWriter, r *http.Request) {
	p, since, ok := s.historyArgs(w, r)
	if !ok {
		return
	}
	runs := s.opt.History.Store.Runs(p, since)
	if exportHistory(w, r, p, rangeName("runs", since), runs, runCSV) {
		return
	}
	recent := runs[max(0, len(runs)-200):]
	// newest first for the table
	for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
		recent[i], recent[j] = recent[j], recent[i]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jobs": history.SummariseRuns(runs), "recent": recent, "total": len(runs), "recording": s.recording(p),
	})
}

func (s *Server) apiBuilds(w http.ResponseWriter, r *http.Request) {
	p, since, ok := s.historyArgs(w, r)
	if !ok {
		return
	}
	if s.recording(p) {
		s.opt.History.PollBuilds(r.Context(), p)
	}
	builds := s.opt.History.Store.Builds(p, since)
	if exportHistory(w, r, p, rangeName("builds", since), builds, buildCSV) {
		return
	}
	recent := builds[max(0, len(builds)-200):]
	for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
		recent[i], recent[j] = recent[j], recent[i]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"names": history.SummariseBuilds(builds), "recent": recent, "total": len(builds),
		"recording": s.recording(p), "error": s.opt.History.BuildError(p),
	})
}

func (s *Server) apiHistory(w http.ResponseWriter, r *http.Request) {
	p, since, ok := s.historyArgs(w, r)
	if !ok {
		return
	}
	points := s.opt.History.Store.Points(p, since)
	if exportHistory(w, r, p, rangeName("history", since), points, pointCSV) {
		return
	}
	week := s.opt.History.Store.Points(p, time.Now().Add(-7*24*time.Hour))
	writeJSON(w, http.StatusOK, map[string]any{
		"points": points, "forecast": history.DiskForecast(week), "recording": s.recording(p),
	})
}
