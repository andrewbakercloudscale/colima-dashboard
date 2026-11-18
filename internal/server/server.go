// Package server serves the Colima Dashboard page and its JSON API on 127.0.0.1 only.
//
// Security model (docs/PLAN.md): the Docker socket is root in the VM and any web page the user
// visits can send requests to localhost, so every request must pass a Host check (DNS rebinding),
// every API call needs the session cookie obtained with the one-time token printed at start, and
// every state-changing request must come from the UI's own Origin.
package server

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
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

	"github.com/andrewbakercloudscale/colima-dashboard/internal/alerts"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/history"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/metrics"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/snapshot"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/update"
)

// CookieName holds the session token in the browser. Browsers share cookies across every port
// on 127.0.0.1, so the page sets cookieName(port) instead: another copy on another port cannot
// overwrite it and sign this one out. The bare name is still accepted (the menu bar app sends it).
const CookieName = "colima_dashboard_session"

func cookieName(port int) string { return CookieName + "_" + strconv.Itoa(port) }

// Options configure a Server.
type Options struct {
	Port       int
	Token      string
	Version    string
	Colima     *colima.Client
	ColimaHome string // ~/.colima unless COLIMA_HOME is set
	Assets     fs.FS  // the embedded web/ directory
	Metrics    *metrics.Hub
	// Updater checks for and installs new versions; nil turns the update routes off.
	Updater *update.Updater
	// OpenBrowser opens the signed-in link in the browser of the user running colima-dashboard.
	OpenBrowser func() error
	// History keeps 30 days of runs, builds and samples; nil turns the history routes off.
	History *history.Recorder
	// OpenTerminal opens Terminal running a fix's command; nil uses osascript. Tests replace it.
	OpenTerminal func(cmd string) error
	// Snapshots saves and restores stopped profiles; nil turns the snapshot routes off.
	Snapshots *snapshot.Store
	// Alerts keeps recent alerts and the notification and menu bar settings; nil turns them off.
	Alerts *alerts.Center
	// LoginItem is the "Start at login" switch; nil hides it.
	LoginItem interface {
		Enabled() bool
		SetEnabled(on bool) error
	}
	// MenuBarAvailable says whether this build includes the menu bar icon.
	// DataDir holds the dashboard's own files (settings, history); "" turns off what needs it.
	DataDir          string
	MenuBarAvailable bool
	Logger           *log.Logger
}

// Server is the HTTP handler.
type Server struct {
	opt        Options
	unlockMu   sync.Mutex
	lastUnlock time.Time
	latestMu   sync.Mutex
	latestTag  string    // newest colima release seen on GitHub
	latestAt   time.Time // when latestTag was fetched; refetched after 6 hours
	jobs       *Jobs
	mux        *http.ServeMux
	shellOnce  sync.Once
	shells     *shells
	fwdOnce    sync.Once
	fwds       *forwards
	details    detailCache // container inspect summaries for the list
	advice     adviceCache // Fix recommendations, 30 seconds
	errs       errCache    // the Errors page, 10 seconds
	prom       promState
}

// New builds the handler.
func New(opt Options) *Server {
	if opt.Logger == nil {
		opt.Logger = log.New(os.Stderr, "colima-dashboard ", log.LstdFlags)
	}
	if opt.Metrics == nil {
		opt.Metrics = metrics.NewHub(opt.Colima.Bin, opt.ColimaHome)
	}
	s := &Server{opt: opt, jobs: NewJobs(), mux: http.NewServeMux()}
	if opt.Updater != nil && opt.Updater.Busy == nil {
		opt.Updater.Busy = s.jobs.Running
	}
	s.routes()
	return s
}

func errorf(format string, a ...any) error { return fmt.Errorf(format, a...) }

func (s *Server) allowedHosts() []string {
	p := strconv.Itoa(s.opt.Port)
	return []string{"127.0.0.1:" + p, "localhost:" + p, LocalName + ":" + p}
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
	mine := cookieName(s.opt.Port)
	for _, c := range r.Cookies() {
		if (c.Name == mine || c.Name == CookieName) && s.tokenOK(c.Value) {
			return true
		}
	}
	return false
}

// auth wraps API handlers: no session cookie, no access.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			writeError(w, http.StatusUnauthorized, errors.New("not signed in: open the link colima-dashboard printed when it started, or run `colima-dashboard open`"))
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
			Name: cookieName(s.opt.Port), Value: s.opt.Token, Path: "/",
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
	// The terminal (xterm.js) builds its own <style> elements. A fresh nonce per page load lets
	// exactly those in, without opening style-src to inline styles in general.
	var nb [16]byte
	_, _ = rand.Read(nb[:])
	nonce := base64.StdEncoding.EncodeToString(nb[:])
	h := w.Header()
	h.Set("Content-Security-Policy", strings.Replace(h.Get("Content-Security-Policy"), "style-src 'self';", "style-src 'self' 'nonce-"+nonce+"';", 1))
	b, err := fs.ReadFile(s.opt.Assets, "index.html")
	if err != nil {
		http.Error(w, "asset missing", http.StatusInternalServerError)
		return
	}
	h.Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(bytes.Replace(b, []byte("{{STYLE_NONCE}}"), []byte(nonce), 1))
	}
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

// handleUnlock serves the locked page's "Open Colima Dashboard" button. It never hands the token to the
// caller: it opens the signed-in link in the browser of the user who runs colima-dashboard, the same as
// `colima-dashboard open`. Another macOS user, or a page on another site (refused by the Origin and
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
