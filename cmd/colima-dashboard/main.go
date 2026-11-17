// Command colima-dashboard serves a local web UI for Colima on http://127.0.0.1:7766.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/history"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/metrics"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/server"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/update"
	"github.com/andrewbakercloudscale/colima-dashboard/web"
)

// version is set at build time with -ldflags "-X main.version=..."
var version = "dev"

const defaultPort = 7766

func usage() {
	fmt.Fprintf(os.Stderr, `Colima Dashboard %s: a local web UI for Colima.

usage:
  colima-dashboard [--port N] [--no-browser] [--new-token]
                                        start the UI and open it in the browser
  colima-dashboard open [--port N]             open the running UI in the browser
  colima-dashboard install-service [--port N]  start the UI at login (LaunchAgent)
  colima-dashboard uninstall-service           remove the LaunchAgent
  colima-dashboard version

The UI listens on 127.0.0.1 only. The sign-in token is kept in %s
(this user only) and reused across restarts; --new-token replaces it and signs out every browser.
`, version, tokenPath())
}

func main() {
	log.SetFlags(log.LstdFlags)
	extendPath()
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	fl := flag.NewFlagSet("colima-dashboard", flag.ExitOnError)
	fl.Usage = usage
	port := fl.Int("port", defaultPort, "port on 127.0.0.1")
	noBrowser := fl.Bool("no-browser", false, "do not open the browser")
	rotate := fl.Bool("new-token", false, "replace the saved sign-in token (signs out every browser)")
	_ = fl.Parse(args)
	if *port < 1 || *port > 65535 {
		fatal(fmt.Errorf("invalid port %d", *port))
	}

	var err error
	switch cmd {
	case "":
		err = serve(*port, !*noBrowser, *rotate)
	case "open":
		err = openRunning(*port)
	case "install-service":
		err = installService(*port)
	case "uninstall-service":
		err = uninstallService()
	case "version":
		fmt.Println("colima-dashboard", version)
	case "help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "colima-dashboard:", err)
	os.Exit(1)
}

// extendPath adds Homebrew's and the user's bin directories: launchd starts agents with a
// bare PATH, so without this a LaunchAgent cannot find colima, docker or limactl.
func extendPath() {
	path := os.Getenv("PATH")
	home, _ := os.UserHomeDir()
	for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(home, ".local", "bin")} {
		if !strings.Contains(":"+path+":", ":"+dir+":") {
			path += ":" + dir
		}
	}
	_ = os.Setenv("PATH", path)
}

func colimaHome() string {
	if h := os.Getenv("COLIMA_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	dot := filepath.Join(home, ".colima")
	if _, err := os.Stat(dot); err == nil {
		return dot
	}
	// Colima falls back to the XDG config folder when ~/.colima does not exist.
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	if alt := filepath.Join(xdg, "colima"); dirExists(alt) {
		return alt
	}
	return dot
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func tokenPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "colima-dashboard", "session")
}

func serve(port int, browser, newToken bool) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("port %d is already in use; is colima-dashboard already running? Try 'colima-dashboard open', or pass --port", port)
		}
		return err
	}
	bin, err := exec.LookPath("colima")
	if err != nil {
		bin = "colima" // the page reports it missing, with the fix
	}
	token, err := loadOrCreateToken(newToken)
	if err != nil {
		return err
	}

	restart := make(chan struct{}, 1)
	exe, _ := os.Executable()
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	col := colima.New(bin)
	upd := &update.Updater{
		Current: version, Exe: exe, ColimaBin: bin,
		SettingsPath:  filepath.Join(filepath.Dir(tokenPath()), "settings.json"),
		ColimaVersion: col.Version,
		Restart: func() {
			select {
			case restart <- struct{}{}:
			default:
			}
		},
	}
	upd.Load()

	hub := metrics.NewHub(bin, colimaHome())
	var rec *history.Recorder
	if store, err := history.Open(filepath.Join(filepath.Dir(tokenPath()), "history")); err != nil {
		log.Printf("history is off: %v", err)
	} else {
		rec = &history.Recorder{Store: store, ColimaHome: colimaHome(), Guest: hub.GuestAverage, Logf: log.Printf}
	}

	handler := server.New(server.Options{
		Port: port, Token: token, Version: version, Updater: upd, Metrics: hub, History: rec,
		Colima: col, ColimaHome: colimaHome(), Assets: web.Assets,
		// The locked page's button: open the signed-in link in this user's own browser session.
		OpenBrowser: func() error { return exec.Command("open", server.BaseURL(port)+"/?token="+token).Run() },
	})
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	// The friendly name when /etc/hosts has it (install.sh adds it), else 127.0.0.1.
	url := server.BaseURL(port) + "/?token=" + token
	fmt.Printf("Colima Dashboard %s on %s\nOpen: %s\n", version, server.BaseURL(port), url)
	if browser {
		go func() {
			time.Sleep(300 * time.Millisecond)
			_ = exec.Command("open", url).Run()
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	go upd.Run(ctx)
	if rec != nil {
		go rec.Run(ctx)
	}
	// SIGHUP: the binary was rebuilt in place (the Rebuild button does this); restart into it.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		<-hup
		select {
		case restart <- struct{}{}:
		default:
		}
	}()
	restarting := false
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	case <-restart:
		restarting = true
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdown)
	if restarting {
		// A new version is in place: become it, keeping the same arguments and process id
		// (launchd keeps tracking it). Open pages reconnect by themselves.
		log.Printf("restarting into the updated %s", exe)
		args := append([]string{exe}, os.Args[1:]...)
		if !containsArg(args, "--no-browser") {
			args = append(args, "--no-browser")
		}
		return syscall.Exec(exe, args, os.Environ())
	}
	return err
}

func containsArg(args []string, a string) bool {
	for _, x := range args {
		if x == a || x == "-"+strings.TrimPrefix(a, "--") {
			return true
		}
	}
	return false
}

// loadOrCreateToken keeps one token across restarts, so a restart does not lock open tabs.
// The file is readable by this user only; anything that can read it can already use the socket.
func loadOrCreateToken(rotate bool) (string, error) {
	if !rotate {
		if b, err := os.ReadFile(tokenPath()); err == nil {
			if t := strings.TrimSpace(string(b)); len(t) == 64 {
				return t, nil
			}
		}
	}
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		return "", err
	}
	t := hex.EncodeToString(tok)
	if err := saveToken(t); err != nil {
		return "", fmt.Errorf("saving the sign-in token: %w", err)
	}
	return t, nil
}

func saveToken(token string) error {
	p := tokenPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(token+"\n"), 0o600)
}

func openRunning(port int) error {
	b, err := os.ReadFile(tokenPath())
	if err != nil {
		return fmt.Errorf("colima-dashboard does not seem to be running (no %s)", tokenPath())
	}
	url := server.BaseURL(port) + "/?token=" + strings.TrimSpace(string(b))
	return exec.Command("open", url).Run()
}
