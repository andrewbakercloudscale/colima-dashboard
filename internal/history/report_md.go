package history

import (
	"fmt"
	"strings"
	"time"
)

// The Markdown download carries the Report page's charts as tables and short summaries.

type mdWriter func(format string, a ...any)

func (r Report) bucketLabel(t time.Time) string {
	if r.Trends.Bucket < 86400 {
		return t.Local().Format("2 Jan 15:04")
	}
	return t.Local().Format("Mon 2 Jan")
}

func (r Report) markdownTrends(w mdWriter) {
	tr := r.Trends
	w("## Performance changes\n\n")
	if len(tr.Regressions) == 0 {
		w("No job or image changed its median time by a quarter or more %s.\n\n", tr.Than)
	} else {
		for _, g := range tr.Regressions {
			w("- %s\n", RegressionText(g, tr.Than))
		}
		w("\n")
	}

	unit := "Day"
	if tr.Bucket < 86400 {
		unit = "Hour"
	}
	w("## Activity by %s\n\n", strings.ToLower(unit))
	var rows []Bucket
	for _, b := range tr.Buckets {
		if b.Passed+b.Failed+b.OOM+b.Stopped+b.Builds > 0 {
			rows = append(rows, b)
		}
	}
	if len(rows) == 0 {
		w("Nothing ran or was built in this window.\n\n")
	} else {
		w("| %s | Passed | Failed | Out of memory | Stopped | Pass rate | Restarts | Builds | Failed builds |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|\n", unit)
		for _, b := range rows {
			rate := -1.0
			if own := b.Passed + b.Failed + b.OOM; own > 0 {
				rate = float64(b.Passed) / float64(own)
			}
			w("| %s | %d | %d | %d | %d | %s | %d | %d | %d |\n", r.bucketLabel(b.T), b.Passed, b.Failed, b.OOM, b.Stopped, Percent(rate), b.Restarts, b.Builds, b.BuildsFailed)
		}
		w("\n")
	}

	durTable := func(title string, list []DurationTrend, empty string) {
		w("## %s\n\n", title)
		if len(list) == 0 {
			w("%s\n\n", empty)
			return
		}
		w("Median time per %s; the slowest 10%% in brackets.\n\n", strings.ToLower(unit))
		w("| %s |", unit)
		for _, d := range list {
			w(" %s |", mdCell(d.Name))
		}
		w("\n|---|%s\n", strings.Repeat("---:|", len(list)))
		for i, b := range tr.Buckets {
			seen := false
			for _, d := range list {
				seen = seen || d.Median[i] != nil
			}
			if !seen {
				continue
			}
			w("| %s |", r.bucketLabel(b.T))
			for _, d := range list {
				if d.Median[i] == nil {
					w(" – |")
				} else {
					w(" %s (%s) |", Duration(*d.Median[i]), Duration(*d.P90[i]))
				}
			}
			w("\n")
		}
		w("\n")
	}
	durTable("Test duration over time", tr.Jobs, "No job ran twice on its own.")
	durTable("Build duration over time", tr.Images, "No image was built twice.")

	w("## Errors\n\n")
	if len(tr.ExitCodes) == 0 {
		w("No run failed.\n\n")
	} else {
		w("| Exit code | Runs | Usually means | Most often in |\n|---:|---:|---|---|\n")
		for _, c := range tr.ExitCodes {
			w("| %d | %d | %s | %s |\n", c.Code, c.Count, c.Meaning, mdCell(strings.Join(c.Jobs, ", ")))
		}
		w("\n")
	}
	if len(tr.BuildErrors) > 0 {
		w("| Build error | Builds | Images |\n|---|---:|---|\n")
		for _, e := range tr.BuildErrors {
			w("| %s | %d | %s |\n", mdCell(e.Reason), e.Count, mdCell(strings.Join(e.Images, ", ")))
		}
		w("\n")
	}
	if len(tr.Trouble) > 0 {
		w("Containers with the most trouble:\n\n| Job | Runs | Failed | Out of memory | Restarts | Unhealthy |\n|---|---:|---:|---:|---:|---:|\n")
		for _, t := range tr.Trouble {
			w("| %s | %d | %d | %d | %d | %d |\n", mdCell(t.Job), t.Runs, t.Failed, t.OOM, t.Restarts, t.Unhealthy)
		}
		w("\n")
	}
}

// RegressionText is one line about a job or image that got slower or faster.
func RegressionText(g Regression, than string) string {
	what := "test job"
	if g.Kind == "build" {
		what = "image build"
	}
	dir := "slower"
	pct := (g.Ratio - 1) * 100
	if g.Ratio < 1 {
		dir = "faster"
		pct = (1 - g.Ratio) * 100
	}
	return fmt.Sprintf("%s (%s) is %.0f%% %s %s: median %s, was %s (%d and %d runs).",
		mdCell(g.Name), what, pct, dir, than, Duration(g.Now), Duration(g.Before), g.NowRuns, g.BeforeRuns)
}

func (r Report) markdownUsage(w mdWriter) {
	w("## Resources over time\n\n")
	var seen bool
	for _, u := range r.Trends.Usage {
		seen = seen || u.HostCPU != nil || u.HostMem != nil
	}
	if !seen {
		w("No samples in this window.\n\n")
		return
	}
	// One row per day (or per hour on a one day report), from the usage buckets.
	type acc struct {
		cpu, mem Stat
		peak     float64
		disk     float64
	}
	var keys []string
	by := map[string]*acc{}
	for _, u := range r.Trends.Usage {
		if u.HostCPU == nil && u.HostMem == nil && u.DiskMac == nil {
			continue
		}
		k := u.T.Local().Format("Mon 2 Jan")
		if r.Trends.Bucket < 86400 {
			k = u.T.Local().Format("2 Jan 15:00")
		}
		a := by[k]
		if a == nil {
			a = &acc{}
			by[k] = a
			keys = append(keys, k)
		}
		if u.HostCPU != nil {
			a.cpu.add(*u.HostCPU)
			a.peak = max(a.peak, *u.HostCPUPeak)
		}
		if u.HostMem != nil {
			a.mem.add(*u.HostMem)
		}
		if u.DiskMac != nil {
			a.disk = *u.DiskMac
		}
	}
	w("| When | Mac CPU average | Mac CPU peak | Mac memory average | VM disk on the Mac |\n|---|---:|---:|---:|---:|\n")
	for _, k := range keys {
		a := by[k]
		disk := "–"
		if a.disk > 0 {
			disk = Bytes(a.disk)
		}
		w("| %s | %s | %s | %s | %s |\n", k, pctStat(a.cpu.Avg, a.cpu.N), pctStat(a.peak, a.cpu.N), bytesStat(a.mem.Avg, a.mem.N), disk)
	}
	w("\n")
}

func (r Report) markdownOutdated(w mdWriter) {
	o := r.Outdated
	if o == nil {
		return
	}
	w("## Out of date\n\n")
	if o.ImagesKnown {
		w("- %s, %s in all.\n", plural(o.Images, "image"), Bytes(float64(o.Size)))
		w("- %d older than %d days (%s).\n", o.OldCount, o.OldDays, Bytes(float64(o.OldSize)))
		w("- %d tagged :latest, which moves under you.\n", o.LatestCount)
		w("- %d dangling, with no tag (%s).\n", o.Dangling, Bytes(float64(o.DanglingSize)))
		w("- %d not used by any container (%s).\n\n", o.Unused, Bytes(float64(o.UnusedSize)))
		if len(o.Old) > 0 {
			w("| Old image | Age | Size | Containers |\n|---|---:|---:|---:|\n")
			for _, im := range o.Old {
				w("| %s | %s | %s | %d |\n", mdCell(im.Ref), plural(im.AgeDays, "day"), Bytes(float64(im.Size)), im.Containers)
			}
			w("\n")
		}
	} else if o.ImagesError != "" {
		w("Images could not be listed: %s\n\n", mdCell(o.ImagesError))
	}
	if !o.ToolsKnown {
		w("Tool versions have not been checked yet.\n\n")
		return
	}
	w("| Tool | Installed | Latest | |\n|---|---|---|---|\n")
	for _, t := range o.Tools {
		state := "up to date"
		if t.Newer {
			state = "**update available**"
		} else if t.Latest == "" {
			state = "not checked"
		}
		latest := t.Latest
		if latest == "" {
			latest = "–"
		}
		w("| %s | %s | %s | %s |\n", mdCell(t.Label), mdCell(t.Current), mdCell(latest), state)
	}
	w("\n")
}
