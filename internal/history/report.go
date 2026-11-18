package history

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Report summarises a profile's last days: tests, builds, disk, cost and uptime. It is computed
// here, from the history on disk, so the page, the Markdown download and the tests agree.
type Report struct {
	Profile string    `json:"profile"`
	Days    int       `json:"days"`
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`

	Tests  TestReport  `json:"tests"`
	Builds BuildReport `json:"builds"`
	Disk   DiskReport  `json:"disk"`
	Usage  UsageReport `json:"usage"`
	Uptime Uptime      `json:"uptime"`
}

// TestReport covers the container runs.
type TestReport struct {
	Runs     int      `json:"runs"`
	Own      int      `json:"own"` // ended on their own (pass, fail, out of memory)
	Passed   int      `json:"passed"`
	Failed   int      `json:"failed"`
	OOM      int      `json:"oom"`
	PassRate float64  `json:"passRate"` // Passed over Own, 0..1; -1 when no run ended on its own
	Jobs     int      `json:"jobs"`
	Flaky    []JobRow `json:"flaky"`
	Slowest  []JobRow `json:"slowest"`
	Failing  []JobRow `json:"failing"`
	// OOMEvents are the runs killed for memory, newest first (at most 20).
	OOMEvents []Event `json:"oomEvents"`
}

// JobRow is one job in a report list.
type JobRow struct {
	Job      string  `json:"job"`
	Runs     int     `json:"runs"`
	Failed   int     `json:"failed"` // failures plus out of memory
	PassRate float64 `json:"passRate"`
	Median   float64 `json:"median"`
	P90      float64 `json:"p90"`
}

// Event is a dated occurrence: a run killed for memory, say.
type Event struct {
	T    time.Time `json:"t"`
	Name string    `json:"name"`
	Job  string    `json:"job"`
}

// BuildReport covers image builds.
type BuildReport struct {
	Builds  int        `json:"builds"`
	Failed  int        `json:"failed"`
	Total   float64    `json:"total"`  // seconds spent building, completed builds
	Median  float64    `json:"median"` // seconds, completed builds
	Slowest []BuildRow `json:"slowest"`
}

// BuildRow is one image in the slowest list.
type BuildRow struct {
	Name   string  `json:"name"`
	Builds int     `json:"builds"`
	Failed int     `json:"failed"`
	Median float64 `json:"median"`
	Max    float64 `json:"max"`
}

// DiskReport is how the VM's disk image grew over the window.
type DiskReport struct {
	Start    uint64    `json:"start"` // bytes on the Mac at the first sample
	End      uint64    `json:"end"`
	Growth   int64     `json:"growth"`
	Size     uint64    `json:"size"`
	Forecast *Forecast `json:"forecast,omitempty"`
}

// UsageReport is peak and average cost, from the five minute samples.
type UsageReport struct {
	HostCPU  Stat `json:"hostCpu"` // % of one Mac core
	HostMem  Stat `json:"hostMem"` // bytes
	GuestCPU Stat `json:"guestCpu"`
	GuestMem Stat `json:"guestMem"`
	Running  Stat `json:"running"` // running containers
}

// Stat is a peak and an average over the samples that had a value; N is how many did.
type Stat struct {
	Peak float64 `json:"peak"`
	Avg  float64 `json:"avg"`
	N    int     `json:"n"`
}

func (s *Stat) add(v float64) {
	s.Avg = (s.Avg*float64(s.N) + v) / float64(s.N+1)
	s.N++
	if s.N == 1 || v > s.Peak {
		s.Peak = v
	}
}

// Uptime is how long the profile was running (and recorded) in the window.
type Uptime struct {
	Seconds  float64 `json:"seconds"`
	Fraction float64 `json:"fraction"` // of the window, 0..1
}

// reportTop is how long the report's lists are.
const reportTop = 5

// pointGap is the longest gap between two samples that still counts as running: samples come
// every pointEvery, so anything well over that means the profile (or the dashboard) was off.
const pointGap = 16 * time.Minute

// MakeReport computes the report for runs, builds and points already limited to the window
// that ends at now and lasts days.
func MakeReport(profile string, days int, now time.Time, runs []Run, builds []Build, points []Point) Report {
	from := now.Add(-time.Duration(days) * 24 * time.Hour)
	rep := Report{Profile: profile, Days: days, From: from, To: now}
	rep.Tests = testReport(runs)
	rep.Builds = buildReport(builds)
	rep.Disk = diskReport(points)
	rep.Usage = usageReport(points)
	rep.Uptime = uptime(points, from, now)
	return rep
}

func testReport(runs []Run) TestReport {
	t := TestReport{Runs: len(runs), PassRate: -1, Flaky: []JobRow{}, Slowest: []JobRow{}, Failing: []JobRow{}, OOMEvents: []Event{}}
	for i := len(runs) - 1; i >= 0; i-- {
		r := runs[i]
		switch r.Outcome {
		case "pass":
			t.Passed++
		case "fail":
			t.Failed++
		case "oom":
			t.OOM++
			if len(t.OOMEvents) < 20 {
				t.OOMEvents = append(t.OOMEvents, Event{T: r.T, Name: r.Name, Job: r.Job})
			}
		}
	}
	t.Own = t.Passed + t.Failed + t.OOM
	if t.Own > 0 {
		t.PassRate = float64(t.Passed) / float64(t.Own)
	}
	jobs := SummariseRuns(runs)
	t.Jobs = len(jobs)
	row := func(j JobSummary) JobRow {
		return JobRow{Job: j.Job, Runs: j.Runs, Failed: j.Fail + j.OOM, PassRate: j.PassRate, Median: j.Median, P90: j.P90}
	}
	var own []JobSummary // jobs with runs that ended on their own: tests and one-off jobs, not services
	for _, j := range jobs {
		if j.PassRate >= 0 {
			own = append(own, j)
		}
		if j.Flaky {
			t.Flaky = append(t.Flaky, row(j))
		}
	}
	sort.SliceStable(t.Flaky, func(a, b int) bool { return t.Flaky[a].Job < t.Flaky[b].Job })
	sort.SliceStable(own, func(a, b int) bool {
		if own[a].Median != own[b].Median {
			return own[a].Median > own[b].Median
		}
		return own[a].Job < own[b].Job
	})
	for _, j := range own[:min(reportTop, len(own))] {
		t.Slowest = append(t.Slowest, row(j))
	}
	sort.SliceStable(own, func(a, b int) bool {
		fa, fb := own[a].Fail+own[a].OOM, own[b].Fail+own[b].OOM
		if fa != fb {
			return fa > fb
		}
		return own[a].Job < own[b].Job
	})
	for _, j := range own {
		if len(t.Failing) == reportTop || j.Fail+j.OOM == 0 {
			break
		}
		t.Failing = append(t.Failing, row(j))
	}
	return t
}

func buildReport(builds []Build) BuildReport {
	b := BuildReport{Builds: len(builds), Slowest: []BuildRow{}}
	var secs []float64
	for _, x := range builds {
		if x.Status != "Completed" {
			b.Failed++
			continue
		}
		secs = append(secs, x.Seconds)
		b.Total += x.Seconds
	}
	b.Median = quantile(secs, 0.5)
	rows := []BuildRow{}
	for _, s := range SummariseBuilds(builds) {
		if s.Builds == s.Failed {
			continue // never completed: no duration to rank
		}
		r := BuildRow{Name: s.Name, Builds: s.Builds, Failed: s.Failed, Median: s.Median}
		for _, x := range builds {
			if x.Name == s.Name && x.Status == "Completed" && x.Seconds > r.Max {
				r.Max = x.Seconds
			}
		}
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Median != rows[j].Median {
			return rows[i].Median > rows[j].Median
		}
		return rows[i].Name < rows[j].Name
	})
	b.Slowest = append(b.Slowest, rows[:min(reportTop, len(rows))]...)
	return b
}

func diskReport(points []Point) DiskReport {
	var d DiskReport
	for _, p := range points {
		if p.DiskMac == 0 {
			continue
		}
		if d.Start == 0 {
			d.Start = p.DiskMac
		}
		d.End, d.Size = p.DiskMac, p.DiskSize
	}
	d.Growth = int64(d.End) - int64(d.Start)
	d.Forecast = DiskForecast(points)
	return d
}

func usageReport(points []Point) UsageReport {
	var u UsageReport
	for _, p := range points {
		if p.HostCPU != nil {
			u.HostCPU.add(*p.HostCPU)
		}
		if p.HostMem > 0 {
			u.HostMem.add(float64(p.HostMem))
		}
		if p.GuestCPU != nil {
			u.GuestCPU.add(*p.GuestCPU)
		}
		if p.GuestMem > 0 {
			u.GuestMem.add(float64(p.GuestMem))
		}
		u.Running.add(float64(p.Running))
	}
	return u
}

// uptime counts, for each sample, the interval before it: back to the previous sample when that
// is no more than pointGap earlier, else one sample interval. Clipped to [from, to].
func uptime(points []Point, from, to time.Time) Uptime {
	var up time.Duration
	for i, p := range points {
		start := p.T.Add(-pointEvery)
		if i > 0 {
			if gap := p.T.Sub(points[i-1].T); gap <= pointGap {
				start = points[i-1].T
			}
		}
		end := p.T
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		if end.After(start) {
			up += end.Sub(start)
		}
	}
	u := Uptime{Seconds: up.Seconds()}
	if w := to.Sub(from); w > 0 {
		u.Fraction = min(1, up.Seconds()/w.Seconds())
	}
	return u
}

// Markdown renders the report for the "Download as Markdown" button.
func (r Report) Markdown() string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("# Colima Dashboard report: %s\n\n", r.Profile)
	w("Last %s, %s to %s.\n\n", plural(r.Days, "day"), r.From.Local().Format("2 Jan 2006 15:04"), r.To.Local().Format("2 Jan 2006 15:04"))

	w("## Summary\n\n")
	w("| | |\n|---|---|\n")
	w("| Uptime | %s (%s of the time) |\n", Duration(r.Uptime.Seconds), Percent(r.Uptime.Fraction))
	w("| Test pass rate | %s of %s that ended on their own |\n", Percent(r.Tests.PassRate), plural(r.Tests.Own, "run"))
	w("| Runs | %d (%d passed, %d failed, %d out of memory) |\n", r.Tests.Runs, r.Tests.Passed, r.Tests.Failed, r.Tests.OOM)
	w("| Flaky jobs | %d |\n", len(r.Tests.Flaky))
	w("| Builds | %d (%d failed), median %s, %s in all |\n", r.Builds.Builds, r.Builds.Failed, Duration(r.Builds.Median), Duration(r.Builds.Total))
	w("| VM disk growth | %s (%s now) |\n", SignedBytes(r.Disk.Growth), Bytes(float64(r.Disk.End)))
	w("| Mac CPU used by the VM | peak %s, average %s of one core |\n", pctStat(r.Usage.HostCPU.Peak, r.Usage.HostCPU.N), pctStat(r.Usage.HostCPU.Avg, r.Usage.HostCPU.N))
	w("| Mac memory used by the VM | peak %s, average %s |\n", bytesStat(r.Usage.HostMem.Peak, r.Usage.HostMem.N), bytesStat(r.Usage.HostMem.Avg, r.Usage.HostMem.N))
	if r.Usage.GuestCPU.N > 0 {
		w("| CPU inside the VM | peak %s, average %s |\n", pctStat(r.Usage.GuestCPU.Peak, 1), pctStat(r.Usage.GuestCPU.Avg, 1))
	}
	if r.Usage.GuestMem.N > 0 {
		w("| Memory inside the VM | peak %s, average %s |\n", Bytes(r.Usage.GuestMem.Peak), Bytes(r.Usage.GuestMem.Avg))
	}
	w("\n")

	jobTable := func(title string, rows []JobRow, empty string) {
		w("## %s\n\n", title)
		if len(rows) == 0 {
			w("%s\n\n", empty)
			return
		}
		w("| Job | Runs | Failed | Passed | Median | Slowest 10%% |\n|---|---:|---:|---:|---:|---:|\n")
		for _, j := range rows {
			w("| %s | %d | %d | %s | %s | %s |\n", mdCell(j.Job), j.Runs, j.Failed, Percent(j.PassRate), Duration(j.Median), Duration(j.P90))
		}
		w("\n")
	}
	jobTable("Flaky jobs", r.Tests.Flaky, "None: no job both passed and failed in its last 10 runs.")
	jobTable("Most failing jobs", r.Tests.Failing, "No job failed.")
	jobTable("Slowest jobs", r.Tests.Slowest, "No runs ended on their own.")

	w("## Slowest images to build\n\n")
	if len(r.Builds.Slowest) == 0 {
		w("No completed builds.\n\n")
	} else {
		w("| Image | Builds | Failed | Median | Longest |\n|---|---:|---:|---:|---:|\n")
		for _, x := range r.Builds.Slowest {
			name := x.Name
			if name == "" {
				name = "(unnamed)"
			}
			w("| %s | %d | %d | %s | %s |\n", mdCell(name), x.Builds, x.Failed, Duration(x.Median), Duration(x.Max))
		}
		w("\n")
	}

	w("## Out of memory\n\n")
	if len(r.Tests.OOMEvents) == 0 {
		w("No container was killed for memory.\n\n")
	} else {
		for _, e := range r.Tests.OOMEvents {
			w("- %s: %s (%s)\n", e.T.Local().Format("2 Jan 15:04"), mdCell(e.Name), mdCell(e.Job))
		}
		w("\n")
	}

	w("## Disk\n\n")
	if r.Disk.End == 0 {
		w("No disk samples in this window.\n\n")
	} else {
		w("The VM's disk took %s on the Mac at the start and %s at the end (%s), of %s.\n", Bytes(float64(r.Disk.Start)), Bytes(float64(r.Disk.End)), SignedBytes(r.Disk.Growth), Bytes(float64(r.Disk.Size)))
		if f := r.Disk.Forecast; f != nil {
			switch {
			case !f.Reliable:
				w("A forecast needs a day of history.\n")
			case f.Days >= 0:
				w("At %s a day it fills in about %.0f days.\n", Bytes(f.PerDay), f.Days)
			default:
				w("It is not growing.\n")
			}
		}
		w("\n")
	}
	w("Generated by Colima Dashboard from history kept on this Mac.\n")
	return b.String()
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func mdCell(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ") }

func pctStat(v float64, n int) string {
	if n == 0 {
		return "–"
	}
	return fmt.Sprintf("%.1f%%", v)
}

func bytesStat(v float64, n int) string {
	if n == 0 {
		return "–"
	}
	return Bytes(v)
}

// Percent formats a 0..1 fraction; negative means not known.
func Percent(f float64) string {
	if f < 0 {
		return "–"
	}
	return fmt.Sprintf("%.0f%%", f*100)
}

// Duration formats seconds the way the page does.
func Duration(sec float64) string {
	switch {
	case sec <= 0:
		return "–"
	case sec < 1:
		return fmt.Sprintf("%.0f ms", sec*1000)
	case sec < 90:
		return fmt.Sprintf("%.1f s", sec)
	case sec < 5400:
		return fmt.Sprintf("%d min %d s", int(sec)/60, int(sec)%60)
	default:
		return fmt.Sprintf("%.1f h", sec/3600)
	}
}

// Bytes formats a size in decimal units, as the page does.
func Bytes(n float64) string {
	u := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for n >= 1000 && i < len(u)-1 {
		n /= 1000
		i++
	}
	if n >= 100 || i == 0 {
		return fmt.Sprintf("%.0f %s", n, u[i])
	}
	return fmt.Sprintf("%.1f %s", n, u[i])
}

// SignedBytes formats a change in size with its sign.
func SignedBytes(n int64) string {
	if n < 0 {
		return "-" + Bytes(float64(-n))
	}
	return "+" + Bytes(float64(n))
}
