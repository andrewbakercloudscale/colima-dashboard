package server

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func postFix(s *Server, body string) (int, string) {
	w := do(s, "POST", "/api/fix", func(r *http.Request) {
		withCookie(r)
		r.Header.Set("Origin", "http://127.0.0.1:7766")
		r.Header.Set("Content-Type", "application/json")
		r.Body = ioNopCloser(body)
		r.ContentLength = int64(len(body))
	})
	return w.Code, w.Body.String()
}

// The page names a fix; the command run is always the server's own, from the checks.
func TestFixRunsOnlyTheServersCommand(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no docker: the "dockercli" check fails with a brew fix
	t.Setenv("HOME", t.TempDir())
	s := newTest(t)
	s.latestTag, s.latestAt = "0.10.3", time.Now() // no GitHub call
	var opened []string
	s.opt.OpenTerminal = func(cmd string) error { opened = append(opened, cmd); return nil }

	// A command from the page is refused outright, not ignored.
	if code, _ := postFix(s, `{"profile":"default","id":"dockercli","command":"rm -rf /"}`); code != http.StatusBadRequest {
		t.Errorf("command in body: got %d, want 400", code)
	}
	code, _ := postFix(s, `{"profile":"default","id":"dockercli"}`)
	if code != http.StatusOK || len(opened) != 1 || opened[0] != "brew install docker" {
		t.Fatalf("dockercli fix: code %d, opened %q", code, opened)
	}
	for _, body := range []string{
		`{"profile":"default","id":"vm"}`,      // the page starts the VM itself, not Terminal
		`{"profile":"default","id":"nope"}`,    // no such check
		`{"profile":"default","id":"colima"}`,  // passing: nothing to fix
		`{"profile":"default","id":"rebuild"}`, // not a dev build
	} {
		if code, _ := postFix(s, body); code != http.StatusConflict {
			t.Errorf("%s: got %d, want 409", body, code)
		}
	}
	if code, _ := postFix(s, `{"profile":"../x","id":"dockercli"}`); code != http.StatusBadRequest {
		t.Errorf("bad profile: got %d", code)
	}
	if len(opened) != 1 {
		t.Errorf("Terminal opened %d times, want 1: %q", len(opened), opened)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/a b/it's"); got != `'/a b/it'\''s'` {
		t.Errorf("got %s", got)
	}
}

func ioNopCloser(s string) *nopBody { return &nopBody{strings.NewReader(s)} }

type nopBody struct{ *strings.Reader }

func (nopBody) Close() error { return nil }
