package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
)

// Fix buttons. A fix runs in Terminal, so the user sees the command, can answer its prompts
// (Homebrew, a sudo password) and keeps its output. The page names which fix it wants; the
// command is always worked out here again, never taken from the request.

func (s *Server) terminalRoutes(m *http.ServeMux) {
	m.HandleFunc("POST /api/fix", s.auth(s.apiFix))
}

// RebuildFix is the fix id for rebuilding a development build from its source folder.
const RebuildFix = "rebuild"

func (s *Server) apiFix(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Profile string `json:"profile"`
		ID      string `json:"id"`
	}
	if err := readJSON(r, &body); err != nil || body.ID == "" {
		writeError(w, http.StatusBadRequest, errors.New(`send {"profile": "...", "id": "..."}`))
		return
	}
	var cmd string
	if body.ID == RebuildFix {
		c, err := s.rebuildCommand()
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		cmd = c
	} else {
		if body.Profile == "" {
			body.Profile = "default"
		}
		if !colima.ValidProfile(body.Profile) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid profile name %q", body.Profile))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		for _, c := range s.diagnose(ctx, body.Profile) {
			if c.ID == body.ID && c.Action == "terminal" && c.Fix != "" {
				cmd = c.Fix
				break
			}
		}
		if cmd == "" {
			writeError(w, http.StatusConflict, errors.New("nothing to fix there any more; run the checks again"))
			return
		}
	}
	open := s.opt.OpenTerminal
	if open == nil {
		open = openTerminal
	}
	if err := open(cmd); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("could not open Terminal (%v); copy the command instead", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"command": cmd})
}

// openTerminal opens a Terminal window running cmd. The command goes in as an argument, not
// spliced into the script, so no quoting can turn it into more AppleScript. The first time,
// macOS asks whether Colima Dashboard may control Terminal.
func openTerminal(cmd string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	c := exec.CommandContext(ctx, "osascript",
		"-e", "on run argv",
		"-e", `tell application "Terminal"`,
		"-e", "activate",
		"-e", "do script (item 1 of argv)",
		"-e", "end tell",
		"-e", "end run",
		"--", cmd)
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}

// rebuildCommand builds a development build again from the source folder it was built in, swaps
// the binary and sends this process SIGHUP, which makes it restart into the new one.
func (s *Server) rebuildCommand() (string, error) {
	if s.opt.Version != "" && s.opt.Version != "dev" {
		return "", errors.New("only a development build is rebuilt from source; this one updates itself")
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	src := sourceDir(filepath.Dir(exe))
	if src == "" {
		return "", errors.New("the source folder was not found next to this binary")
	}
	return fmt.Sprintf("cd %s && go build -o %s ./cmd/colima-dashboard && mv -f %s %s && kill -HUP %d && echo 'Colima Dashboard rebuilt; the page reconnects by itself.'",
		shellQuote(src), shellQuote(exe+".new"), shellQuote(exe+".new"), shellQuote(exe), os.Getpid()), nil
}

// sourceDir finds the folder holding Colima Dashboard's go.mod at or above dir.
func sourceDir(dir string) string {
	for i := 0; i < 4; i++ {
		b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && bytes.Contains(b, []byte("module github.com/andrewbakercloudscale/colima-dashboard\n")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
