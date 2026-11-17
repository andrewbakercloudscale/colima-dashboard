package colima

import (
	"context"
	"io"
	"strings"
	"testing"
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
