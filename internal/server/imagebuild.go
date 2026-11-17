package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
)

// dockerCLIJob runs the docker CLI against the profile's engine as a job. Build and push use the
// CLI rather than the engine API so BuildKit, your credential helper and ~/.docker/config.json
// behave exactly as they do in Terminal.
func (s *Server) dockerCLIJob(w http.ResponseWriter, p, title string, timeout time.Duration, args ...string) {
	if _, err := s.dockerFor(p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	bin, err := exec.LookPath("docker")
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("the docker CLI is not installed: brew install docker docker-buildx"))
		return
	}
	sock := filepath.Join(s.opt.ColimaHome, p, "docker.sock")
	job := s.jobs.Start(title, p, func(j *Job) error {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = append(os.Environ(), "DOCKER_HOST=unix://"+sock)
		cmd.Stdout, cmd.Stderr = j, j
		j.Line("$ docker " + strings.Join(args, " "))
		return cmd.Run()
	})
	writeJSON(w, http.StatusAccepted, job.snapshot())
}

// POST /api/profiles/{p}/images/build {dir, tag, dockerfile}: build a folder on the Mac.
func (s *Server) apiBuild(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Dir        string `json:"dir"`
		Tag        string `json:"tag"`
		Dockerfile string `json:"dockerfile"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	args, err := buildArgs(body.Dir, body.Tag, body.Dockerfile)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	title := "build " + filepath.Base(args[len(args)-1])
	if body.Tag != "" {
		title = "build " + body.Tag
	}
	s.dockerCLIJob(w, r.PathValue("p"), title, time.Hour, args...)
}

// buildArgs checks a build request and returns the docker CLI arguments for it.
func buildArgs(dir, tag, dockerfile string) ([]string, error) {
	dir = strings.TrimSpace(dir)
	if strings.HasPrefix(dir, "~/") {
		dir = filepath.Join(homeDir(), dir[2:])
	}
	if !filepath.IsAbs(dir) {
		return nil, errors.New("give the folder as a full path, such as ~/code/app or /Users/you/code/app")
	}
	dir = filepath.Clean(dir)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a folder on this Mac", dir)
	}
	args := []string{"build", "--progress=plain"}
	if tag = strings.TrimSpace(tag); tag != "" {
		if !docker.ValidRef(tag) || strings.HasPrefix(tag, "-") {
			return nil, fmt.Errorf("invalid image name %q", tag)
		}
		args = append(args, "-t", tag)
	}
	if dockerfile = strings.TrimSpace(dockerfile); dockerfile != "" {
		f := dockerfile
		if !filepath.IsAbs(f) {
			f = filepath.Join(dir, f)
		}
		if fi, err := os.Stat(f); err != nil || fi.IsDir() {
			return nil, fmt.Errorf("%s is not a file", f)
		}
		args = append(args, "-f", f)
	} else if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err != nil {
		if _, err2 := os.Stat(filepath.Join(dir, "Containerfile")); err2 != nil {
			return nil, fmt.Errorf("no Dockerfile in %s; name the file to use", dir)
		}
	}
	return append(args, dir), nil
}

// POST /api/profiles/{p}/images/push {ref}: push with the Mac's registry credentials.
func (s *Server) apiPush(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref string `json:"ref"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !docker.ValidRef(body.Ref) || strings.HasPrefix(body.Ref, "-") || strings.HasPrefix(body.Ref, "sha256:") {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid image reference %q", body.Ref))
		return
	}
	s.dockerCLIJob(w, r.PathValue("p"), "push "+body.Ref, time.Hour, "push", body.Ref)
}
