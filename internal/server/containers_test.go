package server

import (
	"testing"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
)

func TestProjectMembers(t *testing.T) {
	lab := func(project, service, num string) map[string]string {
		return map[string]string{"com.docker.compose.project": project, "com.docker.compose.service": service, "com.docker.compose.container-number": num}
	}
	cs := []docker.Container{
		{ID: "a1", Names: []string{"/shop-web-1"}, Labels: lab("shop", "web", "1")},
		{ID: "a2", Names: []string{"/shop-web-2"}, Labels: lab("shop", "web", "2")},
		{ID: "b1", Names: []string{"/shop-db-1"}, Labels: lab("shop", "db", "1")},
		{ID: "c1", Names: []string{"/other-db-1"}, Labels: lab("other", "db", "1")},
		{ID: "d1", Names: []string{"/loose"}},
	}
	got := projectMembers(cs, "shop")
	want := []string{"db", "web-1", "web-2"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i, m := range got {
		if m.Name != want[i] {
			t.Fatalf("got %v", got)
		}
	}
	if len(projectMembers(cs, "nope")) != 0 {
		t.Fatal("unknown project")
	}
}

func TestDetailCache(t *testing.T) {
	var c detailCache
	d := &docker.Details{Memory: 1}
	c.put("default/x", "running|-1", d)
	if c.get("default/x", "running|-1") != d {
		t.Fatal("cached entry missing")
	}
	if c.get("default/x", "exited|137") != nil {
		t.Fatal("a state change must miss")
	}
	c.prune("default", map[string]bool{})
	if c.get("default/x", "running|-1") != nil {
		t.Fatal("pruned entry still there")
	}
}
