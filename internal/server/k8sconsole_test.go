package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestQuantities(t *testing.T) {
	cpu := map[string]int64{"250m": 250, "1": 1000, "0.5": 500, "1.25": 1250, "1500000n": 2, "100u": 1, "64": 64000}
	for in, want := range cpu {
		if got, ok := parseCPU(in); !ok || got != want {
			t.Errorf("parseCPU(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	mem := map[string]int64{"128Mi": 128 << 20, "1Gi": 1 << 30, "500M": 500e6, "1k": 1000, "1048576": 1 << 20, "1e3": 1000, "3Ki": 3072}
	for in, want := range mem {
		if got, ok := parseMem(in); !ok || got != want {
			t.Errorf("parseMem(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "abc", "-1", "1x"} {
		if _, ok := parseCPU(bad); ok {
			t.Errorf("parseCPU(%q) accepted", bad)
		}
		if _, ok := parseMem(bad); ok && bad != "" {
			t.Errorf("parseMem(%q) accepted", bad)
		}
	}
	if cpuText(250) != "250m" || cpuText(2000) != "2 CPU" || memText(128<<20) != "128Mi" || memText(3<<29) != "1.5Gi" {
		t.Errorf("text: %s %s %s %s", cpuText(250), cpuText(2000), memText(128<<20), memText(3<<29))
	}
}

// podsJSON exercises every issue the checker knows about.
const podsJSON = `{"items":[
{"metadata":{"name":"bare-5d8f-abcde","namespace":"cdtest","labels":{"pod-template-hash":"5d8f"},"ownerReferences":[{"kind":"ReplicaSet","name":"bare-5d8f"}],"creationTimestamp":"2026-11-17T10:00:00Z"},
 "spec":{"nodeName":"n1","containers":[{"name":"web","image":"nginx:latest"}]},
 "status":{"phase":"Running","containerStatuses":[{"name":"web","ready":true,"restartCount":0,"state":{"running":{}}}]}},
{"metadata":{"name":"bare-5d8f-fghij","namespace":"cdtest","labels":{"pod-template-hash":"5d8f"},"ownerReferences":[{"kind":"ReplicaSet","name":"bare-5d8f"}],"creationTimestamp":"2026-11-17T10:00:00Z"},
 "spec":{"nodeName":"n1","containers":[{"name":"web","image":"nginx:latest"}]},
 "status":{"phase":"Running","containerStatuses":[{"name":"web","ready":true,"restartCount":0,"state":{"running":{}}}]}},
{"metadata":{"name":"oom-1","namespace":"cdtest","creationTimestamp":"2026-11-17T10:00:00Z"},
 "spec":{"nodeName":"n1","containers":[{"name":"hog","image":"busybox:1.36","resources":{"requests":{"cpu":"50m","memory":"16Mi"},"limits":{"cpu":"200m","memory":"16Mi"}},"livenessProbe":{"exec":{}},"readinessProbe":{"exec":{}}}]},
 "status":{"phase":"Running","containerStatuses":[{"name":"hog","ready":false,"restartCount":4,"state":{"waiting":{"reason":"CrashLoopBackOff"}},"lastState":{"terminated":{"reason":"OOMKilled","exitCode":137}}}]}},
{"metadata":{"name":"big-1","namespace":"cdtest","creationTimestamp":"2026-11-17T10:00:00Z"},
 "spec":{"containers":[{"name":"big","image":"nginx:1.27","resources":{"requests":{"cpu":"64","memory":"128Mi"},"limits":{"cpu":"64","memory":"256Mi"}},"livenessProbe":{},"readinessProbe":{}}]},
 "status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"False","reason":"Unschedulable","message":"0/1 nodes are available: 1 Insufficient cpu."}]}},
{"metadata":{"name":"pull-1","namespace":"cdtest","creationTimestamp":"2026-11-17T10:00:00Z"},
 "spec":{"nodeName":"n1","containers":[{"name":"app","image":"registry.invalid/x:1.0","resources":{"requests":{"cpu":"10m","memory":"16Mi"},"limits":{"memory":"64Mi"}},"livenessProbe":{},"readinessProbe":{}}]},
 "status":{"phase":"Pending","containerStatuses":[{"name":"app","ready":false,"restartCount":0,"state":{"waiting":{"reason":"ImagePullBackOff","message":"Back-off pulling image"}}}]}},
{"metadata":{"name":"crash-1","namespace":"cdtest","creationTimestamp":"2026-11-17T10:00:00Z"},
 "spec":{"nodeName":"n1","containers":[{"name":"app","image":"busybox:1.36","resources":{"requests":{"cpu":"10m","memory":"16Mi"},"limits":{"memory":"32Mi"}},"readinessProbe":{}}]},
 "status":{"phase":"Running","containerStatuses":[{"name":"app","ready":false,"restartCount":7,"state":{"waiting":{"reason":"CrashLoopBackOff"}},"lastState":{"terminated":{"reason":"Error","exitCode":1}}}]}},
{"metadata":{"name":"flappy-1","namespace":"cdtest","creationTimestamp":"2026-11-17T10:00:00Z"},
 "spec":{"nodeName":"n1","containers":[{"name":"app","image":"busybox@sha256:abc","resources":{"requests":{"cpu":"10m","memory":"16Mi"},"limits":{"memory":"100Mi"}},"livenessProbe":{},"readinessProbe":{}}]},
 "status":{"phase":"Running","containerStatuses":[{"name":"app","ready":true,"restartCount":6,"state":{"running":{}}}]}},
{"metadata":{"name":"bad-limits","namespace":"cdtest","creationTimestamp":"2026-11-17T10:00:00Z"},
 "spec":{"nodeName":"n1","containers":[{"name":"app","image":"busybox:1.36","resources":{"requests":{"cpu":"500m","memory":"256Mi"},"limits":{"cpu":"100m","memory":"128Mi"}},"livenessProbe":{},"readinessProbe":{}}]},
 "status":{"phase":"Running","containerStatuses":[{"name":"app","ready":true,"restartCount":0,"state":{"running":{}}}]}}
]}`

func TestParsePodsResources(t *testing.T) {
	pods, err := parsePods([]byte(podsJSON))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Pod{}
	for _, p := range pods {
		byName[p.Name] = p
	}
	b := byName["bare-5d8f-abcde"]
	if b.Workload != "Deployment/bare" || b.Owner != "ReplicaSet/bare-5d8f" || b.Node != "n1" {
		t.Errorf("bare: %+v", b)
	}
	o := byName["oom-1"].Containers[0]
	if o.MemLim != 16<<20 || o.CPUReq != 50 || o.CPULim != 200 || !o.Liveness || !o.Readiness || o.LastReason != "OOMKilled" || o.LastExit != 137 {
		t.Errorf("oom container: %+v", o)
	}
	if g := byName["big-1"]; g.CPUReq != 64000 || !strings.Contains(g.SchedMsg, "Insufficient cpu") {
		t.Errorf("big: %+v", g)
	}
	if c := byName["crash-1"].Containers[0]; c.Liveness || !c.Readiness {
		t.Errorf("crash probes: %+v", c)
	}
}

func TestCheckIssues(t *testing.T) {
	pods, err := parsePods([]byte(podsJSON))
	if err != nil {
		t.Fatal(err)
	}
	addUsage(pods, parseTopPods("cdtest flappy-1 app 5m 95Mi\ncdtest bare-5d8f-abcde web 1m 3Mi\nbad line\n"))
	nodes := []Node{{Name: "n1", Ready: true, CPUAlloc: 4000, MemAlloc: 8 << 30}}
	sumNodes(nodes, pods, nil)
	issues := checkIssues(pods, nodes, true)
	got := map[string][]K8sIssue{}
	for _, is := range issues {
		got[is.Check] = append(got[is.Check], is)
		if is.Title == "" || is.Detail == "" || is.Fix == "" {
			t.Errorf("issue without words: %+v", is)
		}
	}
	want := map[string]string{ // check -> pod it is reported on
		"no-resources":        "bare-5d8f-abcde",
		"latest-tag":          "bare-5d8f-abcde",
		"no-probes":           "bare-5d8f-abcde",
		"oom-killed":          "oom-1",
		"unschedulable":       "big-1",
		"image-pull":          "pull-1",
		"crashloop":           "crash-1",
		"no-liveness":         "crash-1",
		"restarts":            "flappy-1",
		"memory-near-limit":   "flappy-1",
		"limit-below-request": "bad-limits",
	}
	for check, pod := range want {
		if len(got[check]) == 0 || got[check][0].Pod != pod {
			t.Errorf("%s: got %+v, want it on %s", check, got[check], pod)
		}
	}
	// Spec problems once per workload, not per replica.
	if n := len(got["no-resources"]); n != 1 {
		t.Errorf("no-resources reported %d times for one Deployment", n)
	}
	if n := len(got["limit-below-request"]); n != 2 {
		t.Errorf("limit-below-request: %d, want cpu and memory", n)
	}
	// OOM covers the crash loop it caused; a digest is not :latest.
	for _, is := range got["crashloop"] {
		if is.Pod == "oom-1" {
			t.Error("crashloop reported on top of OOMKilled")
		}
	}
	for _, is := range got["latest-tag"] {
		if is.Pod == "flappy-1" {
			t.Error("digest reported as latest")
		}
	}
	if u := got["unschedulable"][0]; u.Severity != "fail" || !strings.Contains(u.Detail, "64 CPU") || !strings.Contains(u.Detail, "4 CPU") {
		t.Errorf("unschedulable detail: %s", u.Detail)
	}
	if issues[0].Severity != "fail" || issues[len(issues)-1].Severity != "info" {
		t.Errorf("not sorted by severity: %s ... %s", issues[0].Severity, issues[len(issues)-1].Severity)
	}
	// Restarts that all happened before the VM last started are the VM's, not the app's.
	vm := []Pod{{Namespace: "kube-system", Name: "coredns", Node: "n1", Phase: "Running", Containers: []PodContainer{{Name: "c", Image: "c:1", Restarts: 11, LastAt: 1000, Liveness: true, Readiness: true, CPUReq: 1, MemReq: 1, MemLim: 1}}}}
	for _, is := range checkIssues(vm, []Node{{Name: "n1", Ready: true, UpSince: 2000}}, false) {
		if is.Check == "restarts" {
			t.Error("restarts from VM restarts reported")
		}
	}
	vm[0].Containers[0].LastAt = 5000
	if is := checkIssues(vm, []Node{{Name: "n1", Ready: true, UpSince: 2000}}, false); len(is) != 1 || is[0].Check != "restarts" {
		t.Errorf("restarts after the VM started: %+v", is)
	}
	// A big node with modest pods is fine; an overbooked one is reported.
	if len(nodeIssues(Node{Name: "n", Ready: true, CPUAlloc: 4000, MemAlloc: 1 << 30, CPUReq: 100, MemReq: 1 << 20})) != 0 {
		t.Error("a quiet node has issues")
	}
	ni := nodeIssues(Node{Name: "n", Ready: false, CPUAlloc: 1000, MemAlloc: 1 << 30, CPULim: 2000, MemLim: 2 << 30, MemReq: 1000 << 20})
	checks := []string{}
	for _, is := range ni {
		checks = append(checks, is.Check)
	}
	if strings.Join(checks, ",") != "node-not-ready,cpu-overcommitted,memory-overcommitted,node-nearly-full" {
		t.Errorf("node issues: %v", checks)
	}
}

func TestNodesTopEvents(t *testing.T) {
	nodes, err := parseNodes([]byte(`{"items":[{"metadata":{"name":"colima-bench"},"status":{"capacity":{"cpu":"4","memory":"8131260Ki","pods":"110"},
"allocatable":{"cpu":"4","memory":"8131260Ki","pods":"110"},"conditions":[{"type":"Ready","status":"True"}],"nodeInfo":{"kubeletVersion":"v1.35.0+k3s1","osImage":"Ubuntu"}}}]}`))
	if err != nil || len(nodes) != 1 {
		t.Fatal(err, nodes)
	}
	pods := []Pod{{Node: "colima-bench", Phase: "Running", CPUReq: 100, MemReq: 64 << 20, CPULim: 500, MemLim: 128 << 20}, {Node: "colima-bench", Phase: "Succeeded", CPUReq: 999}}
	sumNodes(nodes, pods, parseTopNodes("colima-bench   120m   3%   900Mi   11%\n"))
	n := nodes[0]
	if !n.Ready || n.CPUAlloc != 4000 || n.MemAlloc != 8131260*1024 || n.PodsCap != 110 || n.Pods != 1 || n.CPUReq != 100 || n.CPUUse != 120 || n.MemUse != 900<<20 || !n.HasUsage {
		t.Errorf("node: %+v", n)
	}
	evs, err := parseEvents([]byte(`{"items":[
{"metadata":{"namespace":"cdtest","creationTimestamp":"2026-11-17T10:00:00Z"},"involvedObject":{"kind":"Pod","name":"a"},"type":"Warning","reason":"BackOff","message":"Back-off restarting","count":5,"lastTimestamp":"2026-11-17T10:05:00Z"},
{"metadata":{"namespace":"cdtest","creationTimestamp":"2026-11-17T10:00:00Z"},"involvedObject":{"kind":"Pod","name":"b"},"type":"Warning","reason":"FailedScheduling","message":"0/1 nodes","eventTime":"2026-11-17T10:09:00.123456Z","lastTimestamp":null,"series":{"count":3,"lastObservedTime":"2026-11-17T10:10:00.000000Z"}}]}`))
	if err != nil || len(evs) != 2 {
		t.Fatal(err, evs)
	}
	if evs[0].Name != "b" || evs[0].Count != 3 || evs[1].Count != 5 || evs[0].Last <= evs[1].Last {
		t.Errorf("events: %+v", evs)
	}
}

func TestStripManagedFields(t *testing.T) {
	in := "apiVersion: v1\nkind: Pod\nmetadata:\n  labels:\n    app: x\n  managedFields:\n  - apiVersion: v1\n    fieldsType: FieldsV1\n    fieldsV1:\n      f:metadata: {}\n  - manager: kubectl\n  name: x\n  namespace: d\nspec:\n  containers: []\n"
	want := "apiVersion: v1\nkind: Pod\nmetadata:\n  labels:\n    app: x\n  name: x\n  namespace: d\nspec:\n  containers: []\n"
	if got := stripManagedFields(in); got != want {
		t.Errorf("got:\n%s", got)
	}
}

func TestResourcesPatch(t *testing.T) {
	b, err := resourcesPatch([]ResourceEdit{{Name: "web", CPUReq: "100m", CPULim: "0.5", MemReq: "128Mi", MemLim: ""}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"spec":{"template":{"spec":{"containers":[{"name":"web","resources":{"limits":{"cpu":"0.5","memory":null},"requests":{"cpu":"100m","memory":"128Mi"}}}]}}}}`
	if string(b) != want {
		t.Errorf("patch:\n%s\nwant\n%s", b, want)
	}
	for _, bad := range [][]ResourceEdit{
		nil,
		{{Name: "Web", CPUReq: "100m"}},
		{{Name: "web", CPUReq: "100mm"}},
		{{Name: "web", MemReq: "128MB"}},
		{{Name: "web", MemReq: "1Gi; rm -rf /"}},
		{{Name: "web", CPUReq: "500m", CPULim: "250m"}},
		{{Name: "web", MemReq: "1Gi", MemLim: "512Mi"}},
	} {
		if _, err := resourcesPatch(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestParseWorkloadsAndNetwork(t *testing.T) {
	wls, err := parseWorkloads([]byte(`{"items":[
{"kind":"Deployment","metadata":{"name":"web","namespace":"cdtest","creationTimestamp":"2026-11-17T10:00:00Z","annotations":{"deployment.kubernetes.io/revision":"3"}},
 "spec":{"replicas":2,"template":{"spec":{"containers":[{"name":"web","image":"nginx:1.27","resources":{"requests":{"cpu":"50m"}}}]}}},
 "status":{"readyReplicas":1,"updatedReplicas":2,"availableReplicas":1,"conditions":[{"type":"Progressing","status":"False","message":"exceeded its progress deadline"}]}},
{"kind":"DaemonSet","metadata":{"name":"agent","namespace":"cdtest","creationTimestamp":"2026-11-17T10:00:00Z"},
 "spec":{"template":{"spec":{"containers":[{"name":"a","image":"busybox:1.36"}]}}},"status":{"desiredNumberScheduled":1,"numberReady":1,"updatedNumberScheduled":1,"numberAvailable":1}}]}`))
	if err != nil || len(wls) != 2 {
		t.Fatal(err, wls)
	}
	if d := wls[1]; d.Kind != "Deployment" || d.Desired != 2 || d.Ready != 1 || d.Revision != "3" || d.Message == "" || d.Containers[0].Requests["cpu"] != "50m" || d.Containers[0].Limits == nil {
		t.Errorf("deployment: %+v", d)
	}
	if ds := wls[0]; ds.Kind != "DaemonSet" || ds.Desired != 1 || ds.Ready != 1 {
		t.Errorf("daemonset: %+v", ds)
	}
	svcs, ings, err := parseNetwork([]byte(`{"items":[
{"kind":"Service","metadata":{"name":"web","namespace":"cdtest"},"spec":{"type":"LoadBalancer","clusterIP":"10.43.0.9","ports":[{"port":80,"protocol":"TCP","targetPort":8080}],"selector":{"app":"web"}},"status":{"loadBalancer":{"ingress":[{"ip":"192.168.5.15"}]}}},
{"kind":"Ingress","metadata":{"name":"web","namespace":"cdtest"},"spec":{"ingressClassName":"traefik","tls":[{}],"rules":[{"host":"web.localhost","http":{"paths":[{"path":"/","backend":{"service":{"name":"web","port":{"number":80}}}}]}}]}}]}`))
	if err != nil || len(svcs) != 1 || len(ings) != 1 {
		t.Fatal(err)
	}
	if s := svcs[0]; s.Type != "LoadBalancer" || s.ClusterIP != "10.43.0.9" || len(s.External) != 1 || s.Ports[0].Port != 80 {
		t.Errorf("service: %+v", s)
	}
	if i := ings[0]; !i.TLS || i.Class != "traefik" || len(i.Rules) != 1 || i.Rules[0].Host != "web.localhost" || i.Rules[0].Service != "web" || i.Rules[0].Port != "80" {
		t.Errorf("ingress: %+v", i)
	}
}

func TestCleanKubeMsg(t *testing.T) {
	in := "bash: line 1: cd: /tmp/x: No such file or directory\nThe request is invalid: patch: Invalid value: \"map[" + strings.Repeat("x", 900) + "]\": strict decoding error: unknown field \"spec.bogus\"\ntime=\"2026-11-18T09:38:29+02:00\" level=fatal msg=\"exit status 1\""
	if got := cleanKubeMsg(in); got != `The YAML has a field Kubernetes does not know: unknown field "spec.bogus"` {
		t.Errorf("got %q", got)
	}
	if got := cleanKubeMsg(`Error from server (NotFound): deployments.apps "x" not found`); got != `Error from server (NotFound): deployments.apps "x" not found` {
		t.Errorf("plain: %q", got)
	}
}

func TestImageTag(t *testing.T) {
	cases := map[string]string{"nginx": "", "nginx:1.27": "1.27", "localhost:5000/app": "", "localhost:5000/app:v2": "v2", "nginx:latest": "latest"}
	for in, want := range cases {
		if got, _ := imageTag(in); got != want {
			t.Errorf("imageTag(%q) = %q", in, got)
		}
	}
	if _, d := imageTag("nginx@sha256:abc"); !d {
		t.Error("digest not seen")
	}
}

func TestConsoleRoutesValidate(t *testing.T) {
	s := newTest(t)
	auth := func(body string) func(*http.Request) {
		return func(r *http.Request) {
			r.Header.Set("Origin", "http://127.0.0.1:7766")
			withCookie(r)
			if body != "" {
				r.Body = io.NopCloser(strings.NewReader(body))
				r.ContentLength = int64(len(body))
				r.Header.Set("Content-Type", "application/json")
			}
		}
	}
	cases := []struct{ method, path, body string }{
		{"POST", "/api/profiles/default/kubernetes/workloads/pod/cdtest/web/scale", `{"replicas":1}`},
		{"POST", "/api/profiles/default/kubernetes/workloads/deployment/Bad_NS/web/restart", ""},
		{"POST", "/api/profiles/default/kubernetes/workloads/deployment/cdtest/web/scale", `{"replicas":500}`},
		{"POST", "/api/profiles/default/kubernetes/workloads/daemonset/cdtest/web/scale", `{"replicas":2}`},
		{"POST", "/api/profiles/default/kubernetes/workloads/deployment/cdtest/web/resources", `{"containers":[{"name":"web","cpuReq":"$(id)"}]}`},
		{"GET", "/api/profiles/default/kubernetes/yaml?kind=secret&ns=cdtest&name=x", ""},
		{"GET", "/api/profiles/default/kubernetes/yaml?kind=pod&ns=cdtest&name=--all", ""},
		{"GET", "/api/profiles/default/kubernetes/events?ns=cdtest&name=a,b", ""},
		{"GET", "/api/profiles/default/kubernetes/events?ns=cdtest&kind=Pod%2Cx", ""},
		{"POST", "/api/profiles/default/kubernetes/pods/cdtest/web/shell", `{"container":"--privileged"}`},
		{"POST", "/api/profiles/default/kubernetes/forwards", `{"kind":"node","namespace":"cdtest","name":"x","remote":80,"local":8080}`},
		{"POST", "/api/profiles/default/kubernetes/forwards", `{"kind":"pod","namespace":"cdtest","name":"x","remote":80,"local":80}`},
		{"POST", "/api/profiles/default/kubernetes/apply", `{"yaml":"  "}`},
	}
	for _, c := range cases {
		w := do(s, c.method, c.path, auth(c.body))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s %s: got %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
	var e map[string]string
	w := do(s, "POST", "/api/profiles/default/kubernetes/workloads/deployment/cdtest/web/nuke", auth(""))
	if w.Code != http.StatusNotFound || json.Unmarshal(w.Body.Bytes(), &e) != nil {
		t.Errorf("unknown action: %d", w.Code)
	}
}
