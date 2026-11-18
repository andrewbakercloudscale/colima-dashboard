package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
)

// The support bundle: one zip with what someone helping needs to see, redacted on the way out.

// bundleLogLines is how much of each log file goes in.
const bundleLogLines = 2000

// bundleFile is one file of the bundle and the line the README says about it.
type bundleFile struct {
	name, about string
	data        func(ctx context.Context) ([]byte, error)
}

// apiSupportBundle streams GET /api/profiles/{p}/support-bundle as a zip.
func (s *Server) apiSupportBundle(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("p")
	if !colima.ValidProfile(p) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid profile name %q", p))
		return
	}
	now := time.Now()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+exportName(p, "support-bundle", "zip", now)+`"`)
	w.WriteHeader(http.StatusOK)
	s.writeBundle(r.Context(), w, p, now)
}

func (s *Server) redactor() Redactor {
	home, _ := os.UserHomeDir()
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
		if u.HomeDir != "" {
			home = u.HomeDir // the real home folder, whatever $HOME says
		}
	}
	return NewRedactor(home, name, s.opt.Token)
}

// writeBundle writes the zip. Each part has its own timeout, so one slow part (kubectl on a
// sick cluster, say) costs its own few seconds and is noted in the README, not the whole bundle.
func (s *Server) writeBundle(ctx context.Context, out io.Writer, p string, now time.Time) {
	red := s.redactor()
	zw := zip.NewWriter(out)
	defer zw.Close()

	var st *colima.Status
	{
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		st, _ = s.opt.Colima.Status(c, p)
		cancel()
	}
	running := st != nil

	files := []bundleFile{
		{"dashboard/version.txt", "Colima Dashboard version, Go version and OS", func(context.Context) ([]byte, error) {
			return fmt.Appendf(nil, "colima-dashboard %s\n%s %s/%s\nstarted %s\nbundle made %s\n", s.opt.Version,
				runtime.Version(), runtime.GOOS, runtime.GOARCH, Started.Format(time.RFC3339), now.Format(time.RFC3339)), nil
		}},
		{"dashboard/settings.json", "the dashboard's settings: alerts, menu bar, Prometheus endpoint, update preferences", func(context.Context) ([]byte, error) {
			v := map[string]any{"prometheus": s.promEnabled()}
			if s.opt.Alerts != nil {
				v["alerts"] = s.opt.Alerts.Settings()
			}
			if s.opt.DataDir != "" {
				if b, err := os.ReadFile(filepath.Join(s.opt.DataDir, "settings.json")); err == nil {
					var u any
					if json.Unmarshal(b, &u) == nil {
						v["updates"] = u
					}
				}
			}
			return red.Value(v), nil
		}},
		{"dashboard/alerts.json", "recent alerts (failed runs and builds, out of memory)", func(context.Context) ([]byte, error) {
			if s.opt.Alerts == nil {
				return nil, fmt.Errorf("alerts are off")
			}
			list, _ := s.opt.Alerts.Recent()
			return red.Value(list), nil
		}},
		{"diagnostics.json", "the Diagnostics page's checks for this profile", func(c context.Context) ([]byte, error) {
			return red.Value(s.diagnose(c, p)), nil
		}},
		{"colima/version.txt", "colima version", func(c context.Context) ([]byte, error) {
			v, err := s.opt.Colima.Version(c)
			return []byte(red.Text(v + "\n")), err
		}},
		{"colima/list.json", "colima list: every profile", func(c context.Context) ([]byte, error) {
			v, err := s.opt.Colima.List(c)
			return red.Value(v), err
		}},
		{"colima/status.json", "colima status for this profile", func(context.Context) ([]byte, error) {
			if !running {
				return nil, fmt.Errorf("profile %s is not running", p)
			}
			return red.Value(st), nil
		}},
		{"colima/colima.yaml", "the profile's settings file", func(context.Context) ([]byte, error) {
			b, err := os.ReadFile(filepath.Join(s.opt.ColimaHome, p, "colima.yaml"))
			return []byte(red.Text(string(b))), err
		}},
	}
	if running && st.Runtime == "docker" {
		dc, _ := s.dockerFor(p)
		raw := func(path string) func(context.Context) ([]byte, error) {
			return func(c context.Context) ([]byte, error) {
				rc, err := dc.Raw(c, path, nil)
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				b, err := io.ReadAll(io.LimitReader(rc, 4<<20))
				return red.JSON(b), err
			}
		}
		files = append(files,
			bundleFile{"docker/version.json", "docker version (the engine in the VM)", raw("/version")},
			bundleFile{"docker/info.json", "docker info", raw("/info")})
	}
	files = append(files, bundleFile{"docker/config.json", "the docker CLI's config.json, registry auths removed", func(context.Context) ([]byte, error) {
		dir := os.Getenv("DOCKER_CONFIG")
		if dir == "" {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, ".docker")
		}
		b, err := os.ReadFile(filepath.Join(dir, "config.json"))
		if err != nil {
			return nil, err
		}
		return red.JSON(b), nil
	}})
	lima := filepath.Join(s.opt.ColimaHome, "_lima", colima.LimaInstance(p))
	for _, name := range []string{"ha.stderr.log", "serialv.log", "serial.log"} {
		files = append(files, bundleFile{"logs/" + name, fmt.Sprintf("the last %d lines of Lima's %s", bundleLogLines, name), func(context.Context) ([]byte, error) {
			text, err := tailFile(filepath.Join(lima, name), 1<<20)
			if err != nil {
				return nil, err
			}
			return []byte(red.Text(lastLines(text, bundleLogLines))), nil
		}})
	}
	if running && st.Kubernetes {
		kube := func(args ...string) func(context.Context) ([]byte, error) {
			return func(c context.Context) ([]byte, error) {
				out, err := s.opt.Colima.Kubectl(c, p, args...)
				return []byte(red.Text(out)), err
			}
		}
		files = append(files,
			bundleFile{"kubernetes/nodes.txt", "kubectl get nodes -o wide", kube("get", "nodes", "-o", "wide")},
			bundleFile{"kubernetes/pods.txt", "kubectl get pods --all-namespaces -o wide", kube("get", "pods", "--all-namespaces", "-o", "wide")},
			bundleFile{"kubernetes/events.txt", "kubectl get events --all-namespaces, oldest first", kube("get", "events", "--all-namespaces", "--sort-by=.lastTimestamp")})
	}

	var inside, missing []string
	for _, f := range files {
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		b, err := f.data(c)
		cancel()
		if ctx.Err() != nil {
			return // the browser went away
		}
		if err != nil && len(b) == 0 {
			missing = append(missing, fmt.Sprintf("%-26s %s", f.name, red.Text(err.Error())))
			continue
		}
		if err != nil {
			b = append(b, red.Text("\n(incomplete: "+err.Error()+")\n")...)
		}
		if writeZip(zw, f.name, now, b) != nil {
			return
		}
		inside = append(inside, fmt.Sprintf("%-26s %s", f.name, f.about))
	}
	_ = writeZip(zw, "README.txt", now, []byte(bundleReadme(p, now, inside, missing)))
}

func writeZip(zw *zip.Writer, name string, t time.Time, b []byte) error {
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: t})
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

func lastLines(text string, n int) string {
	text = strings.TrimRight(text, "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}

func bundleReadme(p string, now time.Time, inside, missing []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Colima Dashboard support bundle\nProfile: %s\nMade: %s\n\n", p, now.Format(time.RFC1123))
	b.WriteString("What is inside\n--------------\n")
	for _, l := range inside {
		b.WriteString(l + "\n")
	}
	b.WriteString("README.txt                 this file\n")
	if len(missing) > 0 {
		b.WriteString("\nLeft out (could not be read)\n----------------------------\n")
		for _, l := range missing {
			b.WriteString(l + "\n")
		}
	}
	b.WriteString(`
What was redacted
-----------------
- Your home folder is written as ~ and your macOS user name as <user>.
- The dashboard's session token is written as <session-token>.
- Values of settings and environment variables whose names contain KEY, TOKEN, SECRET,
  PASSWORD, CREDENTIAL, AUTH, PRIVATE, COOKIE or SESSION are written as <redacted>.
- Docker registry auths and identity tokens, kubeconfig client keys and certificates,
  bearer and basic authorization values, and PEM private keys are written as <redacted>.

Redaction is pattern based. Look through the files before you share them, and remove
anything else you would rather keep to yourself. Nothing was sent anywhere: this zip was
made on your Mac and only you decide where it goes.
`)
	return b.String()
}
