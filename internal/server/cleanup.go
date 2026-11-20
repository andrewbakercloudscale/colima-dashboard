package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
)

// Moving from Rancher Desktop to Colima leaves things behind: its data, PATH lines, Docker CLI
// plugin links into ~/.rd that now point nowhere, docker and kubectl contexts, a credential
// helper that went with it, a /var/run/docker.sock link, and the company proxy certificate it
// used to copy into its VM. General groups these checks and offers one button for all of them.

// cleanupIDs are the diagnostics checks that belong to that move.
var cleanupIDs = map[string]bool{
	"rancher": true, "rancher-data": true, "rancher-path": true, "rancher-plugins": true,
	"rancher-sock": true, "rancher-dctx": true, "rancher-kube": true,
	"creds": true, "context": true, "certs": true, "kubeconfig": true,
	"dockercli": true, "kubectl": true, "buildx": true, "compose": true,
}

// isCleanup says whether a failed check belongs to the move. "context" only counts when
// DOCKER_HOST overrides it; a context naming another profile is a choice, not a leftover.
func isCleanup(c Check) bool {
	if !cleanupIDs[c.ID] || (c.State != "fail" && c.State != "warn") || c.Fix == "" {
		return false
	}
	return c.ID != "context" || strings.Contains(c.Detail, "DOCKER_HOST")
}

// rancherDataDirs are the folders Rancher Desktop keeps, that still exist.
func rancherDataDirs(home string) []string {
	lib := filepath.Join(home, "Library")
	var out []string
	for _, d := range []string{
		filepath.Join(lib, "Application Support", "rancher-desktop"),
		filepath.Join(lib, "Caches", "rancher-desktop"),
		filepath.Join(lib, "Logs", "rancher-desktop"),
		filepath.Join(lib, "Preferences", "rancher-desktop"),
		filepath.Join(lib, "Preferences", "io.rancherdesktop.app.plist"),
		filepath.Join(lib, "Saved Application State", "io.rancherdesktop.app.savedState"),
		filepath.Join(home, ".rd"),
	} {
		if _, err := os.Lstat(d); err == nil {
			out = append(out, d)
		}
	}
	return out
}

// rancherLinks finds Docker CLI plugins and the docker socket still linked into ~/.rd.
func rancherLinks(home string) []Check {
	var out []Check
	rd := filepath.Join(home, ".rd") + "/"
	dir := filepath.Join(home, ".docker", "cli-plugins")
	var links, names []string
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			if t, err := os.Readlink(p); err == nil && (strings.HasPrefix(t, rd) || strings.Contains(t, "/.rd/")) {
				links = append(links, shq(p))
				names = append(names, e.Name())
			}
		}
	}
	if len(links) > 0 {
		out = append(out, Check{ID: "rancher-plugins", Title: "Docker plugins linked to Rancher Desktop", State: "warn",
			Detail: "~/.docker/cli-plugins still links " + strings.Join(names, ", ") + " into ~/.rd, which Rancher Desktop owned; they break when it is gone",
			Fix:    "rm " + strings.Join(links, " "), Action: "terminal",
			Confirm: "Removes the links " + strings.Join(names, ", ") + " from ~/.docker/cli-plugins (links only, nothing else). Homebrew's docker-buildx and docker-compose take over."})
	}
	if t, err := os.Readlink("/var/run/docker.sock"); err == nil && strings.Contains(t, ".rd") {
		out = append(out, Check{ID: "rancher-sock", Title: "/var/run/docker.sock points at Rancher Desktop", State: "warn",
			Detail: "the system docker socket links to " + t + ", so tools that ignore docker contexts fail",
			Fix:    "sudo rm /var/run/docker.sock", Action: "terminal",
			Confirm: "Removes the /var/run/docker.sock link. It needs your password, so it runs in Terminal."})
	}
	return out
}

// rancherContexts finds the docker and kubectl contexts Rancher Desktop created.
func rancherContexts(ctx context.Context, profile string) []Check {
	const name = "rancher-desktop"
	var out []Check
	if bin, err := exec.LookPath("docker"); err == nil {
		if list, err := runText(ctx, bin, "context", "ls", "-q"); err == nil && slices.Contains(strings.Fields(list), name) {
			out = append(out, Check{ID: "rancher-dctx", Title: "Rancher Desktop docker context", State: "warn",
				Detail: "docker still has the rancher-desktop context, whose socket is gone",
				Fix:    "docker context use " + colima.DockerContext(profile) + " >/dev/null; docker context rm -f " + name, Action: "terminal",
				Confirm: "Switches docker to " + colima.DockerContext(profile) + " and removes the rancher-desktop context."})
		}
	}
	if bin, err := exec.LookPath("kubectl"); err == nil {
		if list, err := runText(ctx, bin, "config", "get-contexts", "-o", "name"); err == nil && slices.Contains(strings.Fields(list), name) {
			out = append(out, Check{ID: "rancher-kube", Title: "Rancher Desktop kubectl context", State: "warn",
				Detail: "~/.kube/config still has the rancher-desktop context, cluster and user, for a cluster that is gone",
				Fix: "cp ~/.kube/config ~/.kube/config.colima-dashboard.bak && kubectl config delete-context " + name +
					"; kubectl config delete-cluster " + name + "; kubectl config delete-user " + name + "; true",
				Action:  "terminal",
				Confirm: "Removes the rancher-desktop context, cluster and user from ~/.kube/config. Nothing else is touched, and a copy is kept as ~/.kube/config.colima-dashboard.bak first."})
		}
	}
	return out
}

// shq quotes s for a POSIX shell.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func tildes(home string, paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = strings.Replace(p, home, "~", 1)
	}
	return out
}

// POST /api/profiles/{p}/cleanup fixes every cleanup check in one job. The steps are worked out
// here from the checks, never taken from the request. Removing Rancher Desktop uncovers more
// (its data, its contexts), so the checks run again until nothing new turns up.
func (s *Server) apiCleanup(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("p")
	if !colima.ValidProfile(p) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid profile name %q", p))
		return
	}
	if err := s.opt.Colima.Claim(p, "cleanup"); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	job := s.jobs.Start("Rancher Desktop cleanup ("+p+")", p, func(j *Job) error {
		defer s.opt.Colima.Release(p)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		tried := map[string]bool{}
		var failed, manual []string
		for pass := 0; pass < 3; pass++ {
			j.Line("checking…")
			var todo []Check
			for _, c := range s.diagnose(ctx, p) {
				if c.Cleanup && !tried[c.ID+"|"+c.Title] {
					todo = append(todo, c)
				}
			}
			if len(todo) == 0 {
				break
			}
			for _, c := range todo {
				tried[c.ID+"|"+c.Title] = true
				j.Line("")
				j.Line("▸ " + c.Title)
				j.Line("  " + c.Detail)
				switch err := s.cleanupStep(ctx, j, p, c); {
				case errors.Is(err, errNeedsPassword):
					j.Line("  skipped: it needs your password; use its Run in Terminal button")
					manual = append(manual, c.Title)
				case err != nil:
					j.Line("  failed: " + err.Error())
					failed = append(failed, c.Title)
				default:
					j.Line("  done")
				}
			}
		}
		left := 0
		for _, c := range s.diagnose(ctx, p) {
			if c.Cleanup {
				left++
			}
		}
		j.Line("")
		switch {
		case left == 0:
			j.Line("All clean: nothing left over from Rancher Desktop.")
			return nil
		case len(failed) > 0:
			return fmt.Errorf("%d left to fix; failed: %s", left, strings.Join(failed, ", "))
		case len(manual) > 0:
			j.Line(fmt.Sprintf("%d left, which need your password: %s.", left, strings.Join(manual, ", ")))
			return nil
		default:
			return fmt.Errorf("%d still reported after the fixes; see Diagnostics", left)
		}
	})
	writeJSON(w, http.StatusAccepted, job.snapshot())
}

var errNeedsPassword = errors.New("needs a password")

// cleanupStep runs one check's fix inside the job.
func (s *Server) cleanupStep(ctx context.Context, j *Job, p string, c Check) error {
	switch c.Action {
	case "certs":
		bundle, names, err := macCAs(ctx)
		if err != nil || len(names) == 0 {
			return errors.New("no certificate authorities found in the Mac's keychains")
		}
		return s.copyCerts(ctx, j, p, bundle, names)
	case "install":
		formula := strings.TrimPrefix(c.Fix, "brew install ")
		if !installable[formula] {
			return fmt.Errorf("%s is not a tool the dashboard installs", formula)
		}
		brew, err := exec.LookPath("brew")
		if err != nil {
			return errors.New("Homebrew is not installed")
		}
		return s.runStep(ctx, j, brew, "install", formula)
	case "terminal":
		if strings.HasPrefix(c.Fix, "sudo ") || strings.Contains(c.Fix, " sudo ") {
			return errNeedsPassword
		}
		return s.runStep(ctx, j, "/bin/sh", "-c", c.Fix)
	}
	return fmt.Errorf("no automatic fix for %q", c.ID)
}

// runStep runs a command with Homebrew's folders on PATH (a LaunchAgent starts with a bare one).
func (s *Server) runStep(ctx context.Context, j *Job, bin string, args ...string) error {
	if bin == "/bin/sh" {
		j.Line("  $ " + args[len(args)-1])
	} else {
		j.Line("  $ " + filepath.Base(bin) + " " + strings.Join(args, " "))
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr = j, j
	path := "/opt/homebrew/bin:/usr/local/bin:" + os.Getenv("PATH")
	if s.opt.Colima.Bin != "" {
		path = filepath.Dir(s.opt.Colima.Bin) + ":" + path
	}
	cmd.Env = append(cmd.Environ(), "PATH="+path, "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ENV_HINTS=1")
	return cmd.Run()
}
