// Package history keeps 30 days of what happened in each Colima profile, on disk, so the
// dashboard can show trends: every container run (how long, how it ended), every image build,
// and a sample of the VM's cost and disk use every five minutes.
//
// Records are JSON lines, one file per kind per profile, under
// ~/Library/Application Support/colima-dashboard/history/<profile>/. Nothing leaves the Mac.
package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Keep is how long records are kept.
const Keep = 30 * 24 * time.Hour

// Run is one container run, recorded when the container stops.
type Run struct {
	T       time.Time `json:"t"` // when it stopped
	Started time.Time `json:"started,omitzero"`
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Image   string    `json:"image"`
	Project string    `json:"project,omitempty"` // Compose project
	Service string    `json:"service,omitempty"` // Compose service
	Job     string    `json:"job"`               // what runs are grouped by: project/service, else the image
	Exit    int       `json:"exit"`
	// Outcome: "pass" (exit 0 on its own), "fail" (non-zero on its own), "oom" (killed for memory)
	// or "stopped" (someone stopped it: docker stop, compose down).
	Outcome   string  `json:"outcome"`
	Seconds   float64 `json:"seconds"`
	Restarted bool    `json:"restarted,omitempty"` // a restart policy started it again
	Unhealthy bool    `json:"unhealthy,omitempty"` // its health check failed during the run
}

// Build is one image build, from BuildKit's build history.
type Build struct {
	T       time.Time `json:"t"` // when it finished
	Started time.Time `json:"started,omitzero"`
	Ref     string    `json:"ref"`
	Name    string    `json:"name"`
	Status  string    `json:"status"` // Completed, Error, Canceled
	Seconds float64   `json:"seconds"`
	Steps   int       `json:"steps"`
	Cached  int       `json:"cached"`
	Error   string    `json:"error,omitempty"`
}

// Point is a sample of the profile's cost and disk use.
type Point struct {
	T        time.Time `json:"t"`
	HostCPU  *float64  `json:"hostCpu,omitempty"` // % of one Mac core, averaged since the last point
	HostMem  uint64    `json:"hostMem,omitempty"` // bytes, physical footprint of the VM on the Mac
	GuestCPU *float64  `json:"guestCpu,omitempty"`
	GuestMem uint64    `json:"guestMem,omitempty"`
	DiskMac  uint64    `json:"diskMac,omitempty"`  // bytes the VM's disk image takes on the Mac
	DiskSize uint64    `json:"diskSize,omitempty"` // the VM disk's size
	Images   *int64    `json:"images,omitempty"`   // Docker's disk use, when sampled
	Contain  *int64    `json:"containers,omitempty"`
	Volumes  *int64    `json:"volumes,omitempty"`
	Cache    *int64    `json:"buildCache,omitempty"`
	Running  int       `json:"running"` // running containers
}

// Store reads and appends records.
type Store struct {
	dir string
	mu  sync.Mutex
}

// Open returns a store rooted at dir, creating it.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) file(profile, kind string) string {
	return filepath.Join(s.dir, profile, kind+".jsonl")
}

func (s *Store) append(profile, kind string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(s.dir, profile), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.file(profile, kind), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// AddRun records a finished run.
func (s *Store) AddRun(profile string, r Run) error { return s.append(profile, "runs", r) }

// AddBuild records a finished build.
func (s *Store) AddBuild(profile string, b Build) error { return s.append(profile, "builds", b) }

// AddPoint records a sample.
func (s *Store) AddPoint(profile string, p Point) error { return s.append(profile, "points", p) }

// Runs returns runs that stopped after since, oldest first.
func (s *Store) Runs(profile string, since time.Time) []Run {
	return load(s, profile, "runs", since, func(r Run) time.Time { return r.T })
}

// Builds returns builds that finished after since, oldest first.
func (s *Store) Builds(profile string, since time.Time) []Build {
	return load(s, profile, "builds", since, func(b Build) time.Time { return b.T })
}

// Points returns samples taken after since, oldest first.
func (s *Store) Points(profile string, since time.Time) []Point {
	return load(s, profile, "points", since, func(p Point) time.Time { return p.T })
}

func load[T any](s *Store, profile, kind string, since time.Time, at func(T) time.Time) []T {
	s.mu.Lock()
	b, err := os.ReadFile(s.file(profile, kind))
	s.mu.Unlock()
	out := []T{}
	if err != nil {
		return out
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var v T
		if json.Unmarshal(sc.Bytes(), &v) != nil { // a torn last line after a crash: skip it
			continue
		}
		if at(v).After(since) {
			out = append(out, v)
		}
	}
	return out
}

// Prune drops records older than Keep from every file.
func (s *Store) Prune(now time.Time) {
	cut := now.Add(-Keep)
	dirs, _ := os.ReadDir(s.dir)
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		s.prune(d.Name(), "runs", cut, func(b []byte) time.Time { var r Run; _ = json.Unmarshal(b, &r); return r.T })
		s.prune(d.Name(), "builds", cut, func(b []byte) time.Time { var r Build; _ = json.Unmarshal(b, &r); return r.T })
		s.prune(d.Name(), "points", cut, func(b []byte) time.Time { var r Point; _ = json.Unmarshal(b, &r); return r.T })
	}
}

func (s *Store) prune(profile, kind string, cut time.Time, at func([]byte) time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := s.file(profile, kind)
	b, err := os.ReadFile(name)
	if err != nil {
		return
	}
	var keep bytes.Buffer
	dropped := false
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		if !at(line).After(cut) {
			dropped = true
			continue
		}
		keep.Write(line)
		keep.WriteByte('\n')
	}
	if !dropped {
		return
	}
	tmp := name + ".tmp"
	if os.WriteFile(tmp, keep.Bytes(), 0o600) == nil {
		_ = os.Rename(tmp, name)
	}
}
