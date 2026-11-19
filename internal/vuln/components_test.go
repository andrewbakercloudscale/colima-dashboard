package vuln

import "testing"

func TestComponents(t *testing.T) {
	r := &Report{Vulns: []Vuln{
		{ID: "CVE-1", Severity: "CRITICAL", Pkg: "openssl", Installed: "3.0.1", Fixed: "3.0.9", Kind: "os", Target: "debian 12"},
		{ID: "CVE-2", Severity: "HIGH", Pkg: "openssl", Installed: "3.0.1", Fixed: "3.0.12, 3.1.4", Kind: "os", Target: "debian 12"},
		{ID: "CVE-3", Severity: "HIGH", Pkg: "zlib", Installed: "1.2", Kind: "os", Target: "debian 12"},
		{ID: "CVE-4", Severity: "LOW", Pkg: "bash", Installed: "5.2", Kind: "os", Target: "debian 12"},
		{ID: "CVE-5", Severity: "CRITICAL", Pkg: "lodash", Installed: "4.17.0", Kind: "app", Target: "app/package-lock.json"},
	}}
	r.EnsureComponents()
	got := map[string]Component{}
	for _, c := range r.Components {
		got[c.Name] = c
	}
	for name, want := range map[string]string{"openssl": "F", "lodash": "D", "zlib": "B", "bash": "A"} {
		if got[name].Grade != want {
			t.Errorf("%s grade %q, want %q", name, got[name].Grade, want)
		}
	}
	if o := got["openssl"]; o.Total != 2 || o.Fixable != 2 || o.UpgradeTo != "3.0.12" {
		t.Errorf("openssl = %+v", o)
	}
	if r.Components[0].Name != "openssl" || r.Components[len(r.Components)-1].Name != "bash" {
		t.Errorf("order: %v", r.Components)
	}
	s := r.Summarise()
	if s.Components != 4 || s.ComponentGrades["F"] != 1 || len(s.WorstComponents) != 3 || s.WorstComponents[0].Name != "openssl" {
		t.Errorf("summary: %+v", s)
	}
}
