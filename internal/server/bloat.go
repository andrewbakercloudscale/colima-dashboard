package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/bloat"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
)

// Images never change under an ID, so a report is kept until the image goes.
var bloatCache = struct {
	sync.Mutex
	m map[string]*bloat.Report
}{m: map[string]*bloat.Report{}}

func bloatKey(sock, id string, deep bool) string {
	if deep {
		return sock + "|" + id + "|deep"
	}
	return sock + "|" + id
}

func cachedBloat(key string) *bloat.Report {
	bloatCache.Lock()
	defer bloatCache.Unlock()
	return bloatCache.m[key]
}

func storeBloat(key string, r *bloat.Report) {
	bloatCache.Lock()
	defer bloatCache.Unlock()
	if len(bloatCache.m) > 300 {
		clear(bloatCache.m)
	}
	bloatCache.m[key] = r
}

// checkBloat runs the quick check (history only) or the deep one (walks the exported filesystem).
func checkBloat(ctx context.Context, dc *docker.Client, ref string, deep bool) (*bloat.Report, error) {
	img, err := dc.ImageInspect(ctx, ref)
	if err != nil {
		return nil, err
	}
	key := bloatKey(dc.Socket, img.ID, deep)
	if r := cachedBloat(key); r != nil {
		return withRef(r, ref), nil
	}
	hist, err := dc.ImageHistory(ctx, img.ID)
	if err != nil {
		return nil, err
	}
	var tree *bloat.Tree
	if deep {
		err = dc.ExportImage(ctx, img.ID, func(r io.Reader) error {
			var werr error
			tree, werr = bloat.Walk(r)
			_, _ = io.Copy(io.Discard, r)
			return werr
		})
		if err != nil {
			return nil, err
		}
	}
	name := ref
	if len(img.RepoTags) > 0 && strings.HasPrefix(ref, "sha256:") {
		name = img.RepoTags[0]
	}
	r := bloat.Analyze(name, img, hist, tree)
	storeBloat(key, r)
	return r, nil
}

func withRef(r *bloat.Report, ref string) *bloat.Report {
	if strings.HasPrefix(ref, "sha256:") || r.Ref == ref {
		return r
	}
	c := *r
	c.Ref = ref
	return &c
}

// GET /api/profiles/{p}/images/{id}/bloat[?deep=1]
func (s *Server) apiImageBloat(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	ref := r.PathValue("id")
	if !docker.ValidRef(ref) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid image reference %q", ref))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	rep, err := checkBloat(ctx, dc, ref, r.URL.Query().Get("deep") == "1")
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// bloatRow is one image in the Images page summary.
type bloatRow struct {
	ID     string `json:"id"`
	Ref    string `json:"ref"`
	Size   int64  `json:"size"`
	Score  int    `json:"score"`
	Grade  string `json:"grade"`
	Saving int64  `json:"saving"`
	Deep   bool   `json:"deep"`
	Top    string `json:"top"`
	Issues int    `json:"issues"`
}

// GET /api/profiles/{p}/images/bloat: the quick check for every image (a deep result when one
// is cached), worst first.
func (s *Server) apiImagesBloat(w http.ResponseWriter, r *http.Request) {
	dc := s.client(w, r)
	if dc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	imgs, err := dc.Images(ctx)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	if len(imgs) > 80 {
		imgs = imgs[:80]
	}
	rows := make([]bloatRow, len(imgs))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, im := range imgs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ref := im.ID
			for _, t := range im.RepoTags {
				if t != "<none>:<none>" {
					ref = t
					break
				}
			}
			rep := cachedBloat(bloatKey(dc.Socket, im.ID, true))
			if rep == nil {
				var e error
				if rep, e = checkBloat(ctx, dc, im.ID, false); e != nil {
					rows[i] = bloatRow{ID: im.ID, Ref: ref, Size: im.Size, Score: -1}
					return
				}
			}
			row := bloatRow{ID: im.ID, Ref: ref, Size: im.Size, Score: rep.Score, Grade: rep.Grade, Saving: rep.Saving, Deep: rep.Deep, Issues: len(rep.Findings)}
			for _, f := range rep.Findings {
				if f.Severity != "low" {
					row.Top = f.Title
					break
				}
			}
			rows[i] = row
		}()
	}
	wg.Wait()
	rank := func(r bloatRow) int {
		if r.Score < 0 {
			return 1000
		}
		return r.Score
	}
	sort.SliceStable(rows, func(i, j int) bool { return rank(rows[i]) < rank(rows[j]) })
	writeJSON(w, http.StatusOK, rows)
}
