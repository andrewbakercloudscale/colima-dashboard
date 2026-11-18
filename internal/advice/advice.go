// Package advice turns what the dashboard knows about a profile (container limits and exit
// reasons, recent CPU and memory use, the VM's size, runs killed for memory) into
// recommendations a person can apply with one click: raise a memory limit, give a container
// more CPU, give the VM more CPUs or memory.
//
// It is pure logic: the server gathers the numbers and this package decides. Nothing here
// runs a command or polls.
package advice

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	GiB = int64(1) << 30
	MiB = int64(1) << 20

	// MinWindow is how much sampled history a CPU or VM rule needs before it speaks: one busy
	// moment is not a problem.
	MinWindow = 55 * time.Second
	// JobDays is how far back runs killed for memory are looked at.
	JobDays = 7
)

// Container is one container and what is known about its use.
type Container struct {
	ID, Name         string
	Project, Service string
	Image            string
	State            string // running, exited, ...
	OOMKilled        bool
	OOMHandled       bool // killed for memory, and its limit was raised since: no issue, and its job is covered
	ExitCode         int
	FinishedAt       time.Time
	MemLimit         int64   // bytes, 0 = no limit
	CPUs             float64 // CPU limit, 0 = no limit
	Usage            *Usage  // nil when not measured
}

// Usage is a container's measured use.
type Usage struct {
	MemNow  int64   // bytes in use at the newest sample
	MemPeak int64   // highest seen in the window
	CPU     float64 // CPUs (cores) used, averaged over the window
	Window  time.Duration
	// Snapshot is true for a single docker stats reading, not the Monitor's samples.
	Snapshot bool
}

// VM is the VM's size and, when the Monitor has samples, its use.
type VM struct {
	CPUs   int
	Memory int64 // bytes, from colima
	// Sampled is true when the fields below come from the Monitor's samples.
	Sampled  bool
	CPUPct   float64 // average guest CPU, percent of all vCPUs
	MemUsed  int64   // average over the last minute
	MemTotal int64   // as the guest sees it (a little under Memory)
	Window   time.Duration
}

// Mac is the host's size, for caps.
type Mac struct {
	CPUs   int
	Memory int64
}

// JobOOM is a test or job (history "job") that ended out of memory recently.
type JobOOM struct {
	Job, Name, Image string
	Project, Service string
	Count            int
	Last             time.Time
}

// Input is everything the rules look at.
type Input struct {
	Profile    string
	VM         VM
	Mac        Mac
	Containers []Container
	JobOOMs    []JobOOM
	Now        time.Time
}

// Target names what an issue is about.
type Target struct {
	Profile     string `json:"profile"`
	ContainerID string `json:"containerId,omitempty"`
	Name        string `json:"name,omitempty"`
	Project     string `json:"project,omitempty"`
	Service     string `json:"service,omitempty"`
	Job         string `json:"job,omitempty"`
	State       string `json:"state,omitempty"`
}

// Row is one line of the current versus recommended table.
type Row struct {
	Label       string `json:"label"`
	Current     string `json:"current"`
	Recommended string `json:"recommended"`
}

// Apply says what "Apply recommended" does. Type "limits" posts Body to the container's limits
// API (it applies live); "vm" posts Body to the profile's apply action (the VM restarts);
// "none" means there is nothing to apply from here (copy the snippet instead).
type Apply struct {
	Type  string         `json:"type"`
	Body  map[string]any `json:"body,omitempty"`
	Label string         `json:"label,omitempty"`
}

// Snippet is a piece of config or a command to copy.
type Snippet struct {
	Label string `json:"label"`
	Text  string `json:"text"`
}

// Link is a page in the dashboard worth opening.
type Link struct {
	Label string `json:"label"`
	Href  string `json:"href"`
}

// Consumer is a container named in a VM-wide issue, with a limit to suggest for it.
type Consumer struct {
	ContainerID string  `json:"containerId"`
	Name        string  `json:"name"`
	Share       float64 `json:"share"` // percent of the VM's CPU or memory
	Use         string  `json:"use"`   // "1.6 CPUs", "900 MiB"
	Limit       string  `json:"limit,omitempty"`
	Suggest     string  `json:"suggest,omitempty"` // a limit to set, "1" or "768m"; empty when it has one
	Field       string  `json:"field"`             // "cpus" or "memory"
}

// Issue is one recommendation.
type Issue struct {
	ID          string         `json:"id"`
	Severity    string         `json:"severity"` // bad | warn
	Kind        string         `json:"kind"`
	Title       string         `json:"title"`
	Summary     string         `json:"summary"` // one line with the numbers, for lists
	Why         string         `json:"why"`
	Target      Target         `json:"target"`
	Current     map[string]any `json:"current"`
	Recommended map[string]any `json:"recommended"`
	Rows        []Row          `json:"rows"`
	Apply       Apply          `json:"apply"`
	// VMApply is a second step some container fixes need: the VM is too small for the limit
	// the container should have.
	VMApply   *Apply     `json:"vmApply,omitempty"`
	Snippets  []Snippet  `json:"snippets"`
	Links     []Link     `json:"links,omitempty"`
	Consumers []Consumer `json:"consumers,omitempty"`
}

// Kinds, in the order issues of equal severity are listed.
const (
	KindOOM       = "oom"
	KindJobOOM    = "job-oom"
	KindVMMemory  = "vm-memory"
	KindMemNear   = "memory-near-limit"
	KindNoLimit   = "memory-no-limit"
	KindVMCPU     = "vm-cpu"
	KindCPULimit  = "cpu-at-limit"
	sevBad        = "bad"
	sevWarn       = "warn"
	vmMemCapShare = 0.75 // a container limit should leave a quarter of the VM for everything else
)

var kindOrder = map[string]int{KindOOM: 0, KindJobOOM: 1, KindVMMemory: 2, KindMemNear: 3, KindNoLimit: 4, KindVMCPU: 5, KindCPULimit: 6}

var memSteps = func() []int64 {
	var out []int64
	for _, m := range []int64{64, 128, 192, 256, 384, 512, 768} {
		out = append(out, m*MiB)
	}
	for _, g := range []float64{1, 1.5, 2, 3, 4, 6, 8, 12, 16, 24, 32, 48, 64} {
		out = append(out, int64(g*float64(GiB)))
	}
	return out
}()

var cpuSteps = []float64{0.25, 0.5, 0.75, 1, 1.5, 2, 3, 4, 6, 8, 12, 16, 24, 32, 48, 64}

// NiceMemory rounds a size up to the next friendly limit (64m, 128m, ... 1g, 1.5g, 2g ...).
func NiceMemory(b int64) int64 {
	for _, s := range memSteps {
		if s >= b {
			return s
		}
	}
	return int64(math.Ceil(float64(b)/float64(GiB))) * GiB
}

// NiceMemoryFloor rounds a size down to a friendly limit; 0 when it is under the smallest.
func NiceMemoryFloor(b int64) int64 {
	var out int64
	for _, s := range memSteps {
		if s > b {
			break
		}
		out = s
	}
	if b > memSteps[len(memSteps)-1] {
		return b / GiB * GiB
	}
	return out
}

// NiceCPUs rounds a CPU count up to a friendly value (0.25, 0.5, 0.75, 1, 1.5, 2, 3, 4, ...).
func NiceCPUs(n float64) float64 {
	for _, s := range cpuSteps {
		if s >= n-1e-9 {
			return s
		}
	}
	return math.Ceil(n)
}

// MemFlag prints a size the way docker run and compose take it: 512m, 1.5g.
func MemFlag(b int64) string {
	if b >= GiB {
		return trimFloat(float64(b)/float64(GiB)) + "g"
	}
	return strconv.FormatInt(int64(math.Round(float64(b)/float64(MiB))), 10) + "m"
}

// MemText prints a size for people: 512 MiB, 1.5 GiB.
func MemText(b int64) string {
	if b >= GiB {
		return trimFloat(float64(b)/float64(GiB)) + " GiB"
	}
	return strconv.FormatInt(int64(math.Round(float64(b)/float64(MiB))), 10) + " MiB"
}

// CPUText prints a CPU count: "0.5", "2".
func CPUText(n float64) string { return trimFloat(n) }

func trimFloat(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

func plural(n float64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func cpusWord(n float64) string { return CPUText(n) + " " + plural(n, "CPU", "CPUs") }

// CPUUse prints CPUs in use, to two places: "1.25 CPUs"; a trickle is "under 0.01 CPU", not 0.
func CPUUse(n float64) string {
	if n < 0.005 {
		return "under 0.01 CPU"
	}
	return cpusWord(math.Round(n*100) / 100)
}

func pct(v float64) string { return strconv.Itoa(int(math.Round(v))) + "%" }

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// Analyse applies every rule and returns the issues, most serious first.
func Analyse(in Input) []Issue {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	var out []Issue
	covered := map[string]bool{} // jobs a container OOM issue already covers
	for _, c := range in.Containers {
		if c.OOMHandled {
			covered[c.JobKey()] = true
		}
		if is := oomIssue(in, c); is != nil {
			out = append(out, *is)
			covered[c.JobKey()] = true
			continue
		}
		if c.State != "running" || c.Usage == nil {
			continue
		}
		if is := memNearIssue(in, c); is != nil {
			out = append(out, *is)
		} else if is := noLimitIssue(in, c); is != nil {
			out = append(out, *is)
		}
		if is := cpuLimitIssue(in, c); is != nil {
			out = append(out, *is)
		}
	}
	for _, j := range in.JobOOMs {
		if covered[j.Job] {
			continue
		}
		if is := jobOOMIssue(in, j); is != nil {
			out = append(out, *is)
		}
	}
	if is := vmCPUIssue(in); is != nil {
		out = append(out, *is)
	}
	if is := vmMemoryIssue(in); is != nil {
		out = append(out, *is)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Severity == sevBad) != (out[j].Severity == sevBad) {
			return out[i].Severity == sevBad
		}
		return kindOrder[out[i].Kind] < kindOrder[out[j].Kind]
	})
	if out == nil {
		out = []Issue{}
	}
	return out
}

func target(in Input, c Container) Target {
	return Target{Profile: in.Profile, ContainerID: c.ID, Name: c.Name, Project: c.Project, Service: c.Service, State: c.State, Job: c.JobKey()}
}

// JobKey is what the runs history groups this container's runs by (history.jobOf): the
// Compose project/service, else the image without its digest, else the name.
func (c Container) JobKey() string {
	if c.Project != "" && c.Service != "" {
		return c.Project + "/" + c.Service
	}
	img := c.Image
	if i := strings.Index(img, "@"); i > 0 {
		img = img[:i]
	}
	if img == "" || strings.HasPrefix(img, "sha256:") {
		return c.Name
	}
	return img
}

// memoryPlan caps a wanted limit at three quarters of the VM. When the cap bites, it also says
// how big the VM would need to be (capped at half the Mac's memory).
func memoryPlan(in Input, want int64) (limit int64, vmGiB int, capped bool) {
	limit = want
	if in.VM.Memory <= 0 {
		return limit, 0, false
	}
	capB := int64(float64(in.VM.Memory) * vmMemCapShare)
	if want <= capB {
		return limit, 0, false
	}
	limit = NiceMemoryFloor(capB)
	need := int(math.Ceil(float64(want) / vmMemCapShare / float64(GiB)))
	cur := int(in.VM.Memory / GiB)
	if need <= cur {
		need = cur + 1
	}
	if half := int(in.Mac.Memory / 2 / GiB); half > 0 && need > half {
		need = half
	}
	if need <= cur {
		need = 0
	}
	return limit, need, true
}

func vmMemoryApply(gib int) *Apply {
	return &Apply{Type: "vm", Body: map[string]any{"memoryGiB": gib}, Label: fmt.Sprintf("Give the VM %d GiB", gib)}
}

func colimaCmd(profile, flags string) string {
	p := ""
	if profile != "" && profile != "default" {
		p = " -p " + profile
	}
	return fmt.Sprintf("colima stop%s && colima start%s %s", p, p, flags)
}

func memSnippets(c Container, mem string) []Snippet {
	svc := c.Service
	if svc == "" {
		svc = c.Name
	}
	img := c.Image
	if img == "" {
		img = "your-image"
	}
	return []Snippet{
		{Label: "Now, on this container", Text: fmt.Sprintf("docker update --memory %s --memory-swap %s %s", mem, swapFor(mem), c.Name)},
		{Label: "In the compose file", Text: fmt.Sprintf("services:\n  %s:\n    mem_limit: %s", svc, mem)},
		{Label: "With docker run", Text: fmt.Sprintf("docker run --memory %s %s", mem, img)},
	}
}

func cpuSnippets(c Container, cpus string) []Snippet {
	svc := c.Service
	if svc == "" {
		svc = c.Name
	}
	img := c.Image
	if img == "" {
		img = "your-image"
	}
	return []Snippet{
		{Label: "Now, on this container", Text: fmt.Sprintf("docker update --cpus %s %s", cpus, c.Name)},
		{Label: "In the compose file", Text: fmt.Sprintf("services:\n  %s:\n    cpus: %s", svc, cpus)},
		{Label: "With docker run", Text: fmt.Sprintf("docker run --cpus %s %s", cpus, img)},
	}
}

// swapFor doubles a size the way docker run does for --memory-swap (the limits API does the same).
func swapFor(mem string) string {
	n, _ := strconv.ParseFloat(mem[:len(mem)-1], 64)
	return trimFloat(2*n) + mem[len(mem)-1:]
}

// ---------- rules ----------

// oomIssue: a stopped container the kernel killed for memory.
func oomIssue(in Input, c Container) *Issue {
	if !c.OOMKilled || c.State == "running" {
		return nil
	}
	is := &Issue{
		ID: "oom:" + short(c.ID), Severity: sevBad, Kind: KindOOM, Target: target(in, c),
		Title: fmt.Sprintf("%s ran out of memory", c.Name),
	}
	var peak int64
	if c.Usage != nil {
		peak = c.Usage.MemPeak
	}
	when := ""
	if !c.FinishedAt.IsZero() {
		when = " " + agoText(in.Now, c.FinishedAt)
	}
	if c.MemLimit > 0 {
		is.Summary = fmt.Sprintf("Stopped%s at its %s memory limit", when, MemText(c.MemLimit))
	} else {
		is.Summary = fmt.Sprintf("Stopped%s when the VM's %s ran out; it has no limit", when, MemText(in.VM.Memory))
	}
	if peak > 0 {
		is.Summary += fmt.Sprintf("; peak seen %s", MemText(peak))
	}
	if c.MemLimit <= 0 {
		// No limit: the whole VM ran out. The fix is a bigger VM, and a limit so one container
		// cannot take everything again.
		gib := vmMemoryNext(in)
		is.Why = fmt.Sprintf("The kernel stopped %s%s because the VM ran out of memory. It had no memory limit of its own, so it could use all %s of the VM.",
			c.Name, when, MemText(in.VM.Memory))
		is.Current = map[string]any{"memory": "", "vmMemoryGiB": in.VM.Memory / GiB}
		is.Recommended = map[string]any{}
		is.Rows = []Row{{"Memory limit", "none", "none (set one when you know its size)"}}
		if gib > 0 {
			is.Why += fmt.Sprintf(" Give the VM %d GiB so it has room.", gib)
			is.Recommended["vmMemoryGiB"] = gib
			is.Rows = append(is.Rows, Row{"VM memory", MemText(in.VM.Memory), fmt.Sprintf("%d GiB", gib)})
			is.Apply = *vmMemoryApply(gib)
			is.Snippets = []Snippet{{Label: "From a terminal", Text: colimaCmd(in.Profile, fmt.Sprintf("--memory %d", gib))}}
		} else {
			is.Why += " The VM already has half of your Mac's memory; set memory limits on the containers that use the most."
			is.Apply = Apply{Type: "none"}
		}
		return is
	}
	want := 2 * c.MemLimit
	if p := int64(1.5 * float64(peak)); p > want {
		want = p
	}
	want = NiceMemory(want)
	limit, vmGiB, capped := memoryPlan(in, want)
	is.Why = fmt.Sprintf("The kernel stopped %s%s because it used all of its %s memory limit.", c.Name, when, MemText(c.MemLimit))
	if peak > 0 {
		is.Why += fmt.Sprintf(" The Monitor saw it use up to %s.", MemText(peak))
	}
	if peak > 0 && int64(1.5*float64(peak)) > 2*c.MemLimit {
		is.Why += fmt.Sprintf(" Give it half as much again as that: %s.", MemText(want))
	} else {
		is.Why += fmt.Sprintf(" Double the limit, rounded up: %s.", MemText(want))
	}
	is.Current = map[string]any{"memory": MemFlag(c.MemLimit)}
	is.Rows = []Row{{"Memory limit", MemText(c.MemLimit), MemText(limit)}}
	if capped {
		if limit <= c.MemLimit {
			// No room in the VM for a bigger limit: the VM comes first.
			is.Why += fmt.Sprintf(" The VM has only %s, so it needs more memory before the container can have more.", MemText(in.VM.Memory))
			is.Rows[0].Recommended = MemText(want) + " after the VM grows"
			is.Recommended = map[string]any{"memory": MemFlag(want)}
			if vmGiB > 0 {
				is.Recommended["vmMemoryGiB"] = vmGiB
				is.Rows = append(is.Rows, Row{"VM memory", MemText(in.VM.Memory), fmt.Sprintf("%d GiB", vmGiB)})
				is.Apply = *vmMemoryApply(vmGiB)
			} else {
				is.Apply = Apply{Type: "none"}
			}
			is.Snippets = append(memSnippets(c, MemFlag(want)), Snippet{Label: "Give the VM more memory", Text: colimaCmd(in.Profile, fmt.Sprintf("--memory %d", vmGiB))})
			return is
		}
		is.Why += fmt.Sprintf(" That is more than three quarters of the VM's %s, so this sets %s now", MemText(in.VM.Memory), MemText(limit))
		if vmGiB > 0 {
			is.Why += fmt.Sprintf(" and suggests giving the VM %d GiB", vmGiB)
			is.VMApply = vmMemoryApply(vmGiB)
			is.Rows = append(is.Rows, Row{"VM memory", MemText(in.VM.Memory), fmt.Sprintf("%d GiB", vmGiB)})
		}
		is.Why += "."
	}
	is.Recommended = map[string]any{"memory": MemFlag(limit)}
	if is.VMApply != nil {
		is.Recommended["vmMemoryGiB"] = vmGiB
	}
	is.Apply = Apply{Type: "limits", Body: map[string]any{"memory": MemFlag(limit)}, Label: "Set " + MemFlag(limit)}
	is.Snippets = memSnippets(c, MemFlag(limit))
	return is
}

// memNearIssue: a running container at 90% or more of its memory limit.
func memNearIssue(in Input, c Container) *Issue {
	u := c.Usage
	if c.MemLimit <= 0 || u.MemNow <= 0 {
		return nil
	}
	frac := float64(u.MemNow) / float64(c.MemLimit)
	if frac < 0.9 {
		return nil
	}
	peak := max(u.MemPeak, u.MemNow)
	want := NiceMemory(int64(1.5 * float64(peak)))
	if want <= c.MemLimit {
		want = NiceMemory(c.MemLimit + 1)
	}
	limit, vmGiB, capped := memoryPlan(in, want)
	if limit <= c.MemLimit {
		limit = 0 // no room to raise it inside this VM
	}
	sev := sevWarn
	if frac >= 0.97 {
		sev = sevBad
	}
	is := &Issue{
		ID: "mem:" + short(c.ID), Severity: sev, Kind: KindMemNear, Target: target(in, c),
		Title: fmt.Sprintf("%s is close to its memory limit", c.Name),
		Why: fmt.Sprintf("%s is using %s of its %s limit (%s). If it needs a little more, the kernel stops it.",
			c.Name, MemText(u.MemNow), MemText(c.MemLimit), pct(frac*100)),
		Current: map[string]any{"memory": MemFlag(c.MemLimit), "used": MemFlag(u.MemNow)},
	}
	is.Summary = fmt.Sprintf("Using %s of a %s limit (%s)", MemText(u.MemNow), MemText(c.MemLimit), pct(frac*100))
	if !u.Snapshot && u.MemPeak > u.MemNow {
		is.Why += fmt.Sprintf(" Its peak in the last few minutes was %s.", MemText(u.MemPeak))
		is.Summary += fmt.Sprintf("; peak %s", MemText(u.MemPeak))
	}
	if limit == 0 {
		is.Why += fmt.Sprintf(" A bigger limit would not fit in the VM's %s: give the VM more memory first.", MemText(in.VM.Memory))
		is.Rows = []Row{{"Memory limit", MemText(c.MemLimit), MemText(want) + " after the VM grows"}}
		is.Recommended = map[string]any{"memory": MemFlag(want)}
		if vmGiB > 0 {
			is.Recommended["vmMemoryGiB"] = vmGiB
			is.Rows = append(is.Rows, Row{"VM memory", MemText(in.VM.Memory), fmt.Sprintf("%d GiB", vmGiB)})
			is.Apply = *vmMemoryApply(vmGiB)
		} else {
			is.Apply = Apply{Type: "none"}
		}
		is.Snippets = memSnippets(c, MemFlag(want))
		return is
	}
	is.Why += fmt.Sprintf(" Raise the limit to %s for headroom.", MemText(limit))
	is.Rows = []Row{{"Memory limit", MemText(c.MemLimit), MemText(limit)}}
	is.Recommended = map[string]any{"memory": MemFlag(limit)}
	if capped && vmGiB > 0 {
		is.VMApply = vmMemoryApply(vmGiB)
		is.Recommended["vmMemoryGiB"] = vmGiB
		is.Rows = append(is.Rows, Row{"VM memory", MemText(in.VM.Memory), fmt.Sprintf("%d GiB", vmGiB)})
	}
	is.Apply = Apply{Type: "limits", Body: map[string]any{"memory": MemFlag(limit)}, Label: "Set " + MemFlag(limit)}
	is.Snippets = memSnippets(c, MemFlag(limit))
	return is
}

// noLimitIssue: a running container with no memory limit using more than half the VM.
func noLimitIssue(in Input, c Container) *Issue {
	u := c.Usage
	total := in.VM.Memory
	if in.VM.Sampled && in.VM.MemTotal > 0 {
		total = in.VM.MemTotal
	}
	if c.MemLimit > 0 || total <= 0 || float64(u.MemNow) <= 0.5*float64(total) {
		return nil
	}
	peak := max(u.MemPeak, u.MemNow)
	want := NiceMemory(int64(1.5 * float64(peak)))
	limit, vmGiB, capped := memoryPlan(in, want)
	is := &Issue{
		ID: "nolimit:" + short(c.ID), Severity: sevWarn, Kind: KindNoLimit, Target: target(in, c),
		Title: fmt.Sprintf("%s has no memory limit and uses over half the VM", c.Name),
		Why: fmt.Sprintf("%s is using %s of the VM's %s (%s) with no limit. If it keeps growing, the kernel starts stopping containers, and it may not pick this one.",
			c.Name, MemText(u.MemNow), MemText(total), pct(100*float64(u.MemNow)/float64(total))),
		Summary: fmt.Sprintf("Using %s with no limit, %s of the VM's %s", MemText(u.MemNow), pct(100*float64(u.MemNow)/float64(total)), MemText(total)),
		Current: map[string]any{"memory": "", "used": MemFlag(u.MemNow)},
	}
	if limit <= peak {
		// a limit under what it already uses would stop it: the VM is the fix
		is.Why += fmt.Sprintf(" A limit that leaves room for it would not fit in the VM's %s: give the VM more memory first.", MemText(in.VM.Memory))
		is.Rows = []Row{{"Memory limit", "none", MemText(want) + " after the VM grows"}}
		is.Recommended = map[string]any{"memory": MemFlag(want)}
		if vmGiB > 0 {
			is.Recommended["vmMemoryGiB"] = vmGiB
			is.Rows = append(is.Rows, Row{"VM memory", MemText(in.VM.Memory), fmt.Sprintf("%d GiB", vmGiB)})
			is.Apply = *vmMemoryApply(vmGiB)
		} else {
			is.Apply = Apply{Type: "none"}
		}
		is.Snippets = memSnippets(c, MemFlag(want))
		return is
	}
	is.Why += fmt.Sprintf(" A limit of %s leaves it room and protects everything else.", MemText(limit))
	is.Rows = []Row{{"Memory limit", "none", MemText(limit)}}
	is.Recommended = map[string]any{"memory": MemFlag(limit)}
	if capped && vmGiB > 0 {
		is.VMApply = vmMemoryApply(vmGiB)
		is.Recommended["vmMemoryGiB"] = vmGiB
		is.Rows = append(is.Rows, Row{"VM memory", MemText(in.VM.Memory), fmt.Sprintf("%d GiB", vmGiB)})
	}
	is.Apply = Apply{Type: "limits", Body: map[string]any{"memory": MemFlag(limit)}, Label: "Set " + MemFlag(limit)}
	is.Snippets = memSnippets(c, MemFlag(limit))
	return is
}

// cpuLimitIssue: a running container using 90% or more of its CPU limit, for a minute or more
// (a single reading has to be 95% or more).
func cpuLimitIssue(in Input, c Container) *Issue {
	u := c.Usage
	if c.CPUs <= 0 {
		return nil
	}
	if u.Snapshot {
		if u.CPU < 0.95*c.CPUs {
			return nil
		}
	} else if u.Window < MinWindow || u.CPU < 0.9*c.CPUs {
		return nil
	}
	want := NiceCPUs(2 * c.CPUs)
	if in.VM.CPUs > 0 && want > float64(in.VM.CPUs) {
		want = float64(in.VM.CPUs)
	}
	if want <= c.CPUs {
		return nil // already allowed the whole VM: the VM rule speaks for it
	}
	over := fmt.Sprintf("over the last %s", durText(u.Window))
	if u.Snapshot {
		over = "when checked"
	}
	// The kernel lets a container run a little past its quota between checks: 0.27 of a 0.25
	// limit is "all of it", not 107%.
	used := math.Min(u.CPU, c.CPUs)
	return &Issue{
		ID: "cpu:" + short(c.ID), Severity: sevWarn, Kind: KindCPULimit, Target: target(in, c),
		Title: fmt.Sprintf("%s is held back by its CPU limit", c.Name),
		Why: fmt.Sprintf("%s used %s of its %s CPU limit %s (%s). It is waiting for CPU, so it runs slower than it could. Raise the limit to %s.",
			c.Name, CPUUse(used), CPUText(c.CPUs), over, pct(100*used/c.CPUs), cpusWord(want)),
		Summary:     fmt.Sprintf("Using %s of a %s CPU limit (%s) %s", CPUUse(used), CPUText(c.CPUs), pct(100*used/c.CPUs), over),
		Current:     map[string]any{"cpus": CPUText(c.CPUs), "used": CPUText(math.Round(used*100) / 100)},
		Recommended: map[string]any{"cpus": CPUText(want)},
		Rows:        []Row{{"CPU limit", cpusWord(c.CPUs), cpusWord(want)}, {"CPU in use", CPUUse(used), CPUUse(used)}},
		Apply:       Apply{Type: "limits", Body: map[string]any{"cpus": CPUText(want)}, Label: "Set " + cpusWord(want)},
		Snippets:    cpuSnippets(c, CPUText(want)),
	}
}

// jobOOMIssue: a test or job run that ended out of memory in the last week.
func jobOOMIssue(in Input, j JobOOM) *Issue {
	if j.Count == 0 {
		return nil
	}
	// The run's container is usually gone (docker compose run --rm): use the limit of a
	// container of the same service when one is still there, and the peak the Monitor saw.
	var limit, peak int64
	for _, c := range in.Containers {
		if j.Project != "" && c.Project == j.Project && c.Service == j.Service {
			if c.MemLimit > limit {
				limit = c.MemLimit
			}
			if c.Usage != nil && c.Usage.MemPeak > peak {
				peak = c.Usage.MemPeak
			}
		}
	}
	want := int64(GiB)
	basis := "We could not see the limit it ran with, so this suggests 1 GiB as a start."
	switch {
	case limit > 0 && int64(1.5*float64(peak)) > 2*limit:
		want = NiceMemory(int64(1.5 * float64(peak)))
		basis = fmt.Sprintf("It had a %s limit and the Monitor saw it reach %s.", MemText(limit), MemText(peak))
	case limit > 0:
		want = NiceMemory(2 * limit)
		basis = fmt.Sprintf("It had a %s limit: double it.", MemText(limit))
	case peak > 0:
		want = NiceMemory(int64(1.5 * float64(peak)))
		basis = fmt.Sprintf("The Monitor saw it reach %s.", MemText(peak))
	}
	mem, vmGiB, _ := memoryPlan(in, want)
	times := "once"
	if j.Count > 1 {
		times = fmt.Sprintf("%d times", j.Count)
	}
	name := j.Job
	is := &Issue{
		ID: "job:" + j.Job, Severity: sevBad, Kind: KindJobOOM,
		Target: Target{Profile: in.Profile, Name: name, Project: j.Project, Service: j.Service, Job: j.Job},
		Title:  fmt.Sprintf("%s ran out of memory", name),
		Why: fmt.Sprintf("Runs of %s were stopped for using too much memory %s in the last %d days, most recently %s. %s",
			name, times, JobDays, agoText(in.Now, j.Last), basis),
		Summary:     fmt.Sprintf("Out of memory %s in %d days, last %s; %s", times, JobDays, agoText(in.Now, j.Last), limitPhrase(limit)),
		Current:     map[string]any{"memory": memFlagOrEmpty(limit)},
		Recommended: map[string]any{"memory": MemFlag(mem)},
		Rows:        []Row{{"Memory limit", orNone(limit), MemText(mem)}},
		Apply:       Apply{Type: "none"},
		Links:       []Link{{Label: "Open Runs and tests", Href: "#runs"}},
	}
	if j.Service != "" {
		is.Why += " Its containers come and go with each run, so set the limit in the compose file."
		is.Snippets = []Snippet{{Label: "In the compose file", Text: fmt.Sprintf("services:\n  %s:\n    mem_limit: %s", j.Service, MemFlag(mem))}}
	} else {
		img := j.Image
		if img == "" {
			img = j.Job
		}
		is.Why += " Its containers come and go with each run, so set the limit where you start it."
		is.Snippets = []Snippet{{Label: "With docker run", Text: fmt.Sprintf("docker run --memory %s %s", MemFlag(mem), img)}}
	}
	if vmGiB > 0 {
		is.VMApply = vmMemoryApply(vmGiB)
		is.Recommended["vmMemoryGiB"] = vmGiB
		is.Rows = append(is.Rows, Row{"VM memory", MemText(in.VM.Memory), fmt.Sprintf("%d GiB", vmGiB)})
		is.Why += fmt.Sprintf(" %s is more than three quarters of the VM, so give the VM %d GiB too.", MemText(want), vmGiB)
	}
	return is
}

func memFlagOrEmpty(b int64) string {
	if b <= 0 {
		return ""
	}
	return MemFlag(b)
}

func limitPhrase(b int64) string {
	if b <= 0 {
		return "its limit is not known"
	}
	return "limit " + MemText(b)
}

func orNone(b int64) string {
	if b <= 0 {
		return "none or unknown"
	}
	return MemText(b)
}

// vmCPUIssue: the VM's CPUs averaged 85% or more for a minute or more.
func vmCPUIssue(in Input) *Issue {
	vm := in.VM
	if !vm.Sampled || vm.Window < MinWindow || vm.CPUPct < 85 || vm.CPUs <= 0 {
		return nil
	}
	want := vm.CPUs + 2
	if in.Mac.CPUs > 0 && want > in.Mac.CPUs {
		want = in.Mac.CPUs
	}
	cons := topConsumers(in, "cpus")
	is := &Issue{
		ID: "vm-cpu", Severity: sevWarn, Kind: KindVMCPU,
		Target: Target{Profile: in.Profile},
		Title:  "High CPU in the VM",
		Why: fmt.Sprintf("The VM's %d CPUs averaged %s busy over the last %s, so containers wait for CPU and everything runs slower.",
			vm.CPUs, pct(vm.CPUPct), durText(vm.Window)),
		Summary:     fmt.Sprintf("%d %s %s busy on average over %s", vm.CPUs, plural(float64(vm.CPUs), "CPU", "CPUs"), pct(vm.CPUPct), durText(vm.Window)),
		Current:     map[string]any{"vmCpus": vm.CPUs, "cpuPct": math.Round(vm.CPUPct)},
		Recommended: map[string]any{},
		Consumers:   cons,
	}
	if len(cons) > 0 {
		names := make([]string, len(cons))
		for i, c := range cons {
			names[i] = fmt.Sprintf("%s (%s, %s of the VM)", c.Name, c.Use, pct(c.Share))
		}
		is.Why += " Most of it is " + joinAnd(names) + "."
	}
	if want > vm.CPUs {
		is.Why += fmt.Sprintf(" Give the VM %d CPUs (your Mac has %d)", want, in.Mac.CPUs)
		if len(cons) > 0 {
			is.Why += ", or limit the busiest container so the rest keep moving"
		}
		is.Why += "."
		is.Recommended["vmCpus"] = want
		is.Rows = []Row{{"VM CPUs", strconv.Itoa(vm.CPUs), strconv.Itoa(want)}}
		is.Apply = Apply{Type: "vm", Body: map[string]any{"cpus": want}, Label: fmt.Sprintf("Give the VM %d CPUs", want)}
		is.Snippets = []Snippet{{Label: "From a terminal", Text: colimaCmd(in.Profile, fmt.Sprintf("--cpus %d", want))}}
	} else {
		is.Why += " The VM already has every CPU your Mac has: limit the busiest containers instead."
		is.Apply = Apply{Type: "none"}
	}
	for _, c := range cons {
		if c.Suggest != "" {
			is.Snippets = append(is.Snippets, Snippet{Label: "Limit " + c.Name, Text: fmt.Sprintf("docker update --cpus %s %s", c.Suggest, c.Name)})
		}
	}
	return is
}

// vmMemoryIssue: 90% or more of the VM's memory in use.
func vmMemoryIssue(in Input) *Issue {
	vm := in.VM
	if !vm.Sampled || vm.MemTotal <= 0 {
		return nil
	}
	frac := float64(vm.MemUsed) / float64(vm.MemTotal)
	if frac < 0.9 {
		return nil
	}
	sev := sevWarn
	if frac >= 0.95 {
		sev = sevBad
	}
	gib := vmMemoryNext(in)
	cons := topConsumers(in, "memory")
	is := &Issue{
		ID: "vm-memory", Severity: sev, Kind: KindVMMemory,
		Target: Target{Profile: in.Profile},
		Title:  "High memory use in the VM",
		Why: fmt.Sprintf("The VM is using %s of its %s (%s). When it runs out, the kernel stops containers to free memory.",
			MemText(vm.MemUsed), MemText(vm.MemTotal), pct(frac*100)),
		Summary:     fmt.Sprintf("%s of %s in use (%s)", MemText(vm.MemUsed), MemText(vm.MemTotal), pct(frac*100)),
		Current:     map[string]any{"vmMemoryGiB": vm.Memory / GiB, "usedPct": math.Round(frac * 100)},
		Recommended: map[string]any{},
		Consumers:   cons,
	}
	if len(cons) > 0 {
		names := make([]string, len(cons))
		for i, c := range cons {
			names[i] = fmt.Sprintf("%s (%s)", c.Name, c.Use)
		}
		is.Why += " The biggest users are " + joinAnd(names) + "."
	}
	if gib > 0 {
		is.Why += fmt.Sprintf(" Give the VM %d GiB", gib)
		if len(cons) > 0 {
			is.Why += ", or put memory limits on the biggest containers"
		}
		is.Why += "."
		is.Recommended["vmMemoryGiB"] = gib
		is.Rows = []Row{{"VM memory", MemText(vm.Memory), fmt.Sprintf("%d GiB", gib)}}
		is.Apply = *vmMemoryApply(gib)
		is.Snippets = []Snippet{{Label: "From a terminal", Text: colimaCmd(in.Profile, fmt.Sprintf("--memory %d", gib))}}
	} else {
		is.Why += " The VM already has half of your Mac's memory: put memory limits on the biggest containers instead."
		is.Apply = Apply{Type: "none"}
	}
	for _, c := range cons {
		if c.Limit == "" {
			is.Snippets = append(is.Snippets, Snippet{Label: "Limit " + c.Name, Text: fmt.Sprintf("docker update --memory %s --memory-swap %s %s", c.Suggest, swapFor(c.Suggest), c.Name)})
		}
	}
	return is
}

// vmMemoryNext is the VM memory to suggest: 2 GiB more, at most half the Mac; 0 when the VM
// already has that much.
func vmMemoryNext(in Input) int {
	cur := int(in.VM.Memory / GiB)
	want := cur + 2
	if half := int(in.Mac.Memory / 2 / GiB); half > 0 && want > half {
		want = half
	}
	if want <= cur {
		return 0
	}
	return want
}

// topConsumers names the running containers using the most CPU or memory (up to three, each
// at least 10% of the VM), with a limit to suggest.
func topConsumers(in Input, field string) []Consumer {
	var out []Consumer
	for _, c := range in.Containers {
		if c.State != "running" || c.Usage == nil {
			continue
		}
		u := c.Usage
		var cons Consumer
		switch field {
		case "cpus":
			if in.VM.CPUs <= 0 {
				continue
			}
			share := 100 * u.CPU / float64(in.VM.CPUs)
			if share < 10 {
				continue
			}
			// a limit of half what it uses now; one that already has a limit is left alone
			cons = Consumer{ContainerID: c.ID, Name: c.Name, Share: math.Round(share), Use: CPUUse(u.CPU), Field: field}
			if c.CPUs > 0 {
				cons.Limit = CPUText(c.CPUs)
			} else {
				cons.Suggest = CPUText(NiceCPUs(u.CPU / 2))
			}
		case "memory":
			total := in.VM.MemTotal
			if total <= 0 {
				continue
			}
			share := 100 * float64(u.MemNow) / float64(total)
			if share < 10 {
				continue
			}
			peak := max(u.MemPeak, u.MemNow)
			cons = Consumer{ContainerID: c.ID, Name: c.Name, Share: math.Round(share), Use: MemText(u.MemNow), Field: field}
			if c.MemLimit > 0 {
				cons.Limit = MemFlag(c.MemLimit)
			} else {
				cons.Suggest = MemFlag(NiceMemory(int64(1.2 * float64(peak))))
			}
		}
		out = append(out, cons)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Share > out[j].Share })
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func durText(d time.Duration) string {
	if d < 90*time.Second {
		return fmt.Sprintf("%d seconds", int(d.Seconds()+0.5))
	}
	m := int(d.Minutes() + 0.5)
	return fmt.Sprintf("%d %s", m, plural(float64(m), "minute", "minutes"))
}

func agoText(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 0:
		return "just now"
	case d < 90*time.Second:
		return "just now"
	case d < 90*time.Minute:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()+0.5))
	case d < 36*time.Hour:
		h := int(d.Hours() + 0.5)
		return fmt.Sprintf("%d %s ago", h, plural(float64(h), "hour", "hours"))
	}
	days := int(d.Hours()/24 + 0.5)
	return fmt.Sprintf("%d %s ago", days, plural(float64(days), "day", "days"))
}
