package server

import "testing"

// The Rancher cleanup left a context whose user entry was gone; the old check only compared
// current-context and passed.
func TestKubeGapsFrom(t *testing.T) {
	for in, want := range map[string]string{
		"colima-bench|colima-bench|colima-bench": "",
		"colima-bench|colima-bench|":             "user",
		"colima-bench||":                         "cluster and user",
		"":                                       "context and cluster and user",
	} {
		if got := kubeGapsFrom(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}
