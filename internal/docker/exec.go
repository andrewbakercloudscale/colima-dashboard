package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	pathpkg "path"
	"strconv"
	"strings"
)

// ExecCreate prepares a command inside a running container and returns the exec id.
func (c *Client) ExecCreate(ctx context.Context, container string, cmd []string, tty bool) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"AttachStdin": tty, "AttachStdout": true, "AttachStderr": true, "Tty": tty, "Cmd": cmd,
		"Env": []string{"TERM=xterm-256color"},
	})
	resp, err := c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(container)+"/exec", nil, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct{ ID string }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// ExecAttach starts an exec and returns the raw two-way stream (the engine upgrades the HTTP
// connection). With a TTY the stream is the terminal's bytes as they are; the caller closes it.
func (c *Client) ExecAttach(ctx context.Context, execID string, tty bool) (io.ReadWriteCloser, error) {
	body, _ := json.Marshal(map[string]any{"Detach": false, "Tty": tty})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker/exec/"+url.PathEscape(execID)+"/start", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker engine not reachable at %s: %w", c.Socket, err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(b))}
	}
	rw, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		resp.Body.Close()
		return nil, errors.New("docker engine did not hand over the exec stream")
	}
	return rw, nil
}

// ExecResize sets the exec's terminal size.
func (c *Client) ExecResize(ctx context.Context, execID string, cols, rows int) error {
	q := url.Values{"w": {strconv.Itoa(cols)}, "h": {strconv.Itoa(rows)}}
	return c.send(ctx, http.MethodPost, "/exec/"+url.PathEscape(execID)+"/resize", q, nil)
}

// ExecExitCode reports whether the exec is still running and, once it is not, its exit code.
func (c *Client) ExecExitCode(ctx context.Context, execID string) (running bool, code int, err error) {
	var v struct {
		Running  bool
		ExitCode int
	}
	err = c.getJSON(ctx, "/exec/"+url.PathEscape(execID)+"/json", nil, &v)
	return v.Running, v.ExitCode, err
}

// ErrNoShell means the image has no /bin/sh (distroless and scratch images).
var ErrNoShell = errors.New("this container has no shell (/bin/sh), so its files cannot be listed")

// ExecOutput runs cmd without a TTY and returns stdout, stderr (each capped at limit bytes) and
// the exit code.
func (c *Client) ExecOutput(ctx context.Context, container string, cmd []string, limit int) (stdout, stderr []byte, code int, err error) {
	id, err := c.ExecCreate(ctx, container, cmd, false)
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") || strings.Contains(err.Error(), "no such file") {
			return nil, nil, -1, ErrNoShell
		}
		return nil, nil, -1, err
	}
	rw, err := c.ExecAttach(ctx, id, false)
	if err != nil {
		return nil, nil, -1, err
	}
	defer rw.Close()
	var o, e capped
	o.limit, e.limit = limit, limit
	if err := demuxRaw(rw, &o, &e); err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, -1, err
	}
	_, code, err = c.ExecExitCode(ctx, id)
	if code == 126 || code == 127 {
		if bytes.Contains(e.b.Bytes(), []byte("not found")) || bytes.Contains(e.b.Bytes(), []byte("no such file")) {
			return nil, nil, code, ErrNoShell
		}
	}
	return o.b.Bytes(), e.b.Bytes(), code, err
}

type capped struct {
	b     bytes.Buffer
	limit int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.limit - c.b.Len(); room > 0 {
		if len(p) > room {
			c.b.Write(p[:room])
		} else {
			c.b.Write(p)
		}
	}
	return len(p), nil
}

// demuxRaw splits Docker's multiplexed stream (8 byte header per frame) into stdout and stderr.
func demuxRaw(r io.Reader, stdout, stderr io.Writer) error {
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return io.EOF
			}
			return err
		}
		n := int64(binary.BigEndian.Uint32(hdr[4:]))
		w := stdout
		if hdr[0] == 2 {
			w = stderr
		}
		if _, err := io.CopyN(w, r, n); err != nil {
			return err
		}
	}
}

// Entry is one item in a container folder.
type Entry struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"` // "dir", "file", "link" (to a file or broken) or "dirlink" (to a folder)
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"` // unix seconds, 0 when unknown
}

// listScript prints one line per entry: kind, size, mtime, name, tab separated. The folder is
// passed as $1, never spliced into the script. stat -c works in GNU coreutils and BusyBox.
const listScript = `cd -- "$1" 2>/dev/null || { echo "no such folder: $1" >&2; exit 2; }
n=0
for f in .* *; do
  { [ "$f" = . ] || [ "$f" = .. ]; } && continue
  [ -e "$f" ] || [ -L "$f" ] || continue
  n=$((n+1)); [ "$n" -gt 2000 ] && { echo "..." ; break; }
  if [ -L "$f" ] && [ -d "$f" ]; then t=dirlink; elif [ -L "$f" ]; then t=link; elif [ -d "$f" ]; then t=dir; else t=file; fi
  s=$(stat -L -c %s -- "$f" 2>/dev/null || echo 0); m=$(stat -c %Y -- "$f" 2>/dev/null || echo 0)
  printf '%s\t%s\t%s\t%s\n' "$t" "$s" "$m" "$f"
done`

// ListDir lists a folder inside a running container (up to 2000 entries; more reports truncated).
func (c *Client) ListDir(ctx context.Context, container, dir string) (entries []Entry, truncated bool, err error) {
	out, stderr, code, err := c.ExecOutput(ctx, container, []string{"/bin/sh", "-c", listScript, "sh", dir}, 4<<20)
	if err != nil {
		return nil, false, err
	}
	if code != 0 {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = fmt.Sprintf("listing failed (exit %d)", code)
		}
		return nil, false, errors.New(msg)
	}
	entries, truncated = ParseListing(out)
	return entries, truncated, nil
}

// ListScript is the shell script ListDir runs; callers elsewhere (a volume inside the VM) run it
// the same way, with the folder as $1, and read its output with ParseListing.
const ListScript = listScript

// ParseListing reads ListScript's output.
func ParseListing(out []byte) (entries []Entry, truncated bool) {
	entries = []Entry{}
	for _, line := range strings.Split(string(out), "\n") {
		if line == "..." {
			truncated = true
			continue
		}
		f := strings.SplitN(line, "\t", 4)
		if len(f) != 4 {
			continue
		}
		size, _ := strconv.ParseInt(f[1], 10, 64)
		mod, _ := strconv.ParseInt(f[2], 10, 64)
		entries = append(entries, Entry{Kind: f[0], Size: size, Modified: mod, Name: f[3]})
	}
	return entries, truncated
}

// Archive returns the tar stream Docker makes of a path inside a container (file or folder).
// It works for stopped containers and images without a shell too.
func (c *Client) Archive(ctx context.Context, container, path string) (io.ReadCloser, error) {
	return c.Raw(ctx, "/containers/"+url.PathEscape(container)+"/archive", url.Values{"path": {path}})
}

// ReadFile returns the first limit bytes of a regular file inside a container, the file's full
// size, and whether it was cut.
func (c *Client) ReadFile(ctx context.Context, container, path string, limit int64) (data []byte, size int64, err error) {
	rc, tr, h, err := c.OpenFile(ctx, container, path)
	if err != nil {
		return nil, 0, err
	}
	defer rc.Close()
	data, err = io.ReadAll(io.LimitReader(tr, limit))
	return data, h.Size, err
}

// OpenFile opens a regular file inside a container for reading, following symbolic links (up to
// eight). The caller reads the content from tr and closes rc.
func (c *Client) OpenFile(ctx context.Context, container, p string) (rc io.ReadCloser, tr *tar.Reader, h *tar.Header, err error) {
	for hop := 0; hop < 8; hop++ {
		rc, err = c.Archive(ctx, container, p)
		if err != nil {
			return nil, nil, nil, err
		}
		tr = tar.NewReader(rc)
		h, err = tr.Next()
		if err != nil {
			rc.Close()
			return nil, nil, nil, err
		}
		switch h.Typeflag {
		case tar.TypeReg:
			return rc, tr, h, nil
		case tar.TypeSymlink:
			rc.Close()
			target := h.Linkname
			if !pathpkg.IsAbs(target) {
				target = pathpkg.Join(pathpkg.Dir(p), target)
			}
			p = pathpkg.Clean(target)
		default:
			rc.Close()
			return nil, nil, nil, errors.New("not a regular file")
		}
	}
	return nil, nil, nil, errors.New("too many symbolic links")
}
