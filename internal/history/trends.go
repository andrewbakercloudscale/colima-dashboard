package history

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Trends is what the Report page charts: activity per hour or day, how long jobs and builds took
// over the window, what got slower, and what went wrong. Everything is computed from the same
// runs, builds and samples as the rest of the report.
type Trends struct {
	// Bucket is the width of an activity bucket in seconds: an hour for a one day report, else a day.
	Bucket  int64    `json:"bucket"`
	Buckets []Bucket `json:"buckets"`
	// UsageBucket is the width of a usage bucket in seconds (15 minutes, 2 hours or 6 hours).
	UsageBucket int64         `json:"usageBucket"`
	Usage       []UsageBucket `json:"usage"`
	// Jobs and Images are duration trends for the busiest jobs and images, aligned with Buckets.
	Jobs   []DurationTrend `json:"jobs"`
	Images []DurationTrend `json:"images"`
	// Regressions are jobs and images whose median time changed by a quarter or more against
	// the period before (or, when there is no history that old, the first half of the window).
	Regressions []Regression `json:"regressions"`
	Than        string       `json:"than"` // "than the week before", for the page's wording
	ExitCodes   []CodeCount  `json:"exitCodes"`
	BuildErrors []ErrorCount `json:"buildErrors"`
	Trouble     []TroubleRow `json:"trouble"`
}

// Bucket counts what happened in one hour or day.
type Bucket struct {
	T            time.Time `json:"t"` // start of the bucket
	Passed       int       `json:"passed"`
	Failed       int       `json:"failed"`
	OOM          int       `json:"oom"`
	Stopped      int       `json:"stopped"`
	Restarts     int       `json:"restarts"`
	Unhealthy    int       `json:"unhealthy"`
	Builds       int       `json:"builds"`
	BuildsFailed int       `json:"buildsFailed"`
}

// UsageBucket averages the five minute samples in one bucket; nil means no sample had a value.
type UsageBucket struct {
	T           time.Time `json:"t"`
	HostCPU     *float64  `json:"hostCpu"`     // average, % of one Mac core
	HostCPUPeak *float64  `json:"hostCpuPeak"` // highest sample
	HostMem     *float64  `json:"hostMem"`     // average bytes
	GuestCPU    *float64  `json:"guestCpu"`
	GuestMem    *float64  `json:"guestMem"`
	DiskMac     *float64  `json:"diskMac"` // last sample in the bucket
	Running     *float64  `json:"running"`
}

// DurationTrend is the median and slowest-10% time of one job or image in each bucket.
type DurationTrend struct {
	Name   string     `json:"name"`
	Median []*float64 `json:"median"`
	P90    []*float64 `json:"p90"`
	Count  []int      `json:"count"`
}

// Regression is a job or image that got slower or faster.
type Regression struct {
	Kind       string  `json:"kind"` // "test" or "build"
	Name       string  `json:"name"`
	Now        float64 `json:"now"`    // median seconds in the recent period
	Before     float64 `json:"before"` // median seconds in the period before
	Ratio      float64 `json:"ratio"`  // Now over Before: 1.4 is 40% slower
	NowRuns    int     `json:"nowRuns"`
	BeforeRuns int     `json:"beforeRuns"`
}

// CodeCount is how often runs ended with one exit code.
type CodeCount struct {
	Code    int      `json:"code"`
	Count   int      `json:"count"`
	Meaning string   `json:"meaning"`
	Jobs    []string `json:"jobs"` // the jobs it happened to most, at most 3
}

// ErrorCount is one kind of build error.
type ErrorCount struct {
	Reason string   `json:"reason"`
	Count  int      `json:"count"`
	Images []string `json:"images"`
	Last   string   `json:"last"` // the most recent full message, shortened
}

// TroubleRow is a job that failed, ran out of memory, restarted or went unhealthy.
type TroubleRow struct {
	Job       string `json:"job"`
	Runs      int    `json:"runs"`
	Failed    int    `json:"failed"`
	OOM       int    `json:"oom"`
	Restarts  int    `json:"restarts"`
	Unhealthy int    `json:"unhealthy"`
}

// Previous is the history from the period just before a report's window, for regressions.
type Previous struct {
	Runs   []Run
	Builds []Build
}

const (
	trendTop     = 5    // jobs and images on the duration charts
	regressRatio = 1.25 // a quarter slower (or 1/1.25 faster) is worth saying
	regressMin   = 3    // runs needed on each side of a comparison
	regressAbs   = 1.0  // seconds: a 0.1 s job taking 0.2 s is noise, not a regression
)

// bucketStart floors t to its bucket in loc: whole days from local midnight, or a fixed width
// from local midnight, so hourly buckets line up with the clock.
func bucketStart(t time.Time, width time.Duration, loc *time.Location) time.Time {
	t = t.In(loc)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	if width >= 24*time.Hour {
		return day
	}
	return day.Add(t.Sub(day) / width * width)
}

// bucketTimes lists the starts of every bucket from the one holding from to the one holding to.
func bucketTimes(from, to time.Time, width time.Duration, loc *time.Location) []time.Time {
	var out []time.Time
	for b := bucketStart(from, width, loc); !b.After(to); {
		out = append(out, b)
		if width >= 24*time.Hour {
			b = b.AddDate(0, 0, 1)
		} else {
			b = b.Add(width)
		}
	}
	return out
}

// bucketIndex finds t's bucket in starts (sorted); -1 when t is before the first.
func bucketIndex(starts []time.Time, t time.Time) int {
	i := sort.Search(len(starts), func(i int) bool { return starts[i].After(t) }) - 1
	return i
}

func activityWidth(days int) time.Duration {
	if days <= 1 {
		return time.Hour
	}
	return 24 * time.Hour
}

func usageWidth(days int) time.Duration {
	switch {
	case days <= 1:
		return 15 * time.Minute
	case days <= 7:
		return 2 * time.Hour
	default:
		return 6 * time.Hour
	}
}

// MakeTrends computes the charts' data for a window of days ending at now.
func MakeTrends(days int, now time.Time, runs []Run, builds []Build, points []Point, prev Previous) Trends {
	loc := now.Location()
	from := now.Add(-time.Duration(days) * 24 * time.Hour)
	aw, uw := activityWidth(days), usageWidth(days)
	starts := bucketTimes(from, now, aw, loc)
	tr := Trends{Bucket: int64(aw.Seconds()), UsageBucket: int64(uw.Seconds()), Buckets: make([]Bucket, len(starts))}
	for i, s := range starts {
		tr.Buckets[i].T = s
	}
	for _, r := range runs {
		i := bucketIndex(starts, r.T)
		if i < 0 {
			continue
		}
		b := &tr.Buckets[i]
		switch r.Outcome {
		case "pass":
			b.Passed++
		case "fail":
			b.Failed++
		case "oom":
			b.OOM++
		default:
			b.Stopped++
		}
		if r.Restarted {
			b.Restarts++
		}
		if r.Unhealthy {
			b.Unhealthy++
		}
	}
	for _, x := range builds {
		i := bucketIndex(starts, x.T)
		if i < 0 {
			continue
		}
		tr.Buckets[i].Builds++
		if x.Status != "Completed" {
			tr.Buckets[i].BuildsFailed++
		}
	}
	tr.Usage = usageBuckets(points, from, now, uw, loc)
	tr.Jobs = jobTrends(runs, starts)
	tr.Images = imageTrends(builds, starts)
	tr.Regressions, tr.Than = regressions(days, from, now, runs, builds, prev)
	tr.ExitCodes = exitCodes(runs)
	tr.BuildErrors = buildErrors(builds)
	tr.Trouble = trouble(runs)
	return tr
}

func usageBuckets(points []Point, from, to time.Time, width time.Duration, loc *time.Location) []UsageBucket {
	starts := bucketTimes(from, to, width, loc)
	type acc struct{ cpu, cpuPeak, mem, gcpu, gmem, run Stat }
	accs := make([]acc, len(starts))
	disk := make([]*float64, len(starts))
	for _, p := range points {
		i := bucketIndex(starts, p.T)
		if i < 0 {
			continue
		}
		a := &accs[i]
		if p.HostCPU != nil {
			a.cpu.add(*p.HostCPU)
		}
		if p.HostMem > 0 {
			a.mem.add(float64(p.HostMem))
		}
		if p.GuestCPU != nil {
			a.gcpu.add(*p.GuestCPU)
		}
		if p.GuestMem > 0 {
			a.gmem.add(float64(p.GuestMem))
		}
		a.run.add(float64(p.Running))
		if p.DiskMac > 0 {
			v := float64(p.DiskMac)
			disk[i] = &v
		}
	}
	avg := func(s Stat) *float64 {
		if s.N == 0 {
			return nil
		}
		v := s.Avg
		return &v
	}
	peak := func(s Stat) *float64 {
		if s.N == 0 {
			return nil
		}
		v := s.Peak
		return &v
	}
	out := make([]UsageBucket, len(starts))
	for i, s := range starts {
		a := accs[i]
		out[i] = UsageBucket{T: s, HostCPU: avg(a.cpu), HostCPUPeak: peak(a.cpu), HostMem: avg(a.mem),
			GuestCPU: avg(a.gcpu), GuestMem: avg(a.gmem), DiskMac: disk[i], Running: avg(a.run)}
	}
	return out
}

// jobTrends picks the jobs with the most runs that ended on their own and gives each bucket's
// median and slowest-10% time.
func jobTrends(runs []Run, starts []time.Time) []DurationTrend {
	by := map[string][]Run{}
	for _, r := range runs {
		if r.Outcome != "stopped" {
			by[r.Job] = append(by[r.Job], r)
		}
	}
	names := topByCount(len(by), func(yield func(string, int)) {
		for k, v := range by {
			yield(k, len(v))
		}
	})
	out := []DurationTrend{}
	for _, name := range names {
		secs := make([][]float64, len(starts))
		for _, r := range by[name] {
			if i := bucketIndex(starts, r.T); i >= 0 {
				secs[i] = append(secs[i], r.Seconds)
			}
		}
		out = append(out, durationTrend(name, secs))
	}
	return out
}

func imageTrends(builds []Build, starts []time.Time) []DurationTrend {
	by := map[string][]Build{}
	for _, b := range builds {
		if b.Status == "Completed" {
			by[b.Name] = append(by[b.Name], b)
		}
	}
	names := topByCount(len(by), func(yield func(string, int)) {
		for k, v := range by {
			yield(k, len(v))
		}
	})
	out := []DurationTrend{}
	for _, name := range names {
		secs := make([][]float64, len(starts))
		for _, b := range by[name] {
			if i := bucketIndex(starts, b.T); i >= 0 {
				secs[i] = append(secs[i], b.Seconds)
			}
		}
		label := name
		if label == "" {
			label = "(unnamed)"
		}
		out = append(out, durationTrend(label, secs))
	}
	return out
}

// topByCount returns up to trendTop names with at least two entries, most first, then by name.
func topByCount(n int, each func(func(string, int))) []string {
	type kv struct {
		k string
		n int
	}
	list := make([]kv, 0, n)
	each(func(k string, c int) {
		if c >= 2 {
			list = append(list, kv{k, c})
		}
	})
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].k < list[j].k
	})
	out := []string{}
	for _, x := range list[:min(trendTop, len(list))] {
		out = append(out, x.k)
	}
	return out
}

func durationTrend(name string, secs [][]float64) DurationTrend {
	d := DurationTrend{Name: name, Median: make([]*float64, len(secs)), P90: make([]*float64, len(secs)), Count: make([]int, len(secs))}
	for i, s := range secs {
		d.Count[i] = len(s)
		if len(s) == 0 {
			continue
		}
		m, p := quantile(s, 0.5), quantile(s, 0.9)
		d.Median[i], d.P90[i] = &m, &p
	}
	return d
}

// regressions compares each job's and image's median time in the window with the period before
// it. With no history before the window (a 30 day report, or a new install) it compares the
// second half of the window with the first.
func regressions(days int, from, now time.Time, runs []Run, builds []Build, prev Previous) ([]Regression, string) {
	curR, curB, oldR, oldB := runs, builds, prev.Runs, prev.Builds
	var than string
	switch {
	case len(prev.Runs)+len(prev.Builds) > 0 && days == 1:
		than = "than the day before"
	case len(prev.Runs)+len(prev.Builds) > 0 && days == 7:
		than = "than the week before"
	case len(prev.Runs)+len(prev.Builds) > 0:
		than = fmt.Sprintf("than the %d days before", days)
	default:
		mid := from.Add(now.Sub(from) / 2)
		curR, oldR = nil, nil
		for _, r := range runs {
			if r.T.After(mid) {
				curR = append(curR, r)
			} else {
				oldR = append(oldR, r)
			}
		}
		curB, oldB = nil, nil
		for _, b := range builds {
			if b.T.After(mid) {
				curB = append(curB, b)
			} else {
				oldB = append(oldB, b)
			}
		}
		half := now.Sub(from) / 2
		if half >= 48*time.Hour {
			than = fmt.Sprintf("than the %s days before", strings.TrimSuffix(fmt.Sprintf("%.1f", half.Hours()/24), ".0"))
		} else {
			than = fmt.Sprintf("than the %.0f hours before", half.Hours())
		}
	}
	runSecs := func(rs []Run) map[string][]float64 {
		m := map[string][]float64{}
		for _, r := range rs {
			if r.Outcome != "stopped" {
				m[r.Job] = append(m[r.Job], r.Seconds)
			}
		}
		return m
	}
	buildSecs := func(bs []Build) map[string][]float64 {
		m := map[string][]float64{}
		for _, b := range bs {
			if b.Status == "Completed" {
				m[b.Name] = append(m[b.Name], b.Seconds)
			}
		}
		return m
	}
	out := []Regression{}
	compare := func(kind string, now, before map[string][]float64) {
		for name, n := range now {
			b := before[name]
			if len(n) < regressMin || len(b) < regressMin {
				continue
			}
			mn, mb := quantile(n, 0.5), quantile(b, 0.5)
			if mb <= 0 || mn-mb < regressAbs && mb-mn < regressAbs {
				continue
			}
			ratio := mn / mb
			if ratio >= regressRatio || ratio <= 1/regressRatio {
				if name == "" {
					name = "(unnamed)"
				}
				out = append(out, Regression{Kind: kind, Name: name, Now: mn, Before: mb, Ratio: ratio, NowRuns: len(n), BeforeRuns: len(b)})
			}
		}
	}
	compare("test", runSecs(curR), runSecs(oldR))
	compare("build", buildSecs(curB), buildSecs(oldB))
	// slower first, most slower first; then faster, most faster first
	sort.Slice(out, func(i, j int) bool {
		si, sj := out[i].Ratio > 1, out[j].Ratio > 1
		if si != sj {
			return si
		}
		if out[i].Ratio != out[j].Ratio {
			if si {
				return out[i].Ratio > out[j].Ratio
			}
			return out[i].Ratio < out[j].Ratio
		}
		return out[i].Name < out[j].Name
	})
	return out[:min(8, len(out))], than
}

// ExitMeaning says what an exit code usually means for a container.
func ExitMeaning(code int) string {
	switch code {
	case 1:
		return "general error: usually a failed test or assertion"
	case 2:
		return "misuse: bad arguments or a shell builtin error"
	case 125:
		return "Docker could not run the container"
	case 126:
		return "command found but could not run"
	case 127:
		return "command not found"
	case 130:
		return "interrupted (Ctrl-C)"
	case 134:
		return "aborted (SIGABRT)"
	case 137:
		return "killed (SIGKILL): out of memory or docker kill"
	case 139:
		return "segmentation fault"
	case 143:
		return "terminated (SIGTERM)"
	}
	if code > 128 && code < 160 {
		return fmt.Sprintf("killed by signal %d", code-128)
	}
	return "set by the program"
}

func exitCodes(runs []Run) []CodeCount {
	count := map[int]int{}
	jobs := map[int]map[string]int{}
	for _, r := range runs {
		if r.Outcome != "fail" && r.Outcome != "oom" {
			continue
		}
		count[r.Exit]++
		if jobs[r.Exit] == nil {
			jobs[r.Exit] = map[string]int{}
		}
		jobs[r.Exit][r.Job]++
	}
	out := []CodeCount{}
	for code, n := range count {
		out = append(out, CodeCount{Code: code, Count: n, Meaning: ExitMeaning(code), Jobs: topKeys(jobs[code], 3)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Code < out[j].Code
	})
	return out[:min(8, len(out))]
}

var exitCodeRe = regexp.MustCompile(`exit code: (\d+)`)

// BuildReason groups build errors: a failed RUN step by its exit code, else the message's first
// line with numbers and quoted text taken out, so the same failure counts once.
func BuildReason(msg string) string {
	if msg == "" {
		return "no error message"
	}
	if strings.Contains(msg, "did not complete successfully") {
		if m := exitCodeRe.FindStringSubmatch(msg); m != nil {
			return "a RUN step failed with exit code " + m[1]
		}
		return "a RUN step failed"
	}
	switch low := strings.ToLower(msg); {
	case strings.Contains(low, "not found") && (strings.Contains(low, "dockerfile") || strings.Contains(low, "failed to read")):
		return "Dockerfile not found"
	case strings.Contains(low, "canceled") || strings.Contains(low, "cancelled"):
		return "canceled"
	case strings.Contains(low, "pull access denied") || strings.Contains(low, "manifest unknown") || strings.Contains(low, "not found: manifest"):
		return "base image not found or not allowed"
	case strings.Contains(low, "no space left"):
		return "no space left on the VM's disk"
	}
	line, _, _ := strings.Cut(msg, "\n")
	return shorten(line, 90)
}

func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func buildErrors(builds []Build) []ErrorCount {
	type agg struct {
		n      int
		images map[string]int
		last   string
		lastT  time.Time
	}
	by := map[string]*agg{}
	for _, b := range builds {
		if b.Status == "Completed" {
			continue
		}
		reason := BuildReason(b.Error)
		if b.Status == "Canceled" && b.Error == "" {
			reason = "canceled"
		}
		a := by[reason]
		if a == nil {
			a = &agg{images: map[string]int{}}
			by[reason] = a
		}
		a.n++
		name := b.Name
		if name == "" {
			name = "(unnamed)"
		}
		a.images[name]++
		if !b.T.Before(a.lastT) {
			a.last, a.lastT = shorten(b.Error, 200), b.T
		}
	}
	out := []ErrorCount{}
	for reason, a := range by {
		out = append(out, ErrorCount{Reason: reason, Count: a.n, Images: topKeys(a.images, 3), Last: a.last})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Reason < out[j].Reason
	})
	return out[:min(6, len(out))]
}

// trouble ranks jobs by how often something went wrong: an out of memory kill counts double,
// as it says the limit is wrong rather than the code.
func trouble(runs []Run) []TroubleRow {
	by := map[string]*TroubleRow{}
	for _, r := range runs {
		t := by[r.Job]
		if t == nil {
			t = &TroubleRow{Job: r.Job}
			by[r.Job] = t
		}
		t.Runs++
		switch r.Outcome {
		case "fail":
			t.Failed++
		case "oom":
			t.OOM++
		}
		if r.Restarted {
			t.Restarts++
		}
		if r.Unhealthy {
			t.Unhealthy++
		}
	}
	score := func(t *TroubleRow) int { return t.Failed + 2*t.OOM + t.Restarts + t.Unhealthy }
	out := []TroubleRow{}
	for _, t := range by {
		if score(t) > 0 {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		si, sj := score(&out[i]), score(&out[j])
		if si != sj {
			return si > sj
		}
		return out[i].Job < out[j].Job
	})
	return out[:min(8, len(out))]
}

// topKeys returns up to n keys with the highest counts, then by name.
func topKeys(m map[string]int, n int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys[:min(n, len(keys))]
}
