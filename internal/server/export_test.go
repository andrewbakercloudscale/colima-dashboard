package server

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/history"
)

func withHistory(t *testing.T, s *Server) *history.Store {
	t.Helper()
	st, err := history.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.opt.History = &history.Recorder{Store: st}
	now := time.Now()
	_ = st.AddRun("default", history.Run{T: now.Add(-time.Hour), Name: "app-tests-1", Job: "app/tests", Outcome: "pass", Seconds: 12})
	_ = st.AddRun("default", history.Run{T: now.Add(-30 * time.Minute), Name: "app-tests-2", Job: "app/tests", Outcome: "fail", Exit: 1, Seconds: 13, Image: `img,"quoted"`})
	_ = st.AddBuild("default", history.Build{T: now.Add(-time.Hour), Name: "app", Status: "Completed", Seconds: 40, Steps: 10, Cached: 7})
	cpu := 12.5
	_ = st.AddPoint("default", history.Point{T: now.Add(-10 * time.Minute), HostCPU: &cpu, HostMem: 1 << 30, DiskMac: 5e9, DiskSize: 1e11, Running: 2})
	_ = st.AddPoint("default", history.Point{T: now.Add(-5 * time.Minute), HostCPU: &cpu, HostMem: 1 << 30, DiskMac: 5e9, DiskSize: 1e11, Running: 3})
	return st
}

func TestRunsCSVExport(t *testing.T) {
	s := newTest(t)
	withHistory(t, s)
	w := do(s, "GET", "/api/profiles/default/runs?days=7&format=csv", withCookie)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("got %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="default-runs-7d-`) || !strings.HasSuffix(cd, `.csv"`) {
		t.Fatalf("disposition %q", cd)
	}
	rows, err := csv.NewReader(w.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0][0] != "stopped" || rows[2][4] != `img,"quoted"` || rows[2][9] != "fail" {
		t.Fatalf("rows %q", rows)
	}
	// JSON is the full list, not the summary
	w = do(s, "GET", "/api/profiles/default/builds?format=json", withCookie)
	var builds []history.Build
	if err := json.Unmarshal(w.Body.Bytes(), &builds); err != nil || len(builds) != 1 || builds[0].Cached != 7 {
		t.Fatalf("builds json %v %s", err, w.Body.String())
	}
	w = do(s, "GET", "/api/profiles/default/history?days=1&format=csv", withCookie)
	if lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n"); len(lines) != 3 || !strings.HasPrefix(lines[0], "time,host_cpu_pct") {
		t.Fatalf("points csv %q", lines)
	}
	if w := do(s, "GET", "/api/profiles/default/runs?format=csv", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("export without cookie: %d", w.Code)
	}
}

func TestReportEndpoint(t *testing.T) {
	s := newTest(t)
	withHistory(t, s)
	w := do(s, "GET", "/api/profiles/default/report?days=1", withCookie)
	var out struct {
		Report history.Report `json:"report"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Report.Days != 1 || out.Report.Tests.Runs != 2 || out.Report.Builds.Builds != 1 {
		t.Fatalf("report %v %s", err, w.Body.String())
	}
	w = do(s, "GET", "/api/profiles/default/report?format=md", withCookie)
	if !strings.Contains(w.Body.String(), "Last 7 days") || !strings.Contains(w.Header().Get("Content-Disposition"), "default-report-7d-") {
		t.Fatalf("markdown %s %s", w.Header().Get("Content-Disposition"), w.Body.String())
	}
}

func TestPrometheusOptIn(t *testing.T) {
	s := newTest(t)
	s.opt.DataDir = t.TempDir()
	withHistory(t, s)
	if err := os.MkdirAll(filepath.Join(s.opt.ColimaHome, "default"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(s.opt.ColimaHome, "default", "colima.yaml"), []byte("cpu: 2\n"), 0o600)

	if w := do(s, "GET", "/metrics", nil); w.Code != http.StatusNotFound {
		t.Fatalf("off, no cookie: %d", w.Code)
	}
	if w := do(s, "GET", "/metrics", withCookie); w.Code != http.StatusOK {
		t.Fatalf("off, with cookie: %d", w.Code)
	}
	w := do(s, "POST", "/api/prometheus", func(r *http.Request) {
		withCookie(r)
		r.Header.Set("Origin", "http://127.0.0.1:7766")
		r.Body = io.NopCloser(strings.NewReader(`{"enabled":true}`))
		r.ContentLength = int64(len(`{"enabled":true}`))
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatalf("turn on: %d %s", w.Code, w.Body.String())
	}
	w = do(s, "GET", "/metrics", nil)
	body := w.Body.String()
	for _, want := range []string{`colima_dashboard_info{version="test"} 1`, `colima_profile_up{profile="default"} 0`,
		`colima_history_runs{outcome="fail",profile="default"} 1`, `colima_history_builds{profile="default",status="completed"} 1`, "# TYPE colima_profile_up gauge"} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q:\n%s", want, body)
		}
	}
	// a second server reads the setting back from disk
	s2 := newTest(t)
	s2.opt.DataDir = s.opt.DataDir
	if !s2.promEnabled() {
		t.Fatal("setting not kept")
	}
	// the Host check still applies
	if w := do(s, "GET", "/metrics", func(r *http.Request) { r.Host = "evil.example:7766" }); w.Code != http.StatusMisdirectedRequest {
		t.Fatalf("foreign host: %d", w.Code)
	}
}

func TestSupportBundle(t *testing.T) {
	s := newTest(t)
	home, _ := os.UserHomeDir()
	if err := os.MkdirAll(filepath.Join(s.opt.ColimaHome, "default"), 0o700); err != nil {
		t.Fatal(err)
	}
	yaml := "cpu: 2\nenv:\n  NPM_TOKEN: npm_s3cret\nmounts:\n  - location: " + home + "/src\n"
	_ = os.WriteFile(filepath.Join(s.opt.ColimaHome, "default", "colima.yaml"), []byte(yaml), 0o600)
	lima := filepath.Join(s.opt.ColimaHome, "_lima", "colima")
	_ = os.MkdirAll(lima, 0o700)
	var log strings.Builder
	for i := range 2500 {
		log.WriteString("line ")
		log.WriteString(strings.Repeat("x", i%7))
		log.WriteString(" token=" + token + "\n")
	}
	_ = os.WriteFile(filepath.Join(lima, "ha.stderr.log"), []byte(log.String()), 0o600)

	w := do(s, "GET", "/api/profiles/default/support-bundle", withCookie)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
		if strings.Contains(string(b), token) || strings.Contains(string(b), "npm_s3cret") || (len(home) > 1 && strings.Contains(string(b), home+"/")) {
			t.Errorf("%s leaks a secret or the home folder:\n%s", f.Name, b)
		}
	}
	for _, name := range []string{"README.txt", "colima/colima.yaml", "colima/version.txt", "colima/list.json", "logs/ha.stderr.log", "dashboard/version.txt", "diagnostics.json"} {
		if _, ok := files[name]; !ok {
			t.Errorf("bundle lacks %s; has %v", name, keys(files))
		}
	}
	if n := strings.Count(files["logs/ha.stderr.log"], "\n"); n != bundleLogLines {
		t.Errorf("log has %d lines", n)
	}
	if !strings.Contains(files["colima/colima.yaml"], "location: ~/src") || !strings.Contains(files["README.txt"], "What was redacted") {
		t.Errorf("yaml %s\nreadme %s", files["colima/colima.yaml"], files["README.txt"])
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
