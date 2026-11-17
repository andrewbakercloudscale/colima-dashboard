package alerts

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCenter(t *testing.T) {
	var mu sync.Mutex
	var shown []string
	c := &Center{Path: filepath.Join(t.TempDir(), "alerts.json"), Notifier: func(title, body string) error {
		mu.Lock()
		shown = append(shown, title)
		mu.Unlock()
		return nil
	}}
	if s := c.Load(); !s.Notify || !s.MenuBar {
		t.Fatalf("defaults %+v", s)
	}
	c.Add(Alert{Title: "a"})
	c.Add(Alert{Title: "b"}) // within 5 s: recorded, not shown
	time.Sleep(50 * time.Millisecond)
	list, unread := c.Recent()
	if len(list) != 2 || list[0].Title != "b" || unread != 2 {
		t.Fatalf("recent %+v %d", list, unread)
	}
	mu.Lock()
	if len(shown) != 1 {
		t.Fatalf("shown %v", shown)
	}
	mu.Unlock()
	c.MarkRead()
	var got Settings
	c.OnSettings = func(s Settings) { got = s }
	if err := c.Save(Settings{Notify: false, MenuBar: true}); err != nil || got.Notify {
		t.Fatal(err, got)
	}
	c2 := &Center{Path: c.Path}
	if s := c2.Load(); s.Notify || !s.MenuBar {
		t.Fatalf("not persisted: %+v", s)
	}
}
