package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
)

type fakeRunner struct{ out map[string]string }

func (f fakeRunner) Output(_ context.Context, _ string, args ...string) ([]byte, error) {
	return []byte(f.out[strings.Join(args, " ")]), nil
}

func (f fakeRunner) Stream(_ context.Context, w io.Writer, _ string, args ...string) error {
	_, err := io.WriteString(w, "ran "+strings.Join(args, " ")+"\n")
	return err
}

// A fixed test value, not a secret.
var token = strings.Repeat("ab", 32)

func newTest(t *testing.T) *Server {
	t.Helper()
	c := colima.New("colima")
	c.Runner = fakeRunner{out: map[string]string{
		"list --json": `{"name":"default","status":"Stopped","arch":"aarch64","cpus":2,"memory":2147483648,"disk":107374182400,"runtime":"docker"}` + "\n",
		"version":     "colima version 0.10.3\ngit commit: abc\n",
	}}
	return New(Options{
		Port: 7766, Token: token, Version: "test", Colima: c, ColimaHome: t.TempDir(),
		Assets: fstest.MapFS{
			"index.html":  {Data: []byte("INDEX")},
			"locked.html": {Data: []byte("LOCKED")},
			"app.js":      {Data: []byte("JS")},
		},
	})
}

func do(s *Server, method, target string, mod func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r.Host = "127.0.0.1:7766"
	if mod != nil {
		mod(r)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func withCookie(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: token}) }

func TestHostCheckBlocksRebinding(t *testing.T) {
	s := newTest(t)
	for _, host := range []string{"evil.example:7766", "127.0.0.1:8000", "localhost", "0.0.0.0:7766", ""} {
		w := do(s, "GET", "/api/overview", func(r *http.Request) { r.Host = host; withCookie(r) })
		if w.Code != http.StatusMisdirectedRequest {
			t.Errorf("Host %q: got %d, want 421", host, w.Code)
		}
	}
	w := do(s, "GET", "/api/overview", func(r *http.Request) { r.Host = "localhost:7766"; withCookie(r) })
	if w.Code != http.StatusOK {
		t.Errorf("localhost:7766: got %d", w.Code)
	}
}

func TestAPINeedsCookie(t *testing.T) {
	s := newTest(t)
	if w := do(s, "GET", "/api/overview", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie: got %d", w.Code)
	}
	w := do(s, "GET", "/api/overview", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: strings.Repeat("0", len(token))})
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong cookie: got %d", w.Code)
	}
	w = do(s, "GET", "/api/overview", withCookie)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"colimaVersion":"0.10.3"`) {
		t.Fatalf("with cookie: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("API answers must not be cached")
	}
}

func TestTokenSwapsForCookie(t *testing.T) {
	s := newTest(t)
	w := do(s, "GET", "/?token="+token, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("got %d to %q", w.Code, w.Header().Get("Location"))
	}
	c := w.Result().Cookies()
	if len(c) != 1 || c[0].Value != token || !c[0].HttpOnly || c[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie not HttpOnly SameSite=Strict: %+v", c)
	}
	if w := do(s, "GET", "/?token=nope", nil); w.Code != http.StatusUnauthorized || w.Body.String() != "LOCKED" {
		t.Fatalf("bad token: %d %q", w.Code, w.Body.String())
	}
	if w := do(s, "GET", "/", nil); w.Body.String() != "LOCKED" {
		t.Fatalf("no cookie must show the locked page, got %q", w.Body.String())
	}
	if w := do(s, "GET", "/", withCookie); w.Code != http.StatusOK || w.Body.String() != "INDEX" {
		t.Fatalf("with cookie: %d %q", w.Code, w.Body.String())
	}
}

func TestStateChangesNeedOwnOrigin(t *testing.T) {
	s := newTest(t)
	path := "/api/profiles/default/actions/stop"
	for _, origin := range []string{"", "null", "http://evil.example", "http://127.0.0.1:8740", "https://127.0.0.1:7766"} {
		w := do(s, "POST", path, func(r *http.Request) {
			withCookie(r)
			if origin != "" {
				r.Header.Set("Origin", origin)
			}
		})
		if w.Code != http.StatusForbidden {
			t.Errorf("Origin %q: got %d, want 403", origin, w.Code)
		}
	}
	w := do(s, "POST", path, func(r *http.Request) { withCookie(r); r.Header.Set("Origin", "http://127.0.0.1:7766") })
	if w.Code != http.StatusAccepted {
		t.Fatalf("own origin: got %d %s", w.Code, w.Body.String())
	}
}

func TestCrossSiteFetchRefused(t *testing.T) {
	s := newTest(t)
	for _, site := range []string{"cross-site", "same-site"} {
		w := do(s, "GET", "/api/overview", func(r *http.Request) { withCookie(r); r.Header.Set("Sec-Fetch-Site", site) })
		if w.Code != http.StatusForbidden {
			t.Errorf("Sec-Fetch-Site %s: got %d", site, w.Code)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	w := do(newTest(t), "GET", "/", nil)
	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP must not allow inline or eval: %q", csp)
	}
	if w.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("missing X-Frame-Options")
	}
}

func TestProfileNameValidated(t *testing.T) {
	s := newTest(t)
	for _, p := range []string{"..%2f..%2fetc", "a%20b", "-rf"} {
		w := do(s, "POST", "/api/profiles/"+p+"/actions/start", func(r *http.Request) {
			withCookie(r)
			r.Header.Set("Origin", "http://127.0.0.1:7766")
		})
		if w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound {
			t.Errorf("profile %q: got %d", p, w.Code)
		}
	}
}

func TestStaticRefusesHTMLAndTraversal(t *testing.T) {
	s := newTest(t)
	if w := do(s, "GET", "/static/app.js", nil); w.Code != http.StatusOK {
		t.Errorf("app.js: %d", w.Code)
	}
	for _, p := range []string{"/static/index.html", "/static/../index.html", "/static/"} {
		if w := do(s, "GET", p, nil); w.Code == http.StatusOK && strings.Contains(w.Body.String(), "INDEX") {
			t.Errorf("%s served the page without a cookie", p)
		}
	}
}

func TestJobStreamsOutput(t *testing.T) {
	s := newTest(t)
	w := do(s, "POST", "/api/profiles/default/actions/stop", func(r *http.Request) {
		withCookie(r)
		r.Header.Set("Origin", "http://127.0.0.1:7766")
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("got %d", w.Code)
	}
	var id string
	for _, j := range s.jobs.List() {
		id = j.ID
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.jobs.Get(id).snapshot().Done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	ev := do(s, "GET", "/api/jobs/"+id+"/events", withCookie)
	body := ev.Body.String()
	if !strings.Contains(body, `"ran stop -p default"`) || !strings.Contains(body, "event: done") {
		t.Fatalf("stream: %s", body)
	}
}

func TestUnlockOpensBrowserOnlyForOwnOrigin(t *testing.T) {
	s := newTest(t)
	opened := 0
	s.opt.OpenBrowser = func() error { opened++; return nil }
	if w := do(s, "POST", "/unlock", func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }); w.Code != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", w.Code)
	}
	own := func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:7766") }
	w := do(s, "POST", "/unlock", own)
	if w.Code != http.StatusOK || opened != 1 {
		t.Fatalf("own origin: %d opened=%d", w.Code, opened)
	}
	if strings.Contains(w.Body.String(), token) || len(w.Result().Cookies()) > 0 {
		t.Fatal("unlock must never hand out the token")
	}
	if w := do(s, "POST", "/unlock", own); w.Code != http.StatusTooManyRequests || opened != 1 {
		t.Fatalf("second unlock within 5s: %d opened=%d", w.Code, opened)
	}
}

func TestSessionReportsSignIn(t *testing.T) {
	s := newTest(t)
	if w := do(s, "GET", "/session", nil); !strings.Contains(w.Body.String(), `"signedIn":false`) {
		t.Fatal(w.Body.String())
	}
	if w := do(s, "GET", "/session", withCookie); !strings.Contains(w.Body.String(), `"signedIn":true`) {
		t.Fatal(w.Body.String())
	}
}
