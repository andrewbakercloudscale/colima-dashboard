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
