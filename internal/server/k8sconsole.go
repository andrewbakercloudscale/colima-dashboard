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
)

// The Kubernetes console: health, resources, issues and events (read only), then workloads,
// resources, YAML, shells and port forwards. Everything runs kubectl inside the VM with -o json
// and is parsed here. Every name that reaches kubectl is checked first: namespaces and object
// names against k8sNameRe, kinds against a fixed list, quantities against their own patterns.

func (s *Server) k8sRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/profiles/{p}/kubernetes/overview", s.auth(s.apiK8sOverview))
	m.HandleFunc("GET /api/profiles/{p}/kubernetes/events", s.auth(s.apiK8sEvents))
	m.HandleFunc("GET /api/profiles/{p}/kubernetes/workloads", s.auth(s.apiK8sWorkloads))
	m.HandleFunc("GET /api/profiles/{p}/kubernetes/network", s.auth(s.apiK8sNetwork))
	m.HandleFunc("POST /api/profiles/{p}/kubernetes/workloads/{kind}/{ns}/{name}/{action}", s.auth(s.apiK8sWorkloadAction))
	m.HandleFunc("GET /api/profiles/{p}/kubernetes/yaml", s.auth(s.apiK8sYAML))
	m.HandleFunc("POST /api/profiles/{p}/kubernetes/apply", s.auth(s.apiK8sApply))
	m.HandleFunc("POST /api/profiles/{p}/kubernetes/pods/{ns}/{pod}/shell", s.auth(s.apiK8sPodShell))
	m.HandleFunc("GET /api/profiles/{p}/kubernetes/forwards", s.auth(s.apiK8sForwards))
	m.HandleFunc("POST /api/profiles/{p}/kubernetes/forwards", s.auth(s.apiK8sForwardStart))
	m.HandleFunc("DELETE /api/profiles/{p}/kubernetes/forwards/{id}", s.auth(s.apiK8sForwardStop))
}

// kube runs kubectl in the VM and returns kubectl's own error message, without the command line.
func (s *Server) kube(ctx context.Context, p string, args ...string) (string, error) {
	out, err := s.opt.Colima.Kubectl(ctx, p, args...)
	if err != nil {
		full := append([]string{s.opt.Colima.Bin, "ssh", "-p", p, "--", "sudo", "k3s", "kubectl"}, args...)
		return out, errors.New(cleanKubeMsg(strings.TrimPrefix(err.Error(), strings.Join(full, " ")+": ")))
	}
	return out, nil
}

// kubeIn is kube with stdin fed to kubectl (apply -f -, patch --patch-file /dev/stdin).
func (s *Server) kubeIn(ctx context.Context, p string, stdin io.Reader, args ...string) (string, error) {
	out, err := s.opt.Colima.KubectlInput(ctx, p, stdin, args...)
	if err != nil {
		return out, errors.New(cleanKubeMsg(err.Error()))
	}
	return out, nil
}

// cleanKubeMsg keeps what kubectl said: it drops colima's own log lines and the shell's notice
// when the Mac's folder does not exist in the VM, and shortens the server's echo of a whole
// object to the part that names the problem.
func cleanKubeMsg(msg string) string {
	var keep []string
	for _, l := range strings.Split(msg, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, `time="`) || (strings.HasPrefix(t, "bash: ") && strings.Contains(t, ": cd: ")) {
			continue
		}
		if i := strings.Index(t, "strict decoding error: "); i >= 0 {
			t = "The YAML has a field Kubernetes does not know: " + t[i+len("strict decoding error: "):]
		} else if len(t) > 600 {
			t = t[:250] + " … " + t[len(t)-300:]
		}
		keep = append(keep, t)
	}
	if len(keep) == 0 {
		return strings.TrimSpace(msg)
	}
	return strings.Join(keep, "\n")
}

// K8sOverview is everything the console's first view shows, gathered in one round.
type K8sOverview struct {
	Pods         []Pod      `json:"pods"`
	Nodes        []Node     `json:"nodes"`
	Issues       []K8sIssue `json:"issues"`
	Warnings     []K8sEvent `json:"warnings"`
	Metrics      bool       `json:"metrics"`
	MetricsError string     `json:"metricsError,omitempty"`
	NodesError   string     `json:"nodesError,omitempty"`
}

// apiK8sOverview: pods with requests, limits and live use; nodes with what is booked on them;
// the issues found; and recent Warning events.
func (s *Server) apiK8sOverview(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("p")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var podsOut, nodesOut, eventsOut, topPods, topNodes string
	var podsErr, nodesErr, eventsErr, topErr error
	var wg sync.WaitGroup
	run := func(out *string, errp *error, args ...string) {
		wg.Add(1)
		go func() { defer wg.Done(); *out, *errp = s.kube(ctx, p, args...) }()
	}
	run(&podsOut, &podsErr, "get", "pods", "--all-namespaces", "-o", "json")
	run(&nodesOut, &nodesErr, "get", "nodes", "-o", "json")
	run(&eventsOut, &eventsErr, "get", "events", "--all-namespaces", "--field-selector", "type=Warning", "-o", "json")
	run(&topPods, &topErr, "top", "pods", "--all-namespaces", "--containers", "--no-headers")
	var topNodesErr error
	run(&topNodes, &topNodesErr, "top", "nodes", "--no-headers")
	wg.Wait()
	if podsErr != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("kubectl get pods: %w", podsErr))
		return
	}
	pods, err := parsePods([]byte(podsOut))
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	ov := K8sOverview{Pods: pods, Nodes: []Node{}, Warnings: []K8sEvent{}}
	if topErr == nil {
		ov.Metrics = true
		addUsage(pods, parseTopPods(topPods))
	} else {
		ov.MetricsError = metricsHint(topErr)
	}
	if nodesErr == nil {
		if nodes, err := parseNodes([]byte(nodesOut)); err == nil {
			top := map[string]usage{}
			if topNodesErr == nil {
				top = parseTopNodes(topNodes)
			}
			sumNodes(nodes, pods, top)
			ov.Nodes = nodes
		} else {
			ov.NodesError = err.Error()
		}
	} else {
		ov.NodesError = nodesErr.Error()
	}
	if eventsErr == nil {
		if evs, err := parseEvents([]byte(eventsOut)); err == nil {
			if len(evs) > 100 {
				evs = evs[:100]
			}
			ov.Warnings = evs
		}
	}
	ov.Issues = checkIssues(pods, ov.Nodes, ov.Metrics)
	writeJSON(w, http.StatusOK, ov)
}

// metricsHint turns kubectl top's failure into a sentence for the page.
func metricsHint(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Metrics API not available"), strings.Contains(msg, "the server could not find the requested resource"):
		return "metrics-server is not running, so live CPU and memory use is not shown. k3s installs it unless it was turned off with --disable=metrics-server."
	case strings.Contains(msg, "metrics not available yet"):
		return "metrics-server has no numbers yet; it takes about a minute after Kubernetes starts."
	}
	return "Live use is not available: " + shorten(msg, 200)
}

// GET events?ns=&name=&kind= : events for one object, or a namespace's, newest first.
func (s *Server) apiK8sEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns, name, kind := q.Get("ns"), q.Get("name"), q.Get("kind")
	args := []string{"get", "events", "-o", "json"}
	if ns == "" {
		args = append(args, "--all-namespaces")
	} else if !k8sNameRe.MatchString(ns) {
		writeError(w, http.StatusBadRequest, errors.New("invalid namespace"))
		return
	} else {
		args = append(args, "-n", ns)
	}
	var sel []string
	if name != "" {
		if !k8sNameRe.MatchString(name) {
			writeError(w, http.StatusBadRequest, errors.New("invalid name"))
			return
		}
		sel = append(sel, "involvedObject.name="+name)
	}
	if kind != "" {
		if !kindRe.MatchString(kind) {
			writeError(w, http.StatusBadRequest, errors.New("invalid kind"))
			return
		}
		sel = append(sel, "involvedObject.kind="+kind)
	}
	if q.Get("type") == "Warning" {
		sel = append(sel, "type=Warning")
	}
	if len(sel) > 0 {
		args = append(args, "--field-selector", strings.Join(sel, ","))
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := s.kube(ctx, r.PathValue("p"), args...)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	evs, err := parseEvents([]byte(out))
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, evs)
}

// kindRe is an object kind as Kubernetes spells it (Pod, ReplicaSet).
var kindRe = regexp.MustCompile(`^[A-Z][A-Za-z]{1,40}$`)

// ---------- workloads ----------

// WorkloadContainer is one container of a workload's pod template, with resources as written.
type WorkloadContainer struct {
	Name     string            `json:"name"`
	Image    string            `json:"image"`
	Requests map[string]string `json:"requests"`
	Limits   map[string]string `json:"limits"`
}

// Workload is a Deployment, StatefulSet or DaemonSet.
type Workload struct {
	Kind       string              `json:"kind"`
	Namespace  string              `json:"namespace"`
	Name       string              `json:"name"`
	Desired    int                 `json:"desired"`
	Ready      int                 `json:"ready"`
	Updated    int                 `json:"updated"`
	Available  int                 `json:"available"`
	Images     []string            `json:"images"`
	Created    int64               `json:"created"`
	Revision   string              `json:"revision,omitempty"`
	Paused     bool                `json:"paused,omitempty"`
	Message    string              `json:"message,omitempty"` // a rollout problem
	Containers []WorkloadContainer `json:"containers"`
}

func parseWorkloads(b []byte) ([]Workload, error) {
	var list struct {
		Items []struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name              string            `json:"name"`
				Namespace         string            `json:"namespace"`
				CreationTimestamp time.Time         `json:"creationTimestamp"`
				Annotations       map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Replicas *int `json:"replicas"`
				Paused   bool `json:"paused"`
				Template struct {
					Spec struct {
						Containers []struct {
							Name      string       `json:"name"`
							Image     string       `json:"image"`
							Resources k8sResources `json:"resources"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
			Status struct {
				ReadyReplicas          int `json:"readyReplicas"`
				UpdatedReplicas        int `json:"updatedReplicas"`
				AvailableReplicas      int `json:"availableReplicas"`
				DesiredNumberScheduled int `json:"desiredNumberScheduled"`
				NumberReady            int `json:"numberReady"`
				UpdatedNumberScheduled int `json:"updatedNumberScheduled"`
				NumberAvailable        int `json:"numberAvailable"`
				Conditions             []struct {
					Type    string `json:"type"`
					Status  string `json:"status"`
					Reason  string `json:"reason"`
					Message string `json:"message"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("reading kubectl output: %w", err)
	}
	out := make([]Workload, 0, len(list.Items))
	for _, it := range list.Items {
		wl := Workload{Kind: it.Kind, Namespace: it.Metadata.Namespace, Name: it.Metadata.Name,
			Created: it.Metadata.CreationTimestamp.Unix(), Paused: it.Spec.Paused, Images: []string{}, Containers: []WorkloadContainer{},
			Revision: it.Metadata.Annotations["deployment.kubernetes.io/revision"]}
		st := it.Status
		if it.Kind == "DaemonSet" {
			wl.Desired, wl.Ready, wl.Updated, wl.Available = st.DesiredNumberScheduled, st.NumberReady, st.UpdatedNumberScheduled, st.NumberAvailable
		} else {
			wl.Desired = 1
			if it.Spec.Replicas != nil {
				wl.Desired = *it.Spec.Replicas
			}
			wl.Ready, wl.Updated, wl.Available = st.ReadyReplicas, st.UpdatedReplicas, st.AvailableReplicas
		}
		for _, c := range st.Conditions {
			if (c.Type == "Progressing" && c.Status == "False") || (c.Type == "ReplicaFailure" && c.Status == "True") {
				wl.Message = c.Message
			}
		}
		for _, c := range it.Spec.Template.Spec.Containers {
			wl.Images = append(wl.Images, c.Image)
			wc := WorkloadContainer{Name: c.Name, Image: c.Image, Requests: c.Resources.Requests, Limits: c.Resources.Limits}
			if wc.Requests == nil {
				wc.Requests = map[string]string{}
			}
			if wc.Limits == nil {
				wc.Limits = map[string]string{}
			}
			wl.Containers = append(wl.Containers, wc)
		}
		out = append(out, wl)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *Server) apiK8sWorkloads(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := s.kube(ctx, r.PathValue("p"), "get", "deployments,statefulsets,daemonsets", "--all-namespaces", "-o", "json")
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	wls, err := parseWorkloads([]byte(out))
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, wls)
}

// ---------- services and ingresses ----------

// NetService is a Service on the console's Services view.
type NetService struct {
	K8sService
	ClusterIP string            `json:"clusterIP"`
	External  []string          `json:"external"`
	Selector  map[string]string `json:"selector"`
	Created   int64             `json:"created"`
}

// IngressRule is one host and path an Ingress routes.
type IngressRule struct {
	Host    string `json:"host"`
	Path    string `json:"path"`
	Service string `json:"service"`
	Port    string `json:"port"`
}

// NetIngress is an Ingress.
type NetIngress struct {
	Namespace string        `json:"namespace"`
	Name      string        `json:"name"`
	Class     string        `json:"class"`
	Rules     []IngressRule `json:"rules"`
	TLS       bool          `json:"tls"`
	Address   []string      `json:"address"`
	Created   int64         `json:"created"`
}

func parseNetwork(b []byte) ([]NetService, []NetIngress, error) {
	type lbIngress struct {
		IP       string `json:"ip"`
		Hostname string `json:"hostname"`
	}
	var list struct {
		Items []struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name              string    `json:"name"`
				Namespace         string    `json:"namespace"`
				CreationTimestamp time.Time `json:"creationTimestamp"`
			} `json:"metadata"`
			Spec struct {
				Type             string            `json:"type"`
				ClusterIP        string            `json:"clusterIP"`
				Ports            []K8sServicePort  `json:"ports"`
				Selector         map[string]string `json:"selector"`
				ExternalIPs      []string          `json:"externalIPs"`
				IngressClassName string            `json:"ingressClassName"`
				TLS              []any             `json:"tls"`
				DefaultBackend   *struct {
					Service *struct {
						Name string `json:"name"`
						Port struct {
							Number int    `json:"number"`
							Name   string `json:"name"`
						} `json:"port"`
					} `json:"service"`
				} `json:"defaultBackend"`
				Rules []struct {
					Host string `json:"host"`
					HTTP *struct {
						Paths []struct {
							Path    string `json:"path"`
							Backend struct {
								Service *struct {
									Name string `json:"name"`
									Port struct {
										Number int    `json:"number"`
										Name   string `json:"name"`
									} `json:"port"`
								} `json:"service"`
							} `json:"backend"`
						} `json:"paths"`
					} `json:"http"`
				} `json:"rules"`
			} `json:"spec"`
			Status struct {
				LoadBalancer struct {
					Ingress []lbIngress `json:"ingress"`
				} `json:"loadBalancer"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, nil, fmt.Errorf("reading kubectl output: %w", err)
	}
	svcs, ings := []NetService{}, []NetIngress{}
	addrs := func(in []lbIngress) []string {
		out := []string{}
		for _, a := range in {
			if a.IP != "" {
				out = append(out, a.IP)
			} else if a.Hostname != "" {
				out = append(out, a.Hostname)
			}
		}
		return out
	}
	for _, it := range list.Items {
		md := it.Metadata
		switch it.Kind {
		case "Service":
			ports := it.Spec.Ports
			if ports == nil {
				ports = []K8sServicePort{}
			}
			ext := append(addrs(it.Status.LoadBalancer.Ingress), it.Spec.ExternalIPs...)
			svcs = append(svcs, NetService{K8sService: K8sService{Namespace: md.Namespace, Name: md.Name, Type: it.Spec.Type, Ports: ports},
				ClusterIP: it.Spec.ClusterIP, External: ext, Selector: it.Spec.Selector, Created: md.CreationTimestamp.Unix()})
		case "Ingress":
			ing := NetIngress{Namespace: md.Namespace, Name: md.Name, Class: it.Spec.IngressClassName, TLS: len(it.Spec.TLS) > 0,
				Address: addrs(it.Status.LoadBalancer.Ingress), Created: md.CreationTimestamp.Unix(), Rules: []IngressRule{}}
			port := func(n int, name string) string {
				if n > 0 {
					return strconv.Itoa(n)
				}
				return name
			}
			if d := it.Spec.DefaultBackend; d != nil && d.Service != nil {
				ing.Rules = append(ing.Rules, IngressRule{Host: "*", Path: "/", Service: d.Service.Name, Port: port(d.Service.Port.Number, d.Service.Port.Name)})
			}
			for _, rule := range it.Spec.Rules {
				host := rule.Host
				if host == "" {
					host = "*"
				}
				if rule.HTTP == nil {
					continue
				}
				for _, pth := range rule.HTTP.Paths {
					ir := IngressRule{Host: host, Path: pth.Path}
					if sv := pth.Backend.Service; sv != nil {
						ir.Service, ir.Port = sv.Name, port(sv.Port.Number, sv.Port.Name)
					}
					ing.Rules = append(ing.Rules, ir)
				}
			}
			ings = append(ings, ing)
		}
	}
	return svcs, ings, nil
}

func (s *Server) apiK8sNetwork(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := s.kube(ctx, r.PathValue("p"), "get", "services,ingresses", "--all-namespaces", "-o", "json")
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	svcs, ings, err := parseNetwork([]byte(out))
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": svcs, "ingresses": ings})
}

// ---------- workload actions: scale, restart, rollback, resources ----------

// workloadKinds are the kinds the actions accept, as kubectl names them.
var workloadKinds = map[string]bool{"deployment": true, "statefulset": true, "daemonset": true}

// cpuRe and memRe are the quantities the resources editor accepts: 250m, 0.5, 2; 128Mi, 1Gi, 500M.
var (
	cpuRe = regexp.MustCompile(`^([1-9][0-9]{0,5}m|[0-9]{1,3}(\.[0-9]{1,3})?)$`)
	memRe = regexp.MustCompile(`^[1-9][0-9]{0,6}(Ki|Mi|Gi|Ti|k|M|G|T)?$`)
	// containerNameRe is a DNS-1123 label, which container names are.
	containerNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
)

// ResourceEdit is one container's new requests and limits; "" removes a value.
type ResourceEdit struct {
	Name   string `json:"name"`
	CPUReq string `json:"cpuReq"`
	CPULim string `json:"cpuLim"`
	MemReq string `json:"memReq"`
	MemLim string `json:"memLim"`
}

// resourcesPatch checks the edits and builds the strategic merge patch that applies them.
func resourcesPatch(edits []ResourceEdit) ([]byte, error) {
	if len(edits) == 0 || len(edits) > 20 {
		return nil, errors.New("send one to 20 containers")
	}
	type rl map[string]any
	var containers []map[string]any
	for _, e := range edits {
		if !containerNameRe.MatchString(e.Name) {
			return nil, fmt.Errorf("invalid container name %q", e.Name)
		}
		for _, v := range []string{e.CPUReq, e.CPULim} {
			if v != "" && !cpuRe.MatchString(v) {
				return nil, fmt.Errorf("%s: %q is not a CPU amount; use millicores like 250m or cores like 0.5", e.Name, v)
			}
		}
		for _, v := range []string{e.MemReq, e.MemLim} {
			if v != "" && !memRe.MatchString(v) {
				return nil, fmt.Errorf("%s: %q is not a memory amount; use Mi or Gi, like 128Mi or 1Gi", e.Name, v)
			}
		}
		if e.CPUReq != "" && e.CPULim != "" {
			a, _ := parseCPU(e.CPUReq)
			b, _ := parseCPU(e.CPULim)
			if a > b {
				return nil, fmt.Errorf("%s: the CPU limit %s is below the request %s", e.Name, e.CPULim, e.CPUReq)
			}
		}
		if e.MemReq != "" && e.MemLim != "" {
			a, _ := parseMem(e.MemReq)
			b, _ := parseMem(e.MemLim)
			if a > b {
				return nil, fmt.Errorf("%s: the memory limit %s is below the request %s", e.Name, e.MemLim, e.MemReq)
			}
		}
		val := func(v string) any {
			if v == "" {
				return nil // null removes the key in a strategic merge patch
			}
			return v
		}
		containers = append(containers, map[string]any{"name": e.Name, "resources": map[string]rl{
			"requests": {"cpu": val(e.CPUReq), "memory": val(e.MemReq)},
			"limits":   {"cpu": val(e.CPULim), "memory": val(e.MemLim)},
		}})
	}
	return json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": containers}}}})
}

// POST workloads/{kind}/{ns}/{name}/{scale|restart|undo|resources}. Changes run as a job that
// then follows `kubectl rollout status`, so the page shows the rollout as it happens.
func (s *Server) apiK8sWorkloadAction(w http.ResponseWriter, r *http.Request) {
	p, kind, ns, name, action := r.PathValue("p"), r.PathValue("kind"), r.PathValue("ns"), r.PathValue("name"), r.PathValue("action")
	if !workloadKinds[kind] || !k8sNameRe.MatchString(ns) || !k8sNameRe.MatchString(name) {
		writeError(w, http.StatusBadRequest, errors.New("invalid kind, namespace or name"))
		return
	}
	ref := kind + "/" + name
	var title string
	var change func(ctx context.Context, j *Job) error
	switch action {
	case "scale":
		var body struct {
			Replicas *int `json:"replicas"`
		}
		if err := readJSON(r, &body); err != nil || body.Replicas == nil || *body.Replicas < 0 || *body.Replicas > 50 {
			writeError(w, http.StatusBadRequest, errors.New(`send {"replicas": 0 to 50}`))
			return
		}
		if kind == "daemonset" {
			writeError(w, http.StatusBadRequest, errors.New("a DaemonSet runs one pod per node; it cannot be scaled"))
			return
		}
		n := strconv.Itoa(*body.Replicas)
		title = fmt.Sprintf("scale %s/%s to %s", ns, name, n)
		change = func(ctx context.Context, j *Job) error {
			out, err := s.kube(ctx, p, "scale", ref, "-n", ns, "--replicas="+n)
			j.Line(strings.TrimSpace(out))
			return err
		}
	case "restart", "undo":
		verb := map[string]string{"restart": "restart", "undo": "roll back"}[action]
		title = fmt.Sprintf("%s %s/%s", verb, ns, name)
		change = func(ctx context.Context, j *Job) error {
			out, err := s.kube(ctx, p, "rollout", action, ref, "-n", ns)
			j.Line(strings.TrimSpace(out))
			return err
		}
	case "resources":
		var body struct {
			Containers []ResourceEdit `json:"containers"`
			DryRun     bool           `json:"dryRun"`
		}
		if err := readJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		patch, err := resourcesPatch(body.Containers)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		args := []string{"patch", ref, "-n", ns, "--type", "strategic", "--patch-file", "/dev/stdin"}
		if body.DryRun {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			out, err := s.kubeIn(ctx, p, bytes.NewReader(patch), append(args, "--dry-run=server")...)
			if err != nil {
				writeError(w, http.StatusUnprocessableEntity, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"output": strings.TrimSpace(out)})
			return
		}
		title = fmt.Sprintf("set resources on %s/%s", ns, name)
		change = func(ctx context.Context, j *Job) error {
			j.Line("kubectl " + strings.Join(args, " ") + " <<< " + string(patch))
			out, err := s.kubeIn(ctx, p, bytes.NewReader(patch), args...)
			j.Line(strings.TrimSpace(out))
			return err
		}
	default:
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown action %q", action))
		return
	}
	job := s.jobs.Start(title, p, func(j *Job) error {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		if err := change(ctx, j); err != nil {
			return err
		}
		j.Line("Waiting for the rollout (kubectl rollout status, up to 2 minutes)…")
		if err := s.opt.Colima.KubectlStream(ctx, p, j, "rollout", "status", ref, "-n", ns, "--timeout=120s"); err != nil {
			return errors.New("the rollout did not finish; look at the pods' events and logs")
		}
		return nil
	})
	writeJSON(w, http.StatusAccepted, job.snapshot())
}

// ---------- YAML ----------

// yamlKinds are the kinds whose YAML the console shows, as kubectl names them.
var yamlKinds = map[string]bool{"pod": true, "deployment": true, "statefulset": true, "daemonset": true, "replicaset": true,
	"service": true, "ingress": true, "configmap": true, "job": true, "cronjob": true}

// stripManagedFields drops metadata.managedFields from kubectl's YAML: bookkeeping nobody edits.
func stripManagedFields(y string) string {
	lines := strings.Split(y, "\n")
	out := make([]string, 0, len(lines))
	skip := false
	for _, l := range lines {
		if skip {
			if strings.HasPrefix(l, "   ") || strings.HasPrefix(l, "  - ") {
				continue
			}
			skip = false
		}
		if l == "  managedFields:" {
			skip = true
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func (s *Server) apiK8sYAML(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind, ns, name := q.Get("kind"), q.Get("ns"), q.Get("name")
	if !yamlKinds[kind] || !k8sNameRe.MatchString(ns) || !k8sNameRe.MatchString(name) {
		writeError(w, http.StatusBadRequest, errors.New("invalid kind, namespace or name"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := s.kube(ctx, r.PathValue("p"), "get", kind, name, "-n", ns, "-o", "yaml")
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"yaml": stripManagedFields(out)})
}

// maxManifest is the largest YAML the Apply box takes.
const maxManifest = 1 << 20

// POST apply {yaml, dryRun}: kubectl apply -f -, the YAML on stdin. A dry run asks the API
// server to check it without saving anything.
func (s *Server) apiK8sApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		YAML   string `json:"yaml"`
		DryRun bool   `json:"dryRun"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxManifest+4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("bad request body (at most 1 MB of YAML): %w", err))
		return
	}
	if strings.TrimSpace(body.YAML) == "" {
		writeError(w, http.StatusBadRequest, errors.New("paste a manifest first"))
		return
	}
	args := []string{"apply", "-f", "-"}
	if body.DryRun {
		args = append(args, "--dry-run=server")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	out, err := s.kubeIn(ctx, r.PathValue("p"), strings.NewReader(body.YAML), args...)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error(), "output": strings.TrimSpace(out)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": strings.TrimSpace(out)})
}

// ---------- shell into a pod ----------

// POST pods/{ns}/{pod}/shell {container, cols, rows}. On Colima's docker runtime k3s runs pods as
// Docker containers labelled with their pod, so the page's own shell (docker exec with a TTY,
// resizing, the coloured prompt) works unchanged.
func (s *Server) apiK8sPodShell(w http.ResponseWriter, r *http.Request) {
	ns, pod, err := podPath(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req struct {
		Container  string `json:"container"`
		Cols, Rows int
	}
	if err := readJSON(r, &req); err != nil || !containerNameRe.MatchString(req.Container) {
		writeError(w, http.StatusBadRequest, errors.New(`send {"container": "<name>"}`))
		return
	}
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	hint := fmt.Sprintf("kubectl exec -it -n %s %s -c %s -- sh", ns, pod, req.Container)
	cs, err := dc.Containers(ctx)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("the shell needs Colima's docker runtime (%v); in Terminal run: %s", err, hint))
		return
	}
	for _, c := range cs {
		l := c.Labels
		if l["io.kubernetes.pod.namespace"] == ns && l["io.kubernetes.pod.name"] == pod && l["io.kubernetes.container.name"] == req.Container && c.State == "running" {
			s.openShell(w, r, dc, c.ID, pod, req.Cols, req.Rows, shellOpts{})
			return
		}
	}
	writeError(w, http.StatusConflict, fmt.Errorf("container %s of %s is not running, so there is nothing to open a shell in", req.Container, pod))
}

var _ io.Writer = (*Job)(nil)
