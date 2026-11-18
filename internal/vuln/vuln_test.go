package vuln

import (
	"strings"
	"testing"
)

const sample = `{
 "Metadata": {"OS": {"Family": "alpine", "Name": "3.18.4", "EOSL": false}},
 "Results": [
  {"Target": "x (alpine 3.18.4)", "Class": "os-pkgs", "Type": "alpine", "Vulnerabilities": [
    {"VulnerabilityID": "CVE-2026-1", "PkgName": "openssl", "InstalledVersion": "3.1.0", "FixedVersion": "3.1.4", "Severity": "CRITICAL", "Title": "bad", "PrimaryURL": "https://avd.aquasec.com/nvd/cve-2026-1"},
    {"VulnerabilityID": "CVE-2026-1", "PkgName": "openssl", "InstalledVersion": "3.1.0", "FixedVersion": "3.1.4", "Severity": "CRITICAL"},
    {"VulnerabilityID": "CVE-2026-2", "PkgName": "busybox", "InstalledVersion": "1.36", "Severity": "LOW", "PrimaryURL": "javascript:alert(1)"}
  ]},
  {"Target": "app/package-lock.json", "Class": "lang-pkgs", "Type": "npm", "Vulnerabilities": [
    {"VulnerabilityID": "GHSA-1", "PkgName": "lodash", "InstalledVersion": "4.17.10", "FixedVersion": "4.17.12, 4.17.21", "Severity": "HIGH"},
    {"VulnerabilityID": "GHSA-2", "PkgName": "lodash", "InstalledVersion": "4.17.10", "FixedVersion": "4.17.20", "Severity": "MEDIUM"}
  ]},
  {"Target": "empty", "Class": "lang-pkgs", "Vulnerabilities": null}
 ]
}`

func TestParse(t *testing.T) {
	r, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 4 {
		t.Fatalf("total %d, want 4 (duplicate dropped)", r.Total)
	}
	if r.Counts["CRITICAL"] != 1 || r.Fixable["CRITICAL"] != 1 || r.Counts["LOW"] != 1 || r.Fixable["LOW"] != 0 {
		t.Errorf("counts %v fixable %v", r.Counts, r.Fixable)
	}
	if r.Grade != "F" {
		t.Errorf("grade %s, want F (fixable critical)", r.Grade)
	}
	if r.Vulns[0].ID != "CVE-2026-1" || r.Vulns[len(r.Vulns)-1].Severity != "LOW" {
		t.Errorf("not sorted worst first: %+v", r.Vulns)
	}
	for _, v := range r.Vulns {
		if strings.HasPrefix(v.URL, "javascript") {
			t.Errorf("unsafe url kept: %s", v.URL)
		}
	}
	if r.OS != "alpine 3.18.4" || len(r.Advice) < 3 {
		t.Fatalf("os %q advice %+v", r.OS, r.Advice)
	}
	if !strings.Contains(r.Advice[0].Fix, "apk upgrade") {
		t.Errorf("first advice should upgrade alpine packages: %+v", r.Advice[0])
	}
	var app *Advice
	for i := range r.Advice {
		if strings.Contains(r.Advice[i].Title, "app") {
			app = &r.Advice[i]
		}
	}
	if app == nil || !strings.Contains(app.Fix, "lodash 4.17.10 → 4.17.20") {
		t.Errorf("app advice should pick the newest first fix: %+v", app)
	}
	if s := r.Summarise(); s.Top == "" || s.Total != 4 {
		t.Errorf("summary %+v", s)
	}
}

func TestGrades(t *testing.T) {
	cases := []struct {
		r    Report
		want string
	}{
		{Report{}, "A"},
		{Report{Counts: map[string]int{"MEDIUM": 3}}, "B"},
		{Report{Counts: map[string]int{"HIGH": 1}, Fixable: map[string]int{"HIGH": 1}}, "C"},
		{Report{Counts: map[string]int{"CRITICAL": 1}}, "D"},
		{Report{EOL: true}, "F"},
	}
	for _, c := range cases {
		if c.r.Fixable == nil {
			c.r.Fixable = map[string]int{}
		}
		if c.r.Counts == nil {
			c.r.Counts = map[string]int{}
		}
		if g := grade(&c.r); g != c.want {
			t.Errorf("%+v: got %s want %s", c.r, g, c.want)
		}
	}
}

func TestMaxVersion(t *testing.T) {
	for _, c := range [][3]string{{"1.2.3", "1.10.0", "1.10.0"}, {"", "2", "2"}, {"v2.0.1", "v2.0.0", "v2.0.1"}, {"1.2", "1.2.1", "1.2.1"}} {
		if got := maxVersion(c[0], c[1]); got != c[2] {
			t.Errorf("maxVersion(%s,%s)=%s", c[0], c[1], got)
		}
	}
}

func TestParseBad(t *testing.T) {
	if _, err := Parse([]byte("not json")); err == nil {
		t.Error("want error")
	}
}
