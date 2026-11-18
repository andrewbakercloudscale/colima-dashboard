package history

import (
	"sort"
	"strings"
	"time"
)

// OldImageDays is the age past which an image counts as old: base images that old have missed
// months of security fixes.
const OldImageDays = 90

// ImageInfo is the part of an image the "Out of date" card needs; the server fills it from
// Docker's image list so this package does not depend on the Docker client.
type ImageInfo struct {
	ID         string
	Tags       []string
	Created    time.Time
	Size       int64
	Containers int64
}

// VersionInfo is one tool's installed and latest version.
type VersionInfo struct {
	Label   string `json:"label"`
	About   string `json:"about,omitempty"`
	Current string `json:"current"`
	Latest  string `json:"latest,omitempty"`
	Newer   bool   `json:"newer"`
	Where   string `json:"where"` // "brew", "vm" (inside the VM), "dashboard", "k3s"
}

// OutdatedReport is what is old or behind: images and tools.
type OutdatedReport struct {
	ImagesKnown  bool          `json:"imagesKnown"` // Docker answered
	ImagesError  string        `json:"imagesError,omitempty"`
	Images       int           `json:"images"`
	Size         int64         `json:"size"`
	OldDays      int           `json:"oldDays"`
	OldCount     int           `json:"oldCount"`
	OldSize      int64         `json:"oldSize"`
	Old          []ImageRow    `json:"old"` // oldest first, at most 10
	LatestCount  int           `json:"latestCount"`
	Latest       []ImageRow    `json:"latest"` // tagged :latest, oldest first, at most 10
	Dangling     int           `json:"dangling"`
	DanglingSize int64         `json:"danglingSize"`
	Unused       int           `json:"unused"`
	UnusedSize   int64         `json:"unusedSize"`
	Ages         []AgeBucket   `json:"ages"`
	ToolsKnown   bool          `json:"toolsKnown"` // versions have been checked
	Tools        []VersionInfo `json:"tools"`
	Behind       int           `json:"behind"`
}

// ImageRow is one image in a list.
type ImageRow struct {
	Ref        string    `json:"ref"`
	ID         string    `json:"id"` // short
	Created    time.Time `json:"created"`
	AgeDays    int       `json:"ageDays"`
	Size       int64     `json:"size"`
	Containers int64     `json:"containers"`
}

// AgeBucket counts images by age.
type AgeBucket struct {
	Label string `json:"label"`
	Count int    `json:"count"`
	Size  int64  `json:"size"`
}

var ageEdges = []struct {
	label string
	days  int // upper bound, exclusive; 0 for no bound
}{{"under a week", 7}, {"1 to 4 weeks", 30}, {"1 to 3 months", OldImageDays}, {"3 to 12 months", 365}, {"over a year", 0}}

// MakeOutdated sorts images and tools into what is old or behind. images is nil when Docker
// could not be asked; tools is nil when versions have not been checked yet.
func MakeOutdated(now time.Time, images []ImageInfo, imagesErr error, tools []VersionInfo) *OutdatedReport {
	o := &OutdatedReport{OldDays: OldImageDays, Old: []ImageRow{}, Latest: []ImageRow{}, Ages: []AgeBucket{}, Tools: []VersionInfo{}}
	for _, e := range ageEdges {
		o.Ages = append(o.Ages, AgeBucket{Label: e.label})
	}
	if imagesErr != nil {
		o.ImagesError = imagesErr.Error()
	} else if images != nil {
		o.ImagesKnown = true
	}
	var old, latest []ImageRow
	for _, im := range images {
		o.Images++
		o.Size += im.Size
		age := int(now.Sub(im.Created).Hours() / 24)
		if age < 0 {
			age = 0
		}
		for i, e := range ageEdges {
			if e.days == 0 || age < e.days {
				o.Ages[i].Count++
				o.Ages[i].Size += im.Size
				break
			}
		}
		tags := realTags(im.Tags)
		row := ImageRow{ID: shortID(im.ID), Created: im.Created, AgeDays: age, Size: im.Size, Containers: im.Containers}
		if len(tags) == 0 {
			o.Dangling++
			o.DanglingSize += im.Size
			row.Ref = "<none>"
		} else {
			row.Ref = tags[0]
		}
		if im.Containers == 0 {
			o.Unused++
			o.UnusedSize += im.Size
		}
		if age >= OldImageDays {
			o.OldCount++
			o.OldSize += im.Size
			old = append(old, row)
		}
		for _, t := range tags {
			if strings.HasSuffix(t, ":latest") {
				o.LatestCount++
				r := row
				r.Ref = t
				latest = append(latest, r)
				break
			}
		}
	}
	byAge := func(l []ImageRow) {
		sort.SliceStable(l, func(i, j int) bool {
			if !l[i].Created.Equal(l[j].Created) {
				return l[i].Created.Before(l[j].Created)
			}
			return l[i].Ref < l[j].Ref
		})
	}
	byAge(old)
	byAge(latest)
	o.Old = append(o.Old, old[:min(10, len(old))]...)
	o.Latest = append(o.Latest, latest[:min(10, len(latest))]...)
	if tools != nil {
		o.ToolsKnown = true
		o.Tools = append(o.Tools, tools...)
		// behind first, then in the order given
		sort.SliceStable(o.Tools, func(i, j int) bool { return o.Tools[i].Newer && !o.Tools[j].Newer })
		for _, t := range tools {
			if t.Newer {
				o.Behind++
			}
		}
	}
	return o
}

func realTags(tags []string) []string {
	var out []string
	for _, t := range tags {
		if t != "" && t != "<none>:<none>" {
			out = append(out, t)
		}
	}
	return out
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
