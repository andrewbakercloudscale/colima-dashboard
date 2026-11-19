// Package vuln reads Trivy's JSON report for an image and turns it into what the Security page
// shows: counts by severity, how many have a fix, a grade, and what to do about them.
package vuln

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Severities, worst first.
var Severities = []string{"CRITICAL", "HIGH", "MEDIUM", "LOW", "UNKNOWN"}

func sevRank(s string) int {
	for i, x := range Severities {
		if x == s {
			return i
		}
	}
	return len(Severities) - 1
}

// Vuln is one finding: a CVE (or GHSA, …) in one package.
type Vuln struct {
	ID        string `json:"id"`
	Severity  string `json:"severity"`
	Pkg       string `json:"pkg"`
	Installed string `json:"installed"`
	Fixed     string `json:"fixed,omitempty"`
	Status    string `json:"status,omitempty"` // fixed, affected, will_not_fix, fix_deferred…
	Title     string `json:"title,omitempty"`
	URL       string `json:"url,omitempty"`
	Target    string `json:"target"` // "debian 12.5" for OS packages, else the file it was found in
	Kind      string `json:"kind"`   // os or app
	Published string `json:"published,omitempty"`
}

// Advice is one thing to do, worst first.
type Advice struct {
	Severity string `json:"severity"` // high, medium, low
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix,omitempty"` // a Dockerfile line or command
}

// Report is one image's scan.
type Report struct {
	OS      string         `json:"os,omitempty"` // "alpine 3.18.4"
	EOL     bool           `json:"eol"`          // the OS release gets no more security fixes
	Counts  map[string]int `json:"counts"`       // by severity
	Fixable map[string]int `json:"fixable"`      // by severity, those with a fixed version
	Total   int            `json:"total"`
	Grade   string         `json:"grade"`
	Advice  []Advice       `json:"advice"`
	Vulns   []Vuln         `json:"vulns"`
	// Components are the packages with findings, each with its own grade, worst first.
	Components []Component `json:"components,omitempty"`
}

type trivyJSON struct {
	Metadata struct {
		OS *struct {
			Family string
			Name   string
			EOSL   bool
		}
	}
	Results []struct {
		Target          string
		Class           string
		Type            string
		Vulnerabilities []struct {
			VulnerabilityID  string
			PkgName          string
			InstalledVersion string
			FixedVersion     string
			Status           string
			Severity         string
			Title            string
			PrimaryURL       string
			PublishedDate    string
		}
	}
}

// Parse reads `trivy image --format json` output.
func Parse(data []byte) (*Report, error) {
	var t trivyJSON
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("could not read Trivy's report: %w", err)
	}
	rep := &Report{Counts: map[string]int{}, Fixable: map[string]int{}}
	if t.Metadata.OS != nil {
		rep.OS = strings.TrimSpace(t.Metadata.OS.Family + " " + t.Metadata.OS.Name)
		rep.EOL = t.Metadata.OS.EOSL
	}
	seen := map[string]bool{}
	for _, r := range t.Results {
		kind := "app"
		if r.Class == "os-pkgs" {
			kind = "os"
		}
		for _, v := range r.Vulnerabilities {
			key := v.VulnerabilityID + "|" + v.PkgName + "|" + v.InstalledVersion + "|" + r.Target
			if seen[key] {
				continue
			}
			seen[key] = true
			sev := strings.ToUpper(v.Severity)
			if sevRank(sev) == len(Severities)-1 {
				sev = "UNKNOWN"
			}
			rep.Vulns = append(rep.Vulns, Vuln{
				ID: v.VulnerabilityID, Severity: sev, Pkg: v.PkgName, Installed: v.InstalledVersion,
				Fixed: v.FixedVersion, Status: v.Status, Title: v.Title, URL: safeURL(v.PrimaryURL),
				Target: r.Target, Kind: kind, Published: v.PublishedDate,
			})
			rep.Counts[sev]++
			if v.FixedVersion != "" {
				rep.Fixable[sev]++
			}
		}
	}
	rep.Total = len(rep.Vulns)
	sort.SliceStable(rep.Vulns, func(i, j int) bool {
		a, b := rep.Vulns[i], rep.Vulns[j]
		if sevRank(a.Severity) != sevRank(b.Severity) {
			return sevRank(a.Severity) < sevRank(b.Severity)
		}
		if (a.Fixed != "") != (b.Fixed != "") {
			return a.Fixed != ""
		}
		return a.ID > b.ID // newer IDs first
	})
	rep.Grade = grade(rep)
	rep.Advice = advise(rep)
	rep.EnsureComponents()
	return rep, nil
}

// safeURL keeps only http(s) links, so the page never gets a javascript: one.
func safeURL(u string) string {
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		return u
	}
	return ""
}

// grade: A nothing above medium; B some medium or high with no fix; C high with a fix;
// D critical with no fix or many high; F critical with a fix available (fix it now) or an
// end-of-life OS.
func grade(r *Report) string {
	switch {
	case r.Fixable["CRITICAL"] > 0 || r.EOL:
		return "F"
	case r.Counts["CRITICAL"] > 0 || r.Fixable["HIGH"] >= 10:
		return "D"
	case r.Fixable["HIGH"] > 0:
		return "C"
	case r.Counts["HIGH"] > 0 || r.Counts["MEDIUM"] > 0:
		return "B"
	}
	return "A"
}

func advise(r *Report) []Advice {
	var out []Advice
	if r.EOL {
		out = append(out, Advice{Severity: "high", Title: fmt.Sprintf("%s no longer gets security fixes", r.OS),
			Detail: "This release of the base image's OS is past its end of life, so new holes are never patched. Move to a supported release of the base image.",
			Fix:    "FROM <same image, current release>"})
	}
	osFix, appFix := 0, 0
	osSev, appSev := "LOW", "LOW"
	pkgs := map[string]*pkgFix{}
	for _, v := range r.Vulns {
		if v.Fixed == "" {
			continue
		}
		if v.Kind == "os" {
			osFix++
			if sevRank(v.Severity) < sevRank(osSev) {
				osSev = v.Severity
			}
		} else {
			appFix++
			if sevRank(v.Severity) < sevRank(appSev) {
				appSev = v.Severity
			}
			k := v.Pkg + "|" + v.Target
			p := pkgs[k]
			if p == nil {
				p = &pkgFix{pkg: v.Pkg, target: v.Target, from: v.Installed, worst: v.Severity}
				pkgs[k] = p
			}
			p.n++
			p.to = maxVersion(p.to, firstVersion(v.Fixed))
			if sevRank(v.Severity) < sevRank(p.worst) {
				p.worst = v.Severity
			}
		}
	}
	if osFix > 0 {
		cmd := "RUN apt-get update && apt-get upgrade -y && rm -rf /var/lib/apt/lists/*"
		fam := strings.Fields(r.OS)
		if len(fam) > 0 {
			switch fam[0] {
			case "alpine":
				cmd = "RUN apk upgrade --no-cache"
			case "redhat", "centos", "rocky", "alma", "amazon", "oracle", "fedora":
				cmd = "RUN dnf upgrade -y && dnf clean all"
			}
		}
		out = append(out, Advice{Severity: level(osSev),
			Title:  fmt.Sprintf("%d OS package %s a fix", osFix, plural(osFix, "issue has", "issues have")),
			Detail: "Rebuild on the latest tag of the base image (docker build --pull) so you get the patched packages. If the base image itself has not been updated yet, upgrade the packages in the Dockerfile.",
			Fix:    cmd})
	}
	if appFix > 0 {
		list := make([]*pkgFix, 0, len(pkgs))
		for _, p := range pkgs {
			list = append(list, p)
		}
		sort.Slice(list, func(i, j int) bool {
			if sevRank(list[i].worst) != sevRank(list[j].worst) {
				return sevRank(list[i].worst) < sevRank(list[j].worst)
			}
			return list[i].n > list[j].n
		})
		var lines []string
		for i, p := range list {
			if i == 6 {
				lines = append(lines, fmt.Sprintf("… and %d more", len(list)-6))
				break
			}
			lines = append(lines, fmt.Sprintf("%s %s → %s  (%s)", p.pkg, p.from, p.to, p.target))
		}
		out = append(out, Advice{Severity: level(appSev),
			Title:  fmt.Sprintf("%d app %s a fix in a newer version", appFix, plural(appFix, "dependency issue has", "dependency issues have")),
			Detail: "These come from your application's own dependencies or a binary copied into the image. Bump them in your lock file (or update the tool), then rebuild.",
			Fix:    strings.Join(lines, "\n")})
	}
	unfixed := r.Total - sum(r.Fixable)
	if unfixed > 0 {
		out = append(out, Advice{Severity: "low",
			Title:  fmt.Sprintf("%d %s no fix yet", unfixed, plural(unfixed, "issue has", "issues have")),
			Detail: "Nothing to upgrade to yet. A smaller base (alpine, distroless or a slim tag) usually carries far fewer of these, because it ships fewer packages."})
	}
	sort.SliceStable(out, func(i, j int) bool { return advRank(out[i].Severity) < advRank(out[j].Severity) })
	return out
}

type pkgFix struct {
	pkg, target, from, to, worst string
	n                            int
}

func level(sev string) string {
	switch sev {
	case "CRITICAL", "HIGH":
		return "high"
	case "MEDIUM":
		return "medium"
	}
	return "low"
}

func advRank(s string) int {
	switch s {
	case "high":
		return 0
	case "medium":
		return 1
	}
	return 2
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// firstVersion takes the first of Trivy's "1.2.3, 1.3.1" fixed versions.
func firstVersion(s string) string {
	return strings.TrimSpace(strings.Split(s, ",")[0])
}

// maxVersion compares dotted versions number by number; good enough to pick the newest fix.
func maxVersion(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	pa, pb := strings.FieldsFunc(a, sep), strings.FieldsFunc(b, sep)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		var x, y int
		_, ex := fmt.Sscan(pa[i], &x)
		_, ey := fmt.Sscan(pb[i], &y)
		if ex != nil || ey != nil {
			if pa[i] != pb[i] {
				if pa[i] > pb[i] {
					return a
				}
				return b
			}
			continue
		}
		if x != y {
			if x > y {
				return a
			}
			return b
		}
	}
	if len(pb) > len(pa) {
		return b
	}
	return a
}

func sep(r rune) bool { return r == '.' || r == '-' || r == '+' || r == 'v' }

// Summary is a Report without the list, for the overview.
type Summary struct {
	OS      string         `json:"os,omitempty"`
	EOL     bool           `json:"eol"`
	Counts  map[string]int `json:"counts"`
	Fixable map[string]int `json:"fixable"`
	Total   int            `json:"total"`
	Grade   string         `json:"grade"`
	Top     string         `json:"top,omitempty"` // the first piece of advice
	// Components: how many packages have findings, how many got each grade, and the worst few.
	Components      int            `json:"components"`
	ComponentGrades map[string]int `json:"componentGrades,omitempty"`
	WorstComponents []Component    `json:"worstComponents,omitempty"`
}

// Summarise drops the list.
func (r *Report) Summarise() Summary {
	s := Summary{OS: r.OS, EOL: r.EOL, Counts: r.Counts, Fixable: r.Fixable, Total: r.Total, Grade: r.Grade}
	if len(r.Advice) > 0 {
		s.Top = r.Advice[0].Title
	}
	s.ComponentGrades = r.componentCounts()
	s.Components = len(r.Components)
	for _, c := range r.Components {
		if len(s.WorstComponents) == 3 || c.Grade == "A" {
			break
		}
		c.Counts = nil // the summary needs the name, version and grade only
		s.WorstComponents = append(s.WorstComponents, c)
	}
	return s
}
