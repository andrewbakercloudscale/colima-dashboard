package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kubernetes objects as the console shows them: kubectl prints JSON, these functions keep the
// fields the page uses. CPU is in millicores and memory in bytes throughout; 0 means "not set".

// parseCPU reads a CPU quantity ("250m", "1", "0.5", "1500000n") as millicores.
func parseCPU(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	div := 1.0 / 1000 // whole cores
	switch {
	case strings.HasSuffix(s, "m"):
		div, s = 1, s[:len(s)-1]
	case strings.HasSuffix(s, "u"):
		div, s = 1000, s[:len(s)-1]
	case strings.HasSuffix(s, "n"):
		div, s = 1e6, s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	return int64(math.Ceil(f/div - 1e-9)), true
}

var memSuffix = map[string]float64{
	"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40, "Pi": 1 << 50, "Ei": 1 << 60,
	"k": 1e3, "K": 1e3, "M": 1e6, "G": 1e9, "T": 1e12, "P": 1e15, "E": 1e18, "m": 1e-3,
}

// parseMem reads a memory quantity ("128Mi", "1G", "500000", "1e9") as bytes.
func parseMem(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	mul := 1.0
	for _, n := range []int{2, 1} {
		if len(s) > n {
			if m, ok := memSuffix[s[len(s)-n:]]; ok {
				mul, s = m, s[:len(s)-n]
				break
			}
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	return int64(math.Round(f * mul)), true
}

func cpuQ(m map[string]string) int64 { v, _ := parseCPU(m["cpu"]); return v }
func memQ(m map[string]string) int64 { v, _ := parseMem(m["memory"]); return v }

// PodContainer is one container of a pod.
type PodContainer struct {
	Name       string `json:"name"`
	Image      string `json:"image"`
	CPUReq     int64  `json:"cpuReq"`
	CPULim     int64  `json:"cpuLim"`
	MemReq     int64  `json:"memReq"`
	MemLim     int64  `json:"memLim"`
	Liveness   bool   `json:"liveness"`
	Readiness  bool   `json:"readiness"`
	Ready      bool   `json:"ready"`
	Restarts   int    `json:"restarts"`
	State      string `json:"state"` // running, waiting, terminated
	Reason     string `json:"reason,omitempty"`
	Message    string `json:"message,omitempty"`
	LastReason string `json:"lastReason,omitempty"` // why the previous run ended, e.g. OOMKilled
	LastExit   int    `json:"lastExit,omitempty"`
	LastAt     int64  `json:"lastAt,omitempty"`
	Ports      []int  `json:"ports"`
	CPUUse     int64  `json:"cpuUse"`
	MemUse     int64  `json:"memUse"`
}

// Pod is one row of the Kubernetes page's pod list.
type Pod struct {
	Namespace  string         `json:"namespace"`
	Name       string         `json:"name"`
	Phase      string         `json:"phase"`
	Reason     string         `json:"reason,omitempty"` // e.g. CrashLoopBackOff, ImagePullBackOff
	Ready      string         `json:"ready"`            // "1/2"
	Restarts   int            `json:"restarts"`
	Created    int64          `json:"created"` // unix seconds
	Owner      string         `json:"owner,omitempty"`
	Workload   string         `json:"workload,omitempty"` // the controller people think of: Deployment/web
	Images     []string       `json:"images"`
	Node       string         `json:"node,omitempty"`
	IP         string         `json:"ip,omitempty"`
	QOS        string         `json:"qos,omitempty"`
	SchedMsg   string         `json:"schedMsg,omitempty"` // why it is not scheduled
	Containers []PodContainer `json:"containers"`
	CPUReq     int64          `json:"cpuReq"`
	CPULim     int64          `json:"cpuLim"`
	MemReq     int64          `json:"memReq"`
	MemLim     int64          `json:"memLim"`
	CPUUse     int64          `json:"cpuUse"`
	MemUse     int64          `json:"memUse"`
	HasUsage   bool           `json:"hasUsage"`
}

type k8sResources struct {
	Requests map[string]string `json:"requests"`
	Limits   map[string]string `json:"limits"`
}

type k8sTerminated struct {
	Reason     string     `json:"reason"`
	ExitCode   int        `json:"exitCode"`
	FinishedAt *time.Time `json:"finishedAt"`
	Message    string     `json:"message"`
}

type k8sContainerState struct {
	Running *struct{} `json:"running"`
	Waiting *struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	} `json:"waiting"`
	Terminated *k8sTerminated `json:"terminated"`
}

type k8sPodJSON struct {
	Metadata struct {
		Name              string            `json:"name"`
		Namespace         string            `json:"namespace"`
		Labels            map[string]string `json:"labels"`
		CreationTimestamp time.Time         `json:"creationTimestamp"`
		DeletionTimestamp *time.Time        `json:"deletionTimestamp"`
		OwnerReferences   []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		NodeName   string `json:"nodeName"`
		Containers []struct {
			Name           string          `json:"name"`
			Image          string          `json:"image"`
			Resources      k8sResources    `json:"resources"`
			LivenessProbe  json.RawMessage `json:"livenessProbe"`
			ReadinessProbe json.RawMessage `json:"readinessProbe"`
			Ports          []struct {
				ContainerPort int `json:"containerPort"`
			} `json:"ports"`
		} `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase      string `json:"phase"`
		Reason     string `json:"reason"`
		PodIP      string `json:"podIP"`
		QOSClass   string `json:"qosClass"`
		Conditions []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"conditions"`
		ContainerStatuses []struct {
			Name         string            `json:"name"`
			Ready        bool              `json:"ready"`
			RestartCount int               `json:"restartCount"`
			State        k8sContainerState `json:"state"`
			LastState    k8sContainerState `json:"lastState"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

func parsePods(b []byte) ([]Pod, error) {
	var list struct {
		Items []k8sPodJSON `json:"items"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("reading kubectl output: %w", err)
	}
	out := make([]Pod, 0, len(list.Items))
	for _, it := range list.Items {
		out = append(out, podFrom(it))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func podFrom(it k8sPodJSON) Pod {
	p := Pod{Namespace: it.Metadata.Namespace, Name: it.Metadata.Name, Phase: it.Status.Phase,
		Reason: it.Status.Reason, Created: it.Metadata.CreationTimestamp.Unix(), Images: []string{},
		Node: it.Spec.NodeName, IP: it.Status.PodIP, QOS: it.Status.QOSClass, Containers: []PodContainer{}}
	if it.Metadata.DeletionTimestamp != nil {
		p.Reason = "Terminating"
	}
	for _, o := range it.Metadata.OwnerReferences {
		p.Owner = o.Kind + "/" + o.Name
		p.Workload = p.Owner
		// A Deployment's pods belong to a ReplicaSet named <deployment>-<pod-template-hash>.
		if h := it.Metadata.Labels["pod-template-hash"]; o.Kind == "ReplicaSet" && h != "" && strings.HasSuffix(o.Name, "-"+h) {
			p.Workload = "Deployment/" + strings.TrimSuffix(o.Name, "-"+h)
		}
	}
	for _, c := range it.Status.Conditions {
		if c.Type == "PodScheduled" && c.Status == "False" {
			p.SchedMsg = c.Message
		}
	}
	status := map[string]int{}
	for i, cs := range it.Status.ContainerStatuses {
		if cs.Name != "" {
			status[cs.Name] = i
		}
	}
	ready := 0
	for idx, c := range it.Spec.Containers {
		i, ok := status[c.Name]
		if !ok && idx < len(it.Status.ContainerStatuses) && it.Status.ContainerStatuses[idx].Name == "" {
			i, ok = idx, true // statuses without names: match by position
		}
		pc := PodContainer{Name: c.Name, Image: c.Image, Ports: []int{},
			CPUReq: cpuQ(c.Resources.Requests), CPULim: cpuQ(c.Resources.Limits),
			MemReq: memQ(c.Resources.Requests), MemLim: memQ(c.Resources.Limits),
			Liveness: len(c.LivenessProbe) > 0 && string(c.LivenessProbe) != "null", Readiness: len(c.ReadinessProbe) > 0 && string(c.ReadinessProbe) != "null"}
		for _, port := range c.Ports {
			pc.Ports = append(pc.Ports, port.ContainerPort)
		}
		if ok {
			cs := it.Status.ContainerStatuses[i]
			pc.Ready, pc.Restarts = cs.Ready, cs.RestartCount
			switch st := cs.State; {
			case st.Running != nil:
				pc.State = "running"
			case st.Waiting != nil:
				pc.State, pc.Reason, pc.Message = "waiting", st.Waiting.Reason, st.Waiting.Message
			case st.Terminated != nil:
				pc.State, pc.Reason, pc.Message = "terminated", st.Terminated.Reason, st.Terminated.Message
			}
			if t := cs.LastState.Terminated; t != nil {
				pc.LastReason, pc.LastExit = t.Reason, t.ExitCode
				if t.FinishedAt != nil {
					pc.LastAt = t.FinishedAt.Unix()
				}
			}
			if pc.Ready {
				ready++
			}
			p.Restarts += pc.Restarts
			if pc.Reason != "" && pc.Reason != "Completed" && p.Reason == "" {
				p.Reason = pc.Reason
			}
		}
		p.Images = append(p.Images, c.Image)
		p.CPUReq += pc.CPUReq
		p.CPULim += pc.CPULim
		p.MemReq += pc.MemReq
		p.MemLim += pc.MemLim
		p.Containers = append(p.Containers, pc)
	}
	p.Ready = fmt.Sprintf("%d/%d", ready, len(it.Spec.Containers))
	return p
}

// active says whether a pod still holds its requests on a node.
func (p *Pod) active() bool { return p.Phase != "Succeeded" && p.Phase != "Failed" }

// usage is one row of kubectl top: CPU in millicores, memory in bytes.
type usage struct{ cpu, mem int64 }

// parseTopPods reads `kubectl top pods -A --containers --no-headers`:
// NAMESPACE POD CONTAINER CPU MEMORY, keyed "ns/pod/container".
func parseTopPods(s string) map[string]usage {
	out := map[string]usage{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 5 {
			continue
		}
		cpu, ok1 := parseCPU(f[3])
		mem, ok2 := parseMem(f[4])
		if ok1 && ok2 {
			out[f[0]+"/"+f[1]+"/"+f[2]] = usage{cpu, mem}
		}
	}
	return out
}

// parseTopNodes reads `kubectl top nodes --no-headers`: NAME CPU CPU% MEMORY MEMORY%.
func parseTopNodes(s string) map[string]usage {
	out := map[string]usage{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 5 {
			continue
		}
		cpu, ok1 := parseCPU(f[1])
		mem, ok2 := parseMem(f[3])
		if ok1 && ok2 {
			out[f[0]] = usage{cpu, mem}
		}
	}
	return out
}

// addUsage puts kubectl top's numbers on the pods.
func addUsage(pods []Pod, top map[string]usage) {
	for i := range pods {
		p := &pods[i]
		for j := range p.Containers {
			c := &p.Containers[j]
			if u, ok := top[p.Namespace+"/"+p.Name+"/"+c.Name]; ok {
				c.CPUUse, c.MemUse = u.cpu, u.mem
				p.CPUUse += u.cpu
				p.MemUse += u.mem
				p.HasUsage = true
			}
		}
	}
}

// Node is a cluster node with what it offers and what its pods ask for.
type Node struct {
	Name     string `json:"name"`
	Ready    bool   `json:"ready"`
	UpSince  int64  `json:"upSince,omitempty"` // when it last became Ready: the VM's last start
	Version  string `json:"version"`
	OS       string `json:"os"`
	CPUCap   int64  `json:"cpuCap"`
	CPUAlloc int64  `json:"cpuAlloc"`
	MemCap   int64  `json:"memCap"`
	MemAlloc int64  `json:"memAlloc"`
	PodsCap  int    `json:"podsCap"`
	Pods     int    `json:"pods"`
	CPUReq   int64  `json:"cpuReq"`
	CPULim   int64  `json:"cpuLim"`
	MemReq   int64  `json:"memReq"`
	MemLim   int64  `json:"memLim"`
	CPUUse   int64  `json:"cpuUse"`
	MemUse   int64  `json:"memUse"`
	HasUsage bool   `json:"hasUsage"`
}

func parseNodes(b []byte) ([]Node, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Capacity    map[string]string `json:"capacity"`
				Allocatable map[string]string `json:"allocatable"`
				Conditions  []struct {
					Type               string    `json:"type"`
					LastTransitionTime time.Time `json:"lastTransitionTime"`
					Status             string    `json:"status"`
				} `json:"conditions"`
				NodeInfo struct {
					KubeletVersion string `json:"kubeletVersion"`
					OSImage        string `json:"osImage"`
				} `json:"nodeInfo"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("reading kubectl output: %w", err)
	}
	out := make([]Node, 0, len(list.Items))
	for _, it := range list.Items {
		n := Node{Name: it.Metadata.Name, Version: it.Status.NodeInfo.KubeletVersion, OS: it.Status.NodeInfo.OSImage,
			CPUCap: cpuQ(it.Status.Capacity), CPUAlloc: cpuQ(it.Status.Allocatable),
			MemCap: memQ(it.Status.Capacity), MemAlloc: memQ(it.Status.Allocatable)}
		n.PodsCap, _ = strconv.Atoi(it.Status.Allocatable["pods"])
		for _, c := range it.Status.Conditions {
			if c.Type == "Ready" {
				n.Ready = c.Status == "True"
				n.UpSince = c.LastTransitionTime.Unix()
			}
		}
		out = append(out, n)
	}
	return out, nil
}

// sumNodes adds up each node's pods' requests and limits, and kubectl top's usage.
func sumNodes(nodes []Node, pods []Pod, top map[string]usage) {
	for i := range nodes {
		n := &nodes[i]
		for _, p := range pods {
			if p.Node != n.Name || !p.active() {
				continue
			}
			n.Pods++
			n.CPUReq += p.CPUReq
			n.CPULim += p.CPULim
			n.MemReq += p.MemReq
			n.MemLim += p.MemLim
		}
		if u, ok := top[n.Name]; ok {
			n.CPUUse, n.MemUse, n.HasUsage = u.cpu, u.mem, true
		}
	}
}

// K8sEvent is one Kubernetes event.
type K8sEvent struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Type      string `json:"type"` // Normal or Warning
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	Count     int    `json:"count"`
	Last      int64  `json:"last"` // unix seconds
}

func parseEvents(b []byte) ([]K8sEvent, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Namespace         string    `json:"namespace"`
				CreationTimestamp time.Time `json:"creationTimestamp"`
			} `json:"metadata"`
			InvolvedObject struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"involvedObject"`
			Type           string     `json:"type"`
			Reason         string     `json:"reason"`
			Message        string     `json:"message"`
			Count          int        `json:"count"`
			LastTimestamp  *time.Time `json:"lastTimestamp"`
			FirstTimestamp *time.Time `json:"firstTimestamp"`
			EventTime      *time.Time `json:"eventTime"`
			Series         *struct {
				Count            int        `json:"count"`
				LastObservedTime *time.Time `json:"lastObservedTime"`
			} `json:"series"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("reading kubectl output: %w", err)
	}
	out := make([]K8sEvent, 0, len(list.Items))
	for _, it := range list.Items {
		e := K8sEvent{Namespace: it.Metadata.Namespace, Kind: it.InvolvedObject.Kind, Name: it.InvolvedObject.Name,
			Type: it.Type, Reason: it.Reason, Message: strings.TrimSpace(it.Message), Count: max(it.Count, 1)}
		at := it.Metadata.CreationTimestamp
		for _, t := range []*time.Time{it.FirstTimestamp, it.EventTime, it.LastTimestamp} {
			if t != nil && !t.IsZero() && t.After(at) {
				at = *t
			}
		}
		if s := it.Series; s != nil {
			e.Count = max(e.Count, s.Count)
			if s.LastObservedTime != nil && s.LastObservedTime.After(at) {
				at = *s.LastObservedTime
			}
		}
		e.Last = at.Unix()
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Last > out[j].Last })
	return out, nil
}

// imageTag is an image reference's tag ("" when it has none, or is pinned by digest only).
func imageTag(ref string) (tag string, digest bool) {
	if i := strings.Index(ref, "@"); i >= 0 {
		return "", true
	}
	slash := strings.LastIndex(ref, "/")
	if i := strings.LastIndex(ref, ":"); i > slash {
		return ref[i+1:], false
	}
	return "", false
}

// cpuText and memText print quantities the way the page does: 250m, 1.5 cores; 128Mi, 1.5Gi.
func cpuText(m int64) string {
	if m >= 1000 && m%100 == 0 {
		return strconv.FormatFloat(float64(m)/1000, 'f', -1, 64) + " CPU"
	}
	return strconv.FormatInt(m, 10) + "m"
}

func memText(b int64) string {
	switch {
	case b >= 1<<30:
		return strconv.FormatFloat(math.Round(float64(b)/(1<<30)*10)/10, 'f', -1, 64) + "Gi"
	case b >= 1<<20:
		return strconv.FormatFloat(math.Round(float64(b)/(1<<20)), 'f', -1, 64) + "Mi"
	case b >= 1<<10:
		return strconv.FormatFloat(math.Round(float64(b)/(1<<10)), 'f', -1, 64) + "Ki"
	}
	return strconv.FormatInt(b, 10)
}

// insufficientRe finds the resources a FailedScheduling message says the nodes lack.
var insufficientRe = regexp.MustCompile(`Insufficient (cpu|memory)`)
