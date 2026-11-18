package server

import (
	"fmt"
	"sort"
	"strings"
)

// K8sIssue is one configuration or health problem found in the cluster, explained in plain
// English with what to do about it.
type K8sIssue struct {
	Severity  string `json:"severity"` // fail, warn or info
	Check     string `json:"check"`
	Namespace string `json:"namespace,omitempty"`
	Pod       string `json:"pod,omitempty"`
	Workload  string `json:"workload,omitempty"`
	Container string `json:"container,omitempty"`
	Node      string `json:"node,omitempty"`
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	Fix       string `json:"fix"`
	// Action names a button the page offers next to the fix: logs, previous-logs, resources.
	Action string `json:"action,omitempty"`
	// Plan is the recommended change, worked out from the numbers (k8sfix.go).
	Plan *K8sPlan `json:"plan,omitempty"`
}

// Restarts at or above this count are worth a look even when the pod is running now.
const highRestarts = 5

var sevRank = map[string]int{"fail": 0, "warn": 1, "info": 2}

// checkIssues looks at pods and nodes for the usual mistakes. Problems in a pod's spec (no
// requests, no probes, :latest) are reported once per workload and container, not once per
// replica; problems with what is happening (OOMKilled, CrashLoopBackOff) once per pod.
// metrics says whether usage numbers are known.
func checkIssues(pods []Pod, nodes []Node, metrics bool) []K8sIssue {
	out := []K8sIssue{}
	seen := map[string]bool{}
	upSince := map[string]int64{}
	for _, n := range nodes {
		if n.UpSince > 0 {
			upSince[n.Name] = n.UpSince + 120 // a little slack: the clock in the VM catches up after a start
		}
	}
	for _, p := range pods {
		base := K8sIssue{Namespace: p.Namespace, Pod: p.Name, Workload: p.Workload}
		who := "Pod " + p.Name
		if p.Workload != "" {
			who = p.Workload
		}
		add := func(c, sev, check, title, detail, fix, action string) {
			is := base
			is.Container, is.Severity, is.Check, is.Title, is.Detail, is.Fix, is.Action = c, sev, check, title, detail, fix, action
			out = append(out, is)
		}

		if p.SchedMsg != "" && p.Phase == "Pending" {
			sev, check, title, detail, fix, action := unschedulable(p, nodes)
			add("", sev, check, title, detail, fix, action)
		}
		for _, c := range p.Containers {
			// Spec problems: once per workload and container.
			key := p.Namespace + "/" + p.Workload + "/" + c.Name
			if p.Workload == "" {
				key = p.Namespace + "/pod/" + p.Name + "/" + c.Name
			}
			if !seen[key] {
				seen[key] = true
				specIssues(c, who, add)
			}
			statusIssues(p, c, metrics, upSince[p.Node], add)
		}
	}
	for _, n := range nodes {
		out = append(out, nodeIssues(n)...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := sevRank[out[i].Severity], sevRank[out[j].Severity]; a != b {
			return a < b
		}
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Pod < out[j].Pod
	})
	addPlans(out, pods, nodes, metrics)
	return out
}

type addFn func(container, sev, check, title, detail, fix, action string)

func specIssues(c PodContainer, who string, add addFn) {
	in := fmt.Sprintf("Container %s in %s", c.Name, who)
	switch {
	case c.CPUReq == 0 && c.MemReq == 0 && c.CPULim == 0 && c.MemLim == 0:
		add(c.Name, "warn", "no-resources", "No CPU or memory requests or limits",
			in+" asks for nothing and may use as much as it can grab. The scheduler reserves no room for it, "+
				"one busy pod can starve the rest of a small Colima node, and it is the first to be evicted when memory runs short.",
			"Set requests to what it normally uses and a memory limit above its peak, for example requests cpu 100m and memory 128Mi, limit memory 256Mi. Use Edit resources on the workload.",
			"resources")
	case c.MemLim == 0 && c.MemReq > 0:
		add(c.Name, "info", "no-memory-limit", "No memory limit",
			in+" has a memory request but no limit, so a leak can use the whole node's memory before anything stops it.",
			"Add a memory limit somewhat above its peak use, for example twice the request.", "resources")
	}
	if c.CPULim > 0 && c.CPUReq > c.CPULim {
		add(c.Name, "fail", "limit-below-request", "CPU limit is below the request",
			fmt.Sprintf("%s requests %s of CPU but is limited to %s. Kubernetes refuses a pod like this.", in, cpuText(c.CPUReq), cpuText(c.CPULim)),
			"Make the limit at least as large as the request.", "resources")
	}
	if c.MemLim > 0 && c.MemReq > c.MemLim {
		add(c.Name, "fail", "limit-below-request", "Memory limit is below the request",
			fmt.Sprintf("%s requests %s of memory but is limited to %s. Kubernetes refuses a pod like this.", in, memText(c.MemReq), memText(c.MemLim)),
			"Make the limit at least as large as the request.", "resources")
	}
	switch {
	case !c.Liveness && !c.Readiness:
		add(c.Name, "info", "no-probes", "No health checks",
			in+" has neither a readiness nor a liveness probe. Traffic reaches it the moment it starts, before the app is ready, and if it hangs without crashing nothing restarts it.",
			"Add a readinessProbe (and usually a livenessProbe), for example an httpGet on a health URL. Edit the YAML to add them.", "yaml")
	case !c.Readiness:
		add(c.Name, "info", "no-readiness", "No readiness probe",
			in+" has no readiness probe, so a Service sends it traffic as soon as it starts, before the app is ready.",
			"Add a readinessProbe, for example an httpGet on a health URL.", "yaml")
	case !c.Liveness:
		add(c.Name, "info", "no-liveness", "No liveness probe",
			in+" has no liveness probe. If the app hangs without crashing, nothing restarts it.",
			"Add a livenessProbe that fails when the app is stuck.", "yaml")
	}
	if tag, digest := imageTag(c.Image); !digest && (tag == "" || tag == "latest") {
		what := "has no tag, which means :latest"
		if tag == "latest" {
			what = "uses the :latest tag"
		}
		add(c.Name, "warn", "latest-tag", "Image tag is :latest",
			fmt.Sprintf("%s runs %s, which %s. :latest moves, so each restart or new node can run a different version, and Kubernetes pulls it on every start.", in, c.Image, what),
			"Pin a version tag (nginx:1.27) or a digest.", "yaml")
	}
}

// upSince is when the pod's node last became Ready (0 when unknown).
func statusIssues(p Pod, c PodContainer, metrics bool, upSince int64, add addFn) {
	in := fmt.Sprintf("Container %s in pod %s", c.Name, p.Name)
	oom := c.LastReason == "OOMKilled" || (c.State == "terminated" && c.Reason == "OOMKilled")
	switch {
	case oom:
		limit := "its memory limit"
		if c.MemLim > 0 {
			limit = "its memory limit of " + memText(c.MemLim)
		}
		now := ""
		if c.Reason == "CrashLoopBackOff" {
			now = " It is now in CrashLoopBackOff, restarted with growing delays."
		}
		add(c.Name, "fail", "oom-killed", "Killed for running out of memory (OOMKilled)",
			fmt.Sprintf("%s used more than %s and the kernel killed it (exit code 137). It has restarted %d times.%s", in, limit, c.Restarts, now),
			"Raise the memory limit with Edit resources, or find out what makes it grow.", "resources")
	case c.Reason == "CrashLoopBackOff":
		last := ""
		if c.LastReason != "" {
			last = fmt.Sprintf(" The last run ended with %s, exit code %d.", c.LastReason, c.LastExit)
		}
		add(c.Name, "fail", "crashloop", "Crashing on start (CrashLoopBackOff)",
			fmt.Sprintf("%s keeps exiting soon after it starts, so Kubernetes waits longer before each restart (%d so far).%s", in, c.Restarts, last),
			"Read the logs of the previous run to see why it exits: a missing setting, a bad command or a dependency it cannot reach.", "previous-logs")
	case c.Reason == "ImagePullBackOff" || c.Reason == "ErrImagePull" || c.Reason == "InvalidImageName" || c.Reason == "ErrImageNeverPull":
		msg := ""
		if c.Message != "" {
			msg = " Kubernetes says: " + shorten(c.Message, 300)
		}
		add(c.Name, "fail", "image-pull", "Cannot pull the image ("+c.Reason+")",
			fmt.Sprintf("%s cannot start because the image %s could not be pulled.%s", in, c.Image, msg),
			"Check the image name and tag exist and the registry is reachable; a private registry needs an imagePullSecret. "+
				"On Colima's docker runtime k3s can use images built with docker on this profile: set imagePullPolicy: IfNotPresent.", "yaml")
	case c.Reason == "CreateContainerConfigError" || c.Reason == "CreateContainerError" || c.Reason == "RunContainerError":
		add(c.Name, "fail", "cannot-start", "Container cannot be created ("+c.Reason+")",
			fmt.Sprintf("%s could not be created. %s", in, shorten(c.Message, 300)),
			"Usually a ConfigMap or Secret it refers to is missing, or a key in it. Create it, or fix the reference.", "yaml")
	// Every pod restarts when the VM does; a last stop from before the node came up says nothing.
	case c.Restarts >= highRestarts && !(upSince > 0 && c.LastAt > 0 && c.LastAt <= upSince):
		add(c.Name, "warn", "restarts", fmt.Sprintf("Restarted %d times", c.Restarts),
			fmt.Sprintf("%s has restarted %d times. It is running now, but something keeps stopping it.", in, c.Restarts),
			"Read the logs of the previous run, and the pod's events, to see why it stopped.", "previous-logs")
	}
	if metrics && c.MemLim > 0 && c.MemUse*10 > c.MemLim*9 && !oom {
		add(c.Name, "warn", "memory-near-limit", "Memory use is close to the limit",
			fmt.Sprintf("%s uses %s of its %s memory limit (%d%%). A little more and the kernel kills it (OOMKilled).", in, memText(c.MemUse), memText(c.MemLim), c.MemUse*100/c.MemLim),
			"Raise the memory limit with Edit resources, or check whether its memory keeps growing.", "resources")
	}
	if metrics && c.CPULim > 0 && c.CPUUse*10 >= c.CPULim*9 {
		add(c.Name, "warn", "cpu-at-limit", "CPU use is at the limit (throttled)",
			fmt.Sprintf("%s uses %s of its %s CPU limit. At the limit the kernel slows it down, so it responds late rather than failing.", in, cpuText(c.CPUUse), cpuText(c.CPULim)),
			"Raise the CPU limit with Edit resources, or find what keeps it busy.", "resources")
	}
}

func unschedulable(p Pod, nodes []Node) (string, string, string, string, string, string) {
	lacks := insufficientRe.FindAllStringSubmatch(p.SchedMsg, -1)
	if len(lacks) == 0 {
		return "warn", "unschedulable", "Waiting for a node (Pending)",
			fmt.Sprintf("Pod %s is not scheduled. The scheduler says: %s", p.Name, shorten(p.SchedMsg, 300)),
			"Read the pod's events. Common causes are a nodeSelector or taint no node matches, or a PersistentVolumeClaim that is not bound.", ""
	}
	// The biggest node is the one it would have fitted best.
	var n *Node
	for i := range nodes {
		if n == nil || nodes[i].CPUAlloc+nodes[i].MemAlloc/(1<<20) > n.CPUAlloc+n.MemAlloc/(1<<20) {
			n = &nodes[i]
		}
	}
	var parts []string
	what := map[string]bool{}
	for _, m := range lacks {
		what[m[1]] = true
	}
	if what["cpu"] {
		s := "CPU " + cpuText(p.CPUReq)
		if n != nil {
			s += fmt.Sprintf(" (the node has %s, %s already requested)", cpuText(n.CPUAlloc), cpuText(n.CPUReq))
		}
		parts = append(parts, s)
	}
	if what["memory"] {
		s := "memory " + memText(p.MemReq)
		if n != nil {
			s += fmt.Sprintf(" (the node has %s, %s already requested)", memText(n.MemAlloc), memText(n.MemReq))
		}
		parts = append(parts, s)
	}
	return "fail", "unschedulable", "Requests do not fit on the node (Pending)",
		fmt.Sprintf("Pod %s asks for %s, more than is free, so it waits forever. The scheduler says: %s", p.Name, strings.Join(parts, " and "), shorten(p.SchedMsg, 200)),
		"Lower the requests with Edit resources, scale other workloads down, or give the Colima VM more CPU or memory on the General page.", "resources"
}

func nodeIssues(n Node) []K8sIssue {
	var out []K8sIssue
	add := func(sev, check, title, detail, fix string) {
		out = append(out, K8sIssue{Severity: sev, Check: check, Node: n.Name, Title: title, Detail: detail, Fix: fix})
	}
	if !n.Ready {
		add("fail", "node-not-ready", "Node is not ready",
			fmt.Sprintf("Node %s reports NotReady, so no new pods start on it.", n.Name),
			"Look at the node's events; restarting the profile usually brings k3s back.")
	}
	if n.CPUAlloc > 0 && n.CPULim > n.CPUAlloc {
		add("info", "cpu-overcommitted", "CPU limits add up to more than the node has",
			fmt.Sprintf("Pods on %s may use up to %s of CPU between them, and the node has %s. That is normal for CPU, which is shared out when busy, but busy pods will slow each other down.", n.Name, cpuText(n.CPULim), cpuText(n.CPUAlloc)),
			"Nothing to do unless pods are slow; then lower some CPU limits or give the VM more CPUs.")
	}
	if n.MemAlloc > 0 && n.MemLim > n.MemAlloc {
		add("warn", "memory-overcommitted", "Memory limits add up to more than the node has",
			fmt.Sprintf("Pods on %s may use up to %s of memory between them, and the node has %s. If several grow at once, the node runs out and pods are killed or evicted.", n.Name, memText(n.MemLim), memText(n.MemAlloc)),
			"Lower memory limits that are far above real use, or give the Colima VM more memory.")
	}
	if n.MemAlloc > 0 && n.MemReq*10 > n.MemAlloc*9 || n.CPUAlloc > 0 && n.CPUReq*10 > n.CPUAlloc*9 {
		add("warn", "node-nearly-full", "Node is nearly fully booked",
			fmt.Sprintf("Requests on %s take %s of %s CPU and %s of %s memory. New pods may not fit.", n.Name, cpuText(n.CPUReq), cpuText(n.CPUAlloc), memText(n.MemReq), memText(n.MemAlloc)),
			"Lower requests that are far above real use, scale something down, or give the VM more room.")
	}
	return out
}

func shorten(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
