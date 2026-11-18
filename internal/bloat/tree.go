// Package bloat checks an image for wasted space (build tools, caches, deleted files that still
// ship, fat bases) and suggests how to build it smaller, usually with a multi-stage Dockerfile.
package bloat

import (
	"archive/tar"
	"errors"
	"io"
	"path"
	"sort"
	"strings"
)

// File is one file in the image and its size.
type File struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Tree sums an image's final filesystem: folder sizes a few levels down, the biggest files and
// which tools are present. It never keeps file contents apart from os-release.
type Tree struct {
	Total     int64            `json:"total"`
	Files     int              `json:"files"`
	Dirs      map[string]int64 `json:"-"`
	Named     map[string]int64 `json:"-"`
	Has       map[string]bool  `json:"-"`
	Big       []File           `json:"-"`
	Archives  []File           `json:"-"`
	OSRelease string           `json:"-"`
}

const (
	dirDepth  = 4
	bigFiles  = 15
	maxProbes = 8
)

// named are folder names whose total size matters wherever they sit.
var named = map[string]bool{".git": true, "node_modules": true, "__pycache__": true, ".cache": true, "test": true, "tests": true}

// tools are executables whose presence in a bin folder says something about the image.
var tools = map[string]bool{
	"gcc": true, "g++": true, "cc": true, "make": true, "javac": true, "go": true, "cargo": true, "rustc": true,
	"mvn": true, "gradle": true, "git": true, "curl": true, "wget": true, "vim": true, "vi": true, "nano": true,
	"python3": true, "python": true, "node": true, "npm": true, "yarn": true, "pip": true, "pip3": true,
	"apt-get": true, "apk": true, "dnf": true, "yum": true, "sh": true, "bash": true, "busybox": true,
	"dotnet": true, "ruby": true, "php": true, "java": true, "perl": true, "ssh": true,
}

var archiveExt = []string{".tar", ".tar.gz", ".tgz", ".tar.xz", ".tar.bz2", ".zip", ".deb", ".rpm", ".apk", ".whl", ".7z"}

// Walk reads a tar of an image's filesystem (as /containers/{id}/export streams it).
func Walk(r io.Reader) (*Tree, error) {
	t := &Tree{Dirs: map[string]int64{}, Named: map[string]int64{}, Has: map[string]bool{}}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return t, err
		}
		name := path.Clean("/" + h.Name)
		dir, base := path.Split(name)
		dir = strings.TrimSuffix(dir, "/")
		if (strings.HasSuffix(dir, "/bin") || strings.HasSuffix(dir, "/sbin")) && tools[base] {
			t.Has[base] = true
		}
		if h.Typeflag == tar.TypeSymlink || h.Typeflag == tar.TypeLink {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if (name == "/etc/os-release" || name == "/usr/lib/os-release") && h.Size < 16<<10 && t.OSRelease == "" {
			b, _ := io.ReadAll(io.LimitReader(tr, 16<<10))
			t.OSRelease = string(b)
		}
		t.add(name, h.Size)
	}
	return t, nil
}

func (t *Tree) add(name string, size int64) {
	t.Total += size
	t.Files++
	parts := strings.Split(strings.TrimPrefix(name, "/"), "/")
	for d := 1; d < len(parts) && d <= dirDepth; d++ {
		t.Dirs["/"+strings.Join(parts[:d], "/")] += size
	}
	seen := map[string]bool{}
	for _, p := range parts[:len(parts)-1] {
		if named[p] && !seen[p] {
			seen[p] = true
			t.Named[p] += size
		}
	}
	t.Big = keepTop(t.Big, File{name, size}, bigFiles)
	lower := strings.ToLower(name)
	for _, ext := range archiveExt {
		if strings.HasSuffix(lower, ext) && !strings.HasPrefix(name, "/usr/lib/") && !strings.HasPrefix(name, "/lib/") && !strings.HasPrefix(name, "/var/cache/") {
			t.Archives = keepTop(t.Archives, File{name, size}, maxProbes)
			break
		}
	}
}

func keepTop(list []File, f File, n int) []File {
	if len(list) == n && f.Size <= list[n-1].Size {
		return list
	}
	i := sort.Search(len(list), func(i int) bool { return list[i].Size < f.Size })
	list = append(list, File{})
	copy(list[i+1:], list[i:])
	list[i] = f
	if len(list) > n {
		list = list[:n]
	}
	return list
}

// Dir is the total size of the files under folder p (up to four levels deep).
func (t *Tree) Dir(p string) int64 {
	if t == nil {
		return 0
	}
	return t.Dirs[p]
}

// osField reads KEY=value from os-release.
func osField(osr, key string) string {
	for _, line := range strings.Split(osr, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key+"="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}
