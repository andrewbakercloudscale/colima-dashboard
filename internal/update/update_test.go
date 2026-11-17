package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.2.0", "1.1.9", true},
		{"v0.10.4", "0.10.3", true},
		{"0.10.3", "0.10.3", false},
		{"0.10", "0.10.1", false},
		{"1.0.0-rc1", "0.9", true},
		{"dev", "1.0.0", false},
		{"1.0.0", "dev", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func archiveWith(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write(body)
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

// fakeGitHub serves one release of Colima Dashboard and of Colima.
func fakeGitHub(t *testing.T, archive []byte, sumOverride string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	asset := AssetName("1.1.0")
	sum := sha256.Sum256(archive)
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset)
	if sumOverride != "" {
		sums = fmt.Sprintf("%s  %s\n", sumOverride, asset)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+Repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v1.1.0", "name": "1.1.0", "html_url": "https://example/rel",
			"assets": []map[string]string{
				{"name": asset, "browser_download_url": srv.URL + "/dl/archive"},
				{"name": "checksums.txt", "browser_download_url": srv.URL + "/dl/sums"},
			},
		})
	})
	mux.HandleFunc("/repos/"+ColimaRepo+"/releases", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"tag_name": "v0.11.0-rc1", "prerelease": true},
			{"tag_name": "v0.10.4", "name": "v0.10.4", "body": "## Highlights\n<!-- x -->\nFaster starts."},
		})
	})
	mux.HandleFunc("/dl/archive", func(w http.ResponseWriter, r *http.Request) { w.Write(archive) })
	mux.HandleFunc("/dl/sums", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(sums)) })
	srv = httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newUpdater(t *testing.T, srv *httptest.Server) (*Updater, string) {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "colima-dashboard")
	os.WriteFile(exe, []byte("old"), 0o755)
	u := &Updater{
		Current: "1.0.0", Exe: exe, SettingsPath: filepath.Join(dir, "settings.json"),
		ColimaVersion: func(context.Context) (string, error) { return "0.10.3", nil },
		ColimaBin:     "/nonexistent/colima",
		HTTP:          srv.Client(), API: srv.URL,
	}
	u.Load()
	return u, exe
}

func TestCheckAndInstall(t *testing.T) {
	srv := fakeGitHub(t, archiveWith(t, "colima-dashboard", []byte("new binary")), "")
	u, exe := newUpdater(t, srv)
	restarted := make(chan bool, 1)
	u.Restart = func() { restarted <- true }

	st := u.Check(context.Background(), true)
	if st.CheckErr != "" {
		t.Fatal(st.CheckErr)
	}
	if !st.Dashboard.Newer || !st.Dashboard.CanUpdate {
		t.Fatalf("dashboard %+v", st.Dashboard)
	}
	if !st.Colima.Newer || st.Colima.Latest.Version != "0.10.4" || len(st.ColimaNews) != 1 {
		t.Fatalf("colima %+v news %d", st.Colima, len(st.ColimaNews))
	}
	if strings.Contains(st.ColimaNews[0].Notes, "<!--") || strings.Contains(st.ColimaNews[0].Notes, "#") {
		t.Fatalf("notes not cleaned: %q", st.ColimaNews[0].Notes)
	}
	if err := u.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new binary" {
		t.Fatalf("exe now %q", b)
	}
	<-restarted
	if p := u.Snapshot().Dashboard.Phase; p != "installed" {
		t.Fatalf("phase %q", p)
	}
}

func TestInstallRefusesBadChecksum(t *testing.T) {
	srv := fakeGitHub(t, archiveWith(t, "colima-dashboard", []byte("evil")), strings.Repeat("0", 64))
	u, exe := newUpdater(t, srv)
	u.Check(context.Background(), true)
	if err := u.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("exe changed to %q", b)
	}
	if st := u.Snapshot(); st.Dashboard.Phase != "failed" {
		t.Fatalf("phase %q", st.Dashboard.Phase)
	}
}

func TestDevBuildNeverUpdates(t *testing.T) {
	srv := fakeGitHub(t, archiveWith(t, "colima-dashboard", []byte("x")), "")
	u, _ := newUpdater(t, srv)
	u.Current = "dev"
	st := u.Check(context.Background(), true)
	if st.Dashboard.CanUpdate || st.Dashboard.Newer {
		t.Fatalf("dev build offered an update: %+v", st.Dashboard)
	}
	if err := u.Install(context.Background()); err == nil {
		t.Fatal("dev build installed an update")
	}
}

func TestSettingsDefaultOnAndSaved(t *testing.T) {
	srv := fakeGitHub(t, archiveWith(t, "colima-dashboard", []byte("x")), "")
	u, _ := newUpdater(t, srv)
	if !u.Snapshot().Auto {
		t.Fatal("automatic updates should default to on")
	}
	if err := u.SetAuto(false); err != nil {
		t.Fatal(err)
	}
	u2 := &Updater{SettingsPath: u.SettingsPath}
	u2.Load()
	if u2.Snapshot().Auto {
		t.Fatal("setting not saved")
	}
}
