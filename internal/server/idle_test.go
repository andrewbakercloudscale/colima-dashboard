package server

import (
	"testing"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
)

func newIdleProfile() *idleProfile {
	return &idleProfile{pods: map[string]bool{"old": true}, died: map[string]time.Time{}, hc: map[string][]string{}}
}

func ev(typ, action, id string, at time.Time, attrs map[string]string) dockerEvent {
	e := dockerEvent{Type: typ, Action: action, TimeNano: at.UnixNano()}
	e.Actor.ID, e.Actor.Attributes = id, attrs
	return e
}

func TestIdleClassify(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	hc := func(id string) []string {
		if id == "db" {
			return []string{"CMD", "healthcheck.sh", "--connect"}
		}
		if id == "web" {
			return []string{"CMD-SHELL", "curl -f localhost"}
		}
		return nil
	}
	ip := newIdleProfile()
	cases := []struct {
		e    dockerEvent
		used bool
	}{
		{ev("container", "exec_create: healthcheck.sh --connect", "db", t0, nil), false},
		{ev("container", "exec_start: healthcheck.sh --connect", "db", t0, nil), false},
		{ev("container", "exec_create: /bin/sh -c curl -f localhost", "web", t0, nil), false},
		{ev("container", "exec_create: bash", "db", t0, nil), true},
		{ev("container", "health_status: healthy", "db", t0, nil), false},
		// A crash loop: die then start a few seconds later.
		{ev("container", "die", "crashy", t0, nil), false},
		{ev("container", "start", "crashy", t0.Add(10*time.Second), nil), false},
		// A person starting it again much later.
		{ev("container", "start", "crashy", t0.Add(time.Hour), nil), true},
		{ev("container", "create", "new", t0, nil), true},
		{ev("container", "mount", "new", t0, nil), false},
		{ev("volume", "mount", "v", t0, nil), false},
		{ev("volume", "create", "v", t0, nil), true},
		{ev("image", "pull", "nginx", t0, nil), true},
		{ev("network", "connect", "n", t0, nil), false},
		// Kubernetes: the kubelet recreating a known pod's container, kube-system, then a new pod.
		{ev("container", "create", "k1", t0, map[string]string{"io.kubernetes.pod.uid": "old"}), false},
		{ev("container", "create", "k2", t0, map[string]string{"io.kubernetes.pod.uid": "sys", "io.kubernetes.pod.namespace": "kube-system"}), false},
		{ev("container", "start", "k3", t0, map[string]string{"io.kubernetes.pod.uid": "fresh"}), false},
		{ev("container", "create", "k3", t0, map[string]string{"io.kubernetes.pod.uid": "fresh", "io.kubernetes.pod.name": "web-1"}), true},
		{ev("container", "create", "k4", t0, map[string]string{"io.kubernetes.pod.uid": "fresh"}), false},
	}
	for i, c := range cases {
		got := ip.classify(c.e, hc)
		if (got != "") != c.used {
			t.Errorf("%d %s %s: got %q, want used=%v", i, c.e.Type, c.e.Action, got, c.used)
		}
	}
}

func TestIdleDue(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st := IdleSettings{Enabled: true, Minutes: 60}
	ip := &idleProfile{LastActive: now.Add(-56 * time.Minute)}
	if stop, warn := idleDue(st, ip, "p", now); stop || !warn {
		t.Fatalf("56 min: stop=%v warn=%v", stop, warn)
	}
	ip.warned = true
	if _, warn := idleDue(st, ip, "p", now); warn {
		t.Fatal("warned twice")
	}
	ip.LastActive = now.Add(-61 * time.Minute)
	if stop, _ := idleDue(st, ip, "p", now); !stop {
		t.Fatal("61 min should stop")
	}
	if stop, _ := idleDue(IdleSettings{Enabled: true, Minutes: 60, Keep: []string{"p"}}, ip, "p", now); stop {
		t.Fatal("kept profile stopped")
	}
	if stop, _ := idleDue(IdleSettings{Minutes: 60}, ip, "p", now); stop {
		t.Fatal("stopped while off")
	}
	ip.Snooze = now.Add(time.Minute)
	if stop, _ := idleDue(st, ip, "p", now); stop {
		t.Fatal("stopped while kept running")
	}
	// The hour counts again from the end of keep running.
	ip.Snooze = now.Add(-30 * time.Minute)
	if stop, _ := idleDue(st, ip, "p", now); stop {
		t.Fatal("stopped 30 min after keep running ended")
	}
}

func TestParseHostUse(t *testing.T) {
	ps := `  1     0 /sbin/launchd
  500   1 /usr/local/bin/colima-dashboard --no-browser
  501 500 /opt/homebrew/bin/colima ssh -p bench -- cat /proc/stat
  600   1 /bin/zsh
  601 600 docker exec -it web sh
  602 600 /opt/homebrew/bin/colima ssh -p work
  603 600 kubectl --context colima-bench logs -f web
  604 600 docker -H unix:///Users/a/.colima/other/docker.sock logs -f x
  605 600 /opt/homebrew/bin/limactl shell colima-lima2 bash
  606 600 docker-credential-osxkeychain get
  607 600 kubectl get pods -w
  608 600 /Applications/Docker Helper
`
	got := parseHostUse(ps, 500, func() string { return "colima" }, func() string { return "colima-k8s" })
	for _, p := range []string{"default", "work", "bench", "other", "lima2", "k8s"} {
		if got[p] == "" {
			t.Errorf("%s not seen as in use: %v", p, got)
		}
	}
	if len(got) != 6 {
		t.Errorf("got %v", got)
	}
	// Our own colima ssh (the metrics reader) alone does not count.
	got = parseHostUse("  500 1 colima-dashboard\n  501 500 colima ssh -p bench\n", 500, func() string { return "" }, func() string { return "" })
	if len(got) != 0 {
		t.Errorf("own child counted: %v", got)
	}
}

func TestCPUDelta(t *testing.T) {
	before := map[string]uint64{"a": 1e9, "b": 5e9}
	now := map[string]uint64{"a": 4e9, "b": 5e9, "c": 9e9}
	if got := cpuDelta(before, now, 10*time.Second); got < 0.299 || got > 0.301 {
		t.Fatalf("got %v, want 0.3", got)
	}
	if cpuDelta(nil, now, time.Second) != 0 {
		t.Fatal("first sample should be 0")
	}
}

func TestIsHealthcheck(t *testing.T) {
	if isHealthcheck("anything", []string{"NONE"}) || isHealthcheck("x", nil) {
		t.Fatal("no health check matched")
	}
}

func TestMacConns(t *testing.T) {
	out := "p100\ncpython3\nn127.0.0.1:60001->127.0.0.1:55432\n" +
		"p200\nclimactl\nn127.0.0.1:55432->127.0.0.1:60001\n" +
		"p300\ncChrome\nn192.168.1.5:60002->142.250.1.1:443\nn[::1]:60003->[::1]:8080\n" +
		"p500\nccolima-dashboard\nn127.0.0.1:60004->127.0.0.1:9999\n"
	got := parseMacConns(out, 500)
	if got[55432] != "python3" || got[8080] != "Chrome" || len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	list := []docker.Container{{Names: []string{"/db"}, State: "running", Ports: []docker.Port{{PrivatePort: 5432, PublicPort: 55432}}}}
	if r := connectedUse(list, got); r == "" {
		t.Fatal("database in use by python3 not seen")
	}
	list[0].State = "exited"
	if connectedUse(list, got) != "" {
		t.Fatal("stopped container counted")
	}
}
