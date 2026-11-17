// Package server serves the Colima UI page and its JSON API on 127.0.0.1 only.
//
// Security model (docs/PLAN.md): the Docker socket is root in the VM and any web page the user
// visits can send requests to localhost, so every request must pass a Host check (DNS rebinding),
// every API call needs the session cookie obtained with the one-time token printed at start, and
// every state-changing request must come from the UI's own Origin.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrewbakercloudscale/colima-ui/internal/colima"
	"github.com/andrewbakercloudscale/colima-ui/internal/docker"
	"github.com/andrewbakercloudscale/colima-ui/internal/metrics"
)

// CookieName holds the session token in the browser.
const CookieName = "colima_ui_session"

// Options configure a Server.
type Options struct {
	Port       int
	Token      string
	Version    string
	Colima     *colima.Client
	ColimaHome string // ~/.colima unless COLIMA_HOME is set
	Assets     fs.FS  // the embedded web/ directory
	Metrics    *metrics.Hub
	// OpenBrowser opens the signed-in link in the browser of the user running colima-ui.
	OpenBrowser func() error
	Logger      *log.Logger
}

// Server is the HTTP handler.
type Server struct {
	opt        Options
	unlockMu   sync.Mutex
	lastUnlock time.Time
	jobs       *Jobs
	mux        *http.ServeMux
}

// New builds the handler.
func New(opt Options) *Server {
	if opt.Logger == nil {
		opt.Logger = log.New(os.Stderr, "colima-ui ", log.LstdFlags)
	}
	if opt.Metrics == nil {
		opt.Metrics = metrics.NewHub(opt.Colima.Bin, opt.ColimaHome)
	}
	s := &Server{opt: opt, jobs: NewJobs(), mux: http.NewServeMux()}
	s.routes()
	return s
}

func errorf(format string, a ...any) error { return fmt.Errorf(format, a...) }

func (s *Server) allowedHosts() []string {
	p := strconv.Itoa(s.opt.Port)
	return []string{"127.0.0.1:" + p, "localhost:" + p}
}

func (s *Server) allowedOrigin(origin string) bool {
	for _, h := range s.allowedHosts() {
		if origin == "http://"+h {
			return true
		}
	}
	return false
}

// ServeHTTP applies the checks every request must pass, then routes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
		"connect-src 'self'; font-src 'self'; manifest-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")

	if !s.hostOK(r.Host) {
		http.Error(w, "unexpected Host header", http.StatusMisdirectedRequest)
		return
	}
	// Browsers send Sec-Fetch-Site on every request; another site's page must never get through,
	// not even for a GET (logs and environment variables are secrets too).
	if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
		http.Error(w, "cross-site request refused", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if !s.allowedOrigin(r.Header.Get("Origin")) {
			http.Error(w, "missing or foreign Origin", http.StatusForbidden)
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) hostOK(host string) bool {
	for _, h := range s.allowedHosts() {
		if host == h {
			return true
		}
	}
	return false
}

func (s *Server) tokenOK(t string) bool {
	return t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(s.opt.Token)) == 1
}

func (s *Server) authed(r *http.Request) bool {
	c, err := r.Cookie(CookieName)
	return err == nil && s.tokenOK(c.Value)
}

// auth wraps API handlers: no session cookie, no access.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			writeError(w, http.StatusUnauthorized, errors.New("not signed in: open the link colima-ui printed when it started, or run `colima-ui open`"))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	}
}

// handleIndex swaps ?token= for the session cookie, then serves the page.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if t := r.URL.Query().Get("token"); t != "" {
		if !s.tokenOK(t) {
			s.serveAsset(w, r, "locked.html", http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: CookieName, Value: s.opt.Token, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteStrictMode,
		})
		// Drop the token from the address bar and history.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !s.authed(r) {
		s.serveAsset(w, r, "locked.html", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.serveAsset(w, r, "index.html", http.StatusOK)
}

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, name string, status int) {
	b, err := fs.ReadFile(s.opt.Assets, name)
	if err != nil {
		http.Error(w, "asset missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b)
	}
}

// Static assets carry no secrets; they are served without the cookie so the locked page can style itself.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	if name == "" || strings.Contains(name, "..") || strings.HasSuffix(name, ".html") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, s.opt.Assets, name)
}

// dockerFor returns the Docker client for a profile's socket.
func (s *Server) dockerFor(profile string) (*docker.Client, error) {
	if !colima.ValidProfile(profile) {
		return nil, fmt.Errorf("invalid profile name %q", profile)
	}
	return docker.New(filepath.Join(s.opt.ColimaHome, profile, "docker.sock")), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// statusFor maps engine errors to a sensible HTTP status for the page.
func statusFor(err error) int {
	var ae *docker.APIError
	if errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 {
		return ae.Status
	}
	if errors.Is(err, colima.ErrBusy) {
		return http.StatusConflict
	}
	return http.StatusBadGateway
}

func readJSON(r *http.Request, v any) error {
	if r.ContentLength == 0 {
		return nil
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("bad request body: %w", err)
	}
	return nil
}

// sse prepares a Server-Sent Events response.
type sse struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSE(w http.ResponseWriter) (*sse, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &sse{w: w, f: f}, true
}

func (e *sse) send(event string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	e.f.Flush()
	return nil
}

func (e *sse) ping() error {
	if _, err := fmt.Fprint(e.w, ": ping\n\n"); err != nil {
		return err
	}
	e.f.Flush()
	return nil
}

// Started reports the time the process started, for the About line.
var Started = time.Now()

// handleUnlock serves the locked page's "Open Colima UI" button. It never hands the token to the
// caller: it opens the signed-in link in the browser of the user who runs colima-ui, the same as
// `colima-ui open`. Another macOS user, or a page on another site (refused by the Origin and
// Sec-Fetch-Site checks), gets nothing. Rate limited so it cannot be used to flood tabs.
func (s *Server) handleUnlock(w http.ResponseWriter, r *http.Request) {
	if s.opt.OpenBrowser == nil {
		writeError(w, http.StatusNotImplemented, errors.New("not available"))
		return
	}
	s.unlockMu.Lock()
	if time.Since(s.lastUnlock) < 5*time.Second {
		s.unlockMu.Unlock()
		writeError(w, http.StatusTooManyRequests, errors.New("just opened it; check your browser"))
		return
	}
	s.lastUnlock = time.Now()
	s.unlockMu.Unlock()
	if err := s.opt.OpenBrowser(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleSession says whether this browser is signed in, so the locked page can reload itself.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"signedIn": s.authed(r)})
}
