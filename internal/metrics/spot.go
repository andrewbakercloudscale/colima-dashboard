package metrics

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Spot is one reading of the VM's CPU counters and memory, taken without the Monitor open.
// Two spots a minute or more apart give the VM's average CPU between them.
type Spot struct {
	T                time.Time
	Busy, Total      float64
	CPUs             int
	MemTotal, MemAvl uint64
	Load1            float64 // the kernel's one minute load average
}

// MemUsed is the memory in use: total less what the kernel says is available.
func (s *Spot) MemUsed() uint64 {
	if s.MemTotal > s.MemAvl {
		return s.MemTotal - s.MemAvl
	}
	return 0
}

// CPUSince is the VM's average CPU % between an earlier spot and this one.
func (s *Spot) CPUSince(prev *Spot) (float64, bool) {
	if prev == nil {
		return 0, false
	}
	dTotal := s.Total - prev.Total
	if dTotal <= 0 {
		return 0, false
	}
	return clamp((s.Busy-prev.Busy)/dTotal*100, 0, 100), true
}

const spotScript = `grep '^cpu' /proc/stat; grep -E '^(MemTotal|MemAvailable):' /proc/meminfo; cat /proc/loadavg`

// ReadSpot takes one reading over a short colima ssh session.
func ReadSpot(ctx context.Context, bin, profile string) (*Spot, error) {
	out, err := exec.CommandContext(ctx, bin, "ssh", "-p", profile, "--", "sh", "-c", spotScript).Output()
	if err != nil {
		return nil, err
	}
	return parseSpot(string(out)), nil
}

func parseSpot(out string) *Spot {
	g := parseGuest(strings.Split(out, "\n"))
	return &Spot{T: g.t, Busy: g.busy, Total: g.total, CPUs: g.cpus, MemTotal: g.memTotal, MemAvl: g.memAvl, Load1: g.load1}
}
