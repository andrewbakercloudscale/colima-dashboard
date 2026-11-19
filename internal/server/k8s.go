package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
)

// k8sNameRe matches Kubernetes namespace and pod names (DNS subdomain rules).
var k8sNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)

func (s *Server) apiK8sPods(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := s.opt.Colima.Kubectl(ctx, r.PathValue("p"), "get", "pods", "--all-namespaces", "-o", "json")
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("kubectl get pods: %w", err))
		return
	}
	pods, err := parsePods([]byte(out))
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, pods)
}

func podPath(r *http.Request) (ns, pod string, err error) {
	ns, pod = r.PathValue("ns"), r.PathValue("pod")
	if !k8sNameRe.MatchString(ns) || !k8sNameRe.MatchString(pod) {
		return "", "", errors.New("invalid namespace or pod name")
	}
	return ns, pod, nil
}

// apiK8sPodLogs streams a pod's logs (all containers, each line prefixed with its container).
func (s *Server) apiK8sPodLogs(w http.ResponseWriter, r *http.Request) {
	ns, pod, err := podPath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	p := r.PathValue("p")
	if !colima.ValidProfile(p) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid profile name %q", p))
		return
	}
	tail := "500"
	if n, err := strconv.Atoi(r.URL.Query().Get("tail")); err == nil && n > 0 && n <= 10000 {
		tail = strconv.Itoa(n)
	}
	args := []string{"logs", "-n", ns, pod, "--all-containers", "--prefix", "--tail", tail}
	if r.URL.Query().Get("follow") == "1" {
		args = append(args, "-f")
	}
	previous := r.URL.Query().Get("previous") == "1"
	ev, ok := newSSE(w)
	if !ok {
		return
	}
	if previous {
		// k3s on the docker runtime removes a crashed container straight away, so kubectl
		// has no previous run to show. The current container is then the one that crashed.
		var buf bytes.Buffer
		err := s.opt.Colima.KubectlStream(r.Context(), p, &buf, append(args, "--previous")...)
		if r.Context().Err() != nil {
			return
		}
		if out := buf.String(); err == nil && !previousGone(out) {
			lw := &lineWriter{fn: func(line string) error { return ev.send("line", map[string]string{"s": "stdout", "t": line}) }}
			_, _ = lw.Write([]byte(out))
			lw.flush()
			_ = ev.send("done", true)
			return
		}
		_ = ev.send("note", "The previous run's log is gone (k3s removed that container), so this is the latest run.")
	}
	var mu sync.Mutex
	lw := &lineWriter{fn: func(line string) error {
		mu.Lock()
		defer mu.Unlock()
		return ev.send("line", map[string]string{"s": "stdout", "t": line})
	}}
	err = s.opt.Colima.KubectlStream(r.Context(), p, lw, args...)
	lw.flush()
	if err != nil && r.Context().Err() == nil {
		_ = ev.send("failed", err.Error())
		return
	}
	_ = ev.send("done", true)
}

// previousGone reports whether kubectl --previous only said the old container's log is gone.
func previousGone(out string) bool {
	out = strings.TrimSpace(out)
	if out == "" {
		return true
	}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		// With --prefix the line starts "[pod/name/container] ".
		if i := strings.Index(l, "] "); strings.HasPrefix(l, "[") && i > 0 {
			l = l[i+2:]
		}
		if !strings.HasPrefix(l, "unable to retrieve container logs") && !strings.Contains(l, "previous terminated container") {
			return false
		}
	}
	return true
}

// lineWriter turns a byte stream into lines.
type lineWriter struct {
	buf []byte
	fn  func(string) error
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := strings.IndexByte(string(l.buf), '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(l.buf[:i]), "\r")
		l.buf = l.buf[i+1:]
		if err := l.fn(line); err != nil {
			return 0, err
		}
	}
	if len(l.buf) > 64<<10 { // a very long line: pass it on in pieces
		_ = l.fn(string(l.buf))
		l.buf = l.buf[:0]
	}
	return len(p), nil
}

func (l *lineWriter) flush() {
	if len(l.buf) > 0 {
		_ = l.fn(string(l.buf))
		l.buf = nil
	}
}

var _ io.Writer = (*lineWriter)(nil)

func (s *Server) apiK8sDeletePod(w http.ResponseWriter, r *http.Request) {
	ns, pod, err := podPath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if _, err := s.opt.Colima.Kubectl(ctx, r.PathValue("p"), "delete", "pod", "-n", ns, pod, "--wait=false"); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// K3sVersion is a k3s release offered on the Kubernetes page.
type K3sVersion struct {
	Version string `json:"version"`
	Channel string `json:"channel"` // "stable" or the minor, like "v1.33"
}

var (
	k3sMu       sync.Mutex
	k3sVersions []K3sVersion
	k3sAt       time.Time
)

// K3sChannelsURL is k3s's release channel list; a variable so tests can serve their own.
var K3sChannelsURL = "https://update.k3s.io/v1-release/channels"

// apiK3sVersions lists the stable release and the newest patch of the last six minors, from
// k3s's release channels, fetched at most every 6 hours.
func (s *Server) apiK3sVersions(w http.ResponseWriter, r *http.Request) {
	k3sMu.Lock()
	defer k3sMu.Unlock()
	if k3sVersions != nil && time.Since(k3sAt) < 6*time.Hour {
		writeJSON(w, http.StatusOK, k3sVersions)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, K3sChannelsURL, nil)
	req.Header.Set("User-Agent", "colima-dashboard")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, errors.New("could not reach update.k3s.io"))
		return
	}
	defer resp.Body.Close()
	var ch struct {
		Data []struct {
			ID     string `json:"id"`
			Latest string `json:"latest"`
		} `json:"data"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ch) != nil {
		writeError(w, http.StatusBadGateway, errors.New("update.k3s.io did not answer"))
		return
	}
	writeJSON(w, http.StatusOK, pickK3sVersions(ch.Data))
}

func pickK3sVersions(data []struct {
	ID     string `json:"id"`
	Latest string `json:"latest"`
}) []K3sVersion {
	minorRe := regexp.MustCompile(`^v1\.([0-9]+)$`)
	type minor struct {
		n  int
		v  string
		id string
	}
	var minors []minor
	stable := ""
	for _, d := range data {
		if !colima.ValidK3sVersion(d.Latest) {
			continue
		}
		if d.ID == "stable" {
			stable = d.Latest
		}
		if m := minorRe.FindStringSubmatch(d.ID); m != nil {
			n, _ := strconv.Atoi(m[1])
			minors = append(minors, minor{n, d.Latest, d.ID})
		}
	}
	sort.Slice(minors, func(i, j int) bool { return minors[i].n > minors[j].n })
	out := []K3sVersion{}
	if stable != "" {
		out = append(out, K3sVersion{Version: stable, Channel: "stable"})
	}
	for i, m := range minors {
		if i >= 6 {
			break
		}
		if m.v != stable {
			out = append(out, K3sVersion{Version: m.v, Channel: m.id})
		}
	}
	k3sVersions, k3sAt = out, time.Now()
	return out
}
