package metrics

import (
	"testing"
	"time"
)

func TestAppName(t *testing.T) {
	for in, want := range map[string]string{
		"/Applications/Google Chrome.app/Contents/Frameworks/x/Google Chrome Helper (Renderer)": "Google Chrome",
		"/usr/libexec/logd": "logd",
		"/System/Library/Frameworks/Virtualization.framework/Versions/A/XPCServices/com.apple.Virtualization.VirtualMachine.xpc/Contents/MacOS/com.apple.Virtualization.VirtualMachine": "com.apple.Virtualization.VirtualMachine",
		"limactl": "limactl",
	} {
		if got := appName(in); got != want {
			t.Errorf("appName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseIostat(t *testing.T) {
	out := []byte("      cpu    load average\n us sy id   1m   5m   15m\n 19  9 72  24.00 16.71 14.94\n 74 26  0  24.00 16.71 14.94\n")
	busy, sys := parseIostat(out)
	if busy != 100 || sys != 26 {
		t.Fatalf("got busy %v sys %v", busy, sys)
	}
}

func TestParsePSAndSummarise(t *testing.T) {
	var a, b macSnap
	a = macSnap{t: time.Unix(0, 0), cpu: map[int]float64{}, comm: map[int]string{}}
	b = macSnap{t: time.Unix(10, 0), cpu: map[int]float64{}, comm: map[int]string{}, busy: 50, kernel: 10}
	parsePS([]byte("  1 01-19:25:04  31:02.85 /sbin/launchd\n 500    00:20   0:01.00 /Applications/Foo Bar.app/Contents/MacOS/Foo Bar\n"), &a)
	parsePS([]byte("  1 01-19:25:04  31:02.85 /sbin/launchd\n 500    00:30   0:21.00 /Applications/Foo Bar.app/Contents/MacOS/Foo Bar\n 620    00:01   0:00.10 /bin/sh\n"), &b)
	if a.lastPID != 500 || b.lastPID != 620 {
		t.Fatalf("lastPID %d %d", a.lastPID, b.lastPID)
	}
	if b.comm[500] != "/Applications/Foo Bar.app/Contents/MacOS/Foo Bar" {
		t.Fatalf("comm %q", b.comm[500])
	}
	l := summarise([]macSnap{a, b}, 4)
	if !l.Ready || l.Busy != 50 || l.Kernel != 10 {
		t.Fatalf("load %+v", l)
	}
	if l.SpawnPerSec != 12 { // PID 500 -> 620 in 10 s
		t.Fatalf("spawn %v", l.SpawnPerSec)
	}
	// 20 CPU seconds in 10 s on 4 cores = 50% of the Mac
	if len(l.Apps) != 1 || l.Apps[0].Name != "Foo Bar" || l.Apps[0].CPU != 50 {
		t.Fatalf("apps %+v", l.Apps)
	}
}
