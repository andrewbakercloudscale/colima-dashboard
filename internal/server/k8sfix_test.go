package server

import (
	"strings"
	"testing"
)

const mi = 1 << 20

func TestNiceValues(t *testing.T) {
	mem := map[int64]string{1: "16Mi", 100 * mi: "128Mi", 129 * mi: "192Mi", 300 * mi: "384Mi", 600 * mi: "768Mi", 1000 * mi: "1Gi", 1100 * mi: "1536Mi", 5000 * mi: "6Gi", 70000 * mi: "69Gi"}
	for in, want := range mem {
		if got := memQuantity(niceMem(in)); got != want {
			t.Errorf("niceMem(%dMi) = %s, want %s", in/mi, got, want)
		}
	}
	cpu := map[int64]string{1: "10m", 60: "100m", 260: "300m", 900: "1", 1200: "1500m", 2500: "3", 20000: "20"}
	for in, want := range cpu {
		if got := cpuQuantity(niceCPU(in)); got != want {
			t.Errorf("niceCPU(%d) = %s, want %s", in, got, want)
		}
	}
	if niceMemDown(100*mi) != 96*mi || niceMemDown(10*mi) != 0 || niceCPUDown(3600) != 3000 || niceCPUDown(5) != 0 {
		t.Error("rounding down")
	}
}

func plan(t *testing.T, check string, p Pod, nodes []Node, metrics bool) *K8sPlan {
	t.Helper()
	pods := map[string]*Pod{p.Namespace + "/" + p.Name: &p}
	pl := planFor(K8sIssue{Check: check, Namespace: p.Namespace, Pod: p.Name, Container: p.Containers[0].Name}, pods, nodes, metrics)
	if pl == nil {
		t.Fatalf("%s: no plan", check)
	}
	if pl.Why == "" {
		t.Errorf("%s: no reason", check)
	}
	return pl
}

func onePod(c PodContainer, workload string) Pod {
	p := Pod{Namespace: "cdtest", Name: "x-1", Workload: workload, Phase: "Running", Containers: []PodContainer{c},
		CPUReq: c.CPUReq, CPULim: c.CPULim, MemReq: c.MemReq, MemLim: c.MemLim, CPUUse: c.CPUUse, MemUse: c.MemUse, HasUsage: c.CPUUse+c.MemUse > 0}
	return p
}

func TestPlans(t *testing.T) {
	// OOMKilled at 16Mi: double the limit (no usage), request kept below it.
	pl := plan(t, "oom-killed", onePod(PodContainer{Name: "hog", CPUReq: 50, CPULim: 200, MemReq: 16 * mi, MemLim: 16 * mi, LastReason: "OOMKilled"}, "Deployment/oomer"), nil, false)
	if pl.Kind != "resources" || !pl.Applicable || pl.Recommended.MemLim != "32Mi" || pl.Recommended.MemReq != "16Mi" || pl.Current.MemLim != "16Mi" {
		t.Errorf("oom: %+v %+v", pl, pl.Recommended)
	}
	// Near the limit with usage: 1.5x use, request at use.
	pl = plan(t, "memory-near-limit", onePod(PodContainer{Name: "c", MemReq: 50 * mi, MemLim: 50 * mi, MemUse: 46 * mi, CPUUse: 1}, "Deployment/memhog"), nil, true)
	if pl.Recommended.MemLim != "96Mi" || pl.Recommended.MemReq != "64Mi" {
		t.Errorf("near limit: %+v", pl.Recommended)
	}
	// No resources, with usage: request 1.2x use (minimums), limit 2x.
	pl = plan(t, "no-resources", onePod(PodContainer{Name: "web", CPUUse: 300, MemUse: 200 * mi}, "Deployment/web"), nil, true)
	if r := pl.Recommended; r.CPUReq != "400m" || r.MemReq != "256Mi" || r.CPULim != "1" || r.MemLim != "512Mi" {
		t.Errorf("no resources with use: %+v", r)
	}
	if !strings.Contains(pl.Why, "Recommended: request 400m CPU and 256Mi memory; limit 1 CPU and 512Mi memory.") {
		t.Errorf("why: %s", pl.Why)
	}
	pl = plan(t, "no-resources", onePod(PodContainer{Name: "web", MemUse: 9 * mi}, "Deployment/web"), nil, true)
	if !strings.Contains(pl.Why, "uses under 1m CPU and 9Mi memory") {
		t.Errorf("idle use: %s", pl.Why)
	}
	pl = plan(t, "no-resources", onePod(PodContainer{Name: "web"}, "Deployment/web"), nil, false)
	if r := pl.Recommended; r.CPUReq != "50m" || r.MemReq != "64Mi" || r.CPULim != "100m" || r.MemLim != "128Mi" {
		t.Errorf("no resources defaults: %+v", r)
	}
	// CPU at its limit: double it.
	pl = plan(t, "cpu-at-limit", onePod(PodContainer{Name: "c", CPULim: 250, CPUUse: 249}, "StatefulSet/db"), nil, true)
	if pl.Recommended.CPULim != "500m" {
		t.Errorf("cpu: %+v", pl.Recommended)
	}
	// A bare pod cannot be changed in place: a patch to copy instead.
	pl = plan(t, "no-memory-limit", onePod(PodContainer{Name: "c", MemReq: 128 * mi}, ""), nil, false)
	if pl.Applicable || pl.Recommended.MemLim != "256Mi" || !strings.Contains(pl.Patch, "memory: 256Mi") || strings.Contains(pl.Patch, "template") {
		t.Errorf("bare pod: %+v\n%s", pl, pl.Patch)
	}
	pl = plan(t, "limit-below-request", onePod(PodContainer{Name: "c", CPUReq: 500, CPULim: 100, MemReq: 256 * mi, MemLim: 128 * mi}, "Deployment/x"), nil, false)
	if pl.Recommended.CPULim != "500m" || pl.Recommended.MemLim != "256Mi" {
		t.Errorf("limit below request: %+v", pl.Recommended)
	}
	// Probes and :latest are patches.
	pl = plan(t, "no-probes", onePod(PodContainer{Name: "web", Image: "nginx", Ports: []int{80}}, "Deployment/web"), nil, false)
	if pl.Kind != "patch" || !strings.Contains(pl.Patch, "readinessProbe:") || !strings.Contains(pl.Patch, "livenessProbe:") || !strings.Contains(pl.Patch, "port: 80") || !strings.Contains(pl.Patch, "template:") {
		t.Errorf("probes:\n%s", pl.Patch)
	}
	pl = plan(t, "no-liveness", onePod(PodContainer{Name: "web", Readiness: true}, ""), nil, false)
	if strings.Contains(pl.Patch, "readinessProbe") || !strings.Contains(pl.Patch, "exec:") {
		t.Errorf("liveness only:\n%s", pl.Patch)
	}
	pl = plan(t, "latest-tag", onePod(PodContainer{Name: "web", Image: "localhost:5000/nginx:latest"}, "Deployment/web"), nil, false)
	if !strings.Contains(pl.Patch, "image: localhost:5000/nginx:<version>") {
		t.Errorf("latest:\n%s", pl.Patch)
	}
	for _, check := range []string{"crashloop", "image-pull", "cannot-start", "restarts"} {
		if pl := plan(t, check, onePod(PodContainer{Name: "c", Image: "a:1"}, "Deployment/a"), nil, false); len(pl.Steps) == 0 {
			t.Errorf("%s: no steps", check)
		}
	}
}

func TestUnschedulablePlan(t *testing.T) {
	node := []Node{{Name: "n1", CPUCap: 4000, CPUAlloc: 4000, MemCap: 8 << 30, MemAlloc: 8 << 30, CPUReq: 460, MemReq: 360 * mi}}
	big := onePod(PodContainer{Name: "big", CPUReq: 64000, CPULim: 64000, MemReq: 128 * mi, MemLim: 256 * mi}, "Deployment/pending")
	big.Phase = "Pending"
	pl := plan(t, "unschedulable", big, node, false)
	// 3540m free, 90% is 3186m, rounded down to 3000m; memory fits so it stays.
	if pl.Kind != "resources" || pl.Recommended.CPUReq != "3" || pl.Recommended.MemReq != "128Mi" || pl.Recommended.CPULim != "4" || pl.VM == nil || pl.VM.CPUs != 66 {
		t.Errorf("unschedulable: %+v %+v %+v", pl, pl.Recommended, pl.VM)
	}
	full := []Node{{Name: "n1", CPUCap: 2000, CPUAlloc: 2000, MemCap: 2 << 30, MemAlloc: 2 << 30, CPUReq: 1990, MemReq: 1 << 30}}
	pl = plan(t, "unschedulable", big, full, false)
	if pl.Kind != "vm" || pl.VM == nil || pl.VM.HaveCPUs != 2 || pl.VM.CPUs < 3 {
		t.Errorf("full node: %+v %+v", pl, pl.VM)
	}
	ni := planFor(K8sIssue{Check: "memory-overcommitted", Node: "n1", Detail: "d"}, nil, []Node{{Name: "n1", CPUCap: 2000, MemCap: 2 << 30, MemLim: 3 << 30}}, false)
	if ni == nil || ni.Kind != "vm" || ni.VM.MemoryGB != 4 {
		t.Errorf("node plan: %+v", ni)
	}
}
