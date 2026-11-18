package server

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Fix plans: for every issue, what to change and why. Resource changes come with the numbers
// worked out (from live use where metrics-server has it) and rounded to values people write;
// the page prefills its resources editor with them. What cannot be applied from here (probes,
// image tags, a pod no workload owns) comes as a patch to copy.

// K8sPlan is the recommended fix for one issue.
type K8sPlan struct {
	// Kind is resources (apply with the resources editor), patch (copy the YAML), vm (the
	// Colima VM needs more room) or steps (things to look at).
	Kind        string        `json:"kind"`
	Why         string        `json:"why"`
	Container   string        `json:"container,omitempty"`
	Current     *ResourceEdit `json:"current,omitempty"`
	Recommended *ResourceEdit `json:"recommended,omitempty"`
	Applicable  bool          `json:"applicable"` // the resources editor can apply it (owned by a workload)
	Patch       string        `json:"patch,omitempty"`
	Steps       []string      `json:"steps,omitempty"`
	VM          *VMNeed       `json:"vm,omitempty"`
}

// VMNeed says how big the Colima VM should be for everything to fit.
type VMNeed struct {
	CPUs     int `json:"cpus"`
	MemoryGB int `json:"memoryGB"`
	HaveCPUs int `json:"haveCPUs"`
	HaveGB   int `json:"haveGB"`
}

var (
	memLadder = []int64{16, 32, 64, 96, 128, 192, 256, 384, 512, 768, 1024, 1536, 2048, 3072, 4096, 6144, 8192, 12288, 16384, 24576, 32768, 49152, 65536}
	cpuLadder = []int64{10, 25, 50, 100, 150, 200, 250, 300, 400, 500, 750, 1000, 1500, 2000, 3000, 4000, 6000, 8000, 12000, 16000}
)

// niceMem rounds bytes up to the next value on the ladder (128Mi, 192Mi, 256Mi, 384Mi, 512Mi,
// 768Mi, 1Gi, 1536Mi, 2Gi ...).
func niceMem(b int64) int64 {
	for _, mi := range memLadder {
		if v := mi << 20; v >= b {
			return v
		}
	}
	return int64(math.Ceil(float64(b)/(1<<30))) << 30
}

// niceMemDown rounds bytes down to the ladder; 0 when below its first step.
func niceMemDown(b int64) int64 {
	out := int64(0)
	for _, mi := range memLadder {
		if mi<<20 <= b {
			out = mi << 20
		}
	}
	return out
}

// niceCPU rounds millicores up to the next value on the ladder (50m, 100m, 250m, 500m, 1 ...).
func niceCPU(m int64) int64 {
	for _, v := range cpuLadder {
		if v >= m {
			return v
		}
	}
	return (m + 999) / 1000 * 1000
}

func niceCPUDown(m int64) int64 {
	out := int64(0)
	for _, v := range cpuLadder {
		if v <= m {
			out = v
		}
	}
	return out
}

// memQuantity and cpuQuantity write values the way the editor accepts them: 256Mi, 2Gi; 250m, 2.
func memQuantity(b int64) string {
	if b <= 0 {
		return ""
	}
	if b%(1<<30) == 0 {
		return strconv.FormatInt(b>>30, 10) + "Gi"
	}
	return strconv.FormatInt((b+(1<<20)-1)>>20, 10) + "Mi"
}

func cpuQuantity(m int64) string {
	if m <= 0 {
		return ""
	}
	if m%1000 == 0 {
		return strconv.FormatInt(m/1000, 10)
	}
	return strconv.FormatInt(m, 10) + "m"
}

const (
	minCPUReq = 50
	minMemReq = 64 << 20
)

func currentEdit(c PodContainer) *ResourceEdit {
	return &ResourceEdit{Name: c.Name, CPUReq: cpuQuantity(c.CPUReq), CPULim: cpuQuantity(c.CPULim), MemReq: memQuantity(c.MemReq), MemLim: memQuantity(c.MemLim)}
}

// cpuUseText and memUseText say how much is in use, "under 1m" rather than "0m".
func cpuUseText(m int64) string {
	if m <= 0 {
		return "under 1m"
	}
	return cpuText(m)
}

func memUseText(b int64) string {
	if b < 1<<20 {
		return "under 1Mi"
	}
	return memText(b)
}

// recSentence states a recommendation's numbers: "Request 50m CPU and 64Mi memory; limit 100m
// CPU and 128Mi memory."
func recSentence(r *ResourceEdit) string {
	part := func(cpu, mem string) string {
		var s []string
		if cpu != "" {
			s = append(s, cpu+" CPU")
		}
		if mem != "" {
			s = append(s, mem+" memory")
		}
		return strings.Join(s, " and ")
	}
	var out []string
	if p := part(r.CPUReq, r.MemReq); p != "" {
		out = append(out, "request "+p)
	}
	if p := part(r.CPULim, r.MemLim); p != "" {
		out = append(out, "limit "+p)
	}
	if len(out) == 0 {
		return ""
	}
	s := strings.Join(out, "; ")
	return "Recommended: " + s + "."
}

// resourcesPlan wraps a recommendation; it is applicable when a workload the editor can change
// owns the pod, and otherwise carries the same change as a patch to copy.
func resourcesPlan(p Pod, c PodContainer, rec *ResourceEdit, why string) *K8sPlan {
	if s := recSentence(rec); s != "" {
		why = strings.TrimSpace(why) + " " + s
	}
	pl := &K8sPlan{Kind: "resources", Why: why, Container: c.Name, Current: currentEdit(c), Recommended: rec}
	kind, _, _ := strings.Cut(p.Workload, "/")
	pl.Applicable = kind == "Deployment" || kind == "StatefulSet" || kind == "DaemonSet"
	if !pl.Applicable {
		pl.Patch = resourcesYAML(c.Name, rec, false)
		pl.Steps = []string{"Pod " + p.Name + " is not managed by a Deployment, StatefulSet or DaemonSet, and a running pod's resources cannot be changed. Put these values in the manifest it came from and create it again."}
	}
	return pl
}

// resourcesYAML is the change as YAML: the pod template's container for a workload, or the
// pod's own containers.
func resourcesYAML(container string, r *ResourceEdit, template bool) string {
	var b strings.Builder
	ind := ""
	if template {
		b.WriteString("spec:\n  template:\n    spec:\n      containers:\n")
		ind = "      "
	} else {
		b.WriteString("spec:\n  containers:\n")
		ind = "  "
	}
	fmt.Fprintf(&b, "%s- name: %s\n%s  resources:\n", ind, container, ind)
	write := func(title, cpu, mem string) {
		if cpu == "" && mem == "" {
			return
		}
		fmt.Fprintf(&b, "%s    %s:\n", ind, title)
		if cpu != "" {
			fmt.Fprintf(&b, "%s      cpu: %s\n", ind, cpu)
		}
		if mem != "" {
			fmt.Fprintf(&b, "%s      memory: %s\n", ind, mem)
		}
	}
	write("requests", r.CPUReq, r.MemReq)
	write("limits", r.CPULim, r.MemLim)
	return b.String()
}

// probePatch is a readiness and liveness probe to start from: a TCP check on the container's
// first port, or an exec placeholder when it declares none.
func probePatch(c PodContainer, template bool) string {
	check := "exec:\n  command: [\"cat\", \"/tmp/healthy\"]  # replace with a command that fails when the app is unhealthy"
	if len(c.Ports) > 0 {
		check = fmt.Sprintf("tcpSocket:\n  port: %d  # or httpGet: {path: /healthz, port: %d} if it serves HTTP", c.Ports[0], c.Ports[0])
	}
	pre := "spec:\n  containers:\n  - name: " + c.Name + "\n"
	ind := "    "
	if template {
		pre = "spec:\n  template:\n    spec:\n      containers:\n      - name: " + c.Name + "\n"
		ind = "        "
	}
	var b strings.Builder
	b.WriteString(pre)
	probe := func(name string, delay, period int) {
		if (name == "readinessProbe" && c.Readiness) || (name == "livenessProbe" && c.Liveness) {
			return
		}
		fmt.Fprintf(&b, "%s%s:\n", ind, name)
		for _, l := range strings.Split(check, "\n") {
			b.WriteString(ind + "  " + l + "\n")
		}
		fmt.Fprintf(&b, "%s  initialDelaySeconds: %d\n%s  periodSeconds: %d\n", ind, delay, ind, period)
	}
	probe("readinessProbe", 5, 10)
	probe("livenessProbe", 15, 20)
	return b.String()
}

// planFor works out the fix for one issue.
func planFor(is K8sIssue, pods map[string]*Pod, nodes []Node, metrics bool) *K8sPlan {
	if is.Node != "" {
		for _, n := range nodes {
			if n.Name == is.Node {
				return nodePlan(is, n, pods)
			}
		}
		return nil
	}
	p := pods[is.Namespace+"/"+is.Pod]
	if p == nil {
		return nil
	}
	var c PodContainer
	for _, x := range p.Containers {
		if x.Name == is.Container {
			c = x
		}
	}
	template := strings.HasPrefix(p.Workload, "Deployment/") || strings.HasPrefix(p.Workload, "StatefulSet/") || strings.HasPrefix(p.Workload, "DaemonSet/")
	use := metrics && p.HasUsage
	rec := currentEdit(c)
	switch is.Check {
	case "oom-killed", "memory-near-limit":
		var lim int64
		var why string
		switch {
		case is.Check == "oom-killed" && c.MemLim > 0:
			// It died at the limit, so what it uses now (after a restart) is not its peak.
			lim = max(2*c.MemLim, c.MemUse*3/2)
			why = fmt.Sprintf("It was killed at %s, so it needs more than that. The recommendation doubles the limit", memText(c.MemLim))
		case use && c.MemUse > 0:
			lim = c.MemUse * 3 / 2
			why = fmt.Sprintf("It uses %s now. The recommendation leaves half as much again on top", memText(c.MemUse))
		default:
			lim = max(2*c.MemLim, 256<<20)
			why = "With no usage figures, the recommendation doubles the limit"
		}
		lim = niceMem(lim)
		rec.MemLim = memQuantity(lim)
		if use && c.MemUse > 0 {
			rec.MemReq = memQuantity(min(niceMem(max(c.MemUse, minMemReq)), lim))
			why += fmt.Sprintf(", and sets the request near what it uses now (%s) so the scheduler reserves enough.", memText(c.MemUse))
		} else {
			if c.MemReq > lim {
				rec.MemReq = rec.MemLim
			}
			why += "."
		}
		return resourcesPlan(*p, c, rec, why)
	case "cpu-at-limit":
		rec.CPULim = cpuQuantity(niceCPU(max(2*c.CPULim, c.CPUUse*3/2)))
		return resourcesPlan(*p, c, rec, fmt.Sprintf("It uses %s of its %s CPU limit, so it is being slowed down (throttled). The recommendation doubles the limit.", cpuText(c.CPUUse), cpuText(c.CPULim)))
	case "no-resources":
		cpuReq, memReq := int64(minCPUReq), int64(minMemReq)
		why := "There are no usage figures yet, so the recommendation starts from small defaults (50m CPU, 64Mi) with limits at twice that. Check them once it has run a while."
		if use {
			cpuReq = niceCPU(max(minCPUReq, c.CPUUse*6/5))
			memReq = niceMem(max(minMemReq, c.MemUse*6/5))
			why = fmt.Sprintf("It uses %s CPU and %s memory now. Requests are that plus a fifth (at least 50m and 64Mi); limits are twice the use, and at least twice the request.", cpuUseText(c.CPUUse), memUseText(c.MemUse))
		}
		rec.CPUReq, rec.MemReq = cpuQuantity(cpuReq), memQuantity(memReq)
		rec.CPULim = cpuQuantity(niceCPU(max(2*cpuReq, 2*c.CPUUse)))
		rec.MemLim = memQuantity(niceMem(max(2*memReq, 2*c.MemUse)))
		return resourcesPlan(*p, c, rec, why)
	case "no-memory-limit":
		lim := niceMem(max(2*c.MemReq, 2*c.MemUse))
		rec.MemLim = memQuantity(lim)
		return resourcesPlan(*p, c, rec, fmt.Sprintf("A limit of %s is twice its request%s, enough headroom for normal peaks while stopping a leak.", memText(lim), map[bool]string{true: " or its current use, whichever is larger", false: ""}[use]))
	case "limit-below-request":
		if c.CPULim > 0 && c.CPUReq > c.CPULim {
			rec.CPULim = cpuQuantity(c.CPUReq)
		}
		if c.MemLim > 0 && c.MemReq > c.MemLim {
			rec.MemLim = memQuantity(c.MemReq)
		}
		return resourcesPlan(*p, c, rec, "Raising the limit to the request makes the pod valid without taking anything away from it.")
	case "unschedulable":
		return unschedulablePlan(*p, nodes, pods, template)
	case "no-probes", "no-readiness", "no-liveness":
		return &K8sPlan{Kind: "patch", Container: c.Name, Patch: probePatch(c, template),
			Why:   "Probes depend on the app (which port, which URL means healthy), so they are not applied from here. This is a starting point: a check on its port. Add it to the manifest, or paste it into the YAML editor under the container.",
			Steps: []string{"Readiness: until it passes, the pod gets no traffic from Services.", "Liveness: when it keeps failing, Kubernetes restarts the container. Give it a longer delay than readiness so a slow start is not killed."}}
	case "latest-tag":
		repo := c.Image
		if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
			repo = repo[:i]
		}
		yamlPath := "spec:\n  containers:\n  - name: " + c.Name + "\n    image: " + repo + ":<version>\n"
		if template {
			yamlPath = "spec:\n  template:\n    spec:\n      containers:\n      - name: " + c.Name + "\n        image: " + repo + ":<version>  # for example 1.27.3\n"
		}
		return &K8sPlan{Kind: "patch", Container: c.Name, Patch: yamlPath,
			Why: "Pin the image to a version so every pod runs the same thing and a restart cannot quietly upgrade it.",
			Steps: []string{"Look up the current version on the image's registry page (Docker Hub lists tags), and use that tag instead of latest.",
				"For the strictest pin use the digest: image: " + repo + "@sha256:… (docker image inspect shows it as RepoDigests).",
				"Then change it deliberately when you want an upgrade, and use Roll back if the new one misbehaves."}}
	case "crashloop", "restarts":
		return &K8sPlan{Kind: "steps", Container: c.Name, Why: "Kubernetes can restart it but cannot tell why it exits; the logs of the run that ended say.",
			Steps: []string{"Open the logs of the last run (the button below) and read the last lines before it stopped.",
				"An exit code of 1 or 2 is usually the app refusing to start: a missing environment variable, config file or secret.",
				"Exit code 137 without OOMKilled means something stopped it from outside, often a liveness probe that fails before the app is ready: give it a longer initialDelaySeconds.",
				"A dependency it cannot reach (a database, a URL) shows as a connection error in the logs."}}
	case "image-pull":
		return &K8sPlan{Kind: "patch", Container: c.Name, Why: "The image name, its tag or access to the registry is wrong. Fix the image in the manifest.",
			Patch: map[bool]string{true: "spec:\n  template:\n    spec:\n      containers:\n      - name: " + c.Name + "\n        image: " + c.Image + "  # correct the name or tag\n        imagePullPolicy: IfNotPresent  # use an image built locally with docker\n",
				false: "spec:\n  containers:\n  - name: " + c.Name + "\n    image: " + c.Image + "  # correct the name or tag\n    imagePullPolicy: IfNotPresent\n"}[template],
			Steps: []string{"Check the image exists: docker pull " + c.Image + " on this profile shows the registry's answer.",
				"Built it yourself with docker on this profile? k3s here uses the same Docker, so set imagePullPolicy: IfNotPresent (or Never) and it runs without a registry.",
				"A private registry needs an imagePullSecret in the pod spec."}}
	case "cannot-start":
		return &K8sPlan{Kind: "steps", Container: c.Name, Why: "The container could not be created, so it never ran and there are no logs.",
			Steps: []string{"Open the pod's events: the message names the missing ConfigMap, Secret or key.", "Create it (kubectl create configmap / secret), or fix the name in the manifest. The pod starts by itself once it exists."}}
	}
	return nil
}

// unschedulablePlan lowers the pod's requests to what is free on the node, or says how big the VM
// must be when even small requests would not fit.
func unschedulablePlan(p Pod, nodes []Node, pods map[string]*Pod, template bool) *K8sPlan {
	var n *Node
	for i := range nodes {
		if n == nil || nodes[i].CPUAlloc > n.CPUAlloc {
			n = &nodes[i]
		}
	}
	if n == nil || len(p.Containers) == 0 {
		return &K8sPlan{Kind: "steps", Why: "No node was found to compare with.", Steps: []string{"Read the pod's events for the scheduler's reason."}}
	}
	freeCPU, freeMem := n.CPUAlloc-n.CPUReq, n.MemAlloc-n.MemReq
	need := &VMNeed{HaveCPUs: int(n.CPUCap / 1000), HaveGB: int(math.Round(float64(n.MemCap) / (1 << 30))),
		CPUs: int((n.CPUReq+p.CPUReq+999)/1000) + 1, MemoryGB: int((n.MemReq+p.MemReq+(1<<30)-1)>>30) + 1}
	need.CPUs, need.MemoryGB = max(need.CPUs, need.HaveCPUs), max(need.MemoryGB, need.HaveGB)
	// Share what is free between the containers in proportion to what they ask for, keeping a
	// tenth spare, rounded down.
	c := p.Containers[0]
	rec := currentEdit(c)
	fits := true
	if p.CPUReq > freeCPU && c.CPUReq > 0 {
		v := niceCPUDown(freeCPU * 9 / 10 * c.CPUReq / max(p.CPUReq, 1))
		if v < minCPUReq {
			fits = false
		}
		rec.CPUReq = cpuQuantity(v)
		// A limit beyond the whole node means nothing but overcommitment; bring it to the node.
		if c.CPULim > n.CPUAlloc {
			rec.CPULim = cpuQuantity(max(niceCPUDown(n.CPUAlloc), v))
		}
	}
	if p.MemReq > freeMem && c.MemReq > 0 {
		v := niceMemDown(freeMem * 9 / 10 * c.MemReq / max(p.MemReq, 1))
		if v < minMemReq {
			fits = false
		}
		rec.MemReq = memQuantity(v)
		if c.MemLim > n.MemAlloc {
			rec.MemLim = memQuantity(max(niceMemDown(n.MemAlloc), v))
		}
	}
	vmWhy := fmt.Sprintf("Or give the Colima VM more room: it has %d CPUs and %d GB; %d CPUs and %d GB would fit this pod next to the others. Change it on the General page (the VM restarts).", need.HaveCPUs, need.HaveGB, need.CPUs, need.MemoryGB)
	if !fits {
		return &K8sPlan{Kind: "vm", Container: c.Name, VM: need,
			Why:   fmt.Sprintf("Only %s CPU and %s memory are free on node %s, too little for even a small request. The Colima VM needs more room.", cpuText(max(freeCPU, 0)), memText(max(freeMem, 0)), n.Name),
			Steps: []string{vmWhy, "Or scale down other workloads to free their requests."}}
	}
	pl := resourcesPlan(p, c, rec, fmt.Sprintf("Node %s has %s CPU and %s memory free (allocatable minus what other pods request). The recommendation lowers the request to fit, with a tenth to spare.", n.Name, cpuText(max(freeCPU, 0)), memText(max(freeMem, 0))))
	pl.VM = need
	pl.Steps = append(pl.Steps, vmWhy)
	if len(p.Containers) > 1 {
		pl.Steps = append(pl.Steps, "This pod has several containers; the recommendation is for "+c.Name+". Lower the others in the editor too.")
	}
	return pl
}

func nodePlan(is K8sIssue, n Node, pods map[string]*Pod) *K8sPlan {
	need := &VMNeed{HaveCPUs: int(n.CPUCap / 1000), HaveGB: int(math.Round(float64(n.MemCap) / (1 << 30)))}
	need.CPUs = max(need.HaveCPUs, int((max(n.CPUReq, n.CPULim/2)*5/4+999)/1000))
	need.MemoryGB = max(need.HaveGB, int((max(n.MemReq, n.MemLim)*5/4+(1<<30)-1)>>30))
	general := fmt.Sprintf("Give the Colima VM more room on the General page: it has %d CPUs and %d GB; %d CPUs and %d GB covers what the pods ask for with a quarter to spare.", need.HaveCPUs, need.HaveGB, need.CPUs, need.MemoryGB)
	switch is.Check {
	case "node-not-ready":
		return &K8sPlan{Kind: "steps", Why: "k3s on the node is not answering its health checks.", Steps: []string{"Restart the profile from General; k3s starts with it.", "If it stays NotReady, look at the VM's logs under Troubleshooting."}}
	case "cpu-overcommitted":
		return &K8sPlan{Kind: "steps", Why: is.Detail, Steps: []string{"Lower CPU limits that are far above use: each pod's Resources view shows use against its limit.", general}}
	default:
		return &K8sPlan{Kind: "vm", Why: is.Detail, VM: need, Steps: []string{"Lower requests and limits that are far above real use: the Pods view, Resources, shows use against them.", general}}
	}
}

// addPlans attaches a fix plan to each issue.
func addPlans(issues []K8sIssue, pods []Pod, nodes []Node, metrics bool) {
	byName := map[string]*Pod{}
	for i := range pods {
		byName[pods[i].Namespace+"/"+pods[i].Name] = &pods[i]
	}
	for i := range issues {
		issues[i].Plan = planFor(issues[i], byName, nodes, metrics)
	}
}
