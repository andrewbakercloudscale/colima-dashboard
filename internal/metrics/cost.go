package metrics

import (
	"regexp"
	"sort"
	"time"
)

// Cost is what one container (or one Compose service, whose one-off run containers join it)
// cost the Mac over a window. The VM is one process on the Mac; each container gets the part
// of that process's CPU and memory that matches its share of the work inside the VM.
type Cost struct {
	Name     string   `json:"name"`
	Image    string   `json:"image"`
	IDs      []string `json:"ids"`
	System   bool     `json:"system,omitempty"` // the VM itself: kernel, Docker engine, idle overhead
	VMShare  float64  `json:"vmShare"`          // part of all CPU used inside the VM, 0..1
	MacCPU   float64  `json:"macCpu"`           // average, % of one Mac core
	PeakCPU  float64  `json:"peakCpu"`          // highest sample, % of one Mac core
	MacMem   uint64   `json:"macMem"`           // average share of the VM's Mac memory, bytes
	PeakMem  uint64   `json:"peakMem"`
	CPUSecs  float64  `json:"cpuSecs"` // Mac CPU time over the window, core seconds
	Seen     float64  `json:"seen"`    // part of the window it was running, 0..1
	samples  int
	cpuSum   float64
	memSum   float64
	guestCPU float64
}

// CostReport is every container's cost over a window, most expensive first.
type CostReport struct {
	From     int64   `json:"from"` // unix ms of the first sample used
	To       int64   `json:"to"`
	Samples  int     `json:"samples"`
	MacCPU   float64 `json:"macCpu"` // the whole VM, average % of one Mac core
	MacMem   uint64  `json:"macMem"` // the whole VM, average bytes
	CPUSecs  float64 `json:"cpuSecs"`
	Costs    []Cost  `json:"costs"`
	Coverage float64 `json:"coverage"` // part of the window that has samples, 0..1
}

var runSuffix = regexp.MustCompile(`-run-[0-9a-f]{6,}$`)

// costGroup names the group a container is billed to: compose run containers join their service.
func costGroup(name string) string { return runSuffix.ReplaceAllString(name, "") }

// Costs works out what each container cost the Mac over the samples after since (unix ms).
// Samples without both the VM's Mac usage and the inside view are skipped: neither half alone
// says how to split the bill.
func Costs(s Series, since int64, window time.Duration) CostReport {
	rep := CostReport{Costs: []Cost{}}
	groups := map[string]*Cost{}
	sys := &Cost{Name: "VM and system", System: true}
	step := float64(s.IntervalMs) / 1000
	if step <= 0 {
		step = Interval.Seconds()
	}
	var hostCPU, hostMem float64
	for _, smp := range s.Samples {
		if smp.T <= since || smp.Host == nil || smp.Guest == nil {
			continue
		}
		if rep.From == 0 {
			rep.From = smp.T
		}
		rep.To = smp.T
		rep.Samples++
		h, g := smp.Host, smp.Guest
		hostCPU += h.CPU
		hostMem += float64(h.Memory)
		var usedCPU, usedMem float64
		for id, c := range smp.Containers {
			name := s.Names[id]
			if name == "" {
				name = id
				if len(name) > 12 {
					name = name[:12]
				}
			}
			key := costGroup(name)
			grp := groups[key]
			if grp == nil {
				grp = &Cost{Name: key, Image: s.Images[id]}
				groups[key] = grp
			}
			if !contains(grp.IDs, id) {
				grp.IDs = append(grp.IDs, id)
			}
			cpu := share(c.CPU, g.CPU) * h.CPU
			mem := share(float64(c.Memory), float64(g.MemUsed)) * float64(h.Memory)
			usedCPU += cpu
			usedMem += mem
			grp.add(cpu, mem, c.CPU, step)
		}
		sys.add(max(0, h.CPU-usedCPU), max(0, float64(h.Memory)-usedMem), max(0, g.CPU-sumCPU(smp.Containers)), step)
	}
	if rep.Samples == 0 {
		return rep
	}
	n := float64(rep.Samples)
	rep.MacCPU = hostCPU / n
	rep.MacMem = uint64(hostMem / n)
	rep.CPUSecs = hostCPU / 100 * step
	if window > 0 {
		rep.Coverage = min(1, n*step/window.Seconds())
	}
	var guestTotal float64
	all := []*Cost{sys}
	for _, grp := range groups {
		all = append(all, grp)
	}
	for _, c := range all {
		guestTotal += c.guestCPU
	}
	for _, c := range all {
		if c.samples == 0 {
			continue
		}
		c.MacCPU = c.cpuSum / n // averaged over the window, so a container that ran half of it counts half
		c.MacMem = uint64(c.memSum / n)
		c.Seen = float64(c.samples) / n
		if guestTotal > 0 {
			c.VMShare = c.guestCPU / guestTotal
		}
		rep.Costs = append(rep.Costs, *c)
	}
	sort.SliceStable(rep.Costs, func(i, j int) bool {
		a, b := rep.Costs[i], rep.Costs[j]
		if a.CPUSecs != b.CPUSecs {
			return a.CPUSecs > b.CPUSecs
		}
		return a.Name < b.Name
	})
	return rep
}

func (c *Cost) add(cpu, mem, guestCPU, step float64) {
	c.samples++
	c.cpuSum += cpu
	c.memSum += mem
	c.guestCPU += guestCPU
	c.CPUSecs += cpu / 100 * step
	c.PeakCPU = max(c.PeakCPU, cpu)
	c.PeakMem = max(c.PeakMem, uint64(mem))
}

// share is part/whole clamped to 0..1; a busy container can briefly read above the VM total
// because the two are sampled a moment apart.
func share(part, whole float64) float64 {
	if whole <= 0 || part <= 0 {
		return 0
	}
	return min(1, part/whole)
}

func sumCPU(cs map[string]Container) float64 {
	var t float64
	for _, c := range cs {
		t += c.CPU
	}
	return t
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
