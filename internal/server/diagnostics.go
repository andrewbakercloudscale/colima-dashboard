package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
)

// Check is one diagnostics row: what was checked, whether it passed, and the fix when it did not.
type Check struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	State  string `json:"state"` // pass | warn | fail | skip
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"` // the command that fixes it
	// Action is how the page's button runs Fix: "terminal" opens Terminal with it (see
	// terminal.go), "start", "restart" and "certs" use the page's own progress view (for
	// "certs" Fix describes what happens rather than being a command), "install" installs the
	// Homebrew formula named in Fix ("brew install X") through POST /api/install.
	Action string `json:"action,omitempty"`
	// Confirm, when set, is what the page asks before a fix that deletes or edits something.
	Confirm string `json:"confirm,omitempty"`
	// Cleanup marks a problem a move away from Rancher Desktop (or Docker Desktop) typically
	// leaves behind. General groups these, and its "Fix all" button runs every one in a job.
	Cleanup bool `json:"cleanup,omitempty"`
}

// inPlace marks a check's fix as a Homebrew install the page runs itself.
func inPlace(c Check, formula string) Check {
	c.Fix, c.Action = "brew install "+formula, "install"
	return c
}

// inTerminal marks a check's fix as one the page opens in Terminal.
func inTerminal(c Check, fix string) Check {
	c.Fix, c.Action = fix, "terminal"
	return c
}

func (s *Server) apiDiagnostics(w http.ResponseWriter, r *http.Request) {
	profile := r.URL.Query().Get("profile")
	if profile == "" {
		profile = "default"
	}
	if !colima.ValidProfile(profile) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid profile name %q", profile))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.diagnose(ctx, profile))
}

func (s *Server) diagnose(ctx context.Context, profile string) []Check {
	var checks []Check
	add := func(c Check) { checks = append(checks, c) }

	// 1. colima itself
	ver, err := s.opt.Colima.Version(ctx)
	if err != nil {
		add(inTerminal(Check{ID: "colima", Title: "colima is installed", State: "fail", Detail: err.Error()}, "brew install colima docker"))
		return checks
	}
	add(Check{ID: "colima", Title: "colima is installed", State: "pass", Detail: "version " + ver})

	// 2. the VM
	st, stErr := s.opt.Colima.Status(ctx, profile)
	if stErr != nil {
		add(Check{ID: "vm", Title: "Profile " + profile + " is running", State: "fail", Detail: "not running",
			Fix: "colima start " + profile, Action: "start"})
	} else {
		add(Check{ID: "vm", Title: "Profile " + profile + " is running", State: "pass",
			Detail: fmt.Sprintf("%s, %s, %d CPUs, %s memory", st.Driver, st.Runtime, st.CPU, humanBytes(st.Memory))})
	}

	// 3. the engine socket
	sock := filepath.Join(s.opt.ColimaHome, profile, "docker.sock")
	if stErr == nil && st.Runtime == "docker" {
		dc, _ := s.dockerFor(profile)
		if err := dc.Ping(ctx); err != nil {
			add(Check{ID: "socket", Title: "Docker engine answers", State: "fail", Detail: err.Error(), Fix: "colima restart " + profile, Action: "restart"})
		} else {
			add(Check{ID: "socket", Title: "Docker engine answers", State: "pass", Detail: sock})
		}
	} else {
		add(Check{ID: "socket", Title: "Docker engine answers", State: "skip", Detail: "profile not running the docker runtime"})
	}

	// 4. docker CLI, context, credential helper, plugins
	checks = append(checks, s.dockerCLIChecks(ctx, profile, stErr == nil)...)

	// 4b. TLS from inside the VM (a company proxy's certificate)
	if stErr == nil {
		add(s.certCheck(ctx, profile))
	}

	// 5. Kubernetes context
	if stErr == nil && st.Kubernetes {
		want := colima.KubeContext(profile)
		cur, err := kubeCurrentContext(ctx)
		switch {
		case err != nil:
			add(inPlace(Check{ID: "kubectx", Title: "kubectl uses this profile", State: "warn", Detail: err.Error()}, "kubernetes-cli"))
		case cur != want:
			add(inTerminal(Check{ID: "kubectx", Title: "kubectl uses this profile", State: "warn",
				Detail: fmt.Sprintf("current context is %q", cur)}, "kubectl config use-context "+want))
		default:
			add(Check{ID: "kubectx", Title: "kubectl uses this profile", State: "pass", Detail: cur})
		}
		// Having the right current context is not enough: the entry it names also needs its
		// cluster and its user, or every kubectl call fails while the check above still passes.
		if err == nil {
			if missing := kubeContextGaps(ctx, want); missing != "" {
				add(inTerminal(Check{ID: "kubeconfig", Title: "kubeconfig is complete", State: "fail",
					Detail: fmt.Sprintf("~/.kube/config is missing the %s for %s; kubectl cannot reach the cluster", missing, want)},
					"colima restart --profile "+profile))
			} else {
				add(Check{ID: "kubeconfig", Title: "kubeconfig is complete", State: "pass", Detail: want + " has its cluster and user"})
			}
		}
	}

	// 6. disk space on the Mac
	add(diskCheck())
	add(s.localNameCheck())

	// 7. what a removed Rancher Desktop left behind
	checks = append(checks, rancherLeftovers()...)
	if _, err := os.Stat(rancherApp); err != nil {
		checks = append(checks, rancherContexts(ctx, profile)...)
	}

	// 8. a newer colima
	add(s.latestCheck(ctx, ver))
	for i := range checks {
		checks[i].Cleanup = isCleanup(checks[i])
	}
	return checks
}

func (s *Server) dockerCLIChecks(ctx context.Context, profile string, running bool) []Check {
	var out []Check
	dockerBin, err := exec.LookPath("docker")
	if err != nil {
		return append(out, inPlace(Check{ID: "dockercli", Title: "docker CLI is installed", State: "fail",
			Detail: "colima runs the engine; the docker command comes separately"}, "docker"))
	}
	// Without kubectl colima reports Kubernetes as off even when k3s runs, and will not start a
	// k3s profile, so check it whether or not this profile uses Kubernetes.
	if p, err := exec.LookPath("kubectl"); err != nil {
		out = append(out, inPlace(Check{ID: "kubectl", Title: "kubectl is installed", State: "warn",
			Detail: "without it colima reports Kubernetes as off and will not start a k3s profile"}, "kubernetes-cli"))
	} else {
		out = append(out, Check{ID: "kubectl", Title: "kubectl is installed", State: "pass", Detail: p})
	}
	out = append(out, Check{ID: "dockercli", Title: "docker CLI is installed", State: "pass", Detail: dockerBin})

	want := colima.DockerContext(profile)
	if running {
		if host := os.Getenv("DOCKER_HOST"); host != "" {
			c := Check{ID: "context", Title: "docker uses this profile", State: "warn",
				Detail: "DOCKER_HOST is set to " + host + "; it overrides the docker context"}
			if files := profilesMentioning(homeDir(), "DOCKER_HOST"); len(files) > 0 {
				c = inTerminal(c, "sed -i '.colima-dashboard.bak' '/DOCKER_HOST/d' "+strings.Join(files, " ")+
					" && launchctl unsetenv DOCKER_HOST && echo 'Done. Open a new terminal and restart Colima Dashboard.'")
				c.Confirm = "Deletes every line mentioning DOCKER_HOST from " + strings.Join(files, " and ") + ". A backup of each file is kept next to it, ending .colima-dashboard.bak."
			} else {
				c = inTerminal(c, "launchctl unsetenv DOCKER_HOST && echo 'Done. Restart Colima Dashboard.'")
			}
			out = append(out, c)
		} else if cur, err := runText(ctx, dockerBin, "context", "show"); err != nil {
			out = append(out, Check{ID: "context", Title: "docker uses this profile", State: "warn", Detail: err.Error()})
		} else if cur != want {
			out = append(out, Check{ID: "context", Title: "docker uses this profile", State: "warn",
				Detail: fmt.Sprintf("current context is %q", cur), Fix: "docker context use " + want, Action: "terminal"})
		} else {
			out = append(out, Check{ID: "context", Title: "docker uses this profile", State: "pass", Detail: cur})
		}
	}

	// A credsStore whose helper is missing makes every pull fail with a confusing error. The usual
	// cause: Rancher Desktop or Docker Desktop set it, then took the helper away on uninstall.
	cfg := dockerConfig()
	if cfg.CredsStore != "" {
		helper := "docker-credential-" + cfg.CredsStore
		if p, err := exec.LookPath(helper); err != nil {
			out = append(out, Check{ID: "creds", Title: "Docker credential helper exists", State: "fail",
				Detail: fmt.Sprintf("~/.docker/config.json names credsStore %q but %s is not on PATH, so pulls fail", cfg.CredsStore, helper),
				Fix:    "brew install docker-credential-helper", Action: "install"})
		} else {
			out = append(out, Check{ID: "creds", Title: "Docker credential helper exists", State: "pass", Detail: p})
		}
	}
	for _, plugin := range []struct{ name, brew string }{{"buildx", "docker-buildx"}, {"compose", "docker-compose"}} {
		if _, err := runText(ctx, dockerBin, plugin.name, "version"); err != nil {
			out = append(out, Check{ID: plugin.name, Title: "docker " + plugin.name + " plugin", State: "warn",
				Detail: "not installed (Rancher Desktop bundles it; Colima does not)", Fix: "brew install " + plugin.brew, Action: "install"})
		} else {
			out = append(out, Check{ID: plugin.name, Title: "docker " + plugin.name + " plugin", State: "pass", Detail: "installed"})
		}
	}
	return out
}

type dockerCfg struct {
	CredsStore string `json:"credsStore"`
}

func dockerConfig() dockerCfg {
	var c dockerCfg
	dir := os.Getenv("DOCKER_CONFIG")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".docker")
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

func diskCheck() Check {
	home, _ := os.UserHomeDir()
	var fs syscall.Statfs_t
	if err := syscall.Statfs(home, &fs); err != nil {
		return Check{ID: "disk", Title: "Free disk space on the Mac", State: "skip", Detail: err.Error()}
	}
	free := int64(fs.Bavail) * int64(fs.Bsize)
	c := Check{ID: "disk", Title: "Free disk space on the Mac", State: "pass", Detail: humanBytes(free) + " free"}
	if free < 10<<30 {
		c.State, c.Fix, c.Action = "warn", "docker system prune", "terminal"
		c.Detail += "; the VM disk grows into this space"
	}
	return c
}

func rancherLeftovers() []Check {
	home := homeDir()
	if _, err := os.Stat(rancherApp); err == nil {
		c := Check{ID: "rancher", Title: "Rancher Desktop", State: "warn",
			Detail: "Rancher Desktop is installed; do not run it and Colima at the same time (both take the docker socket and contexts)"}
		// Quit it first; Homebrew removes a cask it installed, anything else goes to the Bin.
		fix := `osascript -e 'quit app "Rancher Desktop"'; `
		if brewCask("rancher") {
			fix += "brew uninstall --cask rancher"
		} else {
			fix += `osascript -e 'tell application "Finder" to delete POSIX file "` + rancherApp + `"'`
		}
		c = inTerminal(c, fix)
		c.Confirm = "Quits and uninstalls Rancher Desktop. Its settings and VM data stay; this page then offers to remove them."
		return []Check{c}
	}
	var out []Check
	if dirs := rancherDataDirs(home); len(dirs) > 0 {
		quoted := make([]string, len(dirs))
		for i, d := range dirs {
			quoted[i] = shq(d)
		}
		out = append(out, Check{ID: "rancher-data", Title: "Rancher Desktop leftovers", State: "warn",
			Detail: "Rancher Desktop is gone but its data remains: " + strings.Join(tildes(home, dirs), ", "),
			Fix:    "rm -rf " + strings.Join(quoted, " "), Action: "terminal",
			Confirm: "Deletes Rancher Desktop's VM disks, settings, caches and logs for good, with every image and container in them."})
	}
	out = append(out, rancherLinks(home)...)
	if files := profilesMentioning(home, ".rd/bin"); len(files) > 0 {
		out = append(out, Check{ID: "rancher-path", Title: "Rancher Desktop leftovers", State: "warn",
			Detail: "~/.rd/bin is still added to PATH by " + strings.Join(files, ", ") + " (Rancher Desktop wrote it)",
			Fix:    `sed -i '.colima-dashboard.bak' '/\.rd\/bin/d' ` + strings.Join(files, " "), Action: "terminal",
			Confirm: "Deletes the lines mentioning .rd/bin from " + strings.Join(files, " and ") + ". A backup of each file is kept next to it, ending .colima-dashboard.bak."})
	}
	return out
}

const rancherApp = "/Applications/Rancher Desktop.app"

func homeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

// brewCask says whether Homebrew installed the named cask.
func brewCask(name string) bool {
	for _, prefix := range []string{"/opt/homebrew", "/usr/local"} {
		if _, err := os.Stat(filepath.Join(prefix, "Caskroom", name)); err == nil {
			return true
		}
	}
	return false
}

// profilesMentioning lists the shell profiles that contain text, as ~/ paths. We read the files
// rather than our own environment: run as a LaunchAgent we never see what they set.
func profilesMentioning(home, text string) []string {
	var out []string
	for _, f := range []string{".zshrc", ".zprofile", ".bash_profile", ".bashrc", ".profile"} {
		b, err := os.ReadFile(filepath.Join(home, f))
		if err == nil && strings.Contains(string(b), text) {
			out = append(out, "~/"+f)
		}
	}
	return out
}

// latestCheck asks GitHub for the newest colima release. It sends nothing about this Mac.
func (s *Server) latestCheck(ctx context.Context, current string) Check {
	c := Check{ID: "latest", Title: "colima is up to date", State: "skip"}
	latest, detail := s.latestRelease(ctx)
	if latest == "" {
		c.Detail = detail
		return c
	}
	if latest == strings.TrimPrefix(current, "v") {
		c.State, c.Detail = "pass", "latest is "+latest
	} else {
		c.State, c.Detail = "warn", fmt.Sprintf("you have %s, latest is %s", current, latest)
		c = inTerminal(c, "brew upgrade colima")
	}
	return c
}

// latestRelease asks GitHub at most once every 6 hours: the sidebar badge runs the checks too.
func (s *Server) latestRelease(ctx context.Context) (tag, detail string) {
	s.latestMu.Lock()
	defer s.latestMu.Unlock()
	if s.latestTag != "" && time.Since(s.latestAt) < 6*time.Hour {
		return s.latestTag, ""
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/abiosoft/colima/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "colima-dashboard")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "could not reach GitHub"
	}
	defer resp.Body.Close()
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&rel) != nil || rel.TagName == "" {
		return "", "GitHub did not say"
	}
	s.latestTag, s.latestAt = strings.TrimPrefix(rel.TagName, "v"), time.Now()
	return s.latestTag, ""
}

func kubeCurrentContext(ctx context.Context) (string, error) {
	bin, err := exec.LookPath("kubectl")
	if err != nil {
		return "", errors.New("kubectl is not installed on the Mac")
	}
	return runText(ctx, bin, "config", "current-context")
}

// kubeContextGaps names what the kubeconfig lacks for a context ("context", "cluster", "user",
// joined with "and"), or "" when it is whole.
func kubeContextGaps(ctx context.Context, name string) string {
	bin, err := exec.LookPath("kubectl")
	if err != nil {
		return ""
	}
	out, err := runText(ctx, bin, "config", "view", "--context", name, "--minify", "-o",
		"jsonpath={.contexts[0].name}|{.clusters[0].name}|{.users[0].name}")
	if err != nil {
		return "context"
	}
	return kubeGapsFrom(out)
}

func kubeGapsFrom(out string) string {
	parts := strings.Split(strings.TrimSpace(out)+"||", "|")
	var gaps []string
	for i, what := range []string{"context", "cluster", "user"} {
		if strings.TrimSpace(parts[i]) == "" {
			gaps = append(gaps, what)
		}
	}
	return strings.Join(gaps, " and ")
}

func runText(ctx context.Context, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", errors.New(strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
