package history

import (
	"math"
	"sort"
	"time"
)

// trendLen is how many recent runs or builds a trend line shows.
const trendLen = 30

// JobSummary is the trend for one kind of run (a Compose service or an image).
type JobSummary struct {
	Job      string  `json:"job"`
	Runs     int     `json:"runs"`
	Pass     int     `json:"pass"`
	Fail     int     `json:"fail"`
	OOM      int     `json:"oom"`
	Stopped  int     `json:"stopped"`
	PassRate float64 `json:"passRate"` // of the runs that ended on their own, 0..1; -1 when none did
	Median   float64 `json:"median"`   // seconds, runs that ended on their own
	P90      float64 `json:"p90"`
	Last     Run     `json:"last"`
	// Trend is the last runs, oldest first: seconds and outcome, for the sparkline.
	Trend []TrendPoint `json:"trend"`
	// Flaky: both passed and failed within its last 10 runs that ended on their own.
	Flaky bool `json:"flaky"`
	// Slower is how much slower the last 5 runs are than the 20 before them (1.3 = 30% slower);
	// 0 when there are too few runs to say.
	Slower    float64 `json:"slower"`
	Restarts  int     `json:"restarts"`
	Unhealthy int     `json:"unhealthy"`
}

// TrendPoint is one run or build on a trend line.
type TrendPoint struct {
	T       time.Time `json:"t"`
	Seconds float64   `json:"seconds"`
	Outcome string    `json:"outcome"`
}

// SummariseRuns groups runs by job, most recently run first.
func SummariseRuns(runs []Run) []JobSummary {
	byJob := map[string][]Run{}
	for _, r := range runs {
		byJob[r.Job] = append(byJob[r.Job], r)
	}
	out := []JobSummary{}
	for job, rs := range byJob {
		sort.Slice(rs, func(i, j int) bool { return rs[i].T.Before(rs[j].T) })
		s := JobSummary{Job: job, Runs: len(rs), Last: rs[len(rs)-1], PassRate: -1}
		var own []Run // ended on their own: these say something about the code
		for _, r := range rs {
			switch r.Outcome {
			case "pass":
				s.Pass++
			case "fail":
				s.Fail++
			case "oom":
				s.OOM++
			default:
				s.Stopped++
			}
			if r.Restarted {
				s.Restarts++
			}
			if r.Unhealthy {
				s.Unhealthy++
			}
			if r.Outcome != "stopped" {
				own = append(own, r)
			}
		}
		if len(own) > 0 {
			s.PassRate = float64(s.Pass) / float64(len(own))
			secs := make([]float64, len(own))
			for i, r := range own {
				secs[i] = r.Seconds
			}
			s.Median, s.P90 = quantile(secs, 0.5), quantile(secs, 0.9)
			s.Slower = slower(secs)
			recent := own[max(0, len(own)-10):]
			var p, f bool
			for _, r := range recent {
				p = p || r.Outcome == "pass"
				f = f || r.Outcome == "fail" || r.Outcome == "oom"
			}
			s.Flaky = p && f
		}
		for _, r := range rs[max(0, len(rs)-trendLen):] {
			s.Trend = append(s.Trend, TrendPoint{T: r.T, Seconds: r.Seconds, Outcome: r.Outcome})
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last.T.After(out[j].Last.T) })
	return out
}

// BuildSummary is the trend for one build name.
type BuildSummary struct {
	Name      string       `json:"name"`
	Builds    int          `json:"builds"`
	Failed    int          `json:"failed"`
	Median    float64      `json:"median"`
	CacheRate float64      `json:"cacheRate"` // cached steps over all steps, 0..1
	Last      Build        `json:"last"`
	Trend     []TrendPoint `json:"trend"`
	Slower    float64      `json:"slower"`
}

// SummariseBuilds groups builds by name, most recent first.
func SummariseBuilds(builds []Build) []BuildSummary {
	by := map[string][]Build{}
	for _, b := range builds {
		by[b.Name] = append(by[b.Name], b)
	}
	out := []BuildSummary{}
	for name, bs := range by {
		sort.Slice(bs, func(i, j int) bool { return bs[i].T.Before(bs[j].T) })
		s := BuildSummary{Name: name, Builds: len(bs), Last: bs[len(bs)-1]}
		var secs []float64
		var steps, cached int
		for _, b := range bs {
			if b.Status != "Completed" {
				s.Failed++
				continue
			}
			secs = append(secs, b.Seconds)
			steps += b.Steps
			cached += b.Cached
		}
		if len(secs) > 0 {
			s.Median = quantile(secs, 0.5)
			s.Slower = slower(secs)
		}
		if steps > 0 {
			s.CacheRate = float64(cached) / float64(steps)
		}
		for _, b := range bs[max(0, len(bs)-trendLen):] {
			o := "pass"
			if b.Status != "Completed" {
				o = "fail"
			}
			s.Trend = append(s.Trend, TrendPoint{T: b.T, Seconds: b.Seconds, Outcome: o})
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last.T.After(out[j].Last.T) })
	return out
}

// Forecast is when the VM's disk fills at the rate it has been growing.
type Forecast struct {
	PerDay   float64 `json:"perDay"`             // bytes a day, over the window
	Days     float64 `json:"days"`               // until full; -1 when not growing
	Used     uint64  `json:"used"`               // now
	Size     uint64  `json:"size"`               // the disk's size
	Window   float64 `json:"window"`             // days of history the rate is from
	Reliable bool    `json:"reliable,omitempty"` // at least a day of history
}

// DiskForecast fits a straight line to the VM disk's use on the Mac.
func DiskForecast(points []Point) *Forecast {
	var xs, ys []float64
	var last Point
	for _, p := range points {
		if p.DiskMac == 0 {
			continue
		}
		xs = append(xs, float64(p.T.Unix())/86400)
		ys = append(ys, float64(p.DiskMac))
		last = p
	}
	if len(xs) < 3 {
		return nil
	}
	slope := fitSlope(xs, ys)
	f := &Forecast{PerDay: slope, Days: -1, Used: last.DiskMac, Size: last.DiskSize, Window: xs[len(xs)-1] - xs[0]}
	f.Reliable = f.Window >= 1
	if !f.Reliable {
		// A few hours say nothing about a month: one image pull would read as gigabytes a day
		// and a disk "full in a week". Report use only until there is a day of history.
		f.PerDay = 0
		return f
	}
	if slope > 0 && last.DiskSize > last.DiskMac {
		f.Days = float64(last.DiskSize-last.DiskMac) / slope
	}
	return f
}

func fitSlope(xs, ys []float64) float64 {
	n := float64(len(xs))
	var sx, sy, sxx, sxy float64
	for i := range xs {
		sx += xs[i]
		sy += ys[i]
		sxx += xs[i] * xs[i]
		sxy += xs[i] * ys[i]
	}
	d := n*sxx - sx*sx
	if d == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / d
}

// slower compares the median of the last 5 values with the median of the 20 before them.
func slower(secs []float64) float64 {
	if len(secs) < 8 {
		return 0
	}
	recent := secs[len(secs)-5:]
	before := secs[max(0, len(secs)-25) : len(secs)-5]
	b := quantile(before, 0.5)
	if b <= 0 {
		return 0
	}
	return math.Round(quantile(recent, 0.5)/b*100) / 100
}

func quantile(v []float64, q float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := q * float64(len(s)-1)
	lo := int(math.Floor(i))
	hi := int(math.Ceil(i))
	return s[lo] + (s[hi]-s[lo])*(i-float64(lo))
}
