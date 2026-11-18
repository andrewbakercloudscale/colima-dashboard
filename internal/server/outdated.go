package server

import (
	"context"
	"regexp"
	"time"

	"github.com/andrewbakercloudscale/colima-dashboard/internal/colima"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/docker"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/history"
	"github.com/andrewbakercloudscale/colima-dashboard/internal/update"
)

var semver = regexp.MustCompile(`\d+\.\d+\.\d+`)

// outdated gathers the Report page's "Out of date" card: the profile's images from Docker (when
// it answers within a few seconds) and the tool versions the update checker last saw. It never
// checks for updates itself, so the report stays as cheap as reading the history files.
func (s *Server) outdated(ctx context.Context, p string, now time.Time) *history.OutdatedReport {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var (
		images []history.ImageInfo
		imgErr error
		engine string
	)
	if dc, err := s.dockerFor(p); err != nil {
		imgErr = err
	} else if list, err := dc.Images(ctx); err != nil {
		imgErr = err
	} else {
		images = imageInfos(list)
		if cs, err := dc.Containers(ctx); err == nil {
			use := map[string]int64{}
			for _, c := range cs {
				use[c.ImageID]++
			}
			for i := range images {
				images[i].Containers = use[images[i].ID]
			}
		}
		if v, err := dc.Version(ctx); err == nil {
			engine = semver.FindString(v.Version)
		}
	}
	var tools []history.VersionInfo
	if s.opt.Updater != nil {
		if st := s.opt.Updater.Snapshot(); !st.CheckedAt.IsZero() {
			tools = toolVersions(st, engine)
		}
	}
	if tools != nil {
		if ks, err := colima.ReadKubeSettings(s.opt.ColimaHome, p); err == nil && ks.Version != "" {
			tools = append(tools, history.VersionInfo{Label: "Kubernetes (k3s)", About: "version set for this profile", Current: ks.Version, Where: "k3s"})
		}
	}
	return history.MakeOutdated(now, images, imgErr, tools)
}

func imageInfos(list []docker.Image) []history.ImageInfo {
	out := make([]history.ImageInfo, 0, len(list))
	for _, im := range list {
		out = append(out, history.ImageInfo{ID: im.ID, Tags: im.RepoTags, Created: time.Unix(im.Created, 0), Size: im.Size})
	}
	return out
}

// toolVersions lists the dashboard, Colima, the Homebrew tools and Docker Engine with their
// latest versions, from the update checker's last answer. Docker Engine is compared with the
// Docker CLI's latest release, as the General page does.
func toolVersions(st update.State, engine string) []history.VersionInfo {
	out := []history.VersionInfo{}
	if d := st.Dashboard; d.Current != "" {
		v := history.VersionInfo{Label: "Colima Dashboard", About: "this app", Current: d.Current, Newer: d.Newer, Where: "dashboard"}
		if d.Latest != nil {
			v.Latest = d.Latest.Version
		}
		out = append(out, v)
	}
	hasColima := false
	var dockerLatest string
	for _, t := range st.Tools {
		hasColima = hasColima || t.Formula == "colima"
		if t.Formula == "docker" {
			dockerLatest = t.Latest
		}
		out = append(out, history.VersionInfo{Label: t.Label, About: t.About, Current: t.Current, Latest: t.Latest, Newer: t.Newer, Where: "brew"})
	}
	if c := st.Colima; !hasColima && c.Current != "" {
		v := history.VersionInfo{Label: "Colima", About: "runs the Linux VM", Current: c.Current, Newer: c.Newer, Where: "brew"}
		if c.Latest != nil {
			v.Latest = c.Latest.Version
		}
		out = append(out, v)
	}
	if engine != "" {
		out = append(out, history.VersionInfo{Label: "Docker Engine", About: "inside the VM", Current: engine, Latest: dockerLatest,
			Newer: dockerLatest != "" && update.Newer(dockerLatest, engine), Where: "vm"})
	}
	return out
}
