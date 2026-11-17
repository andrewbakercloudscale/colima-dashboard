package server

import (
	"errors"
	"testing"
	"time"
)

func TestJobWriterSplitsLines(t *testing.T) {
	js := NewJobs()
	release := make(chan struct{})
	j := js.Start("t", "", func(j *Job) error {
		_, _ = j.Write([]byte("one\ntw"))
		_, _ = j.Write([]byte("o\r\nthree"))
		<-release
		return errors.New("boom")
	})
	time.Sleep(20 * time.Millisecond)
	lines, next, done, _, changed := j.Since(0)
	if done || next != 2 || len(lines) != 2 || lines[1] != "two" {
		t.Fatalf("before finish: %q next=%d done=%v", lines, next, done)
	}
	close(release)
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("no change signalled")
	}
	time.Sleep(10 * time.Millisecond)
	lines, _, done, errText, _ := j.Since(2)
	if !done || errText != "boom" || len(lines) != 1 || lines[0] != "three" {
		t.Fatalf("after finish: %q done=%v err=%q", lines, done, errText)
	}
}
