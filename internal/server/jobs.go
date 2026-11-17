package server

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// JobInfo is what the page sees of a job.
type JobInfo struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Profile string    `json:"profile,omitempty"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitzero"`
	Done    bool      `json:"done"`
	Error   string    `json:"error,omitempty"`
}

// Job is a long-running operation (colima start, image pull) whose output the page follows.
// Output is kept so a page that reconnects, or opens later, sees it from the beginning.
type Job struct {
	JobInfo

	mu      sync.Mutex
	lines   []string
	partial []byte
	notify  chan struct{} // closed and replaced on every change
}

const maxJobLines = 5000

// Write makes a Job an io.Writer: complete lines are appended, a trailing partial line is held.
func (j *Job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	buf := append(j.partial, p...)
	start := 0
	for i, b := range buf {
		if b == '\n' || b == '\r' {
			if i > start {
				j.appendLocked(string(buf[start:i]))
			}
			start = i + 1
		}
	}
	j.partial = append([]byte(nil), buf[start:]...)
	j.signalLocked()
	return len(p), nil
}

// Line appends one line.
func (j *Job) Line(s string) {
	j.mu.Lock()
	j.appendLocked(s)
	j.signalLocked()
	j.mu.Unlock()
}

func (j *Job) appendLocked(s string) {
	j.lines = append(j.lines, s)
	if len(j.lines) > maxJobLines {
		j.lines = append([]string{"[earlier output trimmed]"}, j.lines[len(j.lines)-maxJobLines+1:]...)
	}
}

func (j *Job) signalLocked() {
	close(j.notify)
	j.notify = make(chan struct{})
}

func (j *Job) finish(err error) {
	j.mu.Lock()
	if len(j.partial) > 0 {
		j.appendLocked(string(j.partial))
		j.partial = nil
	}
	j.Done = true
	j.Ended = time.Now()
	if err != nil {
		j.Error = err.Error()
	}
	j.signalLocked()
	j.mu.Unlock()
}

// Since returns lines from index from onwards, whether the job is done, and a channel that closes
// on the next change.
func (j *Job) Since(from int) (lines []string, next int, done bool, errText string, changed <-chan struct{}) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if from < 0 || from > len(j.lines) {
		from = len(j.lines)
	}
	lines = append([]string(nil), j.lines[from:]...)
	return lines, len(j.lines), j.Done, j.Error, j.notify
}

func (j *Job) snapshot() JobInfo {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.JobInfo
}

// Jobs holds recent jobs; finished ones are dropped after an hour.
type Jobs struct {
	mu   sync.Mutex
	jobs map[string]*Job
}

// NewJobs returns an empty job list.
func NewJobs() *Jobs { return &Jobs{jobs: map[string]*Job{}} }

// Start runs fn in the background as a new job.
func (js *Jobs) Start(title, profile string, fn func(j *Job) error) *Job {
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	j := &Job{JobInfo: JobInfo{ID: hex.EncodeToString(id), Title: title, Profile: profile, Started: time.Now()}, notify: make(chan struct{})}
	js.mu.Lock()
	for k, old := range js.jobs {
		if s := old.snapshot(); s.Done && time.Since(s.Ended) > time.Hour {
			delete(js.jobs, k)
		}
	}
	js.jobs[j.ID] = j
	js.mu.Unlock()
	go func() {
		var err error
		defer func() {
			if r := recover(); r != nil {
				j.finish(errorf("internal error: %v", r))
				return
			}
			j.finish(err)
		}()
		err = fn(j)
	}()
	return j
}

// Get returns a job by id.
func (js *Jobs) Get(id string) *Job {
	js.mu.Lock()
	defer js.mu.Unlock()
	return js.jobs[id]
}

// List returns snapshots of all jobs.
func (js *Jobs) List() []JobInfo {
	js.mu.Lock()
	defer js.mu.Unlock()
	out := make([]JobInfo, 0, len(js.jobs))
	for _, j := range js.jobs {
		out = append(out, j.snapshot())
	}
	return out
}

// Running reports whether any job is still running (a self-update waits for them).
func (js *Jobs) Running() bool {
	js.mu.Lock()
	defer js.mu.Unlock()
	for _, j := range js.jobs {
		if !j.snapshot().Done {
			return true
		}
	}
	return false
}
