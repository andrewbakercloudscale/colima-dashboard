package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
)

// A shell is a terminal into a container: docker exec with a TTY. The page reads its output over
// Server-Sent Events and sends keystrokes with POSTs, so it needs nothing beyond the standard
// library. Closing the page's stream ends the shell.

const maxShells = 8

// shellRC sets up the shell like a login shell, then gives it colour: a green user@container
// and blue folder in the prompt, and coloured ls and grep when the image's tools support it.
// It is fed through a here-document, so nothing is written into the container.
const shellRC = `[ -r /etc/profile ] && . /etc/profile >/dev/null 2>&1
[ -n "$BASH_VERSION" ] && [ -r "$HOME/.bashrc" ] && . "$HOME/.bashrc" >/dev/null 2>&1
PS1='\[\033[1;32m\]\u@'"${CD_NAME:-\\h}"'\[\033[0m\]:\[\033[1;34m\]\w\[\033[0m\]\$ '
ls --color=auto -d / >/dev/null 2>&1 && alias ls='ls --color=auto'
echo x | grep --color=auto x >/dev/null 2>&1 && alias grep='grep --color=auto'
export LS_COLORS="${LS_COLORS:-di=1;34:ln=1;36:ex=1;32:*.tar=1;31:*.gz=1;31}"
`

// shellCmd starts bash when the image has it, else sh, with shellRC as its start-up file
// ($ENV for sh). $1 is the container's name for the prompt; $2 "sh" asks for sh even when
// bash is there.
var shellCmd = []string{"/bin/sh", "-c", `export CD_NAME="$1"
if [ "$2" != sh ] && command -v bash >/dev/null 2>&1; then exec bash --rcfile /dev/fd/3 -i 3<<'CDRC'
` + shellRC + `CDRC
fi
ENV=/dev/fd/3 exec sh -i 3<<'CDRC'
` + shellRC + `CDRC
`, "sh"}

// shellName is a container name safe to put in the prompt; anything else shows the hostname.
var shellName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

type shell struct {
	id, execID string
	dc         *docker.Client
	rw         io.ReadWriteCloser
	out        chan []byte
	done       chan struct{}
	once       sync.Once
	cleanup    func() // runs once the shell has closed, e.g. to remove a helper container
	streaming  sync.Mutex
	attached   atomic.Bool
}

func (sh *shell) close() {
	sh.once.Do(func() {
		close(sh.done)
		sh.rw.Close()
		if sh.cleanup != nil {
			go sh.cleanup()
		}
	})
}

// shellOpts are the choices the Shells page offers.
type shellOpts struct {
	Shell   string // "", "bash" or "sh"
	User    string // "" for the image's own user
	Prefix  []string
	Cleanup func()
}

// shellUser is a user name or uid[:gid] Docker accepts for exec.
var shellUser = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$|^[0-9]{1,10}(:[0-9]{1,10})?$`)

type shells struct {
	mu sync.Mutex
	m  map[string]*shell
}

func (s *Server) shellStore() *shells {
	s.shellOnce.Do(func() { s.shells = &shells{m: map[string]*shell{}} })
	return s.shells
}

func (ss *shells) get(id string) *shell {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.m[id]
}

func (ss *shells) remove(id string) {
	ss.mu.Lock()
	sh := ss.m[id]
	delete(ss.m, id)
	ss.mu.Unlock()
	if sh != nil {
		sh.close()
	}
}

// POST /api/profiles/{p}/containers/{id}/shell {cols, rows} -> {id}
func (s *Server) apiShellOpen(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	var req struct {
		Cols, Rows  int
		Name        string
		Shell, User string
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Shell != "" && req.Shell != "bash" && req.Shell != "sh" {
		writeError(w, http.StatusBadRequest, errors.New(`shell must be "bash" or "sh"`))
		return
	}
	if req.User != "" && !shellUser.MatchString(req.User) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%q is not a user name or uid", req.User))
		return
	}
	s.openShell(w, r, dc, r.PathValue("id"), req.Name, req.Cols, req.Rows, shellOpts{Shell: req.Shell, User: req.User})
}

// openShell starts a shell in a container and answers {id}. The Kubernetes page uses it too: on
// Colima's docker runtime a pod's containers are Docker containers.
func (s *Server) openShell(w http.ResponseWriter, r *http.Request, dc *docker.Client, containerID, prompt string, cols, rows int, o shellOpts) {
	fail := func(status int, err error) {
		if o.Cleanup != nil {
			go o.Cleanup()
		}
		writeError(w, status, err)
	}
	store := s.shellStore()
	store.mu.Lock()
	n := len(store.m)
	store.mu.Unlock()
	if n >= maxShells {
		fail(http.StatusTooManyRequests, fmt.Errorf("%d shells are open already; close one first", n))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	name := strings.TrimPrefix(prompt, "/")
	if !shellName.MatchString(name) {
		name = ""
	}
	cmd := append(append(append([]string{}, o.Prefix...), shellCmd...), name, o.Shell)
	execID, err := dc.ExecCreateAs(ctx, containerID, cmd, true, o.User)
	if err != nil {
		fail(statusFor(err), err)
		return
	}
	// The stream outlives this request: it ends when the page closes it or the shell exits.
	rw, err := dc.ExecAttach(context.Background(), execID, true)
	if err != nil {
		fail(statusFor(err), err)
		return
	}
	if cols > 0 && rows > 0 {
		_ = dc.ExecResize(ctx, execID, clamp(cols, 10, 500), clamp(rows, 5, 300))
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	sh := &shell{id: hex.EncodeToString(b[:]), execID: execID, dc: dc, rw: rw, out: make(chan []byte, 64), done: make(chan struct{}), cleanup: o.Cleanup}
	go func() {
		defer close(sh.out)
		buf := make([]byte, 32<<10)
		for {
			n, err := rw.Read(buf)
			if n > 0 {
				select {
				case sh.out <- append([]byte(nil), buf[:n]...):
				case <-sh.done:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	store.mu.Lock()
	store.m[sh.id] = sh
	store.mu.Unlock()
	// A shell nobody attaches to within a minute is closed.
	time.AfterFunc(time.Minute, func() {
		if !sh.attached.Load() {
			store.remove(sh.id)
		}
	})
	writeJSON(w, http.StatusOK, map[string]string{"id": sh.id})
}

// GET /api/shells/{sid}/stream: output as base64 chunks ("out"), then "exit" with the code.
func (s *Server) apiShellStream(w http.ResponseWriter, r *http.Request) {
	store := s.shellStore()
	sh := store.get(r.PathValue("sid"))
	if sh == nil {
		writeError(w, http.StatusNotFound, errors.New("no such shell"))
		return
	}
	if !sh.streaming.TryLock() {
		writeError(w, http.StatusConflict, errors.New("this shell is open in another tab"))
		return
	}
	defer sh.streaming.Unlock()
	sh.attached.Store(true)
	defer store.remove(sh.id) // closing the page ends the shell
	ev, ok := newSSE(w)
	if !ok {
		return
	}
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if ev.ping() != nil {
				return
			}
		case b, open := <-sh.out:
			if !open {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, code, _ := sh.dc.ExecExitCode(ctx, sh.execID)
				cancel()
				_ = ev.send("exit", code)
				return
			}
			if ev.send("out", base64.StdEncoding.EncodeToString(b)) != nil {
				return
			}
		}
	}
}

// POST /api/shells/{sid}/input {data}
func (s *Server) apiShellInput(w http.ResponseWriter, r *http.Request) {
	sh := s.shellStore().get(r.PathValue("sid"))
	if sh == nil {
		writeError(w, http.StatusNotFound, errors.New("no such shell"))
		return
	}
	var req struct{ Data string }
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if _, err := io.WriteString(sh.rw, req.Data); err != nil {
		writeError(w, http.StatusGone, errors.New("the shell has ended"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/shells/{sid}/resize {cols, rows}
func (s *Server) apiShellResize(w http.ResponseWriter, r *http.Request) {
	sh := s.shellStore().get(r.PathValue("sid"))
	if sh == nil {
		writeError(w, http.StatusNotFound, errors.New("no such shell"))
		return
	}
	var req struct {
		Cols, Rows int
		Name       string
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := sh.dc.ExecResize(ctx, sh.execID, clamp(req.Cols, 10, 500), clamp(req.Rows, 5, 300)); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/shells/{sid}
func (s *Server) apiShellClose(w http.ResponseWriter, r *http.Request) {
	s.shellStore().remove(r.PathValue("sid"))
	w.WriteHeader(http.StatusNoContent)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// cleanPath accepts an absolute container path and returns it cleaned.
func cleanPath(p string) (string, error) {
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) {
		return "", errors.New("path must be absolute")
	}
	return path.Clean(p), nil
}

// GET /api/profiles/{p}/containers/{id}/files?path=/ -> {path, entries, truncated}
func (s *Server) apiFiles(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	dir, err := cleanPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	entries, truncated, err := dc.ListDir(ctx, r.PathValue("id"), dir)
	if err != nil {
		status := statusFor(err)
		if errors.Is(err, docker.ErrNoShell) {
			status = http.StatusUnprocessableEntity
		} else if status == http.StatusBadGateway {
			status = http.StatusBadRequest
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": dir, "entries": entries, "truncated": truncated})
}

// GET /api/profiles/{p}/containers/{id}/file?path=/etc/hosts -> {text, size, truncated, binary}
func (s *Server) apiFileView(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	p, err := cleanPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	const limit = 512 << 10
	data, size, err := dc.ReadFile(r.Context(), r.PathValue("id"), p, limit)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	bin := !utf8.Valid(data) || strings.ContainsRune(string(data), 0)
	text := ""
	if !bin {
		text = string(data)
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "text": text, "size": size, "truncated": size > int64(len(data)), "binary": bin})
}

// GET /api/profiles/{p}/containers/{id}/download?path=/app -> the file, or a .tar of a folder.
func (s *Server) apiFileDownload(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	p, err := cleanPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	name := path.Base(p)
	if name == "/" {
		name = "root"
	}
	// A file comes back as itself (links followed); a folder as the tar Docker made of it.
	if r.URL.Query().Get("kind") == "file" {
		frc, tr, h, err := dc.OpenFile(r.Context(), r.PathValue("id"), p)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		defer frc.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(h.Size, 10))
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		_, _ = io.Copy(w, tr)
		return
	}
	rc, err := dc.Archive(r.Context(), r.PathValue("id"), p)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".tar"}))
	_, _ = io.Copy(w, rc)
}
