// Package docker is a small Docker Engine API client over a Unix socket: only the endpoints the
// UI uses, standard library only.
package docker

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Client talks to one Docker Engine socket.
type Client struct {
	Socket string
	http   *http.Client
}

// New returns a client for the socket at path (no unix:// prefix).
func New(path string) *Client {
	path = strings.TrimPrefix(path, "unix://")
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	}
	return &Client{Socket: path, http: &http.Client{Transport: tr}}
}

// APIError is a non-2xx answer from the engine.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("docker: %s (HTTP %d)", e.Message, e.Status) }

// IsNotFound reports whether err is a 404 from the engine.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/@-]{0,254}$`)

// ValidRef reports whether s is safe as a container id, name, image reference or volume name in a path.
func ValidRef(s string) bool { return idRe.MatchString(s) && !strings.Contains(s, "..") }

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body io.Reader) (*http.Response, error) {
	u := "http://docker" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker engine not reachable at %s: %w", c.Socket, err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var m struct {
			Message string `json:"message"`
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if json.Unmarshal(b, &m) != nil || m.Message == "" {
			m.Message = strings.TrimSpace(string(b))
		}
		return nil, &APIError{Status: resp.StatusCode, Message: m.Message}
	}
	return resp, nil
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, v any) error {
	resp, err := c.do(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

func (c *Client) send(ctx context.Context, method, path string, query url.Values, v any) error {
	resp, err := c.do(ctx, method, path, query, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if v != nil {
		return json.NewDecoder(resp.Body).Decode(v)
	}
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

// Raw performs a GET and returns the body for the caller to stream and close.
func (c *Client) Raw(ctx context.Context, path string, query url.Values) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Ping reports whether the engine answers.
func (c *Client) Ping(ctx context.Context) error {
	return c.send(ctx, http.MethodGet, "/_ping", nil, nil)
}

// Version is the subset of /version the UI shows.
type Version struct {
	Version       string `json:"Version"`
	APIVersion    string `json:"ApiVersion"`
	OS            string `json:"Os"`
	Arch          string `json:"Arch"`
	KernelVersion string `json:"KernelVersion"`
}

// Version returns the engine version.
func (c *Client) Version(ctx context.Context) (*Version, error) {
	var v Version
	return &v, c.getJSON(ctx, "/version", nil, &v)
}

// Port is a published container port.
type Port struct {
	IP          string `json:"IP,omitempty"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort,omitempty"`
	Type        string `json:"Type"`
}

// Container is one row of /containers/json.
type Container struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	ImageID string            `json:"ImageID"`
	Command string            `json:"Command"`
	Created int64             `json:"Created"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Ports   []Port            `json:"Ports"`
	Labels  map[string]string `json:"Labels"`
	Mounts  []Mount           `json:"Mounts"`
}

// Mount is a container mount as listed by /containers/json.
type Mount struct {
	Type        string `json:"Type"`
	Name        string `json:"Name,omitempty"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
}

// Containers lists containers, running and stopped.
func (c *Client) Containers(ctx context.Context) ([]Container, error) {
	var out []Container
	err := c.getJSON(ctx, "/containers/json", url.Values{"all": {"1"}}, &out)
	return out, err
}

// ContainerAction runs start, stop, restart, pause or unpause.
func (c *Client) ContainerAction(ctx context.Context, id, action string) error {
	if !ValidRef(id) {
		return fmt.Errorf("invalid container id %q", id)
	}
	switch action {
	case "start", "stop", "restart", "pause", "unpause":
	default:
		return fmt.Errorf("unknown container action %q", action)
	}
	err := c.send(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/"+action, nil, nil)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusNotModified {
		return nil // already in that state
	}
	return err
}

// RemoveContainer removes a container, stopping it first when force is set.
func (c *Client) RemoveContainer(ctx context.Context, id string, force bool) error {
	if !ValidRef(id) {
		return fmt.Errorf("invalid container id %q", id)
	}
	q := url.Values{}
	if force {
		q.Set("force", "1")
	}
	return c.send(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), q, nil)
}

// Inspect returns the raw /containers/{id}/json document.
func (c *Client) Inspect(ctx context.Context, id string) (map[string]any, error) {
	if !ValidRef(id) {
		return nil, fmt.Errorf("invalid container id %q", id)
	}
	var out map[string]any
	return out, c.getJSON(ctx, "/containers/"+url.PathEscape(id)+"/json", nil, &out)
}

// Stats is one sample of a container's CPU, memory, network and disk use. The byte counters
// are totals since the container started; the page turns two samples into rates.
type Stats struct {
	CPUPercent  float64 `json:"cpuPercent"`
	MemoryBytes uint64  `json:"memoryBytes"`
	MemoryLimit uint64  `json:"memoryLimit"`
	NetRx       uint64  `json:"netRx"`
	NetTx       uint64  `json:"netTx"`
	BlockRead   uint64  `json:"blockRead"`
	BlockWrite  uint64  `json:"blockWrite"`
	PIDs        uint64  `json:"pids"`
}

type rawStats struct {
	CPUStats    cpuStats `json:"cpu_stats"`
	PreCPUStats cpuStats `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IOServiceBytesRecursive []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
	PidsStats struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
}

type cpuStats struct {
	CPUUsage struct {
		TotalUsage uint64 `json:"total_usage"`
	} `json:"cpu_usage"`
	SystemUsage uint64 `json:"system_cpu_usage"`
	OnlineCPUs  uint64 `json:"online_cpus"`
}

// Stats takes one sample (not a stream) the way `docker stats --no-stream` computes it.
func (c *Client) Stats(ctx context.Context, id string) (*Stats, error) {
	if !ValidRef(id) {
		return nil, fmt.Errorf("invalid container id %q", id)
	}
	var r rawStats
	if err := c.getJSON(ctx, "/containers/"+url.PathEscape(id)+"/stats", url.Values{"stream": {"false"}}, &r); err != nil {
		return nil, err
	}
	return computeStats(r), nil
}

func computeStats(r rawStats) *Stats {
	s := &Stats{MemoryLimit: r.MemoryStats.Limit, PIDs: r.PidsStats.Current}
	for _, n := range r.Networks {
		s.NetRx += n.RxBytes
		s.NetTx += n.TxBytes
	}
	for _, b := range r.BlkioStats.IOServiceBytesRecursive {
		switch strings.ToLower(b.Op) {
		case "read":
			s.BlockRead += b.Value
		case "write":
			s.BlockWrite += b.Value
		}
	}
	// Same as the docker CLI: page cache (inactive_file) is not counted as used.
	used := r.MemoryStats.Usage
	if v, ok := r.MemoryStats.Stats["inactive_file"]; ok && v < used {
		used -= v
	}
	s.MemoryBytes = used
	cpuDelta := float64(r.CPUStats.CPUUsage.TotalUsage) - float64(r.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(r.CPUStats.SystemUsage) - float64(r.PreCPUStats.SystemUsage)
	if cpuDelta > 0 && sysDelta > 0 {
		cpus := float64(r.CPUStats.OnlineCPUs)
		if cpus == 0 {
			cpus = 1
		}
		s.CPUPercent = cpuDelta / sysDelta * cpus * 100
	}
	return s
}

// Top lists the processes running in a container, as `docker top` does.
type Top struct {
	Titles    []string   `json:"titles"`
	Processes [][]string `json:"processes"`
}

// Top runs `ps` in the container's namespace through the engine (no shell needed in the image).
func (c *Client) Top(ctx context.Context, id string) (*Top, error) {
	if !ValidRef(id) {
		return nil, fmt.Errorf("invalid container id %q", id)
	}
	var t struct {
		Titles    []string   `json:"Titles"`
		Processes [][]string `json:"Processes"`
	}
	if err := c.getJSON(ctx, "/containers/"+url.PathEscape(id)+"/top", url.Values{"ps_args": {"-eo pid,user,pcpu,pmem,rss,etime,args"}}, &t); err != nil {
		// BusyBox ps and some engines refuse ps_args; fall back to the default columns.
		if err2 := c.getJSON(ctx, "/containers/"+url.PathEscape(id)+"/top", nil, &t); err2 != nil {
			return nil, err
		}
	}
	return &Top{Titles: t.Titles, Processes: t.Processes}, nil
}

// Logs streams a container's logs to w as lines of text, demultiplexing stdout and stderr.
// tty says whether the container has a TTY (its stream is then raw, not multiplexed).
func (c *Client) Logs(ctx context.Context, id string, tail int, follow, timestamps, tty bool, w func(stream, line string) error) error {
	if !ValidRef(id) {
		return fmt.Errorf("invalid container id %q", id)
	}
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "tail": {fmt.Sprint(tail)}}
	if follow {
		q.Set("follow", "1")
	}
	if timestamps {
		q.Set("timestamps", "1")
	}
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/logs", q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if tty {
		return scanLines(resp.Body, "stdout", w)
	}
	return demux(resp.Body, w)
}

func scanLines(r io.Reader, stream string, w func(stream, line string) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if err := w(stream, sc.Text()); err != nil {
			return err
		}
	}
	return sc.Err()
}

// demux splits Docker's multiplexed log stream: 8-byte headers (stream, 0, 0, 0, size uint32 BE).
func demux(r io.Reader, w func(stream, line string) error) error {
	br := bufio.NewReader(r)
	hdr := make([]byte, 8)
	partial := map[string]string{}
	for {
		if _, err := io.ReadFull(br, hdr); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return err
		}
		stream := "stdout"
		if hdr[0] == 2 {
			stream = "stderr"
		}
		size := binary.BigEndian.Uint32(hdr[4:])
		if size > 16<<20 {
			return fmt.Errorf("log frame of %d bytes is too large", size)
		}
		buf := make([]byte, size)
		if _, err := io.ReadFull(br, buf); err != nil {
			return err
		}
		text := partial[stream] + string(buf)
		lines := strings.Split(text, "\n")
		partial[stream] = lines[len(lines)-1]
		for _, line := range lines[:len(lines)-1] {
			if err := w(stream, line); err != nil {
				return err
			}
		}
	}
	for stream, rest := range partial {
		if rest != "" {
			if err := w(stream, rest); err != nil {
				return err
			}
		}
	}
	return nil
}

// Image is one row of /images/json.
type Image struct {
	ID         string   `json:"Id"`
	RepoTags   []string `json:"RepoTags"`
	RepoDigest []string `json:"RepoDigests"`
	Created    int64    `json:"Created"`
	Size       int64    `json:"Size"`
	Containers int64    `json:"Containers"`
}

// Images lists images.
func (c *Client) Images(ctx context.Context) ([]Image, error) {
	var out []Image
	return out, c.getJSON(ctx, "/images/json", nil, &out)
}

// RemoveImage removes an image.
func (c *Client) RemoveImage(ctx context.Context, ref string, force bool) error {
	if !ValidRef(ref) {
		return fmt.Errorf("invalid image reference %q", ref)
	}
	q := url.Values{}
	if force {
		q.Set("force", "1")
	}
	return c.send(ctx, http.MethodDelete, "/images/"+url.PathEscape(ref), q, nil)
}

// PullEvent is one progress message from a pull.
type PullEvent struct {
	ID       string `json:"id,omitempty"`
	Status   string `json:"status,omitempty"`
	Progress string `json:"progress,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Pull pulls an image, calling fn for each progress event.
func (c *Client) Pull(ctx context.Context, ref string, fn func(PullEvent) error) error {
	if !ValidRef(ref) {
		return fmt.Errorf("invalid image reference %q", ref)
	}
	image, tag := splitRef(ref)
	q := url.Values{"fromImage": {image}}
	if tag != "" {
		q.Set("tag", tag)
	}
	resp, err := c.do(ctx, http.MethodPost, "/images/create", q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var ev PullEvent
		if err := dec.Decode(&ev); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if ev.Error != "" {
			return errors.New(ev.Error)
		}
		if err := fn(ev); err != nil {
			return err
		}
	}
}

// splitRef splits "repo:tag" and "repo@digest", leaving registry ports alone.
func splitRef(ref string) (image, tag string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	slash := strings.LastIndex(ref, "/")
	if i := strings.LastIndex(ref, ":"); i > slash {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

// PruneReport is the answer from a prune endpoint.
type PruneReport struct {
	Deleted        int    `json:"deleted"`
	SpaceReclaimed uint64 `json:"spaceReclaimed"`
}

// Prune removes unused objects of kind "images" (dangling and unused), "volumes" or "containers".
func (c *Client) Prune(ctx context.Context, kind string) (*PruneReport, error) {
	q := url.Values{}
	switch kind {
	case "images":
		q.Set("filters", `{"dangling":["false"]}`)
	case "volumes":
		// Since Engine 23 a volume prune only removes anonymous volumes unless asked for all.
		q.Set("filters", `{"all":["true"]}`)
	case "containers":
	default:
		return nil, fmt.Errorf("unknown prune kind %q", kind)
	}
	var raw struct {
		ImagesDeleted     []map[string]string `json:"ImagesDeleted"`
		VolumesDeleted    []string            `json:"VolumesDeleted"`
		ContainersDeleted []string            `json:"ContainersDeleted"`
		SpaceReclaimed    uint64              `json:"SpaceReclaimed"`
	}
	if err := c.send(ctx, http.MethodPost, "/"+kind+"/prune", q, &raw); err != nil {
		return nil, err
	}
	return &PruneReport{
		Deleted:        len(raw.ImagesDeleted) + len(raw.VolumesDeleted) + len(raw.ContainersDeleted),
		SpaceReclaimed: raw.SpaceReclaimed,
	}, nil
}

// Volume is one volume with its size from /system/df (-1 when unknown).
type Volume struct {
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver"`
	Mountpoint string            `json:"Mountpoint"`
	CreatedAt  string            `json:"CreatedAt"`
	Labels     map[string]string `json:"Labels"`
	UsageData  *struct {
		Size     int64 `json:"Size"`
		RefCount int64 `json:"RefCount"`
	} `json:"UsageData,omitempty"`
}

// Volumes lists volumes.
func (c *Client) Volumes(ctx context.Context) ([]Volume, error) {
	var out struct {
		Volumes []Volume `json:"Volumes"`
	}
	return out.Volumes, c.getJSON(ctx, "/volumes", nil, &out)
}

// RemoveVolume removes a volume.
// Volume returns one volume, with its mount point inside the VM.
func (c *Client) Volume(ctx context.Context, name string) (*Volume, error) {
	if !ValidRef(name) {
		return nil, fmt.Errorf("invalid volume name %q", name)
	}
	var v Volume
	return &v, c.getJSON(ctx, "/volumes/"+url.PathEscape(name), nil, &v)
}

func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	if !ValidRef(name) {
		return fmt.Errorf("invalid volume name %q", name)
	}
	return c.send(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(name), nil, nil)
}

// DiskUsage is the /system/df summary, by type.
type DiskUsage struct {
	LayersSize int64       `json:"LayersSize"`
	Images     []Image     `json:"Images"`
	Containers []dfCont    `json:"Containers"`
	Volumes    []Volume    `json:"Volumes"`
	BuildCache []dfBuild   `json:"BuildCache"`
	Summary    []DiskTotal `json:"summary"`
}

type dfCont struct {
	ID         string `json:"Id"`
	SizeRw     int64  `json:"SizeRw"`
	State      string `json:"State"`
	SizeRootFs int64  `json:"SizeRootFs"`
}

type dfBuild struct {
	Size   int64 `json:"Size"`
	InUse  bool  `json:"InUse"`
	Shared bool  `json:"Shared"`
}

// DiskTotal is one row of the disk use table.
type DiskTotal struct {
	Type        string `json:"type"`
	Count       int    `json:"count"`
	Active      int    `json:"active"`
	Size        int64  `json:"size"`
	Reclaimable int64  `json:"reclaimable"`
}

// BuildCacheStamp summarises BuildKit's cache (records, uses, last use). Any build, even a
// failed one, changes it, so it shows whether builds need reading again.
func (c *Client) BuildCacheStamp(ctx context.Context) (string, error) {
	var du struct {
		BuildCache []struct {
			ID         string `json:"ID"`
			UsageCount int    `json:"UsageCount"`
			LastUsedAt string `json:"LastUsedAt"`
		} `json:"BuildCache"`
	}
	if err := c.getJSON(ctx, "/system/df", url.Values{"type": {"build-cache"}}, &du); err != nil {
		return "", err
	}
	uses, last := 0, ""
	for _, b := range du.BuildCache {
		uses += b.UsageCount
		last = max(last, b.LastUsedAt)
	}
	return fmt.Sprintf("%d|%d|%s", len(du.BuildCache), uses, last), nil
}

// DiskUsage returns /system/df with the totals the CLI shows filled in.
func (c *Client) DiskUsage(ctx context.Context) (*DiskUsage, error) {
	var du DiskUsage
	if err := c.getJSON(ctx, "/system/df", nil, &du); err != nil {
		return nil, err
	}
	du.Summary = summarize(&du)
	return &du, nil
}

func summarize(du *DiskUsage) []DiskTotal {
	img := DiskTotal{Type: "Images", Count: len(du.Images)}
	var used int64
	for _, i := range du.Images {
		img.Size += i.Size
		if i.Containers > 0 {
			img.Active++
			used += i.Size
		}
	}
	// Image sizes share layers; the engine's LayersSize is the true total on disk. What pruning
	// frees is what the images in use do not need (the docker CLI's estimate, too).
	if du.LayersSize > 0 {
		img.Size = du.LayersSize
	}
	img.Reclaimable = max(img.Size-used, 0)
	ct := DiskTotal{Type: "Containers", Count: len(du.Containers)}
	for _, cc := range du.Containers {
		ct.Size += cc.SizeRw
		if cc.State == "running" {
			ct.Active++
		} else {
			ct.Reclaimable += cc.SizeRw
		}
	}
	vol := DiskTotal{Type: "Volumes", Count: len(du.Volumes)}
	for _, v := range du.Volumes {
		if v.UsageData == nil || v.UsageData.Size < 0 {
			continue
		}
		vol.Size += v.UsageData.Size
		if v.UsageData.RefCount > 0 {
			vol.Active++
		} else {
			vol.Reclaimable += v.UsageData.Size
		}
	}
	bc := DiskTotal{Type: "Build cache", Count: len(du.BuildCache)}
	for _, b := range du.BuildCache {
		if !b.Shared {
			bc.Size += b.Size
		}
		if b.InUse {
			bc.Active++
		} else if !b.Shared {
			bc.Reclaimable += b.Size
		}
	}
	return []DiskTotal{img, ct, vol, bc}
}
