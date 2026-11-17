package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func frame(stream byte, s string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
	return append(h, s...)
}

func TestDemux(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(frame(1, "hello\nwor"))
	buf.Write(frame(2, "oops\n"))
	buf.Write(frame(1, "ld\nlast"))
	var got []string
	err := demux(&buf, func(s, l string) error { got = append(got, s+":"+l); return nil })
	if err != nil {
		t.Fatal(err)
	}
	want := "stdout:hello|stderr:oops|stdout:world|stdout:last"
	if strings.Join(got, "|") != want {
		t.Fatalf("got %q", strings.Join(got, "|"))
	}
}

func TestSplitRef(t *testing.T) {
	for in, want := range map[string]string{
		"nginx":                       "nginx latest",
		"nginx:1.27":                  "nginx 1.27",
		"localhost:5000/app":          "localhost:5000/app latest",
		"localhost:5000/app:v2":       "localhost:5000/app v2",
		"alpine@sha256:abc":           "alpine sha256:abc",
		"ghcr.io/org/tool:1.0-alpine": "ghcr.io/org/tool 1.0-alpine",
	} {
		i, tag := splitRef(in)
		if i+" "+tag != want {
			t.Errorf("%s: got %s %s", in, i, tag)
		}
	}
}

func TestValidRef(t *testing.T) {
	for _, ok := range []string{"nginx", "abc123", "sha256:0f", "my_vol", "ghcr.io/a/b:1"} {
		if !ValidRef(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "../x", "a b", "-rf", "x?y", "a\nb"} {
		if ValidRef(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestComputeStats(t *testing.T) {
	var r rawStats
	r.CPUStats.CPUUsage.TotalUsage = 2_000_000
	r.PreCPUStats.CPUUsage.TotalUsage = 1_000_000
	r.CPUStats.SystemUsage = 20_000_000
	r.PreCPUStats.SystemUsage = 10_000_000
	r.CPUStats.OnlineCPUs = 4
	r.MemoryStats.Usage = 100
	r.MemoryStats.Stats = map[string]uint64{"inactive_file": 30}
	s := computeStats(r)
	if s.CPUPercent != 40 || s.MemoryBytes != 70 {
		t.Fatalf("got %+v", s)
	}
}

// A fake engine on a real Unix socket checks paths, methods and error mapping.
func TestClientAgainstFakeEngine(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "cui")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /containers/json", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("all") != "1" {
			t.Error("containers must list all")
		}
		w.Write([]byte(`[{"Id":"abc","Names":["/web"],"State":"running","Ports":[{"PrivatePort":80,"PublicPort":8080,"Type":"tcp"}]}]`))
	})
	mux.HandleFunc("POST /containers/abc/stop", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotModified) })
	mux.HandleFunc("DELETE /containers/gone", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"No such container: gone"}`))
	})
	mux.HandleFunc("POST /volumes/prune", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("filters"), "all") {
			t.Error("volume prune must ask for all unused volumes")
		}
		w.Write([]byte(`{"VolumesDeleted":["a","b"],"SpaceReclaimed":42}`))
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	c := New("unix://" + sock)
	ctx := context.Background()
	cs, err := c.Containers(ctx)
	if err != nil || len(cs) != 1 || cs[0].Ports[0].PublicPort != 8080 {
		t.Fatalf("containers: %v %+v", err, cs)
	}
	if err := c.ContainerAction(ctx, "abc", "stop"); err != nil {
		t.Fatalf("304 means already stopped, not an error: %v", err)
	}
	if err := c.ContainerAction(ctx, "abc", "kill"); err == nil {
		t.Fatal("unknown action accepted")
	}
	if err := c.RemoveContainer(ctx, "gone", false); !IsNotFound(err) || !strings.Contains(err.Error(), "No such container") {
		t.Fatalf("404 mapping: %v", err)
	}
	rep, err := c.Prune(ctx, "volumes")
	if err != nil || rep.Deleted != 2 || rep.SpaceReclaimed != 42 {
		t.Fatalf("prune: %v %+v", err, rep)
	}
}
