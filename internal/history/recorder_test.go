package history

import "testing"

func TestParseBuildError(t *testing.T) {
	got := parseBuildError([]byte(`{"Error":{"Code":2,"Message":"exit code: 4","Name":"[2/2] RUN exit 4","Stack":"x"}}`))
	if got != "[2/2] RUN exit 4: exit code: 4" {
		t.Fatalf("got %q", got)
	}
	if parseBuildError([]byte(`{"Status":"Completed"}`)) != "" {
		t.Fatal("no error expected")
	}
}

func TestContextName(t *testing.T) {
	for in, want := range map[string]string{"default": "colima", "": "colima", "bench": "colima-bench"} {
		if got := contextName(in); got != want {
			t.Errorf("contextName(%q) = %q, want %q", in, got, want)
		}
	}
}
