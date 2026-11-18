package server

import (
	"net/http"
	"testing"
)

func TestVMTerminalOpensColimaSSH(t *testing.T) {
	s := newTest(t)
	s.opt.Colima.Bin = "/opt/my tools/colima"
	var opened []string
	s.opt.OpenTerminal = func(cmd string) error { opened = append(opened, cmd); return nil }
	post := func(path string) int {
		return do(s, "POST", path, func(r *http.Request) {
			withCookie(r)
			r.Header.Set("Origin", "http://127.0.0.1:7766")
		}).Code
	}
	if code := post("/api/profiles/dev/vm/terminal"); code != http.StatusOK {
		t.Fatalf("got %d", code)
	}
	if len(opened) != 1 || opened[0] != `'/opt/my tools/colima' ssh -p dev` {
		t.Fatalf("opened %q", opened)
	}
	if code := post("/api/profiles/a;b/vm/terminal"); code != http.StatusBadRequest {
		t.Errorf("bad profile: got %d", code)
	}
	if len(opened) != 1 {
		t.Errorf("Terminal opened %d times", len(opened))
	}
}

func TestShellUserAndQuote(t *testing.T) {
	for u, ok := range map[string]bool{"root": true, "www-data": true, "1000": true, "1000:1000": true,
		"": false, "a b": false, "root;id": false, "-u": false, "$(id)": false} {
		if shellUser.MatchString(u) != ok {
			t.Errorf("shellUser(%q) = %v", u, !ok)
		}
	}
	if got := shQuote("/opt/homebrew/bin/colima"); got != "/opt/homebrew/bin/colima" {
		t.Errorf("plain path quoted: %s", got)
	}
	if got := shQuote("/a b/it's"); got != `'/a b/it'\''s'` {
		t.Errorf("got %s", got)
	}
}
