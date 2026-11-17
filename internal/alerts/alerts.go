// Package alerts keeps the dashboard's recent alerts (a test container failed, a build failed),
// shows them as macOS notifications when that is on, and holds the menu bar and notification
// settings.
package alerts

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Alert is one thing worth telling the developer about.
type Alert struct {
	Time    time.Time `json:"time"`
	Profile string    `json:"profile"`
	Kind    string    `json:"kind"` // "run" or "build"
	Title   string    `json:"title"`
	Body    string    `json:"body"`
}

// Settings are kept in alerts.json next to the session token.
type Settings struct {
	Notify  bool `json:"notify"`  // macOS notifications for failed runs and builds
	MenuBar bool `json:"menuBar"` // the menu bar icon
}

// Center holds recent alerts and the settings.
type Center struct {
	Path string // settings file
	// Notifier shows a notification; nil uses osascript. Tests replace it.
	Notifier func(title, body string) error
	// OnSettings is called after the settings change (main starts or stops the menu bar icon).
	OnSettings func(Settings)

	mu       sync.Mutex
	settings Settings
	recent   []Alert
	unread   int
	lastNote time.Time
}

// Load reads the settings; a missing file means both on.
func (c *Center) Load() Settings {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings = Settings{Notify: true, MenuBar: true}
	if b, err := os.ReadFile(c.Path); err == nil {
		_ = json.Unmarshal(b, &c.settings)
	}
	return c.settings
}

// Settings returns the current settings.
func (c *Center) Settings() Settings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.settings
}

// Save stores new settings and calls OnSettings.
func (c *Center) Save(s Settings) error {
	c.mu.Lock()
	c.settings = s
	c.mu.Unlock()
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(c.Path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if c.OnSettings != nil {
		c.OnSettings(s)
	}
	return nil
}

// Add records an alert and, when notifications are on, shows it (at most one every 5 seconds,
// so a test suite failing at once does not flood the screen).
func (c *Center) Add(a Alert) {
	if a.Time.IsZero() {
		a.Time = time.Now()
	}
	c.mu.Lock()
	c.recent = append([]Alert{a}, c.recent...)
	if len(c.recent) > 50 {
		c.recent = c.recent[:50]
	}
	c.unread++
	show := c.settings.Notify && time.Since(c.lastNote) > 5*time.Second
	if show {
		c.lastNote = time.Now()
	}
	notifier := c.Notifier
	c.mu.Unlock()
	if !show {
		return
	}
	if notifier == nil {
		notifier = osascriptNotify
	}
	go func() { _ = notifier(a.Title, a.Body) }()
}

// Recent returns the newest alerts first and how many are unread.
func (c *Center) Recent() ([]Alert, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Alert{}, c.recent...), c.unread
}

// MarkRead clears the unread count.
func (c *Center) MarkRead() {
	c.mu.Lock()
	c.unread = 0
	c.mu.Unlock()
}

// osascriptNotify shows a notification. The text goes in as arguments, never into the script.
func osascriptNotify(title, body string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/bin/osascript",
		"-e", "on run argv", "-e", "display notification (item 2 of argv) with title \"Colima Dashboard\" subtitle (item 1 of argv)", "-e", "end run",
		title, body).Run()
}
