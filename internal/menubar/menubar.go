// Package menubar runs Colima Dashboard's menu bar icon: a small native helper (MenuBar.swift)
// built into release binaries, written out next to the session token and started with the
// dashboard's port and token. Builds without it (plain `go build`) report it unavailable.
package menubar

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Available reports whether this build carries the helper.
func Available() bool { return len(helper) > 0 }

// Runner starts and stops the helper.
type Runner struct {
	Dir  string   // where the helper is written
	Env  []string // CD_PORT, CD_TOKEN, CD_URL
	Logf func(format string, a ...any)
	mu   sync.Mutex
	stop context.CancelFunc
}

// SetEnabled starts the helper (once) or stops it.
func (r *Runner) SetEnabled(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !on || !Available() {
		if r.stop != nil {
			r.stop()
			r.stop = nil
		}
		return
	}
	if r.stop != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.stop = cancel
	go r.loop(ctx)
}

// loop keeps the helper running, restarting it after a crash (not after more than 5 in a row).
func (r *Runner) loop(ctx context.Context) {
	path, err := r.install()
	if err != nil {
		r.logf("menu bar icon: %v", err)
		return
	}
	pidFile := filepath.Join(r.Dir, "menubar.pid")
	killStale(pidFile)
	killOrphans()
	for crashes := 0; ctx.Err() == nil && crashes < 5; {
		cmd := exec.CommandContext(ctx, path)
		cmd.Env = append(os.Environ(), r.Env...)
		started := time.Now()
		if err := cmd.Start(); err != nil {
			r.logf("menu bar icon: %v", err)
			return
		}
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
		err := cmd.Wait()
		_ = os.Remove(pidFile)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			return // it quit on its own ("Hide this icon")
		}
		if time.Since(started) > time.Minute {
			crashes = 0
		}
		crashes++
		r.logf("menu bar icon exited: %v", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(crashes) * 2 * time.Second):
		}
	}
}

// killStale stops a helper left by an earlier dashboard process (one replaced by an update or a
// rebuild keeps no handle on it), so there is never a second icon.
func killStale(pidFile string) {
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return
	}
	out, err := exec.Command("/bin/ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err == nil && strings.HasSuffix(strings.TrimSpace(string(out)), "ColimaDashboardMenu") {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	}
	_ = os.Remove(pidFile)
}

// killOrphans stops any helper whose dashboard has died (parent pid 1). Older helpers did not
// notice their parent going away, and the pid file only remembers the latest one.
func killOrphans() {
	out, err := exec.Command("/bin/ps", "-axo", "pid=,ppid=,comm=").Output()
	if err != nil {
		return
	}
	for _, pid := range orphanPids(string(out)) {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	}
}

// orphanPids picks ColimaDashboardMenu processes reparented to launchd out of `ps -o pid,ppid,comm`.
func orphanPids(ps string) []int {
	var pids []int
	for _, line := range strings.Split(ps, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[1] != "1" || !strings.HasSuffix(strings.Join(f[2:], " "), "/ColimaDashboardMenu") {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil && pid > 1 {
			pids = append(pids, pid)
		}
	}
	return pids
}

// install writes the helper when it is missing or different from this build's.
func (r *Runner) install() (string, error) {
	path := filepath.Join(r.Dir, "ColimaDashboardMenu")
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, helper) {
		return path, nil
	}
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return "", err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, helper, 0o700); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}

func (r *Runner) logf(format string, a ...any) {
	if r.Logf != nil {
		r.Logf(format, a...)
	}
}
