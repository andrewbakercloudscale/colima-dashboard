package main

import "testing"

func TestStableExe(t *testing.T) {
	cases := map[string]string{
		"/opt/homebrew/Cellar/colima-dashboard/0.1.24/bin/colima-dashboard": "/opt/homebrew/opt/colima-dashboard/bin/colima-dashboard",
		"/usr/local/Cellar/colima-dashboard/0.1.24/bin/colima-dashboard":    "/usr/local/opt/colima-dashboard/bin/colima-dashboard",
		"/opt/homebrew/bin/colima-dashboard":                                "/opt/homebrew/bin/colima-dashboard",
		"/Users/me/colima-dashboard":                                        "/Users/me/colima-dashboard",
	}
	for in, want := range cases {
		if got := stableExe(in); got != want {
			t.Errorf("stableExe(%q) = %q, want %q", in, got, want)
		}
	}
}
