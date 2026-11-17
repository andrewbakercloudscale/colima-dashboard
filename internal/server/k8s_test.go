package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestParsePods(t *testing.T) {
	pods, err := parsePods([]byte(`{"items":[
{"metadata":{"name":"web-1","namespace":"shop","creationTimestamp":"2026-11-17T10:00:00Z","ownerReferences":[{"kind":"ReplicaSet","name":"web-7f"}]},
 "spec":{"containers":[{"image":"nginx:1"},{"image":"envoy:2"}]},
 "status":{"phase":"Running","containerStatuses":[{"ready":true,"restartCount":3,"state":{}},{"ready":false,"restartCount":1,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}},
{"metadata":{"name":"a","namespace":"default","creationTimestamp":"2026-11-17T10:00:00Z"},"spec":{"containers":[{"image":"busybox"}]},"status":{"phase":"Succeeded","containerStatuses":[{"ready":false,"restartCount":0,"state":{"terminated":{"reason":"Completed"}}}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) != 2 || pods[0].Name != "a" || pods[0].Reason != "" {
		t.Fatalf("sorted or reason wrong: %+v", pods)
	}
	w := pods[1]
	if w.Ready != "1/2" || w.Restarts != 4 || w.Reason != "CrashLoopBackOff" || w.Owner != "ReplicaSet/web-7f" || len(w.Images) != 2 {
		t.Fatalf("web pod: %+v", w)
	}
}

func TestPickK3sVersions(t *testing.T) {
	var ch struct {
		Data []struct {
			ID     string `json:"id"`
			Latest string `json:"latest"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(`{"data":[{"id":"stable","latest":"v1.37.2+k3s1"},{"id":"testing","latest":"v1.18.2-rc3+k3s1"},
{"id":"v1.36","latest":"v1.36.5+k3s1"},{"id":"v1.37","latest":"v1.37.2+k3s1"},{"id":"v1.30","latest":"v1.30.9+k3s1"},
{"id":"v1.31","latest":"v1.31.9+k3s1"},{"id":"v1.32","latest":"v1.32.9+k3s1"},{"id":"v1.33","latest":"v1.33.9+k3s1"},{"id":"v1.34","latest":"v1.34.9+k3s1"},{"id":"v1.35","latest":"v1.35.9+k3s1"}]}`), &ch)
	got := pickK3sVersions(ch.Data)
	if len(got) != 6 || got[0].Channel != "stable" || got[1].Version != "v1.36.5+k3s1" || got[5].Version != "v1.32.9+k3s1" {
		t.Fatalf("got %+v", got)
	}
}

func TestPodRoutesValidateNames(t *testing.T) {
	s := newTest(t)
	for _, path := range []string{"/api/profiles/default/kubernetes/pods/Bad_NS/x/logs", "/api/profiles/default/kubernetes/pods/ns/-x"} {
		method := "GET"
		if path[len(path)-4:] != "logs" {
			method = "DELETE"
		}
		w := do(s, method, path, func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:7766"); withCookie(r) })
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s %s: got %d", method, path, w.Code)
		}
	}
}
