package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestHasLocalName(t *testing.T) {
	old := HostsFile
	t.Cleanup(func() { HostsFile = old })
	HostsFile = filepath.Join(t.TempDir(), "hosts")
	cases := map[string]bool{
		"127.0.0.1 localhost\n":                       false,
		"# 127.0.0.1 colima.localhost\n":              false,
		"10.0.0.1 colima.localhost\n":                 false,
		"127.0.0.1\tlocalhost colima.localhost # x\n": true,
		"127.0.0.1 COLIMA.localhost\n":                true,
	}
	for body, want := range cases {
		if err := os.WriteFile(HostsFile, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := HasLocalName(); got != want {
			t.Errorf("%q: got %v", body, got)
		}
	}
	if err := os.WriteFile(HostsFile, []byte("127.0.0.1 colima.localhost\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := BaseURL(7766); got != "http://colima.localhost:7766" {
		t.Errorf("BaseURL: %s", got)
	}
	HostsFile = filepath.Join(t.TempDir(), "missing")
	if got := BaseURL(7766); got != "http://127.0.0.1:7766" {
		t.Errorf("BaseURL without hosts line: %s", got)
	}
}

func TestLocalNameHostAllowed(t *testing.T) {
	s := newTest(t)
	for host, want := range map[string]int{LocalName + ":7766": http.StatusOK, "evil.localhost:7766": http.StatusMisdirectedRequest} {
		w := do(s, "GET", "/api/overview", func(r *http.Request) { r.Host = host; withCookie(r) })
		if w.Code != want {
			t.Errorf("%s: got %d, want %d", host, w.Code, want)
		}
	}
	w := do(s, "POST", "/api/fix", func(r *http.Request) {
		r.Host = LocalName + ":7766"
		r.Header.Set("Origin", "http://"+LocalName+":7766")
		withCookie(r)
	})
	if w.Code == http.StatusForbidden {
		t.Errorf("POST from %s refused: %s", LocalName, w.Body.String())
	}
}
