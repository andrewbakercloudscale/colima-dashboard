package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Tool is one Homebrew formula the dashboard depends on or works with: Colima, Lima, the
// Docker CLI and its plugins, kubectl, trivy. Only installed ones are reported.
type Tool struct {
	Formula string `json:"formula"`
	Label   string `json:"label"`
	About   string `json:"about"`
	Current string `json:"current"`
	Latest  string `json:"latest,omitempty"`
	Newer   bool   `json:"newer"`
	URL     string `json:"url,omitempty"` // where its release notes live
}

// KnownTools are the formulae checked, in the order the page lists them.
var KnownTools = []Tool{
	{Formula: "colima", Label: "Colima", About: "runs the Linux VM", URL: "https://github.com/abiosoft/colima/releases"},
	{Formula: "lima", Label: "Lima", About: "the VM layer under Colima", URL: "https://github.com/lima-vm/lima/releases"},
	{Formula: "docker", Label: "Docker CLI", About: "the docker command", URL: "https://docs.docker.com/engine/release-notes/"},
	{Formula: "docker-compose", Label: "Docker Compose", About: "docker compose", URL: "https://github.com/docker/compose/releases"},
	{Formula: "docker-buildx", Label: "Docker Buildx", About: "docker build", URL: "https://github.com/docker/buildx/releases"},
	{Formula: "docker-credential-helper", Label: "Docker credential helper", About: "keeps registry logins in the Keychain", URL: "https://github.com/docker/docker-credential-helpers/releases"},
	{Formula: "kubernetes-cli", Label: "kubectl", About: "Kubernetes from the Mac", URL: "https://kubernetes.io/releases/"},
	{Formula: "trivy", Label: "Trivy", About: "image scanning", URL: "https://github.com/aquasecurity/trivy/releases"},
	{Formula: "qemu", Label: "QEMU", About: "VMs for other CPU types", URL: "https://www.qemu.org/download/"},
}

// KnownFormula reports whether name is one of KnownTools, so only those can be upgraded.
func KnownFormula(name string) bool {
	for _, t := range KnownTools {
		if t.Formula == name {
			return true
		}
	}
	return false
}

// BrewPrefix is Homebrew's prefix (/opt/homebrew or /usr/local) from the brew binary's path.
func BrewPrefix(brew string) string {
	if brew == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(brew); err == nil {
		brew = real
	}
	return filepath.Dir(filepath.Dir(brew))
}

var revision = regexp.MustCompile(`_\d+$`)

// installedVersion reads the newest version folder under Cellar/<formula>, without running brew
// (which is slow). "29.9.3_1" (a Homebrew rebuild) reads as 29.9.3.
func installedVersion(cellar, formula string) string {
	ents, err := os.ReadDir(filepath.Join(cellar, formula))
	if err != nil {
		return ""
	}
	var vs []string
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			vs = append(vs, revision.ReplaceAllString(e.Name(), ""))
		}
	}
	if len(vs) == 0 {
		return ""
	}
	sort.Slice(vs, func(i, j int) bool { return Newer(vs[i], vs[j]) })
	return vs[0]
}

// checkTools finds the installed formulae and asks formulae.brew.sh for each one's current
// stable version: the version brew upgrade installs. Only public formula names are sent.
func (u *Updater) checkTools(ctx context.Context) ([]Tool, error) {
	if u.BrewPrefix == "" {
		return nil, nil
	}
	cellar := filepath.Join(u.BrewPrefix, "Cellar")
	var out []Tool
	for _, t := range KnownTools {
		if v := installedVersion(cellar, t.Formula); v != "" {
			t.Current = v
			out = append(out, t)
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, len(out))
	for i := range out {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var doc struct {
				Versions struct {
					Stable string `json:"stable"`
				} `json:"versions"`
			}
			if err := u.getJSON(ctx, u.FormulaAPI+"/"+out[i].Formula+".json", &doc); err != nil {
				errs[i] = fmt.Errorf("%s: %w", out[i].Formula, err)
				return
			}
			out[i].Latest = doc.Versions.Stable
			out[i].Newer = Newer(doc.Versions.Stable, out[i].Current)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func (u *Updater) getJSON(ctx context.Context, url string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "colima-dashboard")
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}
