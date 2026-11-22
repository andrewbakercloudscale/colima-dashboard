package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
)

// Volume backups are plain .tar.gz files of a volume's contents, kept in a folder on the Mac
// (~/colima-backups/volumes), so they survive deleting the VM and can be opened with any tool.
// The archive is made and unpacked inside the VM as root and streamed over colima ssh: no helper
// container or image, and it works with no network.

func (s *Server) volBackupRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/volume-backups", s.auth(s.apiVolumeBackups))
	m.HandleFunc("DELETE /api/volume-backups/{file}", s.auth(s.apiVolumeBackupDelete))
	m.HandleFunc("POST /api/volume-backups/reveal", s.auth(s.apiVolumeBackupReveal))
	m.HandleFunc("POST /api/profiles/{p}/volumes/{name}/backup", s.auth(s.apiVolumeBackup))
	m.HandleFunc("POST /api/profiles/{p}/volume-restore", s.auth(s.apiVolumeRestore))
}

// backupDir is where volume backups live; COLIMA_DASHBOARD_BACKUP_DIR moves it (tests use it).
func backupDir() string {
	if d := os.Getenv("COLIMA_DASHBOARD_BACKUP_DIR"); d != "" {
		return d
	}
	return filepath.Join(homeDir(), "colima-backups", "volumes")
}

// A backup is named <volume>@<yyyymmdd-hhmmss>.tar.gz; volume names cannot contain "@".
var backupName = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_.-]*)@(\d{8}-\d{6})\.tar\.gz$`)

// VolumeBackup is one backup file.
type VolumeBackup struct {
	File   string `json:"file"`
	Volume string `json:"volume"`
	Time   int64  `json:"time"` // unix seconds the backup was taken
	Size   int64  `json:"size"`
}

func listBackups(dir string) ([]VolumeBackup, error) {
	out := []VolumeBackup{}
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	for _, e := range ents {
		m := backupName.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		t, err := time.ParseInLocation("20060102-150405", m[2], time.Local)
		if err != nil {
			continue
		}
		out = append(out, VolumeBackup{File: e.Name(), Volume: m[1], Time: t.Unix(), Size: fi.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return out, nil
}

// backupPath checks a backup file name from a request and returns its full path.
func backupPath(file string) (string, error) {
	if !backupName.MatchString(file) {
		return "", fmt.Errorf("%q is not a volume backup", file)
	}
	p := filepath.Join(backupDir(), file)
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("backup %s not found in %s", file, backupDir())
	}
	return p, nil
}

// GET /api/volume-backups -> {dir, backups}
func (s *Server) apiVolumeBackups(w http.ResponseWriter, _ *http.Request) {
	list, err := listBackups(backupDir())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dir": backupDir(), "backups": list})
}

func (s *Server) apiVolumeBackupDelete(w http.ResponseWriter, r *http.Request) {
	p, err := backupPath(r.PathValue("file"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err := os.Remove(p); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// POST /api/volume-backups/reveal {file?}: show the backup (or the folder) in Finder.
func (s *Server) apiVolumeBackupReveal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		File string `json:"file"`
	}
	_ = readJSON(r, &body)
	args := []string{backupDir()}
	if body.File != "" {
		p, err := backupPath(body.File)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		args = []string{"-R", p}
	} else if err := os.MkdirAll(backupDir(), 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if os.Getenv("COLIMA_DASHBOARD_NO_TERMINAL") != "" { // test runs must not open windows
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if err := exec.Command("/usr/bin/open", args...).Run(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// usersOf lists the running containers that mount a volume.
func usersOf(ctx context.Context, dc *docker.Client, volume string) ([]string, error) {
	cs, err := dc.Containers(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, c := range cs {
		if c.State != "running" {
			continue
		}
		for _, m := range c.Mounts {
			if m.Type == "volume" && m.Name == volume {
				names = append(names, strings.TrimPrefix(firstOr(c.Names, c.ID[:12]), "/"))
				break
			}
		}
	}
	return names, nil
}

func firstOr(list []string, def string) string {
	if len(list) > 0 {
		return list[0]
	}
	return def
}

// POST /api/profiles/{p}/volumes/{name}/backup -> job writing <name>@<time>.tar.gz
func (s *Server) apiVolumeBackup(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	p, name := r.PathValue("p"), r.PathValue("name")
	v, err := dc.Volume(r.Context(), name)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	if !backupName.MatchString(v.Name+"@00000000-000000.tar.gz") || !strings.HasPrefix(v.Mountpoint, "/") {
		writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("volume %s cannot be backed up here (driver %s)", v.Name, v.Driver))
		return
	}
	busy, _ := usersOf(r.Context(), dc, v.Name)
	job := s.jobs.Start("back up volume "+v.Name, p, func(j *Job) error {
		dir := backupDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		file := v.Name + "@" + time.Now().Format("20060102-150405") + ".tar.gz"
		dest := filepath.Join(dir, file)
		j.Line("$ colima ssh -p " + p + " -- sudo tar -C " + v.Mountpoint + " -czf - .  > " + dest)
		if len(busy) > 0 {
			j.Line("Note: " + strings.Join(busy, ", ") + " is running and using this volume; files it writes during the backup may be caught half written. Stop it first for a clean copy.")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		cmd, err := s.opt.Colima.SSHCommand(ctx, p, "sudo", "tar", "-C", v.Mountpoint, "-czf", "-", ".")
		if err != nil {
			return err
		}
		n, err := streamToFile(cmd, dest, j)
		if err != nil {
			return err
		}
		j.Line(fmt.Sprintf("Saved %s (%s) in %s", file, humanBytes(n), dir))
		return nil
	})
	writeJSON(w, http.StatusAccepted, job.snapshot())
}

// streamToFile runs cmd and writes its stdout to dest through a temporary file, so a failed or
// cancelled backup never leaves a file that looks complete. Stderr goes to the job.
func streamToFile(cmd *exec.Cmd, dest string, j *Job) (int64, error) {
	tmp := dest + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp) // no-op after the rename
	cw := &countWriter{w: f}
	cmd.Stdout, cmd.Stderr = cw, j
	runErr := cmd.Run()
	closeErr := f.Close()
	if runErr != nil {
		return 0, fmt.Errorf("backup failed: %w", runErr)
	}
	if closeErr != nil {
		return 0, closeErr
	}
	if cw.n == 0 {
		return 0, errors.New("backup failed: the VM sent nothing")
	}
	return cw.n, os.Rename(tmp, dest)
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// The volume's old contents are removed only once the archive is known to be readable, so a
// broken backup file leaves the volume as it was.
const restoreScript = `set -e
cd "$1"
tmp="$(mktemp -d /tmp/restore.XXXXXX)"
trap 'rm -rf "$tmp"' EXIT
cat > "$tmp/in.tar.gz"
gzip -t "$tmp/in.tar.gz"
find . -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
tar -xzpf "$tmp/in.tar.gz"
echo "restored $(find . -mindepth 1 | wc -l | tr -d " ") files and folders"`

// POST /api/profiles/{p}/volume-restore {file, volume} -> job replacing the volume's contents
// with the backup; creates the volume when it does not exist.
func (s *Server) apiVolumeRestore(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	var body struct {
		File   string `json:"file"`
		Volume string `json:"volume"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	src, err := backupPath(body.File)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if body.Volume == "" {
		body.Volume = backupName.FindStringSubmatch(body.File)[1]
	}
	if !backupName.MatchString(body.Volume + "@00000000-000000.tar.gz") {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid volume name %q: letters, digits, _ . and - only", body.Volume))
		return
	}
	busy, err := usersOf(r.Context(), dc, body.Volume)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if len(busy) > 0 {
		writeError(w, http.StatusConflict, fmt.Errorf("stop %s first: it is running and using volume %s", strings.Join(busy, ", "), body.Volume))
		return
	}
	p := r.PathValue("p")
	job := s.jobs.Start("restore volume "+body.Volume, p, func(j *Job) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		v, err := dc.CreateVolume(ctx, body.Volume) // returns the existing volume unchanged
		if err != nil {
			return err
		}
		if !strings.HasPrefix(v.Mountpoint, "/") {
			return fmt.Errorf("volume %s has no local mount point (driver %s)", v.Name, v.Driver)
		}
		j.Line("$ colima ssh -p " + p + " -- sudo sh -c '…tar -xzpf -' < " + src)
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()
		cmd, err := s.opt.Colima.SSHCommand(ctx, p, "sudo", "sh", "-c", restoreScript, "sh", v.Mountpoint)
		if err != nil {
			return err
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = f, j, j
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("restore failed, the volume was left as it was unless the line above says otherwise: %w", err)
		}
		j.Line("Volume " + v.Name + " now holds the contents of " + body.File)
		return nil
	})
	writeJSON(w, http.StatusAccepted, job.snapshot())
}
