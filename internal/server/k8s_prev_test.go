package server

import "testing"

func TestPreviousGone(t *testing.T) {
	for in, want := range map[string]bool{
		"": true,
		"[pod/crashy-1/crashy] unable to retrieve container logs for docker://ad4d": true,
		"unable to retrieve container logs for docker://ad4d\n":                     true,
		"[pod/x/c] starting\n[pod/x/c] ERROR boom":                                  false,
	} {
		if got := previousGone(in); got != want {
			t.Errorf("previousGone(%q) = %v, want %v", in, got, want)
		}
	}
}
