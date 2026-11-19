package vuln

import "sort"

// Component is one package in the image (an OS package or an app dependency) with its own
// cyber risk grade, worked out from the vulnerabilities found in it.
type Component struct {
	Name      string         `json:"name"`
	Version   string         `json:"version"`
	Kind      string         `json:"kind"`   // os or app
	Target    string         `json:"target"` // where it was found
	Counts    map[string]int `json:"counts"`
	Fixable   int            `json:"fixable"`
	Total     int            `json:"total"`
	Grade     string         `json:"grade"`
	UpgradeTo string         `json:"upgradeTo,omitempty"` // the newest first-fixed version, which fixes all that can be
}

// componentGrade: F a critical with a fix; D a critical with none; C a high with a fix;
// B a high with none or any medium; A only low or unknown.
func componentGrade(c *Component, fixable map[string]int) string {
	switch {
	case fixable["CRITICAL"] > 0:
		return "F"
	case c.Counts["CRITICAL"] > 0:
		return "D"
	case fixable["HIGH"] > 0:
		return "C"
	case c.Counts["HIGH"] > 0 || c.Counts["MEDIUM"] > 0:
		return "B"
	}
	return "A"
}

// Components groups the report's vulnerabilities by package, worst first. Reports saved
// before components existed get them worked out here from their list.
func (r *Report) EnsureComponents() {
	if r.Components != nil || len(r.Vulns) == 0 {
		return
	}
	type acc struct {
		c   *Component
		fix map[string]int
	}
	byKey := map[string]*acc{}
	var order []string
	for _, v := range r.Vulns {
		k := v.Pkg + "|" + v.Installed + "|" + v.Target
		a := byKey[k]
		if a == nil {
			a = &acc{c: &Component{Name: v.Pkg, Version: v.Installed, Kind: v.Kind, Target: v.Target, Counts: map[string]int{}}, fix: map[string]int{}}
			byKey[k] = a
			order = append(order, k)
		}
		a.c.Counts[v.Severity]++
		a.c.Total++
		if v.Fixed != "" {
			a.c.Fixable++
			a.fix[v.Severity]++
			a.c.UpgradeTo = maxVersion(a.c.UpgradeTo, firstVersion(v.Fixed))
		}
	}
	out := make([]Component, 0, len(order))
	for _, k := range order {
		a := byKey[k]
		a.c.Grade = componentGrade(a.c, a.fix)
		out = append(out, *a.c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Grade != out[j].Grade {
			return out[i].Grade > out[j].Grade // F before D before C …
		}
		if out[i].Fixable != out[j].Fixable {
			return out[i].Fixable > out[j].Fixable
		}
		return out[i].Total > out[j].Total
	})
	r.Components = out
}

// componentCounts is how many components got each grade.
func (r *Report) componentCounts() map[string]int {
	r.EnsureComponents()
	m := map[string]int{}
	for _, c := range r.Components {
		m[c.Grade]++
	}
	return m
}
