package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRancherLeftoverFiles(t *testing.T) {
	home := t.TempDir()
	plugins := filepath.Join(home, ".docker", "cli-plugins")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatal(err)
	}
	// A Rancher link that now points nowhere, and an unrelated link that must stay.
	_ = os.Symlink(filepath.Join(home, ".rd", "bin", "docker-buildx"), filepath.Join(plugins, "docker-buildx"))
	_ = os.Symlink("/opt/homebrew/bin/docker-scan", filepath.Join(plugins, "docker-scan"))
	_ = os.MkdirAll(filepath.Join(home, "Library", "Caches", "rancher-desktop"), 0o755)
	_ = os.MkdirAll(filepath.Join(home, ".rd", "bin"), 0o755)

	links := rancherLinks(home)
	if len(links) == 0 || links[0].ID != "rancher-plugins" {
		t.Fatalf("want the plugin link reported, got %+v", links)
	}
	if !strings.Contains(links[0].Fix, "docker-buildx") || strings.Contains(links[0].Fix, "docker-scan") {
		t.Errorf("fix should remove only the Rancher link: %q", links[0].Fix)
	}
	dirs := rancherDataDirs(home)
	if len(dirs) != 2 {
		t.Fatalf("want the cache and ~/.rd, got %v", dirs)
	}
	// The fix really works on these paths, quoting and all.
	if out, err := exec.Command("/bin/sh", "-c", links[0].Fix).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Lstat(filepath.Join(plugins, "docker-scan")); err != nil {
		t.Error("the unrelated plugin link went too")
	}
	if len(rancherLinks(home)) != 0 {
		t.Error("still reported after the fix")
	}
}

func TestCleanupShellQuote(t *testing.T) {
	got, err := exec.Command("/bin/sh", "-c", "printf %s "+shq("it's a ~/Library/Application Support")).Output()
	if err != nil || string(got) != "it's a ~/Library/Application Support" {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestCleanupNeedsNoPasswordPrompt(t *testing.T) {
	s := &Server{}
	err := s.cleanupStep(t.Context(), nil, "default", Check{ID: "rancher-sock", Action: "terminal", Fix: "sudo rm /var/run/docker.sock"})
	if err != errNeedsPassword {
		t.Fatalf("a sudo fix must be skipped, got %v", err)
	}
}

func TestIsCleanup(t *testing.T) {
	for _, tc := range []struct {
		c    Check
		want bool
	}{
		{Check{ID: "rancher-plugins", State: "warn", Fix: "rm x"}, true},
		{Check{ID: "rancher-plugins", State: "pass", Fix: "rm x"}, false},
		{Check{ID: "certs", State: "fail", Fix: "Copy 3 certificates", Action: "certs"}, true},
		{Check{ID: "context", State: "warn", Detail: `current context is "colima"`, Fix: "docker context use colima-bench"}, false},
		{Check{ID: "context", State: "warn", Detail: "DOCKER_HOST is set to unix:///x", Fix: "launchctl unsetenv DOCKER_HOST"}, true},
		{Check{ID: "disk", State: "warn", Fix: "docker system prune"}, false},
	} {
		if got := isCleanup(tc.c); got != tc.want {
			t.Errorf("%s %s: got %v", tc.c.ID, tc.c.Detail, got)
		}
	}
}
