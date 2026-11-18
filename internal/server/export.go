package server

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/history"
)

// Exports: the history routes answer ?format=csv or ?format=json with every record in the range
// as a download, streamed, instead of the page's summary (30 days of runs can be large).

// exportName is <profile>-<what>-<YYYYMMDD-HHMM>.<ext>, the same shape the page uses.
func exportName(profile, what, ext string, now time.Time) string {
	return fmt.Sprintf("%s-%s-%s.%s", profile, what, now.Format("20060102-1504"), ext)
}

// rangeName is what plus the range in days, e.g. "runs-7d", matching the page's file names.
func rangeName(what string, since time.Time) string {
	return fmt.Sprintf("%s-%dd", what, rangeDays(since))
}

func rangeDays(since time.Time) int {
	return int(time.Since(since).Round(time.Hour).Hours() / 24)
}

// csvRow turns one record into its CSV header (when header is true) or values.
type csvRow[T any] func(v T, header bool) []string

// exportHistory writes records as CSV or JSON when ?format= asks for it, and says whether it did.
func exportHistory[T any](w http.ResponseWriter, r *http.Request, profile, what string, records []T, row csvRow[T]) bool {
	format := r.URL.Query().Get("format")
	if format != "csv" && format != "json" {
		return false
	}
	name := exportName(profile, what, format, time.Now())
	h := w.Header()
	h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if format == "json" {
		h.Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		enc := json.NewEncoder(w)
		_, _ = w.Write([]byte("[\n"))
		for i, v := range records {
			if i > 0 {
				_, _ = w.Write([]byte(","))
			}
			if enc.Encode(v) != nil {
				return true
			}
		}
		_, _ = w.Write([]byte("]\n"))
		return true
	}
	h.Set("Content-Type", "text/csv; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	cw := csv.NewWriter(w)
	var zero T
	_ = cw.Write(row(zero, true))
	for i, v := range records {
		if cw.Write(row(v, false)) != nil {
			return true
		}
		if i%500 == 499 {
			cw.Flush()
		}
	}
	cw.Flush()
	return true
}

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func optNum(f *float64) string {
	if f == nil {
		return ""
	}
	return num(*f)
}

func optInt(n *int64) string {
	if n == nil {
		return ""
	}
	return strconv.FormatInt(*n, 10)
}

func runCSV(v history.Run, header bool) []string {
	if header {
		return []string{"stopped", "started", "id", "name", "image", "project", "service", "job", "exit", "outcome", "seconds", "restarted", "unhealthy"}
	}
	return []string{ts(v.T), ts(v.Started), v.ID, v.Name, v.Image, v.Project, v.Service, v.Job, strconv.Itoa(v.Exit), v.Outcome,
		num(v.Seconds), strconv.FormatBool(v.Restarted), strconv.FormatBool(v.Unhealthy)}
}

func buildCSV(v history.Build, header bool) []string {
	if header {
		return []string{"finished", "started", "ref", "name", "status", "seconds", "steps", "cached", "error"}
	}
	return []string{ts(v.T), ts(v.Started), v.Ref, v.Name, v.Status, num(v.Seconds), strconv.Itoa(v.Steps), strconv.Itoa(v.Cached), v.Error}
}

func pointCSV(v history.Point, header bool) []string {
	if header {
		return []string{"time", "host_cpu_pct", "host_mem_bytes", "guest_cpu_pct", "guest_mem_bytes", "disk_on_mac_bytes", "disk_size_bytes",
			"images_bytes", "containers_bytes", "volumes_bytes", "build_cache_bytes", "running"}
	}
	u := func(n uint64) string {
		if n == 0 {
			return ""
		}
		return strconv.FormatUint(n, 10)
	}
	return []string{ts(v.T), optNum(v.HostCPU), u(v.HostMem), optNum(v.GuestCPU), u(v.GuestMem), u(v.DiskMac), u(v.DiskSize),
		optInt(v.Images), optInt(v.Contain), optInt(v.Volumes), optInt(v.Cache), strconv.Itoa(v.Running)}
}

// apiReport returns the report for ?days= (1, 7 or 30; default 7), as JSON or, with
// ?format=md, as a Markdown download.
func (s *Server) apiReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("days") == "" {
		q.Set("days", "7")
		r.URL.RawQuery = q.Encode()
	}
	p, since, ok := s.historyArgs(w, r)
	if !ok {
		return
	}
	now := time.Now()
	days := rangeDays(since)
	st := s.opt.History.Store
	// The period before the window, for "slower than the week before". Records older than Keep
	// are pruned, so a 30 day report has none and compares the halves of its own window.
	before := since.Add(-time.Duration(days) * 24 * time.Hour)
	var prev history.Previous
	runs, builds := []history.Run{}, []history.Build{}
	for _, r := range st.Runs(p, before) {
		if r.T.After(since) {
			runs = append(runs, r)
		} else {
			prev.Runs = append(prev.Runs, r)
		}
	}
	for _, b := range st.Builds(p, before) {
		if b.T.After(since) {
			builds = append(builds, b)
		} else {
			prev.Builds = append(prev.Builds, b)
		}
	}
	rep := history.MakeReport(p, days, now, runs, builds, st.Points(p, since), prev)
	rep.Outdated = s.outdated(r.Context(), p, now)
	if q.Get("format") == "md" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+exportName(p, fmt.Sprintf("report-%dd", days), "md", now)+`"`)
		_, _ = w.Write([]byte(rep.Markdown()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": rep, "recording": s.recording(p)})
}
