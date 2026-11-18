package metrics

import (
	"testing"
	"time"
)

func TestSpot(t *testing.T) {
	a := parseSpot("cpu  100 0 100 800 0 0 0 0 0 0\ncpu0 1 1 1 1\ncpu1 1 1 1 1\nMemTotal: 4000 kB\nMemAvailable: 1000 kB\n0.50 0.40 0.30 1/100 42\n")
	if a.CPUs != 2 || a.MemTotal != 4000*1024 || a.MemUsed() != 3000*1024 {
		t.Fatalf("parse: %+v", a)
	}
	if a.Load1 != 0.5 {
		t.Fatalf("load: %v", a.Load1)
	}
	b := parseSpot("cpu  400 0 400 1000 0 0 0 0 0 0\n")
	b.T = a.T.Add(time.Minute)
	pct, ok := b.CPUSince(a)
	if !ok || pct != 75 {
		t.Fatalf("cpu %v %v", pct, ok)
	}
	if _, ok := b.CPUSince(nil); ok {
		t.Fatal("no previous spot should give no CPU")
	}
}
