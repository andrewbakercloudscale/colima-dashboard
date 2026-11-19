package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
)

// Docker does not record when an image was last used, so it is worked out from the containers
// made from it: now for a running one, otherwise when the latest one stopped. What is found is
// also saved per profile, so an image keeps its date after its containers are removed.

type lastUse struct {
	At int64  `json:"at"` // unix seconds
	By string `json:"by"` // container name
}

var lastUsed = struct {
	sync.Mutex
	byProfile map[string]map[string]lastUse // profile -> image ID -> last use
	// container ID -> when it stopped. A stopped container can be started again, so an entry is
	// dropped when the container is seen running and is re-read after stoppedRecheck anyway.
	finished map[string]stopSeen
}{byProfile: map[string]map[string]lastUse{}, finished: map[string]stopSeen{}}

type stopSeen struct{ at, checked int64 }

const stoppedRecheck = 5 * 60 // seconds

func (s *Server) lastUsedFile(p string) string {
	if s.opt.DataDir == "" {
		return ""
	}
	return filepath.Join(s.opt.DataDir, "lastused", p+".json")
}

// lastUsedFor returns the profile's saved map, reading it from disk once; call with lastUsed held.
func (s *Server) lastUsedFor(p string) map[string]lastUse {
	if m := lastUsed.byProfile[p]; m != nil {
		return m
	}
	m := map[string]lastUse{}
	if f := s.lastUsedFile(p); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			_ = json.Unmarshal(b, &m)
		}
	}
	lastUsed.byProfile[p] = m
	return m
}

// stoppedAt is when a stopped container last stopped (or started, or was made).
func stoppedAt(ctx context.Context, dc *docker.Client, c docker.Container) int64 {
	lastUsed.Lock()
	seen, ok := lastUsed.finished[c.ID]
	lastUsed.Unlock()
	if ok && time.Now().Unix()-seen.checked < stoppedRecheck {
		return seen.at
	}
	at := c.Created
	info, err := dc.Inspect(ctx, c.ID)
	if err != nil {
		return at
	}
	if st, _ := info["State"].(map[string]any); st != nil {
		for _, k := range []string{"FinishedAt", "StartedAt"} {
			v, _ := st[k].(string)
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil && t.Unix() > at {
				at = t.Unix()
			}
		}
	}
	lastUsed.Lock()
	lastUsed.finished[c.ID] = stopSeen{at: at, checked: time.Now().Unix()}
	lastUsed.Unlock()
	return at
}

// fillLastUsed sets LastUsed, LastUsedBy and Running on each image.
func (s *Server) fillLastUsed(ctx context.Context, p string, dc *docker.Client, cs []docker.Container, imgs []docker.Image) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	now := time.Now().Unix()
	found := map[string]lastUse{}
	running := map[string]bool{}
	for i, c := range cs {
		at := now
		if c.State == "running" {
			running[c.ImageID] = true
			lastUsed.Lock()
			delete(lastUsed.finished, c.ID) // it will stop at a new time
			lastUsed.Unlock()
		} else if i < 300 && ctx.Err() == nil {
			at = stoppedAt(ctx, dc, c)
		} else {
			at = c.Created
		}
		name := strings.TrimPrefix(firstName(c.Names), "/")
		if prev, ok := found[c.ImageID]; !ok || at > prev.At {
			found[c.ImageID] = lastUse{At: at, By: name}
		}
	}
	lastUsed.Lock()
	saved := s.lastUsedFor(p)
	changed := false
	for id, u := range found {
		// Running images are saved as "now" so the date survives the container going away.
		if prev, ok := saved[id]; !ok || u.At > prev.At+60 || (u.At > prev.At && !running[id]) {
			saved[id] = u
			changed = true
		}
	}
	live := map[string]bool{}
	for i := range imgs {
		id := imgs[i].ID
		live[id] = true
		if u, ok := saved[id]; ok {
			imgs[i].LastUsed, imgs[i].LastUsedBy = u.At, u.By
		}
		imgs[i].Running = running[id]
	}
	for id := range saved {
		if !live[id] {
			delete(saved, id) // the image is gone
			changed = true
		}
	}
	var out []byte
	if changed {
		out, _ = json.Marshal(saved)
	}
	lastUsed.Unlock()
	if f := s.lastUsedFile(p); out != nil && f != "" {
		if os.MkdirAll(filepath.Dir(f), 0o700) == nil {
			_ = os.WriteFile(f, out, 0o600)
		}
	}
}
