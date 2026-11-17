package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildArgs(t *testing.T) {
	dir := t.TempDir()
	if _, err := buildArgs(dir, "app:dev", ""); err == nil || !strings.Contains(err.Error(), "no Dockerfile") {
		t.Fatalf("want missing Dockerfile error, got %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, err := buildArgs(dir, "app:dev", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(args, " "); got != "build --progress=plain -t app:dev "+dir {
		t.Fatalf("got %q", got)
	}
	for _, bad := range []struct{ dir, tag, file string }{
		{"relative/path", "", ""},
		{dir, "-oops", ""},
		{dir, "", "missing.Dockerfile"},
		{filepath.Join(dir, "Dockerfile"), "", ""},
	} {
		if _, err := buildArgs(bad.dir, bad.tag, bad.file); err == nil {
			t.Errorf("buildArgs(%q, %q, %q) should fail", bad.dir, bad.tag, bad.file)
		}
	}
}
