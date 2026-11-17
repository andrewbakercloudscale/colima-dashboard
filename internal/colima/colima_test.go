package colima

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type rec struct {
	out  string
	runs []string
}

func (r *rec) Output(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.runs = append(r.runs, strings.Join(args, " "))
	return []byte(r.out), nil
}

func (r *rec) Stream(_ context.Context, w io.Writer, _ string, args ...string) error {
	r.runs = append(r.runs, strings.Join(args, " "))
	return nil
}

func TestList(t *testing.T) {
	r := &rec{out: `{"name":"default","status":"Running","cpus":2,"memory":2147483648,"runtime":"docker"}
{"name":"dev","status":"Stopped","cpus":4,"memory":8589934592,"runtime":"containerd"}
`}
	c := New("colima")
	c.Runner = r
	ps, err := c.List(context.Background())
	if err != nil || len(ps) != 2 || ps[1].Name != "dev" || ps[1].CPUs != 4 {
		t.Fatalf("%v %+v", err, ps)
	}
}

func TestStartArgsAndValidation(t *testing.T) {
	on := true
	args, err := StartOptions{CPUs: 4, MemoryGiB: 8, VMType: "vz", Kubernetes: &on}.Args()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(args, " "); got != "--cpus 4 --memory 8 --vm-type vz --kubernetes=true" {
		t.Fatalf("got %q", got)
	}
	for _, bad := range []StartOptions{{Runtime: "docker; rm -rf ~"}, {VMType: "--foo"}, {CPUs: 1000}, {MountType: "nfs"}} {
		if _, err := bad.Args(); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestProfileValidation(t *testing.T) {
	for _, ok := range []string{"default", "dev-1", "a_b"} {
		if !ValidProfile(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "-p", "a b", "../x", "x;y", strings.Repeat("a", 65)} {
		if ValidProfile(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestApplyStopsThenStarts(t *testing.T) {
	r := &rec{}
	c := New("colima")
	c.Runner = r
	off := false
	if err := c.Run(context.Background(), io.Discard, ActionApply, "dev", &StartOptions{Kubernetes: &off}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.runs, " | "); got != "stop -p dev | start -p dev --kubernetes=false" {
		t.Fatalf("got %q", got)
	}
}

func TestKubernetesReset(t *testing.T) {
	r := &rec{}
	c := New("colima")
	c.Runner = r
	if err := c.Run(context.Background(), io.Discard, ActionK8sReset, "dev", nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.runs, " | "); got != "kubernetes reset -p dev" {
		t.Fatalf("got %q", got)
	}
}

func TestOneOperationPerProfile(t *testing.T) {
	c := New("colima")
	if err := c.Claim("dev", "start"); err != nil {
		t.Fatal(err)
	}
	if err := c.Run(context.Background(), io.Discard, ActionStop, "dev", nil); err != ErrBusy {
		t.Fatalf("got %v, want ErrBusy", err)
	}
	c.Release("dev")
}

func TestNames(t *testing.T) {
	if LimaInstance("default") != "colima" || LimaInstance("dev") != "colima-dev" {
		t.Fatal("lima instance names")
	}
}

func TestKubeSettings(t *testing.T) {
	ks := parseKubeSettings(`runtime: docker
kubernetes:
  # Enable kubernetes.
  enabled: false
  version: v1.35.0+k3s1
  k3sArgs:
    - --disable=traefik
    - "--disable=servicelb,traefik"
  port: 0
network:
  address: false
`)
	if ks.Version != "v1.35.0+k3s1" || len(ks.K3sArgs) != 2 || ks.K3sArgs[1] != "--disable=servicelb,traefik" {
		t.Fatalf("parsed %+v", ks)
	}
	if ks.TraefikOn() {
		t.Error("traefik should be off")
	}
	on := ks.WithTraefik(true)
	if len(on) != 1 || on[0] != "--disable=servicelb" {
		t.Errorf("WithTraefik(true) = %q", on)
	}
	if (KubeSettings{K3sArgs: on}).TraefikOn() != true {
		t.Error("traefik should be on")
	}
	if got := parseKubeSettings("kubernetes:\n  k3sArgs: []\n").WithTraefik(false); len(got) != 1 || got[0] != "--disable=traefik" {
		t.Errorf("empty list off: %q", got)
	}
	args, err := StartOptions{KubernetesVersion: "v1.33.5+k3s1", SetK3sArgs: true, K3sArgs: on}.Args()
	if err != nil || strings.Join(args, " ") != `--kubernetes-version v1.33.5+k3s1 --k3s-arg="--disable=servicelb"` {
		t.Errorf("args %q %v", args, err)
	}
	if args, _ := (StartOptions{SetK3sArgs: true}).Args(); len(args) != 1 || args[0] != "--k3s-arg=" {
		t.Errorf("empty k3s args: %q", args)
	}
	for _, bad := range []string{"1.33", "v1.33.5", "v1.33.5+k3s1; rm", "latest"} {
		if _, err := (StartOptions{KubernetesVersion: bad}).Args(); err == nil {
			t.Errorf("accepted version %q", bad)
		}
	}
}

// An idle dashboard must not run colima: a settled list is kept while colima's files look the
// same, and fetched again when a VM starts or stops or an action runs.
func TestListKeptUntilFilesChange(t *testing.T) {
	home := t.TempDir()
	inst := filepath.Join(home, "_lima", "colima")
	if err := os.MkdirAll(inst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "default"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &rec{out: `{"name":"default","status":"Running"}` + "\n"}
	c := New("colima")
	c.Runner, c.Home = r, home
	list := func() {
		c.cmu.Lock()
		c.list.at = c.list.at.Add(-time.Minute) // past the short TTL
		c.cmu.Unlock()
		if _, err := c.List(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	list()
	list()
	list()
	if len(r.runs) != 1 {
		t.Fatalf("idle: colima ran %d times, want 1", len(r.runs))
	}
	// A VM starts: its host agent writes ha.pid.
	if err := os.WriteFile(filepath.Join(inst, "ha.pid"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	list()
	if len(r.runs) != 2 {
		t.Fatalf("after start: colima ran %d times, want 2", len(r.runs))
	}
	c.Forget()
	list()
	if len(r.runs) != 3 {
		t.Fatalf("after Forget: colima ran %d times, want 3", len(r.runs))
	}
	// A profile on its way up is asked again after the short TTL even with the files unchanged.
	r.out = `{"name":"default","status":"Starting"}` + "\n"
	c.Forget()
	list()
	list()
	if len(r.runs) != 5 {
		t.Fatalf("starting: colima ran %d times, want 5", len(r.runs))
	}
}
