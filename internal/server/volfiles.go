package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
)

// Volume files are read inside the VM, where the volume's data lives, as root (sudo): no helper
// container or image is needed, so it works for any volume, even with no network.

// volumePath resolves the request's volume and path to a folder or file inside the VM.
func (s *Server) volumePath(w http.ResponseWriter, r *http.Request) (vmPath, rel string, ok bool) {
	dc := s.client(w, r)
	if dc == nil {
		return "", "", false
	}
	rel, err := cleanPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return "", "", false
	}
	v, err := dc.Volume(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return "", "", false
	}
	if v.Mountpoint == "" || !strings.HasPrefix(v.Mountpoint, "/") {
		writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("volume %s has no local mount point (driver %s)", v.Name, v.Driver))
		return "", "", false
	}
	return path.Join(v.Mountpoint, rel), rel, true
}

// vmOutput runs a script as root inside the VM with args as $1..., returning stdout.
func (s *Server) vmOutput(ctx context.Context, profile, script string, max int, args ...string) ([]byte, error) {
	cmd, err := s.opt.Colima.SSHCommand(ctx, profile, append([]string{"sudo", "sh", "-c", script, "sh"}, args...)...)
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out, nil
}

// GET /api/profiles/{p}/volumes/{name}/files?path=/ -> {path, entries, truncated}
func (s *Server) apiVolumeFiles(w http.ResponseWriter, r *http.Request) {
	dir, rel, ok := s.volumePath(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := s.vmOutput(ctx, r.PathValue("p"), docker.ListScript, 0, dir)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	entries, truncated := docker.ParseListing(out)
	writeJSON(w, http.StatusOK, map[string]any{"path": rel, "entries": entries, "truncated": truncated})
}

// The size on the first line, then up to $2 bytes of the file.
const volReadScript = `[ -f "$1" ] || { echo "not a regular file" >&2; exit 2; }
stat -L -c %s -- "$1" && head -c "$2" -- "$1"`

// GET /api/profiles/{p}/volumes/{name}/file?path=/x -> {text, size, truncated, binary}
func (s *Server) apiVolumeFile(w http.ResponseWriter, r *http.Request) {
	file, rel, ok := s.volumePath(w, r)
	if !ok {
		return
	}
	const limit = 512 << 10
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := s.vmOutput(ctx, r.PathValue("p"), volReadScript, 0, file, strconv.Itoa(limit))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	nl := bytes.IndexByte(out, '\n')
	if nl < 0 {
		writeError(w, http.StatusBadGateway, errors.New("unexpected answer from the VM"))
		return
	}
	size, _ := strconv.ParseInt(string(out[:nl]), 10, 64)
	data := out[nl+1:]
	bin := !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0
	text := ""
	if !bin {
		text = string(data)
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": rel, "text": text, "size": size, "truncated": size > int64(len(data)), "binary": bin})
}

// GET /api/profiles/{p}/volumes/{name}/download?path=/x&kind=file|dir -> the file, or a .tar.
func (s *Server) apiVolumeDownload(w http.ResponseWriter, r *http.Request) {
	target, rel, ok := s.volumePath(w, r)
	if !ok {
		return
	}
	name := path.Base(rel)
	if rel == "/" {
		name = r.PathValue("name")
	}
	var args []string
	if r.URL.Query().Get("kind") == "file" {
		args = []string{"sudo", "sh", "-c", `[ -f "$1" ] || { echo "not a regular file" >&2; exit 2; }; exec cat -- "$1"`, "sh", target}
	} else {
		args = []string{"sudo", "tar", "-C", path.Dir(target), "-cf", "-", "--", path.Base(target)}
		name += ".tar"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	cmd, err := s.opt.Colima.SSHCommand(ctx, r.PathValue("p"), args...)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := cmd.Start(); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	// Wait for the first bytes before sending headers, so an error can still be reported as one.
	first := make([]byte, 32<<10)
	n, rerr := io.ReadAtLeast(stdout, first, 1)
	if n == 0 {
		werr := cmd.Wait()
		msg := strings.TrimSpace(stderr.String())
		if msg == "" && werr != nil {
			msg = werr.Error()
		}
		if msg == "" && rerr != nil && !errors.Is(rerr, io.EOF) {
			msg = rerr.Error()
		}
		if msg != "" {
			writeError(w, http.StatusBadRequest, errors.New(msg))
			return
		}
	}
	ctype := "application/octet-stream"
	if strings.HasSuffix(name, ".tar") {
		ctype = "application/x-tar"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	_, _ = w.Write(first[:n])
	_, _ = io.Copy(w, stdout)
	_ = cmd.Wait()
}
