package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Details is what the container list shows beyond /containers/json: why the container last
// stopped and the limits it runs with. It comes from one inspect, so callers cache it.
type Details struct {
	OOMKilled         bool   `json:"oomKilled"`
	ExitCode          int    `json:"exitCode"`
	StartedAt         string `json:"startedAt,omitempty"`
	FinishedAt        string `json:"finishedAt,omitempty"`
	HasHealthcheck    bool   `json:"hasHealthcheck"`
	Memory            int64  `json:"memory"`            // bytes, 0 = no limit
	MemorySwap        int64  `json:"memorySwap"`        // bytes, -1 = unlimited, 0 = unset
	MemoryReservation int64  `json:"memoryReservation"` // bytes, 0 = none
	NanoCPUs          int64  `json:"nanoCpus"`          // 1e9 = one CPU
	CPUQuota          int64  `json:"cpuQuota"`
	CPUPeriod         int64  `json:"cpuPeriod"`
	Restart           string `json:"restart"`
	MaxRetries        int    `json:"maxRetries"`
}

// CPUs is the CPU limit in CPUs, from NanoCpus or CpuQuota/CpuPeriod; 0 = no limit.
func (d *Details) CPUs() float64 {
	if d.NanoCPUs > 0 {
		return float64(d.NanoCPUs) / 1e9
	}
	if d.CPUQuota > 0 {
		period := d.CPUPeriod
		if period <= 0 {
			period = 100000
		}
		return float64(d.CPUQuota) / float64(period)
	}
	return 0
}

// Details inspects a container for its exit reason and limits.
func (c *Client) Details(ctx context.Context, id string) (*Details, error) {
	if !ValidRef(id) {
		return nil, fmt.Errorf("invalid container id %q", id)
	}
	var raw struct {
		State struct {
			OOMKilled  bool   `json:"OOMKilled"`
			ExitCode   int    `json:"ExitCode"`
			StartedAt  string `json:"StartedAt"`
			FinishedAt string `json:"FinishedAt"`
		} `json:"State"`
		Config struct {
			Healthcheck *struct {
				Test []string `json:"Test"`
			} `json:"Healthcheck"`
		} `json:"Config"`
		HostConfig struct {
			Memory            int64 `json:"Memory"`
			MemorySwap        int64 `json:"MemorySwap"`
			MemoryReservation int64 `json:"MemoryReservation"`
			NanoCPUs          int64 `json:"NanoCpus"`
			CPUQuota          int64 `json:"CpuQuota"`
			CPUPeriod         int64 `json:"CpuPeriod"`
			RestartPolicy     struct {
				Name              string `json:"Name"`
				MaximumRetryCount int    `json:"MaximumRetryCount"`
			} `json:"RestartPolicy"`
		} `json:"HostConfig"`
	}
	if err := c.getJSON(ctx, "/containers/"+url.PathEscape(id)+"/json", nil, &raw); err != nil {
		return nil, err
	}
	hc := raw.HostConfig
	d := &Details{
		OOMKilled: raw.State.OOMKilled, ExitCode: raw.State.ExitCode,
		StartedAt: raw.State.StartedAt, FinishedAt: raw.State.FinishedAt,
		Memory: hc.Memory, MemorySwap: hc.MemorySwap, MemoryReservation: hc.MemoryReservation,
		NanoCPUs: hc.NanoCPUs, CPUQuota: hc.CPUQuota, CPUPeriod: hc.CPUPeriod,
		Restart: hc.RestartPolicy.Name, MaxRetries: hc.RestartPolicy.MaximumRetryCount,
	}
	if hc := raw.Config.Healthcheck; hc != nil && len(hc.Test) > 0 && hc.Test[0] != "NONE" {
		d.HasHealthcheck = true
	}
	if d.Restart == "" {
		d.Restart = "no"
	}
	return d, nil
}

// HealthFromStatus reads the health state out of the list's Status text
// ("Up 2 minutes (healthy)", "Up 3 seconds (health: starting)"); "" when there is none.
func HealthFromStatus(status string) string {
	switch {
	case strings.HasSuffix(status, "(healthy)"):
		return "healthy"
	case strings.HasSuffix(status, "(unhealthy)"):
		return "unhealthy"
	case strings.HasSuffix(status, "(health: starting)"):
		return "starting"
	}
	return ""
}

var exitRe = regexp.MustCompile(`^Exited \((-?\d+)\)`)

// ExitCodeFromStatus reads the exit code out of "Exited (137) 5 minutes ago"; -1 when absent.
func ExitCodeFromStatus(status string) int {
	m := exitRe.FindStringSubmatch(status)
	if m == nil {
		return -1
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

var sizeRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([kmgt]?)(i?b?)$`)

// MinMemory is the smallest memory limit the engine accepts.
const MinMemory = 6 << 20

// ParseSize reads a memory size the way docker run does: a number of bytes, or a number with
// k, m, g or t (binary units: 1m is 1024*1024), optionally followed by b or ib: "256m", "1g",
// "1.5g", "512MiB". It is case-insensitive.
func ParseSize(s string) (int64, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	m := sizeRe.FindStringSubmatch(t)
	if m == nil || (m[2] == "" && m[3] == "ib") {
		return 0, fmt.Errorf("%q is not a size: use a number with k, m or g, like 256m or 1g", s)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a size", s)
	}
	mult := map[string]float64{"": 1, "k": 1 << 10, "m": 1 << 20, "g": 1 << 30, "t": 1 << 40}[m[2]]
	v := n * mult
	if v >= math.MaxInt64/2 {
		return 0, fmt.Errorf("%q is too large", s)
	}
	return int64(v), nil
}

// ParseCPUs reads a CPU count like "0.5" or "2" as nano CPUs (1e9 per CPU).
func ParseCPUs(s string) (int64, error) {
	t := strings.TrimSpace(s)
	n, err := strconv.ParseFloat(t, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, fmt.Errorf("%q is not a number of CPUs: use a number like 0.5 or 2", s)
	}
	if n < 0.01 {
		return 0, fmt.Errorf("the CPU limit must be at least 0.01")
	}
	if n > 1024 {
		return 0, fmt.Errorf("%q CPUs is more than any Mac has", s)
	}
	return int64(math.Round(n * 1e9)), nil
}

// RestartPolicies are the names Docker accepts.
var RestartPolicies = []string{"no", "on-failure", "unless-stopped", "always"}

// LimitsRequest is a limits change from the page. An empty string leaves that setting as it
// is; Restart nil leaves the restart policy alone.
type LimitsRequest struct {
	Memory            string  `json:"memory"`
	MemoryReservation string  `json:"memoryReservation"`
	CPUs              string  `json:"cpus"`
	Restart           *string `json:"restart"`
	MaxRetries        int     `json:"maxRetries"`
}

// ErrNoChange is returned when a LimitsRequest changes nothing.
var ErrNoChange = errors.New("nothing to change")

// UpdateBody turns a request into the body of POST /containers/{id}/update, given the
// container's current settings. It checks what the engine would refuse, and says why in
// words a person can act on.
func UpdateBody(cur *Details, req LimitsRequest) (map[string]any, error) {
	body := map[string]any{}
	mem := cur.Memory
	if req.Memory != "" {
		v, err := ParseSize(req.Memory)
		if err != nil {
			return nil, fmt.Errorf("memory limit: %w", err)
		}
		if v < MinMemory {
			return nil, fmt.Errorf("memory limit: the smallest Docker allows is 6m")
		}
		mem = v
		body["Memory"] = v
		// The engine refuses a memory limit above the current swap limit unless both change.
		// Keep unlimited swap unlimited; otherwise do what docker run does: swap = 2 x memory.
		if cur.MemorySwap == -1 {
			body["MemorySwap"] = int64(-1)
		} else {
			body["MemorySwap"] = 2 * v
		}
	}
	if req.MemoryReservation != "" {
		v, err := ParseSize(req.MemoryReservation)
		if err != nil {
			return nil, fmt.Errorf("memory reservation: %w", err)
		}
		if v <= 0 {
			return nil, fmt.Errorf("memory reservation: must be more than 0")
		}
		body["MemoryReservation"] = v
		if mem > 0 && v > mem {
			return nil, fmt.Errorf("memory reservation (%s) must not be more than the memory limit (%s)", req.MemoryReservation, HumanSize(mem))
		}
	} else if req.Memory != "" && cur.MemoryReservation > 0 && cur.MemoryReservation > mem {
		return nil, fmt.Errorf("memory limit: must not be less than the memory reservation (%s)", HumanSize(cur.MemoryReservation))
	}
	if req.CPUs != "" {
		n, err := ParseCPUs(req.CPUs)
		if err != nil {
			return nil, err
		}
		if cur.CPUQuota > 0 && cur.NanoCPUs == 0 {
			// Set with --cpu-quota: the engine will not mix the two, so change the quota instead.
			period := cur.CPUPeriod
			if period <= 0 {
				period = 100000
				body["CpuPeriod"] = period
			}
			body["CpuQuota"] = n * period / 1e9
		} else {
			body["NanoCpus"] = n
		}
	}
	if req.Restart != nil {
		name := *req.Restart
		ok := false
		for _, p := range RestartPolicies {
			ok = ok || p == name
		}
		if !ok {
			return nil, fmt.Errorf("restart policy %q: use one of %s", name, strings.Join(RestartPolicies, ", "))
		}
		if req.MaxRetries < 0 || req.MaxRetries > 1000 {
			return nil, fmt.Errorf("retries must be between 0 and 1000")
		}
		if name != "on-failure" && req.MaxRetries != 0 {
			return nil, fmt.Errorf("retries only apply to the on-failure policy")
		}
		if name != cur.Restart || req.MaxRetries != cur.MaxRetries {
			body["RestartPolicy"] = map[string]any{"Name": name, "MaximumRetryCount": req.MaxRetries}
		}
	}
	if len(body) == 0 {
		return nil, ErrNoChange
	}
	return body, nil
}

// HumanSize prints bytes in Docker's binary units: 512m, 1.5g.
func HumanSize(n int64) string {
	for _, u := range []struct {
		s string
		v int64
	}{{"t", 1 << 40}, {"g", 1 << 30}, {"m", 1 << 20}, {"k", 1 << 10}} {
		if n >= u.v {
			return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.2f", float64(n)/float64(u.v)), "0"), ".0") + u.s
		}
	}
	return fmt.Sprint(n)
}

// Update changes a container's resources and restart policy in place (no restart).
func (c *Client) Update(ctx context.Context, id string, body map[string]any) ([]string, error) {
	if !ValidRef(id) {
		return nil, fmt.Errorf("invalid container id %q", id)
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/update", nil, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Warnings []string `json:"Warnings"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Warnings, nil
}

// StampFormat is the fixed width RFC 3339 form log timestamps are normalised to, so they sort
// as strings.
const StampFormat = "2006-01-02T15:04:05.000000000Z"

// SplitStamp splits the timestamp Docker puts in front of a log line when asked to; ok is
// false when the line has none. The stamp comes back in UTC in StampFormat.
func SplitStamp(line string) (stamp, text string, ok bool) {
	i := strings.IndexByte(line, ' ')
	head := line
	if i >= 0 {
		head = line[:i]
	}
	t, err := time.Parse(time.RFC3339Nano, head)
	if err != nil {
		return "", line, false
	}
	if i < 0 {
		return t.UTC().Format(StampFormat), "", true
	}
	return t.UTC().Format(StampFormat), line[i+1:], true
}
