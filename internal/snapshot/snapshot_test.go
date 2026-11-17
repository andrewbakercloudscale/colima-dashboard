package snapshot

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func copyFile(_ context.Context, src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTakeRestoreDelete(t *testing.T) {
	home := t.TempDir()
	s := &Store{Dir: t.TempDir(), ColimaHome: home, Instance: func(p string) string { return "colima-" + p }, Clone: copyFile}
	disk := filepath.Join(home, "_lima", "colima-dev", "disk")
	data := filepath.Join(home, "_lima", "_disks", "colima-dev", "datadisk")
	cfg := filepath.Join(home, "dev", "colima.yaml")
	write(t, disk, "v1")
	write(t, data, "d1")
	write(t, cfg, "cpus: 2")
	write(t, filepath.Join(home, "_lima", "colima-dev", "ha.stderr.log"), "noise")
	ctx := context.Background()

	sn, err := s.Take(ctx, io.Discard, "dev", "clean", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(sn.Files) != 3 {
		t.Fatalf("files %v (logs must be skipped)", sn.Files)
	}
	write(t, disk, "v2")
	write(t, data, "d2")
	write(t, cfg, "cpus: 4")
	if err := s.Restore(ctx, io.Discard, "dev", sn.ID); err != nil {
		t.Fatal(err)
	}
	if read(t, disk) != "v1" || read(t, data) != "d1" || read(t, cfg) != "cpus: 2" {
		t.Fatal("restore did not put the files back")
	}
	list, _ := s.List("dev")
	if len(list) != 2 || !list[0].Auto && !list[1].Auto {
		t.Fatalf("want the snapshot plus an automatic one: %+v", list)
	}
	var auto Snapshot
	for _, x := range list {
		if x.Auto {
			auto = x
		}
	}
	if err := s.Restore(ctx, io.Discard, "dev", auto.ID); err != nil || read(t, disk) != "v2" {
		t.Fatalf("undo restore: %v %s", err, read(t, disk))
	}
	if err := s.Delete("dev", sn.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("dev", "../x"); err == nil {
		t.Error("accepted a path as an id")
	}
	if _, err := s.Take(ctx, io.Discard, "missing", "x", false); err == nil {
		t.Error("snapshot of a profile without a VM")
	}
}
