package metrics

import (
	"math"
	"testing"
	"time"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestCosts(t *testing.T) {
	smp := func(ts int64, hostCPU float64, guestCPU float64, cs map[string]Container) Sample {
		return Sample{T: ts, Host: &Host{CPU: hostCPU, Memory: 4000}, Guest: &Guest{CPU: guestCPU, MemUsed: 2000}, Containers: cs}
	}
	s := Series{IntervalMs: 2000,
		Names:  map[string]string{"a1": "web", "b1": "app-run-0123abcd", "b2": "app", "c1": "db"},
		Images: map[string]string{"a1": "nginx", "c1": "postgres"},
		Samples: []Sample{
			{T: 500}, // no host or guest: skipped
			smp(1000, 100, 50, map[string]Container{"a1": {CPU: 25, Memory: 1000}, "b1": {CPU: 10, Memory: 200}}),
			smp(3000, 100, 50, map[string]Container{"a1": {CPU: 25, Memory: 1000}, "b2": {CPU: 10, Memory: 200}}),
		}}
	rep := Costs(s, 0, 8*time.Second)
	if rep.Samples != 2 || !near(rep.Coverage, 0.5) || !near(rep.MacCPU, 100) || rep.MacMem != 4000 {
		t.Fatalf("report totals wrong: %+v", rep)
	}
	if !near(rep.CPUSecs, 4) { // 1 core for two 2 s samples
		t.Fatalf("CPUSecs = %v, want 4", rep.CPUSecs)
	}
	by := map[string]Cost{}
	for _, c := range rep.Costs {
		by[c.Name] = c
	}
	web := by["web"]
	if !near(web.MacCPU, 50) || web.MacMem != 2000 || !near(web.Seen, 1) || web.Image != "nginx" || !near(web.VMShare, 0.5) {
		t.Errorf("web = %+v", web)
	}
	app := by["app"] // the run container joins its service
	if len(app.IDs) != 2 || !near(app.MacCPU, 20) || !near(app.CPUSecs, 0.8) {
		t.Errorf("app = %+v", app)
	}
	sys := by["VM and system"]
	if !sys.System || !near(sys.MacCPU, 30) || sys.MacMem != 1600 {
		t.Errorf("system = %+v", sys)
	}
	if rep.Costs[0].Name != "web" {
		t.Errorf("not sorted by cost: first is %s", rep.Costs[0].Name)
	}
	var total float64
	for _, c := range rep.Costs {
		total += c.CPUSecs
	}
	if !near(total, rep.CPUSecs) {
		t.Errorf("parts add to %v, whole is %v", total, rep.CPUSecs)
	}
	if _, ok := by["db"]; ok {
		t.Error("db never ran but has a row")
	}
	if got := Costs(s, 3000, time.Minute); got.Samples != 0 || got.Costs == nil {
		t.Errorf("empty window: %+v", got)
	}
}
