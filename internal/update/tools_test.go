package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckTools(t *testing.T) {
	prefix := t.TempDir()
	for _, d := range []string{"docker/29.9.3", "docker/29.10.0_1", "lima/2.3.0", "colima/0.10.3"} {
		if err := os.MkdirAll(filepath.Join(prefix, "Cellar", d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		latest := map[string]string{"/docker.json": "29.11.0", "/lima.json": "2.3.0", "/colima.json": "0.10.3"}[r.URL.Path]
		if latest == "" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"versions":{"stable":"` + latest + `"}}`))
	}))
	defer srv.Close()
	u := &Updater{BrewPrefix: prefix, FormulaAPI: srv.URL, HTTP: srv.Client()}
	tools, err := u.checkTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Tool{}
	for _, x := range tools {
		got[x.Formula] = x
	}
	if len(got) != 3 {
		t.Fatalf("want colima, lima and docker only, got %+v", tools)
	}
	if d := got["docker"]; d.Current != "29.10.0" || d.Latest != "29.11.0" || !d.Newer {
		t.Errorf("docker: %+v", d)
	}
	if l := got["lima"]; l.Newer {
		t.Errorf("lima is current: %+v", l)
	}
	if tools[0].Formula != "colima" {
		t.Errorf("order should follow KnownTools, got %s first", tools[0].Formula)
	}
}

func TestKnownFormula(t *testing.T) {
	if !KnownFormula("docker-compose") || KnownFormula("wget") || KnownFormula("docker; rm -rf /") {
		t.Fatal("KnownFormula accepts only the listed tools")
	}
}

func TestNoBrewNoTools(t *testing.T) {
	tools, err := (&Updater{}).checkTools(context.Background())
	if err != nil || tools != nil {
		t.Fatalf("got %v %v", tools, err)
	}
}
