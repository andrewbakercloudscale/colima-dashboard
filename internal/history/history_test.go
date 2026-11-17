package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func ev(action, id string, t time.Time, attrs map[string]string) event {
	e := event{Type: "container", Action: action, TimeNano: t.UnixNano()}
	e.Actor.ID = id
	e.Actor.Attributes = attrs
	return e
}

func TestTrackerOutcomes(t *testing.T) {
	t0 := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	tr := newTracker(nil)
	compose := map[string]string{"name": "app-tests-1", "image": "app-tests", "exitCode": "1",
		"com.docker.compose.project": "app", "com.docker.compose.service": "tests"}

	// A test container that fails on its own.
	tr.handle(ev("start", "aaaa", t0, nil))
	r, ok := tr.handle(ev("die", "aaaa", t0.Add(42*time.Second), compose))
	if !ok || r.Outcome != "fail" || r.Exit != 1 || r.Seconds != 42 || r.Job != "app/tests" {
		t.Fatalf("fail run: %+v", r)
	}
	// Someone stops a server: not a failure, whatever the exit code.
	tr.handle(ev("start", "bbbb", t0, nil))
	tr.handle(ev("kill", "bbbb", t0.Add(time.Minute), nil))
	r, _ = tr.handle(ev("die", "bbbb", t0.Add(61*time.Second), map[string]string{"image": "nginx:1.27@sha256:abc", "exitCode": "143"}))
	if r.Outcome != "stopped" || r.Job != "nginx:1.27" {
		t.Fatalf("stopped run: %+v", r)
	}
	// Killed for memory.
	tr.handle(ev("start", "cccc", t0, nil))
	tr.handle(ev("oom", "cccc", t0.Add(time.Second), nil))
	r, _ = tr.handle(ev("die", "cccc", t0.Add(2*time.Second), map[string]string{"image": "x", "exitCode": "137"}))
	if r.Outcome != "oom" {
		t.Fatalf("oom run: %+v", r)
	}
	// A restart policy brings it back: the second run is marked.
	tr.handle(ev("start", "cccc", t0.Add(3*time.Second), nil))
	r, _ = tr.handle(ev("die", "cccc", t0.Add(5*time.Second), map[string]string{"image": "x", "exitCode": "0"}))
	if r.Outcome != "pass" || !r.Restarted || r.Seconds != 2 {
		t.Fatalf("restarted run: %+v", r)
	}
	// Already running when watching began: the start time comes from inspect.
	tr = newTracker(func(string) time.Time { return t0 })
	r, _ = tr.handle(ev("die", "dddd", t0.Add(10*time.Second), map[string]string{"image": "y", "exitCode": "0"}))
	if r.Seconds != 10 || r.Outcome != "pass" {
		t.Fatalf("inspected run: %+v", r)
	}
	if _, ok := tr.handle(event{Type: "image", Action: "pull"}); ok {
		t.Fatal("image events are not runs")
	}
}

func TestSummariseRuns(t *testing.T) {
	t0 := time.Now()
	var runs []Run
	for i := 0; i < 25; i++ {
		secs := 10.0
		if i >= 20 {
			secs = 15 // the last five got slower
		}
		out := "pass"
		if i == 22 {
			out = "fail"
		}
		runs = append(runs, Run{T: t0.Add(time.Duration(i) * time.Minute), Job: "app/tests", Outcome: out, Seconds: secs})
	}
	runs = append(runs, Run{T: t0, Job: "app/web", Outcome: "stopped", Seconds: 3600})
	got := SummariseRuns(runs)
	if len(got) != 2 || got[0].Job != "app/tests" {
		t.Fatalf("jobs: %+v", got)
	}
	s := got[0]
	if s.Runs != 25 || s.Fail != 1 || !s.Flaky || s.Slower != 1.5 || s.Median != 10 || len(s.Trend) != 25 {
		t.Errorf("summary: runs %d fail %d flaky %v slower %v median %v trend %d", s.Runs, s.Fail, s.Flaky, s.Slower, s.Median, len(s.Trend))
	}
	if web := got[1]; web.PassRate != -1 || web.Stopped != 1 {
		t.Errorf("a server that was only ever stopped has no pass rate: %+v", web)
	}
}

func TestStoreAndPrune(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_ = s.AddRun("default", Run{T: now.Add(-40 * 24 * time.Hour), Job: "old"})
	_ = s.AddRun("default", Run{T: now.Add(-time.Hour), Job: "new"})
	// A torn line (a crash mid-write) is skipped, not fatal.
	f, _ := os.OpenFile(filepath.Join(s.dir, "default", "runs.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"t":"2026-`)
	f.Close()
	if got := s.Runs("default", time.Time{}); len(got) != 2 {
		t.Fatalf("before prune: %d runs", len(got))
	}
	s.Prune(now)
	got := s.Runs("default", time.Time{})
	if len(got) != 1 || got[0].Job != "new" {
		t.Fatalf("after prune: %+v", got)
	}
	if got := s.Runs("other", time.Time{}); len(got) != 0 {
		t.Fatalf("missing profile: %+v", got)
	}
}

func TestDiskForecast(t *testing.T) {
	t0 := time.Now().Add(-48 * time.Hour)
	var pts []Point
	for h := 0; h <= 48; h++ { // grows 1 GiB a day on a 100 GiB disk, from 50 GiB
		pts = append(pts, Point{T: t0.Add(time.Duration(h) * time.Hour), DiskMac: uint64(50<<30 + float64(h)/24*(1<<30)), DiskSize: 100 << 30})
	}
	f := DiskForecast(pts)
	if f == nil || !f.Reliable || f.Days < 47 || f.Days > 49 {
		t.Fatalf("forecast: %+v", f)
	}
}

func TestParseBuilds(t *testing.T) {
	out := []byte(`{"ref":"default/default/k3x","name":"app","status":"Completed","created_at":"2026-11-17T10:00:00Z","completed_at":"2026-11-17T10:00:12.5Z","total_steps":10,"cached_steps":7}
{"ref":"default/default/run","name":"app","status":"Running","created_at":"2026-11-17T10:01:00Z"}`)
	b := parseBuilds(out)
	if len(b) != 2 || b[0].Ref != "k3x" || b[0].Seconds != 12.5 || b[0].Cached != 7 || !b[1].T.IsZero() {
		t.Fatalf("builds: %+v", b)
	}
}
