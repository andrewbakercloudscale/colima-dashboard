// Package colima wraps the colima CLI. Every call passes an argument list, never a shell string,
// and every profile name is validated before it reaches the command line.
package colima

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var profileRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidProfile reports whether name is safe to pass to colima as a profile.
func ValidProfile(name string) bool { return profileRe.MatchString(name) }

// ErrBusy is returned when another start, stop or similar operation is running on the profile.
var ErrBusy = errors.New("another operation is running on this profile")

// Runner runs a command and returns its combined output. Swapped out in tests.
type Runner interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	Stream(ctx context.Context, w io.Writer, name string, args ...string) error
}

// ExecRunner runs real processes.
type ExecRunner struct{}

// Output runs the command and returns stdout; stderr is folded into the error.
func (ExecRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return out, fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return out, nil
}

// Stream runs the command and copies stdout and stderr to w as they arrive.
func (ExecRunner) Stream(ctx context.Context, w io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}

// Client runs colima.
type Client struct {
	Bin    string
	Runner Runner

	mu   sync.Mutex
	busy map[string]string // profile -> operation
}

// New returns a client for the colima binary at bin ("colima" to use PATH).
func New(bin string) *Client {
	return &Client{Bin: bin, Runner: ExecRunner{}, busy: map[string]string{}}
}

// Profile is one line of `colima list --json`.
type Profile struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Arch    string `json:"arch"`
	CPUs    int    `json:"cpus"`
	Memory  int64  `json:"memory"`
	Disk    int64  `json:"disk"`
	Runtime string `json:"runtime"`
	Address string `json:"address,omitempty"`
	// Busy names the operation in progress from this UI, if any.
	Busy string `json:"busy,omitempty"`
}

// Status is `colima status --json` for a running profile.
type Status struct {
	DisplayName      string `json:"display_name"`
	Driver           string `json:"driver"`
	Arch             string `json:"arch"`
	Runtime          string `json:"runtime"`
	MountType        string `json:"mount_type"`
	Address          string `json:"ip_address,omitempty"`
	DockerSocket     string `json:"docker_socket"`
	ContainerdSocket string `json:"containerd_socket"`
	Kubernetes       bool   `json:"kubernetes"`
	CPU              int    `json:"cpu"`
	Memory           int64  `json:"memory"`
	Disk             int64  `json:"disk"`
}

// List returns every profile. colima prints one JSON object per line.
func (c *Client) List(ctx context.Context) ([]Profile, error) {
	out, err := c.Runner.Output(ctx, c.Bin, "list", "--json")
	if err != nil {
		return nil, err
	}
	var profiles []Profile
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var p Profile
		if err := json.Unmarshal(line, &p); err != nil {
			return nil, fmt.Errorf("colima list: unexpected output %q: %w", line, err)
		}
		profiles = append(profiles, p)
	}
	c.mu.Lock()
	for i := range profiles {
		profiles[i].Busy = c.busy[profiles[i].Name]
	}
	c.mu.Unlock()
	return profiles, nil
}

// Status returns the detailed status of a running profile.
func (c *Client) Status(ctx context.Context, profile string) (*Status, error) {
	if !ValidProfile(profile) {
		return nil, fmt.Errorf("invalid profile name %q", profile)
	}
	out, err := c.Runner.Output(ctx, c.Bin, "status", "--json", "-p", profile)
	if err != nil {
		return nil, err
	}
	var s Status
	if err := json.Unmarshal(bytes.TrimSpace(out), &s); err != nil {
		return nil, fmt.Errorf("colima status: unexpected output: %w", err)
	}
	return &s, nil
}

// Version returns the colima version string, e.g. "0.10.3".
func (c *Client) Version(ctx context.Context) (string, error) {
	out, err := c.Runner.Output(ctx, c.Bin, "version")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "colima version "); ok {
			return strings.TrimSpace(v), nil
		}
	}
	return "", errors.New("colima version: version line not found")
}

// StartOptions are the settings the Preferences page can change. Zero values are left to colima.
type StartOptions struct {
	CPUs       int    `json:"cpus"`
	MemoryGiB  int    `json:"memoryGiB"`
	DiskGiB    int    `json:"diskGiB"`
	Runtime    string `json:"runtime"`   // docker | containerd
	VMType     string `json:"vmType"`    // vz | qemu
	MountType  string `json:"mountType"` // virtiofs | sshfs | 9p
	Rosetta    *bool  `json:"rosetta"`
	Kubernetes *bool  `json:"kubernetes"`
	// KubernetesVersion is a k3s release such as v1.33.5+k3s1.
	KubernetesVersion string `json:"kubernetesVersion"`
	// Traefik turns k3s's bundled ingress on or off. The server turns it into K3sArgs.
	Traefik *bool `json:"traefik"`
	// K3sArgs, when SetK3sArgs, replaces the profile's k3s arguments (an empty list clears them).
	K3sArgs    []string `json:"-"`
	SetK3sArgs bool     `json:"-"`
}

var k3sVersionRe = regexp.MustCompile(`^v1\.[0-9]{1,3}\.[0-9]{1,3}\+k3s[0-9]{1,3}$`)

// ValidK3sVersion reports whether v looks like a k3s release tag.
func ValidK3sVersion(v string) bool { return k3sVersionRe.MatchString(v) }

var allowed = map[string]map[string]bool{
	"runtime":   {"docker": true, "containerd": true},
	"vmType":    {"vz": true, "qemu": true},
	"mountType": {"virtiofs": true, "sshfs": true, "9p": true},
}

// Args turns options into colima start flags, rejecting anything outside the known values.
func (o StartOptions) Args() ([]string, error) {
	var args []string
	if o.CPUs < 0 || o.CPUs > 64 {
		return nil, fmt.Errorf("cpus must be between 1 and 64")
	}
	if o.MemoryGiB < 0 || o.MemoryGiB > 512 {
		return nil, fmt.Errorf("memory must be between 1 and 512 GiB")
	}
	if o.DiskGiB < 0 || o.DiskGiB > 4096 {
		return nil, fmt.Errorf("disk must be between 1 and 4096 GiB")
	}
	if o.CPUs > 0 {
		args = append(args, "--cpus", fmt.Sprint(o.CPUs))
	}
	if o.MemoryGiB > 0 {
		args = append(args, "--memory", fmt.Sprint(o.MemoryGiB))
	}
	if o.DiskGiB > 0 {
		args = append(args, "--disk", fmt.Sprint(o.DiskGiB))
	}
	for _, f := range []struct{ key, flag, val string }{
		{"runtime", "--runtime", o.Runtime},
		{"vmType", "--vm-type", o.VMType},
		{"mountType", "--mount-type", o.MountType},
	} {
		if f.val == "" {
			continue
		}
		if !allowed[f.key][f.val] {
			return nil, fmt.Errorf("%s %q is not supported", f.key, f.val)
		}
		args = append(args, f.flag, f.val)
	}
	if o.Rosetta != nil {
		args = append(args, fmt.Sprintf("--vz-rosetta=%t", *o.Rosetta))
	}
	if o.Kubernetes != nil {
		args = append(args, fmt.Sprintf("--kubernetes=%t", *o.Kubernetes))
	}
	if o.KubernetesVersion != "" {
		if !ValidK3sVersion(o.KubernetesVersion) {
			return nil, fmt.Errorf("kubernetes version %q is not a k3s release (like v1.33.5+k3s1)", o.KubernetesVersion)
		}
		args = append(args, "--kubernetes-version", o.KubernetesVersion)
	}
	if o.SetK3sArgs {
		if len(o.K3sArgs) == 0 {
			args = append(args, "--k3s-arg=") // an empty list: no extra k3s arguments
		}
		for _, a := range o.K3sArgs {
			if !strings.HasPrefix(a, "--") || strings.ContainsAny(a, "\n\r") {
				return nil, fmt.Errorf("k3s argument %q is not a flag", a)
			}
			// colima reads each value as CSV, so quote it to keep commas inside one argument.
			args = append(args, `--k3s-arg="`+strings.ReplaceAll(a, `"`, `""`)+`"`)
		}
	}
	return args, nil
}

// Action is a long-running colima command whose output is streamed to the page.
type Action string

// Actions the UI can run.
const (
	ActionStart   Action = "start"
	ActionStop    Action = "stop"
	ActionRestart Action = "restart"
	ActionDelete  Action = "delete"
	ActionPrune   Action = "prune"
	// ActionApply stops the profile if it runs, then starts it with new settings.
	ActionApply Action = "apply"
	// ActionK8sReset deletes and recreates the profile's k3s cluster (colima kubernetes reset).
	ActionK8sReset Action = "kubernetes-reset"
)

// Run runs an action on a profile, streaming output to w. Only one action per profile runs at once.
func (c *Client) Run(ctx context.Context, w io.Writer, action Action, profile string, opts *StartOptions) error {
	if err := c.Claim(profile, string(action)); err != nil {
		return err
	}
	defer c.Release(profile)
	return c.RunClaimed(ctx, w, action, profile, opts)
}

// RunClaimed is Run for a caller that already holds the profile's claim.
func (c *Client) RunClaimed(ctx context.Context, w io.Writer, action Action, profile string, opts *StartOptions) error {
	if action != ActionPrune && !ValidProfile(profile) {
		return fmt.Errorf("invalid profile name %q", profile)
	}
	var steps [][]string
	switch action {
	case ActionStart, ActionApply:
		args := []string{"start", "-p", profile}
		if opts != nil {
			extra, err := opts.Args()
			if err != nil {
				return err
			}
			// colima saves start flags to the profile's config by default (--save-config).
			args = append(args, extra...)
		}
		if action == ActionApply {
			steps = append(steps, []string{"stop", "-p", profile})
		}
		steps = append(steps, args)
	case ActionStop:
		steps = [][]string{{"stop", "-p", profile}}
	case ActionRestart:
		steps = [][]string{{"restart", "-p", profile}}
	case ActionDelete:
		steps = [][]string{{"delete", "-f", "-p", profile}}
	case ActionPrune:
		steps = [][]string{{"prune", "-f"}}
	case ActionK8sReset:
		steps = [][]string{{"kubernetes", "reset", "-p", profile}}
	default:
		return fmt.Errorf("unknown action %q", action)
	}
	// A VM half started because a browser tab closed is worse than one that finishes.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Minute)
	defer cancel()
	for _, args := range steps {
		fmt.Fprintf(w, "$ colima %s\n", strings.Join(args, " "))
		if err := c.Runner.Stream(ctx, w, c.Bin, args...); err != nil {
			return fmt.Errorf("colima %s: %w", args[0], err)
		}
	}
	return nil
}

// Claim marks a profile busy with op, or returns ErrBusy.
func (c *Client) Claim(profile, op string) error { return c.claim(profile, op) }

// Release clears a claim.
func (c *Client) Release(profile string) { c.release(profile) }

// LimaInstance is the Lima instance name colima uses for a profile.
func LimaInstance(profile string) string {
	if profile == "default" {
		return "colima"
	}
	return "colima-" + profile
}

// KubeContext is the kubectl context name colima writes for a profile.
func KubeContext(profile string) string { return LimaInstance(profile) }

// DockerContext is the Docker context name colima creates for a profile.
func DockerContext(profile string) string { return LimaInstance(profile) }

// Kubectl runs kubectl inside the profile's VM (k3s), so no kubectl is needed on the Mac.
func (c *Client) Kubectl(ctx context.Context, profile string, args ...string) (string, error) {
	if !ValidProfile(profile) {
		return "", fmt.Errorf("invalid profile name %q", profile)
	}
	full := append([]string{"ssh", "-p", profile, "--", "sudo", "k3s", "kubectl"}, args...)
	out, err := c.Runner.Output(ctx, c.Bin, full...)
	return string(out), err
}

// KubectlStream runs kubectl inside the profile's VM and copies its output to w as it arrives.
func (c *Client) KubectlStream(ctx context.Context, profile string, w io.Writer, args ...string) error {
	if !ValidProfile(profile) {
		return fmt.Errorf("invalid profile name %q", profile)
	}
	full := append([]string{"ssh", "-p", profile, "--", "sudo", "k3s", "kubectl"}, args...)
	return c.Runner.Stream(ctx, w, c.Bin, full...)
}

func (c *Client) claim(profile, op string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.busy == nil {
		c.busy = map[string]string{}
	}
	if _, ok := c.busy[profile]; ok {
		return ErrBusy
	}
	c.busy[profile] = op
	return nil
}

func (c *Client) release(profile string) {
	c.mu.Lock()
	delete(c.busy, profile)
	c.mu.Unlock()
}

// KubeSettings are the kubernetes settings saved in a profile's colima.yaml.
type KubeSettings struct {
	Version string   `json:"version"`
	K3sArgs []string `json:"k3sArgs"`
}

// ReadKubeSettings reads the kubernetes block of <colimaHome>/<profile>/colima.yaml. It is a
// small line reader for the layout colima writes, not a general YAML parser.
func ReadKubeSettings(colimaHome, profile string) (KubeSettings, error) {
	var ks KubeSettings
	if !ValidProfile(profile) {
		return ks, fmt.Errorf("invalid profile name %q", profile)
	}
	b, err := os.ReadFile(filepath.Join(colimaHome, profile, "colima.yaml"))
	if err != nil {
		return ks, err
	}
	return parseKubeSettings(string(b)), nil
}

func parseKubeSettings(text string) KubeSettings {
	ks := KubeSettings{K3sArgs: []string{}}
	inKube, inArgs := false, false
	unquote := func(v string) string {
		v = strings.TrimSpace(v)
		if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		return v
	}
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent == 0 {
			inKube = strings.HasPrefix(trim, "kubernetes:")
			inArgs = false
			continue
		}
		if !inKube {
			continue
		}
		if inArgs && strings.HasPrefix(trim, "- ") {
			ks.K3sArgs = append(ks.K3sArgs, unquote(trim[2:]))
			continue
		}
		inArgs = false
		switch {
		case strings.HasPrefix(trim, "version:"):
			ks.Version = unquote(strings.TrimPrefix(trim, "version:"))
		case strings.HasPrefix(trim, "k3sArgs:"):
			rest := strings.TrimSpace(strings.TrimPrefix(trim, "k3sArgs:"))
			if rest == "" {
				inArgs = true
			} else if strings.HasPrefix(rest, "[") {
				for _, a := range strings.Split(strings.Trim(rest, "[]"), ",") {
					if a = unquote(a); a != "" {
						ks.K3sArgs = append(ks.K3sArgs, a)
					}
				}
			}
		}
	}
	return ks
}

// TraefikOn reports whether these k3s arguments leave Traefik enabled.
func (ks KubeSettings) TraefikOn() bool {
	for _, a := range ks.K3sArgs {
		if v, ok := strings.CutPrefix(a, "--disable="); ok {
			for _, c := range strings.Split(v, ",") {
				if strings.TrimSpace(c) == "traefik" {
					return false
				}
			}
		}
	}
	return true
}

// WithTraefik returns the k3s arguments with Traefik switched on or off, other arguments kept.
func (ks KubeSettings) WithTraefik(on bool) []string {
	out := []string{}
	for _, a := range ks.K3sArgs {
		v, ok := strings.CutPrefix(a, "--disable=")
		if !ok {
			out = append(out, a)
			continue
		}
		var keep []string
		for _, c := range strings.Split(v, ",") {
			if c = strings.TrimSpace(c); c != "" && c != "traefik" {
				keep = append(keep, c)
			}
		}
		if len(keep) > 0 {
			out = append(out, "--disable="+strings.Join(keep, ","))
		}
	}
	if !on {
		out = append(out, "--disable=traefik")
	}
	return out
}
