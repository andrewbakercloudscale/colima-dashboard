// Package update checks GitHub for new releases of Colima Dashboard and Colima, and updates
// Colima Dashboard itself.
//
// A self-update downloads the release archive for this Mac's architecture and the release's
// checksums file over HTTPS from GitHub, refuses the archive unless its SHA-256 matches,
// writes the new binary next to the running one and renames it into place. The caller then
// restarts. Builds named "dev" and binaries Homebrew manages never replace themselves.
package update

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Repo is where Colima Dashboard is released.
const Repo = "andrewbakercloudscale/colima-dashboard"

// ColimaRepo is Colima's own repository.
const ColimaRepo = "abiosoft/colima"

// checkEvery is how often the background loop asks GitHub, and how long answers are cached.
const checkEvery = 6 * time.Hour

// Release is the part of a GitHub release the page shows.
type Release struct {
	Tag       string    `json:"tag"`
	Version   string    `json:"version"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Published time.Time `json:"published"`
	Notes     string    `json:"notes,omitempty"`
	assets    map[string]string
}

// Settings are kept in settings.json next to the session token.
type Settings struct {
	// Auto checks GitHub every 6 hours and installs a new Colima Dashboard by itself.
	Auto bool `json:"auto"`
}

// State is what GET /api/updates returns.
type State struct {
	Auto      bool      `json:"auto"`
	CheckedAt time.Time `json:"checkedAt,omitzero"`
	CheckErr  string    `json:"checkError,omitempty"`
	Dashboard Component `json:"dashboard"`
	Colima    Component `json:"colima"`
	// ColimaNews is Colima's latest releases, newest first, for the welcome page.
	ColimaNews []Release `json:"colimaNews,omitempty"`
}

// Component is one updatable thing.
type Component struct {
	Current string   `json:"current"`
	Latest  *Release `json:"latest,omitempty"`
	Newer   bool     `json:"newer"`
	// CanUpdate says whether the page can update it; Reason says why not.
	CanUpdate bool   `json:"canUpdate"`
	Reason    string `json:"reason,omitempty"`
	// Phase of a self-update: "", "downloading", "installed" (restart pending) or "failed".
	Phase string `json:"phase,omitempty"`
	Error string `json:"error,omitempty"`
}

// Updater holds the cached answers and the settings.
type Updater struct {
	Current       string // this binary's version
	Exe           string // path of the running binary
	SettingsPath  string
	ColimaVersion func(context.Context) (string, error)
	ColimaBin     string // path of the colima binary, to tell whether Homebrew manages it
	HTTP          *http.Client
	API           string // https://api.github.com, swapped in tests
	// Restart is called after a self-update is installed, when it is safe to restart.
	Restart func()
	// Busy reports whether a long job (a VM start, say) is running; a restart waits for it.
	Busy func() bool

	mu       sync.Mutex
	settings Settings
	state    State
	fetched  time.Time
}

// Load reads the settings; a missing file means automatic updates on, like Rancher Desktop.
func (u *Updater) Load() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.settings = Settings{Auto: true}
	if b, err := os.ReadFile(u.SettingsPath); err == nil {
		_ = json.Unmarshal(b, &u.settings)
	}
	if u.HTTP == nil {
		u.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	if u.API == "" {
		u.API = "https://api.github.com"
	}
}

// SetAuto saves the automatic updates setting.
func (u *Updater) SetAuto(on bool) error {
	u.mu.Lock()
	u.settings.Auto = on
	b, _ := json.MarshalIndent(u.settings, "", "  ")
	u.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(u.SettingsPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(u.SettingsPath, append(b, '\n'), 0o600)
}

// Snapshot returns the cached state without asking GitHub.
func (u *Updater) Snapshot() State {
	u.mu.Lock()
	defer u.mu.Unlock()
	st := u.state
	st.Auto = u.settings.Auto
	st.Dashboard.Current = u.Current
	st.Dashboard.CanUpdate, st.Dashboard.Reason = u.selfUpdatable()
	return st
}

// Run checks now and then every 6 hours while automatic updates are on, installing a newer
// Colima Dashboard when it finds one. It returns when ctx ends.
func (u *Updater) Run(ctx context.Context) {
	// Let the page come up first.
	select {
	case <-ctx.Done():
		return
	case <-time.After(20 * time.Second):
	}
	for {
		u.mu.Lock()
		auto := u.settings.Auto
		u.mu.Unlock()
		if auto {
			if st := u.Check(ctx, false); st.Dashboard.Newer && st.Dashboard.CanUpdate && st.Dashboard.Phase == "" {
				_ = u.Install(ctx)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(checkEvery):
		}
	}
}

// Check asks GitHub for the latest releases, unless the cached answer is recent and force is
// false. It never sends anything about this Mac beyond what any HTTPS request carries.
func (u *Updater) Check(ctx context.Context, force bool) State {
	u.mu.Lock()
	fresh := !u.fetched.IsZero() && time.Since(u.fetched) < checkEvery
	u.mu.Unlock()
	if fresh && !force {
		return u.Snapshot()
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	var dash *Release
	var news []Release
	var errs []string
	if rel, err := u.latest(ctx, Repo); err == nil {
		dash = rel
	} else if !errors.Is(err, errNoRelease) {
		errs = append(errs, "Colima Dashboard: "+err.Error())
	}
	if rels, err := u.releases(ctx, ColimaRepo, 3); err == nil {
		news = rels
	} else {
		errs = append(errs, "Colima: "+err.Error())
	}
	colimaNow := ""
	if u.ColimaVersion != nil {
		colimaNow, _ = u.ColimaVersion(ctx)
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	u.fetched = time.Now()
	u.state.CheckedAt = u.fetched
	u.state.CheckErr = strings.Join(errs, "; ")
	phase, perr := u.state.Dashboard.Phase, u.state.Dashboard.Error
	u.state.Dashboard = Component{Current: u.Current, Latest: dash, Phase: phase, Error: perr}
	if dash != nil {
		u.state.Dashboard.Newer = Newer(dash.Version, u.Current)
	}
	u.state.ColimaNews = news
	c := Component{Current: strings.TrimPrefix(colimaNow, "v")}
	if len(news) > 0 {
		c.Latest = &news[0]
		c.Newer = c.Current != "" && Newer(news[0].Version, c.Current)
	}
	c.CanUpdate, c.Reason = u.colimaUpdatable()
	u.state.Colima = c
	st := u.state
	st.Auto = u.settings.Auto
	st.Dashboard.CanUpdate, st.Dashboard.Reason = u.selfUpdatable()
	return st
}

// selfUpdatable says whether this binary may replace itself. Caller holds mu.
func (u *Updater) selfUpdatable() (bool, string) {
	switch {
	case u.Current == "" || u.Current == "dev":
		return false, "a development build; build it again from source to update"
	case strings.Contains(u.Exe, "/Cellar/") || strings.Contains(u.Exe, "/Caskroom/"):
		return false, "installed with Homebrew; run brew upgrade colima-dashboard"
	}
	dir := filepath.Dir(u.Exe)
	f, err := os.CreateTemp(dir, ".colima-dashboard-probe-*")
	if err != nil {
		return false, "cannot write to " + dir
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true, ""
}

// colimaUpdatable: Colima is updated through Homebrew, so only a Homebrew install qualifies.
func (u *Updater) colimaUpdatable() (bool, string) {
	real, err := filepath.EvalSymlinks(u.ColimaBin)
	if err != nil {
		return false, "colima was not found"
	}
	if strings.Contains(real, "/Cellar/") {
		return true, ""
	}
	return false, "colima was not installed with Homebrew; update it the way you installed it"
}

var errNoRelease = errors.New("no release yet")

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (g ghRelease) release(notes bool) Release {
	r := Release{Tag: g.TagName, Version: strings.TrimPrefix(g.TagName, "v"), Name: g.Name, URL: g.HTMLURL, Published: g.PublishedAt, assets: map[string]string{}}
	if notes {
		r.Notes = summarise(g.Body, 600)
	}
	for _, a := range g.Assets {
		r.assets[a.Name] = a.URL
	}
	return r
}

func (u *Updater) get(ctx context.Context, url string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "colima-dashboard")
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return errors.New("could not reach GitHub")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNoRelease
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub answered %s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(into)
}

func (u *Updater) latest(ctx context.Context, repo string) (*Release, error) {
	var g ghRelease
	if err := u.get(ctx, u.API+"/repos/"+repo+"/releases/latest", &g); err != nil {
		return nil, err
	}
	r := g.release(true)
	return &r, nil
}

func (u *Updater) releases(ctx context.Context, repo string, n int) ([]Release, error) {
	var gs []ghRelease
	if err := u.get(ctx, fmt.Sprintf("%s/repos/%s/releases?per_page=%d", u.API, repo, n+5), &gs); err != nil {
		return nil, err
	}
	var out []Release
	for _, g := range gs {
		if g.Draft || g.Prerelease {
			continue
		}
		out = append(out, g.release(true))
		if len(out) == n {
			break
		}
	}
	return out, nil
}

// AssetName is the release archive for this Mac.
func AssetName(version string) string {
	return fmt.Sprintf("colima-dashboard_%s_darwin_%s.tar.gz", version, runtime.GOARCH)
}

// Install downloads, verifies and installs the latest Colima Dashboard, then restarts once no
// long job is running. It returns when the binary is in place.
func (u *Updater) Install(ctx context.Context) error {
	u.mu.Lock()
	ok, why := u.selfUpdatable()
	latest := u.state.Dashboard.Latest
	busy := u.state.Dashboard.Phase == "downloading"
	if ok && !busy {
		u.state.Dashboard.Phase, u.state.Dashboard.Error = "downloading", ""
	}
	u.mu.Unlock()
	switch {
	case !ok:
		return errors.New(why)
	case busy:
		return errors.New("an update is already downloading")
	case latest == nil || !Newer(latest.Version, u.Current):
		u.setPhase("", "")
		return errors.New("already up to date")
	}
	if err := u.install(ctx, latest); err != nil {
		u.setPhase("failed", err.Error())
		return err
	}
	u.setPhase("installed", "")
	go u.restartWhenIdle()
	return nil
}

func (u *Updater) setPhase(phase, msg string) {
	u.mu.Lock()
	u.state.Dashboard.Phase, u.state.Dashboard.Error = phase, msg
	u.mu.Unlock()
}

func (u *Updater) restartWhenIdle() {
	for u.Busy != nil && u.Busy() {
		time.Sleep(5 * time.Second)
	}
	if u.Restart != nil {
		u.Restart()
	}
}

func (u *Updater) install(ctx context.Context, rel *Release) error {
	name := AssetName(rel.Version)
	archiveURL, sumsURL := rel.assets[name], rel.assets["checksums.txt"]
	if archiveURL == "" || sumsURL == "" {
		return fmt.Errorf("release %s has no %s or checksums.txt", rel.Tag, name)
	}
	if !strings.HasPrefix(archiveURL, "https://") || !strings.HasPrefix(sumsURL, "https://") {
		return errors.New("refusing a download that is not HTTPS")
	}
	sums, err := u.download(ctx, sumsURL, 1<<20)
	if err != nil {
		return fmt.Errorf("checksums: %w", err)
	}
	want, err := checksumFor(sums, name)
	if err != nil {
		return err
	}
	archive, err := u.download(ctx, archiveURL, 64<<20)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return errors.New("the download does not match the release checksum; nothing was changed")
	}
	bin, err := binaryFrom(archive)
	if err != nil {
		return err
	}
	return replaceExe(u.Exe, bin)
}

func (u *Updater) download(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "colima-dashboard")
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("download is larger than expected")
	}
	return b, nil
}

// checksumFor reads a sha256sum-style file ("<hex>  <name>" per line).
func checksumFor(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt does not list %s", name)
}

// binaryFrom returns the colima-dashboard file from a .tar.gz archive.
func binaryFrom(archive []byte) ([]byte, error) {
	zr, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return nil, fmt.Errorf("reading the archive: %w", err)
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("the archive has no colima-dashboard binary")
		}
		if err != nil {
			return nil, fmt.Errorf("reading the archive: %w", err)
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == "colima-dashboard" {
			return io.ReadAll(io.LimitReader(tr, 64<<20))
		}
	}
}

// replaceExe writes the new binary beside the old one and renames it into place, so the
// running process keeps its file and the next start gets the new one (a new inode also keeps
// macOS's code signing cache honest).
func replaceExe(exe string, bin []byte) error {
	dir := filepath.Dir(exe)
	f, err := os.CreateTemp(dir, ".colima-dashboard-new-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after the rename
	if _, err := f.Write(bin); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o755); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, exe)
}

// Newer reports whether version a is newer than b (dotted numbers; a "v" prefix is ignored).
func Newer(a, b string) bool {
	pa, pb := parts(a), parts(b)
	if pa == nil || pb == nil {
		return false
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func parts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return nil
	}
	var out []int
	for _, s := range strings.Split(v, ".") {
		n, err := strconv.Atoi(s)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// summarise keeps the first paragraphs of release notes as plain text, up to max runes.
func summarise(body string, max int) string {
	var keep []string
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "<!--") {
			continue
		}
		l = strings.TrimLeft(l, "#")
		keep = append(keep, strings.TrimSpace(l))
	}
	s := strings.TrimSpace(strings.Join(keep, "\n"))
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max])) + "…"
	}
	return s
}
