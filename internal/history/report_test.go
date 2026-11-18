package history

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestMakeReport(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	at := func(h float64) time.Time { return now.Add(-time.Duration(h * float64(time.Hour))) }
	run := func(h float64, job, outcome string, secs float64) Run {
		return Run{T: at(h), Name: job + "-1", Job: job, Outcome: outcome, Seconds: secs}
	}
	runs := []Run{
		run(50, "app/tests", "pass", 30), run(40, "app/tests", "fail", 32), run(30, "app/tests", "pass", 28),
		run(20, "app/lint", "pass", 5), run(19, "app/lint", "pass", 6),
		run(10, "app/e2e", "fail", 120), run(9, "app/e2e", "oom", 90), run(8, "app/e2e", "fail", 110),
		run(5, "nginx", "stopped", 3600),
	}
	builds := []Build{
		{T: at(30), Name: "app", Status: "Completed", Seconds: 60},
		{T: at(20), Name: "app", Status: "Completed", Seconds: 100},
		{T: at(10), Name: "app", Status: "Error", Seconds: 5},
		{T: at(9), Name: "web", Status: "Completed", Seconds: 20},
		{T: at(8), Name: "broken", Status: "Error", Seconds: 1},
	}
	f := func(v float64) *float64 { return &v }
	var points []Point
	// two hours of samples every 5 minutes, a gap of 10 hours, then one lone sample
	for i := 0; i <= 24; i++ {
		points = append(points, Point{T: at(30).Add(time.Duration(i) * 5 * time.Minute), HostCPU: f(float64(i)), HostMem: uint64(1000 + i),
			DiskMac: uint64(10_000_000_000 + i*1_000_000), DiskSize: 100_000_000_000, Running: i % 3})
	}
	points = append(points, Point{T: at(18), HostCPU: f(50), HostMem: 5000, DiskMac: 10_100_000_000, DiskSize: 100_000_000_000, GuestCPU: f(40), GuestMem: 900})

	r := MakeReport("default", 7, now, runs, builds, points)

	if r.Tests.Runs != 9 || r.Tests.Own != 8 || r.Tests.Passed != 4 || r.Tests.Failed != 3 || r.Tests.OOM != 1 {
		t.Fatalf("counts: %+v", r.Tests)
	}
	if r.Tests.PassRate != 0.5 {
		t.Fatalf("pass rate %v", r.Tests.PassRate)
	}
	if len(r.Tests.Flaky) != 1 || r.Tests.Flaky[0].Job != "app/tests" {
		t.Fatalf("flaky: %+v", r.Tests.Flaky)
	}
	if len(r.Tests.Failing) != 2 || r.Tests.Failing[0].Job != "app/e2e" || r.Tests.Failing[0].Failed != 3 || r.Tests.Failing[1].Job != "app/tests" {
		t.Fatalf("failing: %+v", r.Tests.Failing)
	}
	// slowest by median; the stopped nginx service is not a job with a duration that means anything
	if len(r.Tests.Slowest) != 3 || r.Tests.Slowest[0].Job != "app/e2e" || r.Tests.Slowest[2].Job != "app/lint" {
		t.Fatalf("slowest: %+v", r.Tests.Slowest)
	}
	if len(r.Tests.OOMEvents) != 1 || r.Tests.OOMEvents[0].Job != "app/e2e" {
		t.Fatalf("oom: %+v", r.Tests.OOMEvents)
	}

	if r.Builds.Builds != 5 || r.Builds.Failed != 2 || r.Builds.Total != 180 || r.Builds.Median != 60 {
		t.Fatalf("builds: %+v", r.Builds)
	}
	if len(r.Builds.Slowest) != 2 || r.Builds.Slowest[0].Name != "app" || r.Builds.Slowest[0].Median != 80 || r.Builds.Slowest[0].Max != 100 || r.Builds.Slowest[1].Name != "web" {
		t.Fatalf("slowest builds: %+v", r.Builds.Slowest)
	}

	if r.Disk.Start != 10_000_000_000 || r.Disk.End != 10_100_000_000 || r.Disk.Growth != 100_000_000 || r.Disk.Forecast == nil {
		t.Fatalf("disk: %+v", r.Disk)
	}

	if r.Usage.HostCPU.Peak != 50 || r.Usage.HostCPU.N != 26 || r.Usage.GuestCPU.N != 1 || r.Usage.GuestMem.Peak != 900 {
		t.Fatalf("usage: %+v", r.Usage)
	}
	wantAvg := (300.0 + 50) / 26 // 0..24 sums to 300
	if math.Abs(r.Usage.HostCPU.Avg-wantAvg) > 1e-9 {
		t.Fatalf("avg cpu %v, want %v", r.Usage.HostCPU.Avg, wantAvg)
	}

	// 2 hours of joined samples, plus 5 minutes before the first and 5 minutes for the lone one
	if got, want := r.Uptime.Seconds, (2*time.Hour + 10*time.Minute).Seconds(); got != want {
		t.Fatalf("uptime %v, want %v", got, want)
	}
	if r.Uptime.Fraction <= 0 || r.Uptime.Fraction > 1 {
		t.Fatalf("fraction %v", r.Uptime.Fraction)
	}

	md := r.Markdown()
	for _, want := range []string{"# Colima Dashboard report: default", "| Test pass rate | 50% of 8 runs", "## Flaky jobs", "app/tests", "## Slowest images to build", "| app | 3 | 1 |", "## Out of memory", "app/e2e"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
}

func TestMakeReportEmpty(t *testing.T) {
	now := time.Now()
	r := MakeReport("p", 1, now, nil, nil, nil)
	if r.Tests.PassRate != -1 || r.Uptime.Seconds != 0 || r.Disk.Forecast != nil || r.Tests.Flaky == nil || r.Builds.Slowest == nil {
		t.Fatalf("empty report: %+v", r)
	}
	if md := r.Markdown(); !strings.Contains(md, "No completed builds.") || !strings.Contains(md, "No disk samples") {
		t.Fatalf("empty markdown:\n%s", md)
	}
}

func TestUptimeClipsToWindow(t *testing.T) {
	to := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	from := to.Add(-time.Hour)
	// samples from before the window to its end, every 5 minutes: the whole hour is up
	var pts []Point
	for tm := from.Add(-30 * time.Minute); !tm.After(to); tm = tm.Add(5 * time.Minute) {
		pts = append(pts, Point{T: tm})
	}
	if u := uptime(pts, from, to); u.Seconds != 3600 || u.Fraction != 1 {
		t.Fatalf("uptime %+v", u)
	}
}

func TestFormatters(t *testing.T) {
	cases := map[string]string{Duration(0.25): "250 ms", Duration(42): "42.0 s", Duration(125): "2 min 5 s", Duration(7200): "2.0 h",
		Bytes(999): "999 B", Bytes(1_500_000): "1.5 MB", SignedBytes(-2_000_000_000): "-2.0 GB", Percent(-1): "–", Percent(0.875): "88%"}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
