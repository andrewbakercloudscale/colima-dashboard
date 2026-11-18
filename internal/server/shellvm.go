package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
)

// A VM shell is a root terminal inside the Colima VM itself, the same machine "colima ssh"
// reaches. It needs a TTY that can be resized, which the standard library cannot make on the
// Mac, so it goes through Docker instead: a small privileged helper container shares the VM's
// process namespace, and the shell runs through nsenter into PID 1's namespaces. The helper is
// removed when the shell closes, and removes itself after an hour in any case.

// vmShellImages are tried in order; the first one already on the VM is used, else the first
// is pulled. Each has busybox nsenter.
var vmShellImages = []string{"alpine:3.22", "alpine:latest", "alpine:3.21", "busybox:1.37", "busybox:latest"}

// vmShellPrefix runs the shell in the VM's own namespaces rather than the helper's.
var vmShellPrefix = []string{"nsenter", "-t", "1", "-m", "-u", "-i", "-n", "-p", "-S", "0", "-G", "0", "--"}

// POST /api/profiles/{p}/vm/shell {cols, rows, shell} -> {id}
func (s *Server) apiVMShell(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cols, Rows int
		Shell      string
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Shell != "" && req.Shell != "bash" && req.Shell != "sh" {
		writeError(w, http.StatusBadRequest, errors.New(`shell must be "bash" or "sh"`))
		return
	}
	p := r.PathValue("p")
	dc, err := s.dockerFor(p)
	if err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		err = dc.Ping(ctx)
		cancel()
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("the VM shell in this page needs the profile running with the docker runtime (%v); use Open in Terminal instead", err))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	image, err := vmShellImage(ctx, dc)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	name := "colima-dashboard-vm-shell-" + hex.EncodeToString(b[:])
	id, err := dc.RunHelper(ctx, image, name, []string{"sleep", "3600"},
		map[string]any{"Privileged": true, "PidMode": "host", "NetworkMode": "host"})
	if err != nil {
		writeError(w, statusFor(err), fmt.Errorf("could not start the VM shell helper: %w", err))
		return
	}
	cleanup := func() {
		rm, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = dc.RemoveContainer(rm, id, true)
	}
	prompt := "colima"
	if p != "default" {
		prompt = "colima-" + p
	}
	s.openShell(w, r, dc, id, prompt, req.Cols, req.Rows, shellOpts{Shell: req.Shell, Prefix: vmShellPrefix, Cleanup: cleanup})
}

func vmShellImage(ctx context.Context, dc *docker.Client) (string, error) {
	for _, ref := range vmShellImages {
		if _, err := dc.ImageInspect(ctx, ref); err == nil {
			return ref, nil
		}
	}
	ref := vmShellImages[0]
	if err := dc.Pull(ctx, ref, func(docker.PullEvent) error { return nil }); err != nil {
		return "", fmt.Errorf("could not pull %s for the VM shell: %w", ref, err)
	}
	return ref, nil
}

// POST /api/profiles/{p}/vm/terminal opens Terminal running "colima ssh" for the profile.
func (s *Server) apiVMTerminal(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("p")
	if !colima.ValidProfile(p) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid profile name %q", p))
		return
	}
	cmd := shQuote(s.opt.Colima.Bin) + " ssh -p " + p
	open := s.opt.OpenTerminal
	if open == nil {
		open = openTerminal
	}
	if err := open(cmd); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("could not open Terminal (%v); run %s yourself", err, cmd))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"command": cmd})
}

// shQuote is shellQuote, left bare when s needs no quoting so the command reads cleanly.
func shQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-") == "" {
		return s
	}
	return shellQuote(s)
}
