// Package snapshot saves and restores a stopped Colima profile: its VM disk, its data disk (where
// Docker keeps images, containers and volumes) and its settings. Copies are APFS clones (cp -c),
// so a snapshot takes a second and only the blocks that change afterwards cost disk space.
package snapshot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Snapshot describes one saved state of a profile.
type Snapshot struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Note    string    `json:"note,omitempty"` // what it is for, in the user's words
	Profile string    `json:"profile"`
	Created time.Time `json:"created"`
	// Size is the total logical size of the files; clones share blocks, so the disk space
	// really used is usually far smaller.
	Size  int64    `json:"size"`
	Files []string `json:"files"`
	Auto  bool     `json:"auto,omitempty"` // taken by the dashboard before a restore
}

// Store keeps snapshots under Dir/<profile>/<id>/.
type Store struct {
	Dir        string
	ColimaHome string
	// Instance maps a profile to its Lima instance name ("colima" or "colima-<profile>").
	Instance func(profile string) string
	// Clone copies src to dst as an APFS clone; tests replace it.
	Clone func(ctx context.Context, src, dst string) error
}

// ErrNoClone means the disk does not support clones (not APFS), so a snapshot would be a full copy.
var ErrNoClone = errors.New("this disk cannot make clones (APFS is needed), so snapshots are not offered")

// CloneFile clones src to dst with macOS cp -c, which never falls back to a full copy silently:
// it fails when the file system cannot clone.
func CloneFile(ctx context.Context, src, dst string) error {
	out, err := exec.CommandContext(ctx, "/bin/cp", "-c", "-p", src, dst).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "not supported") {
			return ErrNoClone
		}
		return fmt.Errorf("cp -c %s: %s", filepath.Base(src), msg)
	}
	return nil
}

// sources lists the files that make up a profile: path inside the snapshot -> live path.
func (s *Store) sources(profile string) (map[string]string, error) {
	inst := s.Instance(profile)
	out := map[string]string{}
	add := func(prefix, dir string, skip func(string) bool) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || skip(e.Name()) {
				continue
			}
			out[prefix+"/"+e.Name()] = filepath.Join(dir, e.Name())
		}
		return nil
	}
	vmSkip := func(n string) bool {
		for _, suf := range []string{".log", ".sock", ".pid", ".tmp"} {
			if strings.HasSuffix(n, suf) {
				return true
			}
		}
		return strings.HasPrefix(n, "ssh.")
	}
	if err := add("vm", filepath.Join(s.ColimaHome, "_lima", inst), vmSkip); err != nil {
		return nil, fmt.Errorf("profile %s has no VM to save: %w", profile, err)
	}
	if err := add("data", filepath.Join(s.ColimaHome, "_lima", "_disks", inst), func(string) bool { return false }); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	cfg := filepath.Join(s.ColimaHome, profile, "colima.yaml")
	if _, err := os.Stat(cfg); err == nil {
		out["settings/colima.yaml"] = cfg
	}
	return out, nil
}

// List returns a profile's snapshots, newest first.
func (s *Store) List(profile string) ([]Snapshot, error) {
	dir := filepath.Join(s.Dir, profile)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []Snapshot{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Snapshot{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), "snapshot.json"))
		if err != nil {
			continue // a half-made snapshot: no metadata yet
		}
		var sn Snapshot
		if json.Unmarshal(b, &sn) == nil && sn.ID == e.Name() {
			out = append(out, sn)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// Take saves the profile's current files. The profile must be stopped (the caller checks).
func (s *Store) Take(ctx context.Context, w io.Writer, profile, name, note string, auto bool) (*Snapshot, error) {
	src, err := s.sources(profile)
	if err != nil {
		return nil, err
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	sn := &Snapshot{ID: time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b[:]), Name: name, Note: note, Profile: profile, Created: time.Now(), Auto: auto}
	dir := filepath.Join(s.Dir, profile, sn.ID)
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, rel := range keys {
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, err
		}
		fmt.Fprintf(w, "cloning %s\n", rel)
		if err := s.Clone(ctx, src[rel], dst); err != nil {
			return nil, err
		}
		if fi, err := os.Stat(dst); err == nil {
			sn.Size += fi.Size()
		}
		sn.Files = append(sn.Files, rel)
	}
	meta, _ := json.MarshalIndent(sn, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), meta, 0o600); err != nil {
		return nil, err
	}
	ok = true
	fmt.Fprintf(w, "saved snapshot %q (%d files)\n", name, len(sn.Files))
	return sn, nil
}

// Restore puts a snapshot's files back over the profile's. The profile must be stopped. It first
// takes an automatic snapshot of the current state, so a restore can itself be undone.
func (s *Store) Restore(ctx context.Context, w io.Writer, profile, id string) error {
	sn, err := s.get(profile, id)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "saving the current state first, so this restore can be undone")
	if _, err := s.Take(ctx, w, profile, "Before restoring "+sn.Name, "", true); err != nil {
		return fmt.Errorf("saving the current state: %w", err)
	}
	live, err := s.sources(profile)
	if err != nil {
		return err
	}
	inst := s.Instance(profile)
	target := func(rel string) string {
		if p, ok := live[rel]; ok {
			return p
		}
		dir, name := filepath.Split(rel)
		switch dir {
		case "vm/":
			return filepath.Join(s.ColimaHome, "_lima", inst, name)
		case "data/":
			return filepath.Join(s.ColimaHome, "_lima", "_disks", inst, name)
		default:
			return filepath.Join(s.ColimaHome, profile, name)
		}
	}
	for _, rel := range sn.Files {
		dst := target(rel)
		fmt.Fprintf(w, "restoring %s\n", rel)
		tmp := dst + ".restoring"
		_ = os.Remove(tmp)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := s.Clone(ctx, filepath.Join(s.Dir, profile, id, filepath.FromSlash(rel)), tmp); err != nil {
			return err
		}
		if err := os.Rename(tmp, dst); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "restored %q\n", sn.Name)
	return nil
}

// Delete removes a snapshot.
func (s *Store) Delete(profile, id string) error {
	if _, err := s.get(profile, id); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.Dir, profile, id))
}

func (s *Store) get(profile, id string) (*Snapshot, error) {
	if id == "" || strings.ContainsAny(id, `/\.`) {
		return nil, errors.New("invalid snapshot id")
	}
	b, err := os.ReadFile(filepath.Join(s.Dir, profile, id, "snapshot.json"))
	if err != nil {
		return nil, errors.New("no such snapshot")
	}
	var sn Snapshot
	if err := json.Unmarshal(b, &sn); err != nil {
		return nil, err
	}
	return &sn, nil
}
