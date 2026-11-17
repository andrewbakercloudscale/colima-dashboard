package server

import (
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

// Pod is one row of the Kubernetes page's workload list.
type Pod struct {
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Phase     string   `json:"phase"`
	Reason    string   `json:"reason,omitempty"` // e.g. CrashLoopBackOff, ImagePullBackOff
	Ready     string   `json:"ready"`            // "1/2"
	Restarts  int      `json:"restarts"`
	Created   int64    `json:"created"` // unix seconds
	Owner     string   `json:"owner,omitempty"`
	Images    []string `json:"images"`
}

func parsePods(b []byte) ([]Pod, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string     `json:"name"`
				Namespace         string     `json:"namespace"`
				CreationTimestamp time.Time  `json:"creationTimestamp"`
				DeletionTimestamp *time.Time `json:"deletionTimestamp"`
				OwnerReferences   []struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"ownerReferences"`
			} `json:"metadata"`
			Spec struct {
				Containers []struct {
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
			Status struct {
				Phase             string `json:"phase"`
				Reason            string `json:"reason"`
				ContainerStatuses []struct {
					Ready        bool `json:"ready"`
					RestartCount int  `json:"restartCount"`
					State        struct {
						Waiting *struct {
							Reason string `json:"reason"`
						} `json:"waiting"`
						Terminated *struct {
							Reason string `json:"reason"`
						} `json:"terminated"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("reading kubectl output: %w", err)
	}
	out := make([]Pod, 0, len(list.Items))
	for _, it := range list.Items {
		p := Pod{Namespace: it.Metadata.Namespace, Name: it.Metadata.Name, Phase: it.Status.Phase,
			Reason: it.Status.Reason, Created: it.Metadata.CreationTimestamp.Unix(), Images: []string{}}
		if it.Metadata.DeletionTimestamp != nil {
			p.Reason = "Terminating"
		}
		for _, o := range it.Metadata.OwnerReferences {
			p.Owner = o.Kind + "/" + o.Name
		}
		for _, c := range it.Spec.Containers {
			p.Images = append(p.Images, c.Image)
		}
		ready := 0
		for _, cs := range it.Status.ContainerStatuses {
			if cs.Ready {
				ready++
			}
			p.Restarts += cs.RestartCount
			if w := cs.State.Waiting; w != nil && w.Reason != "" && p.Reason == "" {
				p.Reason = w.Reason
			}
			if t := cs.State.Terminated; t != nil && t.Reason != "" && t.Reason != "Completed" && p.Reason == "" {
				p.Reason = t.Reason
			}
		}
		p.Ready = fmt.Sprintf("%d/%d", ready, len(it.Spec.Containers))
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

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
	if r.URL.Query().Get("previous") == "1" {
		args = append(args, "--previous")
	}
	ev, ok := newSSE(w)
	if !ok {
		return
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
