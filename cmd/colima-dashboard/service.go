package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

const serviceLabel = "io.github.colima-dashboard"

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist")
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// installService writes a LaunchAgent that starts the UI at login (no browser) and loads it now.
func installService(port int) error {
	p, logPath, err := writePlist(port)
	if err != nil {
		return err
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	_ = exec.Command("launchctl", "bootout", domain+"/"+serviceLabel).Run() // replace an older copy
	if out, err := exec.Command("launchctl", "bootstrap", domain, p).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, bytes.TrimSpace(out))
	}
	fmt.Printf("Installed %s\nColima Dashboard now starts at login on port %d. Open it with: colima-dashboard open\nLog: %s\n", p, port, logPath)
	return nil
}

// loginItem is the page's "Start at login" switch. It only writes or removes the LaunchAgent
// file, which launchd reads at the next login: loading or unloading it now could stop this very
// process, or start a second copy on the same port.
type loginItem struct{ port int }

func (l loginItem) Enabled() bool {
	_, err := os.Stat(plistPath())
	return err == nil
}

func (l loginItem) SetEnabled(on bool) error {
	if on {
		_, _, err := writePlist(l.port)
		return err
	}
	if err := os.Remove(plistPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// writePlist writes the LaunchAgent file for this binary and returns its path and log path.
func writePlist(port int) (string, string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", "", err
	}
	home, _ := os.UserHomeDir()
	logPath := filepath.Join(home, "Library", "Logs", "colima-dashboard.log")
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>` + serviceLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlEscape(exe) + `</string>
		<string>--no-browser</string>
		<string>--port</string>
		<string>` + strconv.Itoa(port) + `</string>
	</array>
	<key>RunAtLoad</key><true/>
	<key>ProcessType</key><string>Background</string>
	<key>StandardOutPath</key><string>` + xmlEscape(logPath) + `</string>
	<key>StandardErrorPath</key><string>` + xmlEscape(logPath) + `</string>
</dict>
</plist>
`
	p := plistPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", "", err
	}
	return p, logPath, os.WriteFile(p, []byte(plist), 0o644)
}

func uninstallService() error {
	domain := "gui/" + strconv.Itoa(os.Getuid())
	_ = exec.Command("launchctl", "bootout", domain+"/"+serviceLabel).Run()
	if err := os.Remove(plistPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Println("Removed the Colima Dashboard LaunchAgent.")
	return nil
}
