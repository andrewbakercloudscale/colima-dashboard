package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFindCompose(t *testing.T) {
	root := t.TempDir()
	mk := func(rel, file string) {
		d := filepath.Join(root, rel)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if file != "" {
			if err := os.WriteFile(filepath.Join(d, file), []byte("services: {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("app", "compose.yaml")
	mk("old", "docker-compose.yml")
	mk("deep/a/b/c/d/e", "compose.yml")      // too deep
	mk("app/node_modules/x", "compose.yaml") // skipped folder
	mk(".hidden", "compose.yaml")            // hidden
	mk("both", "compose.yaml")               // two files: listed once, compose.yaml wins
	mk("both", "docker-compose.yml")
	got := findCompose(root, 4, 50, time.Now().Add(time.Minute))
	byDir := map[string]string{}
	for _, p := range got {
		rel, _ := filepath.Rel(root, p.Dir)
		byDir[rel] = p.File
	}
	want := map[string]string{"app": "compose.yaml", "old": "docker-compose.yml", "both": "compose.yaml"}
	if len(byDir) != len(want) {
		t.Fatalf("found %v, want %v", byDir, want)
	}
	for d, f := range want {
		if byDir[d] != f {
			t.Errorf("%s: got %q, want %q", d, byDir[d], f)
		}
	}
	if n := len(findCompose(root, 4, 1, time.Now().Add(time.Minute))); n != 1 {
		t.Errorf("limit 1 returned %d", n)
	}
}

func TestComposeDir(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d, f, err := composeDir(root); err != nil || d != root || f != "docker-compose.yml" {
		t.Errorf("folder: %q %q %v", d, f, err)
	}
	if d, f, err := composeDir(filepath.Join(root, "docker-compose.yml")); err != nil || d != root || f != "docker-compose.yml" {
		t.Errorf("file: %q %q %v", d, f, err)
	}
	for _, bad := range []string{"relative/path", "", filepath.Join(root, "missing"), t.TempDir()} {
		if _, _, err := composeDir(bad); err == nil {
			t.Errorf("composeDir(%q) accepted", bad)
		}
	}
}

func TestParseComposeConfigAndPS(t *testing.T) {
	cfg := []byte(`{"name":"shop","services":{
		"web":{"image":"nginx:1.27","ports":[{"target":80,"published":"8080","protocol":"tcp"}]},
		"api":{"build":{"context":"."},"image":"shop-api"},
		"db":{"image":"postgres:16","ports":[{"target":5432}]}}}`)
	name, svcs, err := parseComposeConfig(cfg)
	if err != nil || name != "shop" || len(svcs) != 3 {
		t.Fatalf("parse: %q %+v %v", name, svcs, err)
	}
	if svcs[0].Name != "api" || !svcs[0].Build || svcs[2].Ports[0] != "8080→80" || svcs[1].Ports[0] != "5432" {
		t.Errorf("services: %+v", svcs)
	}
	// newer compose prints one object per line
	applyComposePS(svcs, []byte(`{"Service":"web","State":"running","Health":"healthy"}
{"Service":"web","State":"exited"}
{"Service":"db","State":"running"}
{"Service":"ghost","State":"running"}`))
	st := map[string]ComposeService{}
	for _, s := range svcs {
		st[s.Name] = s
	}
	if st["web"].State != "partial" || st["web"].Running != 1 || st["web"].Containers != 2 || st["web"].Health != "healthy" {
		t.Errorf("web: %+v", st["web"])
	}
	if st["db"].State != "running" || st["api"].State != "" {
		t.Errorf("db %+v api %+v", st["db"], st["api"])
	}
	// older compose prints one array
	_, svcs, _ = parseComposeConfig(cfg)
	applyComposePS(svcs, []byte(`[{"Service":"api","State":"exited"}]`))
	if svcs[0].State != "exited" {
		t.Errorf("array form: %+v", svcs[0])
	}
	if _, _, err := parseComposeConfig([]byte("not json")); err == nil {
		t.Error("bad config accepted")
	}
}

func TestValidServiceName(t *testing.T) {
	for _, ok := range []string{"web", "api_1", "my.svc-2"} {
		if !validServiceName(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "a b", "x;rm", "--build", "../x", ".x"} {
		if validServiceName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestBackupNamesAndList(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COLIMA_DASHBOARD_BACKUP_DIR", dir)
	for _, f := range []string{"pgdata@20261101-120000.tar.gz", "pgdata@20261120-090000.tar.gz", "cache_v2@20261115-000000.tar.gz",
		"notes.txt", "bad@2026.tar.gz", "pgdata@20261121-000000.tar.gz.part"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	list, err := listBackups(dir)
	if err != nil || len(list) != 3 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if list[0].File != "pgdata@20261120-090000.tar.gz" || list[0].Volume != "pgdata" || list[1].Volume != "cache_v2" {
		t.Errorf("order or parse wrong: %+v", list)
	}
	if _, err := backupPath("../etc/passwd"); err == nil {
		t.Error("path escape accepted")
	}
	if _, err := backupPath("pgdata@20261121-000000.tar.gz"); err == nil {
		t.Error("missing backup accepted")
	}
	if p, err := backupPath("pgdata@20261101-120000.tar.gz"); err != nil || filepath.Dir(p) != dir {
		t.Errorf("backupPath: %q %v", p, err)
	}
	if l, err := listBackups(filepath.Join(dir, "none")); err != nil || l == nil || len(l) != 0 {
		t.Errorf("missing folder: %v %v", l, err)
	}
}

// The restore script must leave the volume alone when the archive is broken, and replace its
// contents (including hidden files) when it is good.
func TestRestoreScript(t *testing.T) {
	vol := t.TempDir()
	write := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(vol, "old.txt"), "old")
	write(filepath.Join(vol, ".hidden"), "old")

	run := func(stdin string) error {
		cmd := exec.Command("/bin/sh", "-c", restoreScript, "sh", vol)
		f, err := os.Open(stdin)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		cmd.Stdin = f
		out, err := cmd.CombinedOutput()
		t.Logf("%s", out)
		return err
	}
	broken := filepath.Join(t.TempDir(), "broken.tar.gz")
	write(broken, "this is not gzip")
	if run(broken) == nil {
		t.Fatal("broken archive restored")
	}
	if _, err := os.Stat(filepath.Join(vol, "old.txt")); err != nil {
		t.Fatal("broken archive wiped the volume")
	}

	src := t.TempDir()
	write(filepath.Join(src, "new.txt"), "new")
	write(filepath.Join(src, "sub", "deep.txt"), "deep")
	good := filepath.Join(t.TempDir(), "good.tar.gz")
	if out, err := exec.Command("tar", "-C", src, "-czf", good, ".").CombinedOutput(); err != nil {
		t.Fatalf("tar: %v %s", err, out)
	}
	if err := run(good); err != nil {
		t.Fatalf("good archive failed: %v", err)
	}
	for _, gone := range []string{"old.txt", ".hidden"} {
		if _, err := os.Stat(filepath.Join(vol, gone)); err == nil {
			t.Errorf("%s survived the restore", gone)
		}
	}
	if b, err := os.ReadFile(filepath.Join(vol, "sub", "deep.txt")); err != nil || string(b) != "deep" {
		t.Errorf("restored file wrong: %q %v", b, err)
	}
}
