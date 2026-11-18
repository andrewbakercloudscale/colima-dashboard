package server

import (
	"math"
	"testing"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/history"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/metrics"
)

func TestUsageFromSamples(t *testing.T) {
	now := time.Now()
	var samples []metrics.Sample
	// two minutes of samples, every two seconds: "spin" at 12.5% of a 2 CPU VM (0.25 CPUs),
	// "old" stopped half way, guest at 90%
	for i := 0; i < 60; i++ {
		ts := now.Add(-time.Duration(120-2*i) * time.Second)
		s := metrics.Sample{T: ts.UnixMilli(), Guest: &metrics.Guest{CPU: 90, CPUs: 2, MemUsed: 1 << 30, MemTotal: 2 << 30},
			Containers: map[string]metrics.Container{"spin": {CPU: 12.5, Memory: uint64(10+i) << 20}}}
		if i < 30 {
			s.Containers["old"] = metrics.Container{Memory: 300 << 20}
		}
		samples = append(samples, s)
	}
	u := usageFromSamples(samples, 2, now)
	if !u.fresh || u.vm == nil {
		t.Fatalf("want fresh samples with VM use: %+v", u)
	}
	if math.Abs(u.vm.cpuPct-90) > 0.01 || u.vm.memUsed != 1<<30 || u.vm.window < 115*time.Second {
		t.Fatalf("vm %+v", u.vm)
	}
	spin := u.running["spin"]
	if spin == nil || math.Abs(spin.CPU-0.25) > 0.001 || spin.MemNow != 69<<20 || spin.MemPeak != 69<<20 {
		t.Fatalf("spin %+v", spin)
	}
	if u.running["old"] != nil || u.peak["old"] != 300<<20 {
		t.Fatalf("a stopped container keeps its peak only: %+v %d", u.running["old"], u.peak["old"])
	}
	// stale samples: peaks still count, live use does not
	u = usageFromSamples(samples, 2, now.Add(10*time.Minute))
	if u.fresh || u.vm != nil || len(u.running) != 0 || u.peak["old"] != 300<<20 {
		t.Fatalf("stale %+v", u)
	}
	if u := usageFromSamples(nil, 2, now); u.fresh {
		t.Fatal("no samples is not fresh")
	}
}

func TestJobOOMs(t *testing.T) {
	now := time.Now()
	runs := []history.Run{
		{T: now.Add(-3 * time.Hour), Job: "shop/tests", Project: "shop", Service: "tests", Outcome: "oom", Name: "a"},
		{T: now.Add(-2 * time.Hour), Job: "shop/tests", Project: "shop", Service: "tests", Outcome: "pass"},
		{T: now.Add(-1 * time.Hour), Job: "shop/tests", Project: "shop", Service: "tests", Outcome: "oom", Name: "b"},
		{T: now.Add(-30 * time.Minute), Job: "alpine", Image: "alpine", Outcome: "oom", Name: "c"},
	}
	got := jobOOMs(runs)
	if len(got) != 2 || got[0].Job != "alpine" || got[1].Count != 2 || got[1].Name != "b" || got[1].Service != "tests" {
		t.Fatalf("got %+v", got)
	}
}
