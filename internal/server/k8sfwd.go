package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Kubernetes port forwards. `kubectl port-forward` runs inside the VM, listening on the VM's
// 127.0.0.1, and Colima's own port forwarding (Lima watches the VM's listening sockets) makes
// the same port appear on the Mac's localhost a second or two later. So nothing on the Mac needs
// kubectl, and nothing listens beyond localhost on either side.
//
// The remote side is `sudo sh -c '... & cat >/dev/null; kill $!'`: it ends kubectl when its
// standard input closes. Stopping a forward closes that pipe, and so does this process exiting,
// so a forward never outlives the dashboard inside the VM.

const fwdScript = `k3s kubectl port-forward --address 127.0.0.1 -n "$1" "$2" "$3" & p=$!; cat >/dev/null; kill $p 2>/dev/null; wait $p`

const maxForwards = 16

// Forward is one port forward as the page sees it.
type Forward struct {
	ID        string    `json:"id"`
	Profile   string    `json:"profile"`
	Namespace string    `json:"namespace"`
	Kind      string    `json:"kind"` // pod or service
	Name      string    `json:"name"`
	Remote    int       `json:"remote"`
	Local     int       `json:"local"`
	Started   time.Time `json:"started"`
	Running   bool      `json:"running"`
	Reachable bool      `json:"reachable"` // localhost:Local answers on the Mac
	Output    string    `json:"output,omitempty"`
}

type forward struct {
	Forward
	mu    sync.Mutex
	out   strings.Builder
	stdin io.WriteCloser
	cmd   *exec.Cmd
	done  chan struct{}
}

func (f *forward) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.out.Len() < 16<<10 {
		f.out.Write(p)
	}
	return len(p), nil
}

func (f *forward) view() Forward {
	f.mu.Lock()
	v := f.Forward
	v.Output = strings.TrimSpace(f.out.String())
	f.mu.Unlock()
	select {
	case <-f.done:
		v.Running = false
	default:
		v.Running = true
		v.Reachable = portAnswers(v.Local)
	}
	return v
}

func (f *forward) stop() {
	_ = f.stdin.Close()
	select {
	case <-f.done:
	case <-time.After(3 * time.Second):
		if f.cmd.Process != nil {
			_ = f.cmd.Process.Kill()
		}
	}
}

type forwards struct {
	mu sync.Mutex
	m  map[string]*forward
}

func (s *Server) fwdStore() *forwards {
	s.fwdOnce.Do(func() { s.fwds = &forwards{m: map[string]*forward{}} })
	return s.fwds
}

// portAnswers says whether something accepts connections on localhost:port.
func portAnswers(port int) bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// portFree says whether localhost:port can be listened on, so is not in use on the Mac.
func portFree(port int) bool {
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return !portAnswers(port)
}

func (s *Server) apiK8sForwards(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("p")
	st := s.fwdStore()
	st.mu.Lock()
	list := make([]*forward, 0, len(st.m))
	for _, f := range st.m {
		if f.Profile == p {
			list = append(list, f)
		}
	}
	st.mu.Unlock()
	out := make([]Forward, 0, len(list))
	for _, f := range list {
		out = append(out, f.view())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Local < out[j].Local })
	writeJSON(w, http.StatusOK, out)
}

// POST forwards {kind, namespace, name, remote, local}
func (s *Server) apiK8sForwardStart(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("p")
	var req struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		Remote    int    `json:"remote"`
		Local     int    `json:"local"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if (req.Kind != "pod" && req.Kind != "service") || !k8sNameRe.MatchString(req.Namespace) || !k8sNameRe.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, errors.New("invalid kind, namespace or name"))
		return
	}
	if req.Remote < 1 || req.Remote > 65535 {
		writeError(w, http.StatusBadRequest, errors.New("the pod or service port must be 1 to 65535"))
		return
	}
	if req.Local < 1024 || req.Local > 65535 {
		writeError(w, http.StatusBadRequest, errors.New("the port on your Mac must be 1024 to 65535"))
		return
	}
	if req.Local == s.opt.Port {
		writeError(w, http.StatusBadRequest, errors.New("that is Colima Dashboard's own port"))
		return
	}
	st := s.fwdStore()
	st.mu.Lock()
	n := 0
	for _, f := range st.m {
		if f.Local == req.Local && f.view().Running {
			st.mu.Unlock()
			writeError(w, http.StatusConflict, fmt.Errorf("localhost:%d is already forwarded to %s/%s", req.Local, f.Namespace, f.Name))
			return
		}
		n++
	}
	st.mu.Unlock()
	if n >= maxForwards {
		writeError(w, http.StatusTooManyRequests, fmt.Errorf("%d forwards are open; stop one first", n))
		return
	}
	if !portFree(req.Local) {
		writeError(w, http.StatusConflict, fmt.Errorf("localhost:%d is in use on your Mac; pick another port", req.Local))
		return
	}
	cmd, err := s.opt.Colima.SSHCommand(context.Background(), p, "sudo", "sh", "-c", fwdScript, "sh",
		req.Namespace, req.Kind+"/"+req.Name, fmt.Sprintf("%d:%d", req.Local, req.Remote))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	f := &forward{Forward: Forward{ID: hex.EncodeToString(b[:]), Profile: p, Namespace: req.Namespace, Kind: req.Kind, Name: req.Name,
		Remote: req.Remote, Local: req.Local, Started: time.Now()}, cmd: cmd, done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = f, f
	if f.stdin, err = cmd.StdinPipe(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := cmd.Start(); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	go func() { _ = cmd.Wait(); close(f.done) }()
	st.mu.Lock()
	st.m[f.ID] = f
	st.mu.Unlock()
	// Wait for kubectl to say it is listening, or to fail, then for Lima to bring it to the Mac.
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		v := f.view()
		if !v.Running {
			st.mu.Lock()
			delete(st.m, f.ID)
			st.mu.Unlock()
			msg := v.Output
			if msg == "" {
				msg = "kubectl port-forward stopped"
			}
			writeError(w, http.StatusBadGateway, errors.New(shorten(msg, 500)))
			return
		}
		if v.Reachable {
			writeJSON(w, http.StatusOK, v)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
	writeJSON(w, http.StatusOK, f.view()) // still coming up; the list shows when it answers
}

func (s *Server) apiK8sForwardStop(w http.ResponseWriter, r *http.Request) {
	st := s.fwdStore()
	st.mu.Lock()
	f := st.m[r.PathValue("id")]
	if f != nil && f.Profile == r.PathValue("p") {
		delete(st.m, f.ID)
	} else {
		f = nil
	}
	st.mu.Unlock()
	if f == nil {
		writeError(w, http.StatusNotFound, errors.New("no such forward"))
		return
	}
	f.stop()
	w.WriteHeader(http.StatusNoContent)
}
