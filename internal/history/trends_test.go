package history

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBucketsAlignToLocalClock(t *testing.T) {
	loc := time.FixedZone("SAST", 2*3600)
	now := time.Date(2026, 11, 18, 9, 40, 0, 0, loc)
	hours := bucketTimes(now.Add(-24*time.Hour), now, time.Hour, loc)
	if len(hours) != 25 || !hours[0].Equal(time.Date(2026, 11, 17, 9, 0, 0, 0, loc)) || !hours[24].Equal(time.Date(2026, 11, 18, 9, 0, 0, 0, loc)) {
		t.Fatalf("hours: %d %v .. %v", len(hours), hours[0], hours[len(hours)-1])
	}
	days := bucketTimes(now.Add(-7*24*time.Hour), now, 24*time.Hour, loc)
	if len(days) != 8 || !days[0].Equal(time.Date(2026, 11, 11, 0, 0, 0, 0, loc)) {
		t.Fatalf("days: %d %v", len(days), days[0])
	}
	if i := bucketIndex(days, time.Date(2026, 11, 17, 23, 59, 0, 0, loc)); i != 6 {
		t.Fatalf("index %d", i)
	}
	if i := bucketIndex(days, time.Date(2026, 11, 1, 0, 0, 0, 0, loc)); i != -1 {
		t.Fatalf("before the window: %d", i)
	}
	// A UTC time lands in the local day it falls on: 23:30 UTC on the 17th is 01:30 on the 18th.
	if i := bucketIndex(days, time.Date(2026, 11, 17, 23, 30, 0, 0, time.UTC)); i != 7 {
		t.Fatalf("utc index %d", i)
	}
}

func TestMakeTrends(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	day := func(d int, h int) time.Time { return time.Date(2026, 11, 18-d, h, 0, 0, 0, time.UTC) }
	var runs []Run
	// tests: 4 s for days 6..4, 8 s for days 2..0, so twice as slow in the second half
	for d := 6; d >= 0; d-- {
		secs := 4.0
		if d <= 2 {
			secs = 8
		}
		runs = append(runs, Run{T: day(d, 10), Job: "app/tests", Outcome: "pass", Seconds: secs})
		runs = append(runs, Run{T: day(d, 11), Job: "app/tests", Outcome: "pass", Seconds: secs})
	}
	runs = append(runs,
		Run{T: day(1, 9), Job: "app/e2e", Outcome: "fail", Exit: 1, Seconds: 30},
		Run{T: day(1, 9), Job: "app/e2e", Outcome: "oom", Exit: 137, Seconds: 30},
		Run{T: day(0, 9), Job: "app/e2e", Outcome: "fail", Exit: 1, Seconds: 30},
		Run{T: day(0, 8), Job: "web", Outcome: "stopped", Exit: 0, Seconds: 600, Restarted: true, Unhealthy: true},
	)
	builds := []Build{
		{T: day(3, 10), Name: "app", Status: "Completed", Seconds: 60},
		{T: day(1, 10), Name: "app", Status: "Completed", Seconds: 70},
		{T: day(1, 11), Name: "app", Status: "Error", Seconds: 3, Error: `[2/2] RUN make: process "/bin/sh -c make" did not complete successfully: exit code: 2`},
		{T: day(0, 11), Name: "app", Status: "Error", Seconds: 3, Error: `[2/2] RUN test: process "/bin/sh -c test" did not complete successfully: exit code: 2`},
	}
	f := func(v float64) *float64 { return &v }
	points := []Point{
		{T: day(0, 10), HostCPU: f(10), HostMem: 1000, DiskMac: 500, Running: 2},
		{T: day(0, 10).Add(5 * time.Minute), HostCPU: f(30), HostMem: 3000, DiskMac: 600, Running: 4},
	}
	tr := MakeTrends(7, now, runs, builds, points, Previous{})

	if tr.Bucket != 86400 || len(tr.Buckets) != 8 {
		t.Fatalf("buckets %d of %d s", len(tr.Buckets), tr.Bucket)
	}
	last := tr.Buckets[7] // 18 Nov
	if last.Passed != 2 || last.Failed != 1 || last.Stopped != 1 || last.Restarts != 1 || last.Unhealthy != 1 || last.Builds != 1 || last.BuildsFailed != 1 {
		t.Fatalf("today: %+v", last)
	}
	if b := tr.Buckets[6]; b.OOM != 1 || b.Failed != 1 || b.Builds != 2 {
		t.Fatalf("yesterday: %+v", b)
	}

	// usage: 2 hour buckets, the two samples share one
	var u *UsageBucket
	for i := range tr.Usage {
		if tr.Usage[i].HostCPU != nil {
			if u != nil {
				t.Fatal("samples split across buckets")
			}
			u = &tr.Usage[i]
		}
	}
	if u == nil || *u.HostCPU != 20 || *u.HostCPUPeak != 30 || *u.HostMem != 2000 || *u.DiskMac != 600 || *u.Running != 3 || u.GuestCPU != nil {
		t.Fatalf("usage bucket: %+v", u)
	}

	// duration trends: app/tests has the most runs; e2e has 3 own runs; web was only stopped
	if len(tr.Jobs) != 2 || tr.Jobs[0].Name != "app/tests" || tr.Jobs[1].Name != "app/e2e" {
		t.Fatalf("jobs: %+v", tr.Jobs)
	}
	if m := tr.Jobs[0].Median; m[0] != nil || m[1] == nil || *m[1] != 4 || *m[7] != 8 || tr.Jobs[0].Count[7] != 2 {
		t.Fatalf("app/tests medians: %v", m)
	}
	if len(tr.Images) != 1 || tr.Images[0].Name != "app" || *tr.Images[0].Median[6] != 70 {
		t.Fatalf("images: %+v", tr.Images)
	}

	// halves: 8 s against 4 s is 100% slower; e2e has no runs in the first half
	if len(tr.Regressions) != 1 || tr.Regressions[0].Name != "app/tests" || tr.Regressions[0].Ratio != 2 || tr.Regressions[0].Kind != "test" {
		t.Fatalf("regressions: %+v", tr.Regressions)
	}
	if tr.Than != "than the 3.5 days before" {
		t.Fatalf("than %q", tr.Than)
	}
	if s := RegressionText(tr.Regressions[0], tr.Than); !strings.Contains(s, "100% slower than the 3.5 days before") || !strings.Contains(s, "median 8.0 s, was 4.0 s") {
		t.Fatalf("text %q", s)
	}

	if len(tr.ExitCodes) != 2 || tr.ExitCodes[0].Code != 1 || tr.ExitCodes[0].Count != 2 || tr.ExitCodes[1].Code != 137 || !strings.Contains(tr.ExitCodes[1].Meaning, "out of memory") || tr.ExitCodes[0].Jobs[0] != "app/e2e" {
		t.Fatalf("exit codes: %+v", tr.ExitCodes)
	}
	if len(tr.BuildErrors) != 1 || tr.BuildErrors[0].Reason != "a RUN step failed with exit code 2" || tr.BuildErrors[0].Count != 2 || !strings.Contains(tr.BuildErrors[0].Last, "RUN test") {
		t.Fatalf("build errors: %+v", tr.BuildErrors)
	}
	// e2e: 2 failed + 1 oom (counts double) = 4; web: restart + unhealthy = 2
	if len(tr.Trouble) != 2 || tr.Trouble[0].Job != "app/e2e" || tr.Trouble[0].OOM != 1 || tr.Trouble[1].Job != "web" {
		t.Fatalf("trouble: %+v", tr.Trouble)
	}
}

func TestRegressionsAgainstPreviousWeek(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	from := now.Add(-7 * 24 * time.Hour)
	mk := func(start time.Time, n int, secs float64, job string) []Run {
		var out []Run
		for i := range n {
			out = append(out, Run{T: start.Add(time.Duration(i+1) * time.Hour), Job: job, Outcome: "pass", Seconds: secs})
		}
		return out
	}
	cur := append(mk(from, 4, 140, "integration"), mk(from, 4, 10, "unit")...)
	cur = append(cur, mk(from, 4, 1.2, "lint")...) // 0.2 s slower: under the absolute floor
	prev := Previous{Runs: append(append(mk(from.Add(-7*24*time.Hour), 4, 100, "integration"), mk(from.Add(-7*24*time.Hour), 4, 20, "unit")...), mk(from.Add(-7*24*time.Hour), 4, 1, "lint")...)}
	prev.Builds = []Build{{T: from.Add(-time.Hour), Name: "x", Status: "Completed", Seconds: 5}}
	regs, than := regressions(7, from, now, cur, nil, prev)
	if than != "than the week before" || len(regs) != 2 {
		t.Fatalf("%q %+v", than, regs)
	}
	if regs[0].Name != "integration" || regs[0].Ratio != 1.4 || regs[1].Name != "unit" || regs[1].Ratio != 0.5 {
		t.Fatalf("order: %+v", regs)
	}
	if s := RegressionText(regs[0], than); !strings.HasPrefix(s, "integration (test job) is 40% slower than the week before") {
		t.Fatalf("text %q", s)
	}
	if s := RegressionText(regs[1], than); !strings.Contains(s, "50% faster") {
		t.Fatalf("text %q", s)
	}
}

func TestBuildReason(t *testing.T) {
	cases := map[string]string{
		"": "no error message",
		`process "/bin/sh -c x" did not complete successfully: exit code: 127`:                              "a RUN step failed with exit code 127",
		"failed to solve: failed to read dockerfile: open Dockerfile: no such file or directory: not found": "Dockerfile not found",
		"no space left on device": "no space left on the VM's disk",
		"something odd\nmore":     "something odd",
	}
	for in, want := range cases {
		if got := BuildReason(in); got != want {
			t.Errorf("BuildReason(%q) = %q, want %q", in, got, want)
		}
	}
	if ExitMeaning(141) != "killed by signal 13" || ExitMeaning(3) != "set by the program" {
		t.Fatal("exit meanings")
	}
}

func TestMakeOutdated(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	ago := func(d int) time.Time { return now.Add(-time.Duration(d) * 24 * time.Hour) }
	images := []ImageInfo{
		{ID: "sha256:aaaaaaaaaaaaaaaa", Tags: []string{"redis:latest"}, Created: ago(400), Size: 100, Containers: 1},
		{ID: "sha256:bbbbbbbbbbbbbbbb", Tags: []string{"<none>:<none>"}, Created: ago(100), Size: 50},
		{ID: "sha256:cccccccccccccccc", Tags: []string{"app:1.2", "app:latest"}, Created: ago(3), Size: 30, Containers: 2},
		{ID: "sha256:dddddddddddddddd", Tags: []string{"postgres:16"}, Created: ago(45), Size: 20},
	}
	tools := []VersionInfo{{Label: "Colima", Current: "0.10.3", Latest: "0.10.3"}, {Label: "Docker CLI", Current: "29.1.0", Latest: "29.2.0", Newer: true}}
	o := MakeOutdated(now, images, nil, tools)
	if !o.ImagesKnown || o.Images != 4 || o.Size != 200 {
		t.Fatalf("totals %+v", o)
	}
	if o.OldCount != 2 || o.OldSize != 150 || o.Old[0].Ref != "redis:latest" || o.Old[1].Ref != "<none>" || o.Old[0].ID != "aaaaaaaaaaaa" || o.Old[0].AgeDays != 400 {
		t.Fatalf("old %+v", o.Old)
	}
	if o.LatestCount != 2 || o.Latest[0].Ref != "redis:latest" || o.Latest[1].Ref != "app:latest" {
		t.Fatalf("latest %+v", o.Latest)
	}
	if o.Dangling != 1 || o.DanglingSize != 50 || o.Unused != 2 || o.UnusedSize != 70 {
		t.Fatalf("dangling/unused %+v", o)
	}
	want := []int{1, 0, 1, 1, 1} // 3 days, 45 days, 100 days, 400 days
	for i, a := range o.Ages {
		if a.Count != want[i] {
			t.Fatalf("ages %+v", o.Ages)
		}
	}
	if !o.ToolsKnown || o.Behind != 1 || o.Tools[0].Label != "Docker CLI" {
		t.Fatalf("tools %+v", o.Tools)
	}

	o = MakeOutdated(now, nil, errors.New("no socket"), nil)
	if o.ImagesKnown || o.ImagesError != "no socket" || o.ToolsKnown || o.Old == nil || o.Tools == nil {
		t.Fatalf("unknown %+v", o)
	}
}

func TestReportMarkdownCarriesCharts(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	runs := []Run{
		{T: now.Add(-3 * time.Hour), Job: "app/tests", Outcome: "pass", Seconds: 3},
		{T: now.Add(-2 * time.Hour), Job: "app/tests", Outcome: "fail", Exit: 1, Seconds: 4},
	}
	f := func(v float64) *float64 { return &v }
	points := []Point{{T: now.Add(-time.Hour), HostCPU: f(12), HostMem: 2e9, DiskMac: 3e9}}
	r := MakeReport("default", 1, now, runs, nil, points)
	r.Outdated = MakeOutdated(now, []ImageInfo{{ID: "x", Tags: []string{"old:latest"}, Created: now.Add(-200 * 24 * time.Hour), Size: 1e9}}, nil,
		[]VersionInfo{{Label: "Docker CLI", Current: "1.0.0", Latest: "2.0.0", Newer: true}})
	md := r.Markdown()
	for _, want := range []string{"## Performance changes", "## Activity by hour", "| 50% |", "## Test duration over time", "## Errors", "| 1 | 1 | general error",
		"## Resources over time", "| 12.0% |", "## Out of date", "1 older than 90 days", "| old:latest | 200 days |", "**update available**"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
}
