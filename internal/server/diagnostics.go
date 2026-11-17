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
	Fix    string `json:"fix,omitempty"` // a command to copy, never run for the user
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
		add(Check{ID: "colima", Title: "colima is installed", State: "fail", Detail: err.Error(), Fix: "brew install colima docker"})
		return checks
	}
	add(Check{ID: "colima", Title: "colima is installed", State: "pass", Detail: "version " + ver})

	// 2. the VM
	st, stErr := s.opt.Colima.Status(ctx, profile)
	if stErr != nil {
		add(Check{ID: "vm", Title: "Profile " + profile + " is running", State: "fail", Detail: "not running",
			Fix: "colima start " + profile})
	} else {
		add(Check{ID: "vm", Title: "Profile " + profile + " is running", State: "pass",
			Detail: fmt.Sprintf("%s, %s, %d CPUs, %s memory", st.Driver, st.Runtime, st.CPU, humanBytes(st.Memory))})
	}

	// 3. the engine socket
	sock := filepath.Join(s.opt.ColimaHome, profile, "docker.sock")
	if stErr == nil && st.Runtime == "docker" {
		dc, _ := s.dockerFor(profile)
		if err := dc.Ping(ctx); err != nil {
			add(Check{ID: "socket", Title: "Docker engine answers", State: "fail", Detail: err.Error(), Fix: "colima restart " + profile})
		} else {
			add(Check{ID: "socket", Title: "Docker engine answers", State: "pass", Detail: sock})
		}
	} else {
		add(Check{ID: "socket", Title: "Docker engine answers", State: "skip", Detail: "profile not running the docker runtime"})
	}

	// 4. docker CLI, context, credential helper, plugins
	checks = append(checks, s.dockerCLIChecks(ctx, profile, stErr == nil)...)

	// 5. Kubernetes context
	if stErr == nil && st.Kubernetes {
		want := colima.KubeContext(profile)
		cur, err := kubeCurrentContext(ctx)
		switch {
		case err != nil:
			add(Check{ID: "kubectx", Title: "kubectl uses this profile", State: "warn", Detail: err.Error(), Fix: "brew install kubectl"})
		case cur != want:
			add(Check{ID: "kubectx", Title: "kubectl uses this profile", State: "warn",
				Detail: fmt.Sprintf("current context is %q", cur), Fix: "kubectl config use-context " + want})
		default:
			add(Check{ID: "kubectx", Title: "kubectl uses this profile", State: "pass", Detail: cur})
		}
	}

	// 6. disk space on the Mac
	add(diskCheck())

	// 7. what a removed Rancher Desktop left behind
	checks = append(checks, rancherLeftovers()...)

	// 8. a newer colima
	add(s.latestCheck(ctx, ver))
	return checks
}

func (s *Server) dockerCLIChecks(ctx context.Context, profile string, running bool) []Check {
	var out []Check
	dockerBin, err := exec.LookPath("docker")
	if err != nil {
		return append(out, Check{ID: "dockercli", Title: "docker CLI is installed", State: "fail",
			Detail: "colima runs the engine; the docker command comes separately", Fix: "brew install docker"})
	}
	out = append(out, Check{ID: "dockercli", Title: "docker CLI is installed", State: "pass", Detail: dockerBin})

	want := colima.DockerContext(profile)
	if running {
		if host := os.Getenv("DOCKER_HOST"); host != "" {
			out = append(out, Check{ID: "context", Title: "docker uses this profile", State: "warn",
				Detail: "DOCKER_HOST is set to " + host + "; it overrides the docker context", Fix: "unset DOCKER_HOST"})
		} else if cur, err := runText(ctx, dockerBin, "context", "show"); err != nil {
			out = append(out, Check{ID: "context", Title: "docker uses this profile", State: "warn", Detail: err.Error()})
		} else if cur != want {
			out = append(out, Check{ID: "context", Title: "docker uses this profile", State: "warn",
				Detail: fmt.Sprintf("current context is %q", cur), Fix: "docker context use " + want})
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
				Fix:    "brew install docker-credential-helper"})
		} else {
			out = append(out, Check{ID: "creds", Title: "Docker credential helper exists", State: "pass", Detail: p})
		}
	}
	for _, plugin := range []struct{ name, brew string }{{"buildx", "docker-buildx"}, {"compose", "docker-compose"}} {
		if _, err := runText(ctx, dockerBin, plugin.name, "version"); err != nil {
			out = append(out, Check{ID: plugin.name, Title: "docker " + plugin.name + " plugin", State: "warn",
				Detail: "not installed (Rancher Desktop bundles it; Colima does not)", Fix: "brew install " + plugin.brew})
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
		c.State, c.Fix = "warn", "docker system prune"
		c.Detail += "; the VM disk grows into this space"
	}
	return c
}

func rancherLeftovers() []Check {
	home, _ := os.UserHomeDir()
	if _, err := os.Stat("/Applications/Rancher Desktop.app"); err == nil {
		return []Check{{ID: "rancher", Title: "Rancher Desktop", State: "warn",
			Detail: "Rancher Desktop is installed; do not run it and Colima at the same time (both take the docker socket and contexts)"}}
	}
	var out []Check
	data := filepath.Join(home, "Library", "Application Support", "rancher-desktop")
	if _, err := os.Stat(data); err == nil {
		out = append(out, Check{ID: "rancher-data", Title: "Rancher Desktop leftovers", State: "warn",
			Detail: "Rancher Desktop is gone but its data (VM disks) remains at " + data,
			Fix:    `rm -rf ~/Library/Application\ Support/rancher-desktop`})
	}
	if strings.Contains(os.Getenv("PATH"), "/.rd/bin") {
		out = append(out, Check{ID: "rancher-path", Title: "Rancher Desktop leftovers", State: "warn",
			Detail: "~/.rd/bin is still on PATH (added to your shell profile by Rancher Desktop)",
			Fix:    "remove the ~/.rd/bin line from ~/.zshrc and ~/.bash_profile"})
	}
	return out
}

// latestCheck asks GitHub for the newest colima release. It sends nothing about this Mac.
func (s *Server) latestCheck(ctx context.Context, current string) Check {
	c := Check{ID: "latest", Title: "colima is up to date", State: "skip"}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/abiosoft/colima/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "colima-dashboard")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.Detail = "could not reach GitHub"
		return c
	}
	defer resp.Body.Close()
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&rel) != nil || rel.TagName == "" {
		c.Detail = "GitHub did not say"
		return c
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	if latest == strings.TrimPrefix(current, "v") {
		c.State, c.Detail = "pass", "latest is "+latest
	} else {
		c.State, c.Detail, c.Fix = "warn", fmt.Sprintf("you have %s, latest is %s", current, latest), "brew upgrade colima"
	}
	return c
}

func kubeCurrentContext(ctx context.Context) (string, error) {
	bin, err := exec.LookPath("kubectl")
	if err != nil {
		return "", errors.New("kubectl is not installed on the Mac")
	}
	return runText(ctx, bin, "config", "current-context")
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
