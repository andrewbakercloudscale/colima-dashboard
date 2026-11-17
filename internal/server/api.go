package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
)

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /{$}", s.handleIndex)
	m.HandleFunc("GET /static/", s.handleStatic)
	m.HandleFunc("POST /unlock", s.handleUnlock)
	m.HandleFunc("GET /session", s.handleSession)

	m.HandleFunc("GET /api/overview", s.auth(s.apiOverview))
	m.HandleFunc("GET /api/jobs/{id}/events", s.auth(s.apiJobEvents))
	m.HandleFunc("POST /api/colima/prune", s.auth(s.apiColimaPrune))
	m.HandleFunc("GET /api/diagnostics", s.auth(s.apiDiagnostics))
	s.updateRoutes(m)
	s.terminalRoutes(m)

	m.HandleFunc("GET /api/profiles/{p}/status", s.auth(s.apiStatus))
	m.HandleFunc("POST /api/profiles/{p}/actions/{action}", s.auth(s.apiProfileAction))
	m.HandleFunc("GET /api/profiles/{p}/df", s.auth(s.apiDiskUsage))
	m.HandleFunc("GET /api/profiles/{p}/metrics", s.auth(s.apiMetrics))
	m.HandleFunc("GET /api/profiles/{p}/vmlogs", s.auth(s.apiVMLogs))
	m.HandleFunc("GET /api/profiles/{p}/kubernetes", s.auth(s.apiKubernetes))
	m.HandleFunc("GET /api/profiles/{p}/kubernetes/services", s.auth(s.apiK8sServices))

	m.HandleFunc("GET /api/profiles/{p}/containers", s.auth(s.apiContainers))
	m.HandleFunc("GET /api/profiles/{p}/containers/{id}", s.auth(s.apiInspect))
	m.HandleFunc("GET /api/profiles/{p}/containers/{id}/stats", s.auth(s.apiStats))
	m.HandleFunc("GET /api/profiles/{p}/containers/{id}/logs", s.auth(s.apiLogs))
	m.HandleFunc("POST /api/profiles/{p}/containers/{id}/{action}", s.auth(s.apiContainerAction))
	m.HandleFunc("DELETE /api/profiles/{p}/containers/{id}", s.auth(s.apiRemoveContainer))

	m.HandleFunc("GET /api/profiles/{p}/images", s.auth(s.apiImages))
	m.HandleFunc("POST /api/profiles/{p}/images/pull", s.auth(s.apiPull))
	m.HandleFunc("POST /api/profiles/{p}/images/prune", s.auth(s.apiPrune("images")))
	m.HandleFunc("POST /api/profiles/{p}/images/scan", s.auth(s.apiScan))
	m.HandleFunc("DELETE /api/profiles/{p}/images/{id}", s.auth(s.apiRemoveImage))

	m.HandleFunc("GET /api/profiles/{p}/volumes", s.auth(s.apiVolumes))
	m.HandleFunc("POST /api/profiles/{p}/volumes/prune", s.auth(s.apiPrune("volumes")))
	m.HandleFunc("DELETE /api/profiles/{p}/volumes/{name}", s.auth(s.apiRemoveVolume))
}

func (s *Server) apiOverview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	out := map[string]any{"version": s.opt.Version, "jobs": s.jobs.List()}
	if v, err := s.opt.Colima.Version(ctx); err == nil {
		out["colimaVersion"] = v
	} else {
		out["colimaError"] = err.Error()
	}
	profiles, err := s.opt.Colima.List(ctx)
	if err != nil {
		out["profilesError"] = err.Error()
	}
	if profiles == nil {
		profiles = []colima.Profile{}
	}
	out["profiles"] = profiles
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("p")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	st, err := s.opt.Colima.Status(ctx, p)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"running": false, "error": err.Error()})
		return
	}
	out := map[string]any{"running": true, "status": st}
	if dc, err := s.dockerFor(p); err == nil && st.Runtime == "docker" {
		if v, err := dc.Version(ctx); err == nil {
			out["engine"] = v
		} else {
			out["engineError"] = err.Error()
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiProfileAction(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("p")
	action := colima.Action(r.PathValue("action"))
	if !colima.ValidProfile(p) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid profile name %q", p))
		return
	}
	var opts *colima.StartOptions
	switch action {
	case colima.ActionStart, colima.ActionApply:
		opts = &colima.StartOptions{}
		if err := readJSON(r, opts); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if _, err := opts.Args(); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	case colima.ActionStop, colima.ActionRestart, colima.ActionDelete, colima.ActionK8sReset:
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown action %q", action))
		return
	}
	if err := s.opt.Colima.Claim(p, string(action)); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	title := fmt.Sprintf("colima %s (%s)", action, p)
	job := s.jobs.Start(title, p, func(j *Job) error {
		defer s.opt.Colima.Release(p)
		return s.opt.Colima.RunClaimed(context.Background(), j, action, p, opts)
	})
	writeJSON(w, http.StatusAccepted, job.snapshot())
}

func (s *Server) apiColimaPrune(w http.ResponseWriter, r *http.Request) {
	const key = "_prune"
	if err := s.opt.Colima.Claim(key, "prune"); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	job := s.jobs.Start("colima prune (cached downloads)", "", func(j *Job) error {
		defer s.opt.Colima.Release(key)
		return s.opt.Colima.RunClaimed(context.Background(), j, colima.ActionPrune, key, nil)
	})
	writeJSON(w, http.StatusAccepted, job.snapshot())
}

// apiJobEvents streams a job's output: "line" events, then one "done" event.
func (s *Server) apiJobEvents(w http.ResponseWriter, r *http.Request) {
	job := s.jobs.Get(r.PathValue("id"))
	if job == nil {
		writeError(w, http.StatusNotFound, errors.New("no such job"))
		return
	}
	from, _ := strconv.Atoi(r.URL.Query().Get("from"))
	ev, ok := newSSE(w)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		lines, next, done, errText, changed := job.Since(from)
		for _, l := range lines {
			if ev.send("line", l) != nil {
				return
			}
		}
		from = next
		if done {
			_ = ev.send("done", map[string]any{"ok": errText == "", "error": errText})
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-changed:
		case <-tick.C:
			if ev.ping() != nil {
				return
			}
		}
	}
}

func (s *Server) client(w http.ResponseWriter, r *http.Request) *docker.Client {
	dc, err := s.dockerFor(r.PathValue("p"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return nil
	}
	return dc
}

func (s *Server) apiDiskUsage(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	du, err := dc.DiskUsage(r.Context())
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, du.Summary)
}

func (s *Server) apiContainers(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	cs, err := dc.Containers(r.Context())
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	if cs == nil {
		cs = []docker.Container{}
	}
	writeJSON(w, http.StatusOK, cs)
}

func (s *Server) apiInspect(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	doc, err := dc.Inspect(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) apiStats(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	st, err := dc.Stats(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// apiLogs streams container logs as SSE "line" events: {s: stream, t: text}.
func (s *Server) apiLogs(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	id := r.PathValue("id")
	q := r.URL.Query()
	tail, err := strconv.Atoi(q.Get("tail"))
	if err != nil || tail <= 0 || tail > 10000 {
		tail = 500
	}
	doc, err := dc.Inspect(r.Context(), id)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	tty := false
	if cfg, ok := doc["Config"].(map[string]any); ok {
		tty, _ = cfg["Tty"].(bool)
	}
	ev, ok := newSSE(w)
	if !ok {
		return
	}
	var mu sync.Mutex
	err = dc.Logs(r.Context(), id, tail, q.Get("follow") == "1", q.Get("timestamps") == "1", tty,
		func(stream, line string) error {
			mu.Lock()
			defer mu.Unlock()
			return ev.send("line", map[string]string{"s": stream, "t": line})
		})
	if err != nil && r.Context().Err() == nil {
		_ = ev.send("failed", err.Error())
		return
	}
	_ = ev.send("done", true)
}

func (s *Server) apiContainerAction(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := dc.ContainerAction(ctx, r.PathValue("id"), r.PathValue("action")); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) apiRemoveContainer(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	if err := dc.RemoveContainer(r.Context(), r.PathValue("id"), r.URL.Query().Get("force") == "1"); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) apiImages(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	imgs, err := dc.Images(r.Context())
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	// /images/json reports Containers as -1; count users from the container list instead.
	if cs, err := dc.Containers(r.Context()); err == nil {
		use := map[string]int64{}
		for _, c := range cs {
			use[c.ImageID]++
		}
		for i := range imgs {
			imgs[i].Containers = use[imgs[i].ID]
		}
	}
	sort.Slice(imgs, func(i, j int) bool { return imgs[i].Created > imgs[j].Created })
	if imgs == nil {
		imgs = []docker.Image{}
	}
	writeJSON(w, http.StatusOK, imgs)
}

func (s *Server) apiPull(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	var body struct {
		Ref string `json:"ref"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ref := strings.TrimSpace(body.Ref)
	if !docker.ValidRef(ref) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid image reference %q", ref))
		return
	}
	job := s.jobs.Start("pull "+ref, r.PathValue("p"), func(j *Job) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		last := map[string]string{}
		err := dc.Pull(ctx, ref, func(ev docker.PullEvent) error {
			// Progress bars repeat many times a second; keep a line per layer status change.
			key := ev.ID
			if last[key] == ev.Status && ev.Progress != "" {
				return nil
			}
			last[key] = ev.Status
			if ev.ID != "" {
				j.Line(ev.ID + ": " + ev.Status)
			} else {
				j.Line(ev.Status)
			}
			return nil
		})
		if err == nil {
			j.Line("Pulled " + ref)
		}
		return err
	})
	writeJSON(w, http.StatusAccepted, job.snapshot())
}

func (s *Server) apiPrune(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dc := s.client(w, r)
		if dc == nil {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		rep, err := dc.Prune(ctx, kind)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, rep)
	}
}

// apiScan runs Trivy against an image in the profile's engine, when Trivy is installed.
func (s *Server) apiScan(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("p")
	if _, err := s.dockerFor(p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var body struct {
		Ref string `json:"ref"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !docker.ValidRef(body.Ref) || strings.HasPrefix(body.Ref, "-") {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid image reference %q", body.Ref))
		return
	}
	trivy, err := exec.LookPath("trivy")
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("trivy is not installed: brew install trivy"))
		return
	}
	sock := filepath.Join(s.opt.ColimaHome, p, "docker.sock")
	job := s.jobs.Start("scan "+body.Ref, p, func(j *Job) error {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, trivy, "image", "--scanners", "vuln", "--no-progress",
			"--severity", "UNKNOWN,LOW,MEDIUM,HIGH,CRITICAL", "--image-src", "docker", "--", body.Ref)
		cmd.Env = append(os.Environ(), "DOCKER_HOST=unix://"+sock)
		cmd.Stdout, cmd.Stderr = j, j
		j.Line("$ trivy image " + body.Ref)
		return cmd.Run()
	})
	writeJSON(w, http.StatusAccepted, job.snapshot())
}

func (s *Server) apiRemoveImage(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	if err := dc.RemoveImage(r.Context(), r.PathValue("id"), r.URL.Query().Get("force") == "1"); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) apiVolumes(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	// /system/df carries sizes and reference counts; /volumes does not.
	du, err := dc.DiskUsage(r.Context())
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	users := map[string][]string{}
	if cs, err := dc.Containers(r.Context()); err == nil {
		for _, c := range cs {
			for _, m := range c.Mounts {
				if m.Type == "volume" {
					users[m.Name] = append(users[m.Name], strings.TrimPrefix(firstName(c.Names), "/"))
				}
			}
		}
	}
	type vol struct {
		docker.Volume
		UsedBy []string `json:"usedBy"`
	}
	out := make([]vol, 0, len(du.Volumes))
	for _, v := range du.Volumes {
		u := users[v.Name]
		if u == nil {
			u = []string{}
		}
		out = append(out, vol{Volume: v, UsedBy: u})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

func firstName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func (s *Server) apiRemoveVolume(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	if err := dc.RemoveVolume(r.Context(), r.PathValue("name")); err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// apiVMLogs returns the tail of the Lima host agent and serial console logs for a profile.
func (s *Server) apiVMLogs(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("p")
	if !colima.ValidProfile(p) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid profile name %q", p))
		return
	}
	dir := filepath.Join(s.opt.ColimaHome, "_lima", colima.LimaInstance(p))
	out := map[string]string{}
	for _, name := range []string{"ha.stderr.log", "serialv.log", "serial.log"} {
		if text, err := tailFile(filepath.Join(dir, name), 256<<10); err == nil {
			out[name] = text
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func tailFile(path string, max int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	off := st.Size() - max
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	n, err := f.ReadAt(buf, off)
	if err != nil && n == 0 {
		return "", err
	}
	text := string(buf[:n])
	if off > 0 {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	return text, nil
}

func (s *Server) apiKubernetes(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("p")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	st, err := s.opt.Colima.Status(ctx, p)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"running": false, "error": err.Error()})
		return
	}
	out := map[string]any{"running": true, "enabled": st.Kubernetes, "context": colima.KubeContext(p)}
	if cur, err := kubeCurrentContext(ctx); err == nil {
		out["currentContext"] = cur
	} else {
		out["currentContextError"] = err.Error()
	}
	if st.Kubernetes {
		if nodes, err := s.opt.Colima.Kubectl(ctx, p, "get", "nodes", "-o", "wide"); err == nil {
			out["nodes"] = nodes
		} else {
			out["nodesError"] = err.Error()
		}
		if v, err := s.opt.Colima.Kubectl(ctx, p, "version"); err == nil {
			out["version"] = v
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// K8sService is a Kubernetes service with the ports it exposes, for the Port Forwarding page.
type K8sService struct {
	Namespace string           `json:"namespace"`
	Name      string           `json:"name"`
	Type      string           `json:"type"`
	Ports     []K8sServicePort `json:"ports"`
}

// K8sServicePort is one port of a service. On Colima, a LoadBalancer port is reachable on
// localhost at Port (k3s's service load balancer plus Lima's port forwarding); a NodePort
// on localhost at NodePort.
type K8sServicePort struct {
	Name       string `json:"name,omitempty"`
	Protocol   string `json:"protocol"`
	Port       int    `json:"port"`
	TargetPort any    `json:"targetPort,omitempty"`
	NodePort   int    `json:"nodePort,omitempty"`
}

func (s *Server) apiK8sServices(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("p")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := s.opt.Colima.Kubectl(ctx, p, "get", "services", "--all-namespaces", "-o", "json")
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("kubectl get services: %w", err))
		return
	}
	svcs, err := parseServices([]byte(out))
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, svcs)
}

func parseServices(b []byte) ([]K8sService, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Type  string           `json:"type"`
				Ports []K8sServicePort `json:"ports"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("reading kubectl output: %w", err)
	}
	out := make([]K8sService, 0, len(list.Items))
	for _, it := range list.Items {
		out = append(out, K8sService{Namespace: it.Metadata.Namespace, Name: it.Metadata.Name, Type: it.Spec.Type, Ports: it.Spec.Ports})
	}
	return out, nil
}

// apiMetrics returns samples newer than ?since= (unix ms). Asking keeps the sampler running.
func (s *Server) apiMetrics(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("p")
	if !colima.ValidProfile(p) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid profile name %q", p))
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	writeJSON(w, http.StatusOK, s.opt.Metrics.Get(p, since))
}
