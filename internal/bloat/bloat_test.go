package bloat

import (
	"archive/tar"
	"bytes"
	"strings"
	"testing"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
)

func TestCleanStep(t *testing.T) {
	cases := []struct{ in, kind, text string }{
		{`/bin/sh -c #(nop)  CMD ["node"]`, "CMD", `CMD ["node"]`},
		{`/bin/sh -c apt-get update && apt-get install -y curl`, "RUN", "RUN apt-get update && apt-get install -y curl"},
		{`RUN /bin/sh -c npm ci # buildkit`, "RUN", "RUN npm ci"},
		{`RUN |2 A=b C=d /bin/sh -c make   install # buildkit`, "RUN", "RUN make install"},
		{`COPY . . # buildkit`, "COPY", "COPY . ."},
		{`/bin/sh -c #(nop) ADD file:abc in / `, "ADD", "ADD file:abc in /"},
		{``, "", "(imported layer, no build step recorded)"},
	}
	for _, c := range cases {
		k, s := CleanStep(c.in)
		if k != c.kind || s != c.text {
			t.Errorf("CleanStep(%q) = %q, %q; want %q, %q", c.in, k, s, c.kind, c.text)
		}
	}
}

func fakeTar(t *testing.T, files map[string]int, links map[string]string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, size := range files {
		body := []byte(strings.Repeat("x", size))
		if name == "etc/os-release" {
			body = []byte("ID=debian\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n")
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		tw.Write(body)
	}
	for name, target := range links {
		tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: target})
	}
	tw.Close()
	return &buf
}

func TestWalk(t *testing.T) {
	tree, err := Walk(fakeTar(t, map[string]int{
		"usr/local/go/bin/go":          3000,
		"usr/local/go/src/x.go":        2000,
		"app/.git/objects/pack":        500,
		"app/node_modules/a/.git/x":    100,
		"var/lib/apt/lists/foo":        700,
		"tmp/build.tar.gz":             900,
		"etc/os-release":               0,
		"usr/share/doc/readme":         10,
		"usr/lib/x86_64-linux-gnu/a.a": 50,
	}, map[string]string{"usr/bin/sh": "dash", "usr/bin/gcc": "gcc-12"}))
	if err != nil {
		t.Fatal(err)
	}
	if tree.Dir("/usr/local/go") != 5000 || tree.Dir("/usr") != 5060 {
		t.Errorf("dirs: go=%d usr=%d", tree.Dir("/usr/local/go"), tree.Dir("/usr"))
	}
	if tree.Named[".git"] != 600 || tree.Named["node_modules"] != 100 {
		t.Errorf("named: %v", tree.Named)
	}
	if !tree.Has["go"] || !tree.Has["gcc"] || !tree.Has["sh"] {
		t.Errorf("has: %v", tree.Has)
	}
	if len(tree.Archives) != 1 || tree.Archives[0].Path != "/tmp/build.tar.gz" {
		t.Errorf("archives: %v", tree.Archives)
	}
	if tree.Big[0].Path != "/usr/local/go/bin/go" {
		t.Errorf("biggest: %v", tree.Big[0])
	}
	if osField(tree.OSRelease, "ID") != "debian" {
		t.Errorf("os-release: %q", tree.OSRelease)
	}
}

func img(size int64, user string, env ...string) *docker.ImageConfig {
	c := &docker.ImageConfig{ID: "sha256:abc", Size: size}
	c.Config.User = user
	c.Config.Env = env
	return c
}

func has(r *Report, id string) bool {
	for _, f := range r.Findings {
		if f.ID == id {
			return true
		}
	}
	return false
}

func TestAnalyzeQuick(t *testing.T) {
	hist := []docker.HistoryEntry{ // newest first, as the engine returns them
		{CreatedBy: `RUN /bin/sh -c chown -R app /app # buildkit`, Size: 80 * mb},
		{CreatedBy: `RUN /bin/sh -c rm -rf /app/tmp # buildkit`, Size: 0},
		{CreatedBy: `COPY . /app # buildkit`, Size: 80 * mb},
		{CreatedBy: `RUN /bin/sh -c apt-get update && apt-get install -y build-essential # buildkit`, Size: 300 * mb},
		{CreatedBy: `RUN /bin/sh -c npm install # buildkit`, Size: 200 * mb},
		{CreatedBy: `/bin/sh -c #(nop) ADD file:x in / `, Size: 120 * mb},
	}
	r := Analyze("demo:1", img(100*mb, "", "NODE_VERSION=22"), hist, nil)
	for _, id := range []string{"chown-layer", "cleanup-layer", "copy-all", "apt-recommends", "apt-lists", "npm-dev", "root"} {
		if !has(r, id) {
			t.Errorf("missing finding %s in %+v", id, r.Findings)
		}
	}
	if r.Size != 780*mb {
		t.Errorf("size should be the unpacked layer sum, got %d", r.Size)
	}
	if r.Stack != "node" || r.Findings[0].Severity != "high" || r.Grade == "A" {
		t.Errorf("stack=%s first=%v grade=%s", r.Stack, r.Findings[0], r.Grade)
	}
	if r.Layers[0].Kind != "ADD" || !strings.Contains(r.Dockerfile, "--omit=dev") {
		t.Errorf("layers or dockerfile wrong: %v", r.Layers[0])
	}
}

func TestChownInsideInstallIsNotFlagged(t *testing.T) {
	hist := []docker.HistoryEntry{
		{CreatedBy: `RUN /bin/sh -c set -ex; apt-get install -y --no-install-recommends mariadb-server && rm -rf /var/lib/apt/lists/*; chown -R mysql /var/lib/mysql # buildkit`, Size: 250 * mb},
	}
	r := Analyze("m:1", img(250*mb, "mysql"), hist, nil)
	if has(r, "chown-layer") || has(r, "root") || has(r, "apt-lists") {
		t.Errorf("unexpected findings: %+v", r.Findings)
	}
}

func TestAnalyzeDeep(t *testing.T) {
	tree, _ := Walk(fakeTar(t, map[string]int{
		"usr/local/go/bin/go":     40 * mb,
		"root/.cache/go-build/aa": 30 * mb,
		"app/server":              8 * mb,
		"etc/os-release":          0,
	}, map[string]string{"bin/sh": "dash"}))
	hist := []docker.HistoryEntry{
		{CreatedBy: `RUN /bin/sh -c go build -o /app/server . # buildkit`, Size: 38 * mb},
		{CreatedBy: `/bin/sh -c #(nop) ADD file:x in / `, Size: 60 * mb},
	}
	r := Analyze("go:1", img(98*mb, "1000", "GOLANG_VERSION=1.23"), hist, tree)
	if r.Stack != "go" || !has(r, "toolchain") || !has(r, "caches") {
		t.Fatalf("stack=%s findings=%+v", r.Stack, r.Findings)
	}
	if r.Saving < 60*mb || r.Wasted < 20*mb-1024 || r.Wasted > 20*mb {
		t.Errorf("saving=%d wasted=%d", r.Saving, r.Wasted)
	}
	if !strings.Contains(r.Dockerfile, "distroless/static") || len(r.Dirs) == 0 {
		t.Errorf("dockerfile or dirs missing")
	}
}

func TestSuggestAlpine(t *testing.T) {
	if d, _ := Suggest("", "alpine"); !strings.Contains(d, "apk add --no-cache") {
		t.Error("an Alpine image should get an Alpine Dockerfile")
	}
	if d, _ := Suggest("", ""); !strings.Contains(d, "bookworm-slim") {
		t.Error("the default should be Debian slim")
	}
}

func TestOnlyCmds(t *testing.T) {
	if !onlyCmds("run mkdir -p /a && chown -r app /a", "chown", "mkdir") {
		t.Error("mkdir and chown only should match")
	}
	if onlyCmds("run apt-get install x; chown -r a /b", "chown", "mkdir") {
		t.Error("an install step should not match")
	}
}
