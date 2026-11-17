package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/alerts"
)

// GET /api/alerts -> {alerts, unread, settings, menuBarAvailable}
func (s *Server) apiAlerts(w http.ResponseWriter, r *http.Request) {
	if s.opt.Alerts == nil {
		writeError(w, http.StatusNotFound, errors.New("alerts are off"))
		return
	}
	list, unread := s.opt.Alerts.Recent()
	writeJSON(w, http.StatusOK, map[string]any{"alerts": list, "unread": unread, "settings": s.opt.Alerts.Settings(), "menuBarAvailable": s.opt.MenuBarAvailable})
}

// POST /api/alerts/settings {notify, menuBar}
func (s *Server) apiAlertSettings(w http.ResponseWriter, r *http.Request) {
	if s.opt.Alerts == nil {
		writeError(w, http.StatusNotFound, errors.New("alerts are off"))
		return
	}
	var req alerts.Settings
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.opt.Alerts.Save(req); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

// POST /api/alerts/read
func (s *Server) apiAlertsRead(w http.ResponseWriter, r *http.Request) {
	if s.opt.Alerts != nil {
		s.opt.Alerts.MarkRead()
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/menubar: everything the menu bar icon shows, in one call.
func (s *Server) apiMenuBar(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	type prof struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		Kubernetes bool   `json:"kubernetes"`
		Busy       string `json:"busy,omitempty"`
	}
	out := map[string]any{"profiles": []prof{}}
	if list, err := s.opt.Colima.List(ctx); err == nil {
		ps := []prof{}
		for _, p := range list {
			ps = append(ps, prof{Name: p.Name, Status: p.Status, Kubernetes: strings.Contains(p.Runtime, "k3s"), Busy: p.Busy})
		}
		out["profiles"] = ps
	} else {
		out["error"] = err.Error()
	}
	if s.opt.Alerts != nil {
		list, unread := s.opt.Alerts.Recent()
		if len(list) > 5 {
			list = list[:5]
		}
		out["alerts"], out["unread"] = list, unread
	}
	writeJSON(w, http.StatusOK, out)
}
