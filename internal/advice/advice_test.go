package advice

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func base() Input {
	return Input{
		Profile: "default",
		VM:      VM{CPUs: 2, Memory: 2 * GiB},
		Mac:     Mac{CPUs: 10, Memory: 32 * GiB},
		Now:     now,
	}
}

func find(t *testing.T, issues []Issue, kind string) Issue {
	t.Helper()
	for _, is := range issues {
		if is.Kind == kind {
			return is
		}
	}
	t.Fatalf("no %s issue in %+v", kind, issues)
	return Issue{}
}

func none(t *testing.T, issues []Issue, kind string) {
	t.Helper()
	for _, is := range issues {
		if is.Kind == kind {
			t.Fatalf("unexpected %s issue: %+v", kind, is)
		}
	}
}

func TestNice(t *testing.T) {
	mem := map[int64]string{1: "64m", 64 * MiB: "64m", 65 * MiB: "128m", 200 * MiB: "256m", 600 * MiB: "768m", 900 * MiB: "1g",
		GiB + 1: "1.5g", 5 * GiB: "6g", 9 * GiB: "12g", 100 * GiB: "100g"}
	for in, want := range mem {
		if got := MemFlag(NiceMemory(in)); got != want {
			t.Errorf("NiceMemory(%d) = %s, want %s", in, got, want)
		}
	}
	if got := MemFlag(NiceMemoryFloor(1536 * MiB)); got != "1.5g" {
		t.Errorf("floor 1.5g = %s", got)
	}
	if got := NiceMemoryFloor(10 * MiB); got != 0 {
		t.Errorf("floor under the smallest = %d", got)
	}
	cpu := map[float64]float64{0.1: 0.25, 0.25: 0.25, 0.3: 0.5, 0.5: 0.5, 0.6: 0.75, 1.2: 1.5, 2.5: 3, 5: 6, 100: 100}
	for in, want := range cpu {
		if got := NiceCPUs(in); got != want {
			t.Errorf("NiceCPUs(%v) = %v, want %v", in, got, want)
		}
	}
	if CPUUse(0.001) != "under 0.01 CPU" || CPUUse(0) != "under 0.01 CPU" || CPUUse(0.248) != "0.25 CPUs" || CPUUse(1) != "1 CPU" {
		t.Error("CPUUse", CPUUse(0.001), CPUUse(0.248), CPUUse(1))
	}
	if MemText(1536*MiB) != "1.5 GiB" || MemText(256*MiB) != "256 MiB" || swapFor("1.5g") != "3g" || swapFor("64m") != "128m" {
		t.Error("formatting")
	}
}

func TestOOMDoublesTheLimit(t *testing.T) {
	in := base()
	in.Containers = []Container{{ID: "abcdef0123456789", Name: "hog", State: "exited", OOMKilled: true, ExitCode: 137, MemLimit: 16 * MiB, FinishedAt: now.Add(-10 * time.Minute)}}
	is := find(t, Analyse(in), KindOOM)
	if is.Severity != "bad" || is.Apply.Type != "limits" || is.Apply.Body["memory"] != "64m" {
		t.Fatalf("got %+v", is)
	}
	if is.ID != "oom:abcdef012345" || is.Target.ContainerID != "abcdef0123456789" || is.VMApply != nil {
		t.Fatalf("got %+v", is)
	}
	if !strings.Contains(is.Why, "16 MiB") || !strings.Contains(is.Why, "10 minutes ago") {
		t.Fatalf("why: %s", is.Why)
	}
	if len(is.Snippets) != 3 || !strings.Contains(is.Snippets[1].Text, "mem_limit: 64m") {
		t.Fatalf("snippets: %+v", is.Snippets)
	}
}

func TestOOMUsesPeakWhenBigger(t *testing.T) {
	in := base()
	in.Containers = []Container{{ID: "c1", Name: "api", State: "exited", OOMKilled: true, MemLimit: 128 * MiB, Usage: &Usage{MemPeak: 300 * MiB}}}
	is := find(t, Analyse(in), KindOOM)
	if is.Apply.Body["memory"] != "512m" { // 1.5 x 300m = 450m -> 512m (more than 2 x 128m)
		t.Fatalf("got %v", is.Apply.Body)
	}
}

func TestOOMCappedAtThreeQuartersOfTheVM(t *testing.T) {
	in := base()
	in.Containers = []Container{{ID: "c1", Name: "big", State: "exited", OOMKilled: true, MemLimit: 1 * GiB}}
	is := find(t, Analyse(in), KindOOM)
	// 2g wanted, the VM has 2 GiB: set 1.5g now and suggest a bigger VM (2g / 0.75 = 2.67 -> 3 GiB)
	if is.Apply.Type != "limits" || is.Apply.Body["memory"] != "1.5g" {
		t.Fatalf("apply %+v", is.Apply)
	}
	if is.VMApply == nil || is.VMApply.Body["memoryGiB"] != 3 {
		t.Fatalf("vm apply %+v", is.VMApply)
	}
}

func TestOOMNoRoomMeansVMFirst(t *testing.T) {
	in := base()
	in.Containers = []Container{{ID: "c1", Name: "big", State: "exited", OOMKilled: true, MemLimit: 1536 * MiB}}
	is := find(t, Analyse(in), KindOOM)
	if is.Apply.Type != "vm" || is.Apply.Body["memoryGiB"] != 4 { // 3g / 0.75 = 4 GiB
		t.Fatalf("apply %+v", is.Apply)
	}
}

func TestOOMWithoutLimitRaisesVM(t *testing.T) {
	in := base()
	in.Containers = []Container{{ID: "c1", Name: "free", State: "exited", OOMKilled: true}}
	is := find(t, Analyse(in), KindOOM)
	if is.Apply.Type != "vm" || is.Apply.Body["memoryGiB"] != 4 {
		t.Fatalf("apply %+v", is.Apply)
	}
	// at half the Mac already: nothing to apply
	in.VM.Memory = 16 * GiB
	is = find(t, Analyse(in), KindOOM)
	if is.Apply.Type != "none" {
		t.Fatalf("apply %+v", is.Apply)
	}
}

func TestRunningOOMFlagIgnored(t *testing.T) {
	in := base()
	in.Containers = []Container{{ID: "c1", Name: "back", State: "running", OOMKilled: true, MemLimit: 64 * MiB}}
	none(t, Analyse(in), KindOOM)
}

func TestMemoryNearLimit(t *testing.T) {
	in := base()
	c := Container{ID: "c1", Name: "web", State: "running", MemLimit: 100 * MiB, Usage: &Usage{MemNow: 92 * MiB, MemPeak: 95 * MiB, Window: time.Minute}}
	in.Containers = []Container{c}
	is := find(t, Analyse(in), KindMemNear)
	if is.Severity != "warn" || is.Apply.Body["memory"] != "192m" { // 1.5 x 95m = 142.5m -> 192m
		t.Fatalf("got %+v", is)
	}
	c.Usage = &Usage{MemNow: 80 * MiB, MemPeak: 99 * MiB}
	in.Containers = []Container{c}
	none(t, Analyse(in), KindMemNear) // judged on use now, not the peak
}

func TestNoLimitOverHalfTheVM(t *testing.T) {
	in := base()
	in.VM.Sampled, in.VM.MemTotal, in.VM.MemUsed = true, 1900*MiB, 1300*MiB
	in.Containers = []Container{{ID: "c1", Name: "db", State: "running", Usage: &Usage{MemNow: 1000 * MiB, MemPeak: 1000 * MiB}}}
	is := find(t, Analyse(in), KindNoLimit)
	// 1.5 x 1000m = 1.46g -> 1.5g, which fits 75% of 2 GiB exactly
	if is.Apply.Type != "limits" || is.Apply.Body["memory"] != "1.5g" {
		t.Fatalf("got %+v", is.Apply)
	}
	in.Containers[0].Usage.MemNow = 500 * MiB
	none(t, Analyse(in), KindNoLimit)
}

func TestNoLimitTooBigForVM(t *testing.T) {
	in := base()
	in.Containers = []Container{{ID: "c1", Name: "db", State: "running", Usage: &Usage{MemNow: 1600 * MiB, MemPeak: 1600 * MiB}}}
	is := find(t, Analyse(in), KindNoLimit)
	if is.Apply.Type != "vm" {
		t.Fatalf("a limit under its use would stop it: %+v", is.Apply)
	}
}

func TestCPUAtLimit(t *testing.T) {
	in := base()
	c := Container{ID: "c1", Name: "spin", State: "running", CPUs: 0.25, Usage: &Usage{CPU: 0.248, Window: 2 * time.Minute}}
	in.Containers = []Container{c}
	is := find(t, Analyse(in), KindCPULimit)
	if is.Apply.Body["cpus"] != "0.5" || !strings.Contains(is.Why, "2 minutes") {
		t.Fatalf("got %+v", is)
	}
	// not enough history yet
	c.Usage.Window = 20 * time.Second
	in.Containers = []Container{c}
	none(t, Analyse(in), KindCPULimit)
	// a single reading must be 95% or more
	c.Usage = &Usage{CPU: 0.23, Snapshot: true}
	in.Containers = []Container{c}
	none(t, Analyse(in), KindCPULimit)
	c.Usage = &Usage{CPU: 0.245, Snapshot: true}
	in.Containers = []Container{c}
	if is := find(t, Analyse(in), KindCPULimit); !strings.Contains(is.Why, "when checked") {
		t.Fatalf("why: %s", is.Why)
	}
	// capped at the VM's CPUs; already there means no container issue
	c.CPUs, c.Usage = 1.5, &Usage{CPU: 1.5, Window: time.Minute}
	in.Containers = []Container{c}
	if is := find(t, Analyse(in), KindCPULimit); is.Apply.Body["cpus"] != "2" {
		t.Fatalf("cap: %+v", is.Apply)
	}
	c.CPUs, c.Usage = 2, &Usage{CPU: 2, Window: time.Minute}
	in.Containers = []Container{c}
	none(t, Analyse(in), KindCPULimit)
}

func TestVMHighCPU(t *testing.T) {
	in := base()
	in.VM.Sampled, in.VM.CPUPct, in.VM.Window = true, 93, 2*time.Minute
	in.VM.MemTotal, in.VM.MemUsed = 1900*MiB, 600*MiB
	in.Containers = []Container{
		{ID: "a", Name: "hog1", State: "running", Usage: &Usage{CPU: 1.1, Window: time.Minute}},
		{ID: "b", Name: "hog2", State: "running", Usage: &Usage{CPU: 0.7, Window: time.Minute}},
		{ID: "c", Name: "quiet", State: "running", Usage: &Usage{CPU: 0.01, Window: time.Minute}},
	}
	is := find(t, Analyse(in), KindVMCPU)
	if is.Apply.Type != "vm" || is.Apply.Body["cpus"] != 4 {
		t.Fatalf("apply %+v", is.Apply)
	}
	if len(is.Consumers) != 2 || is.Consumers[0].Name != "hog1" || is.Consumers[0].Share != 55 || is.Consumers[0].Suggest != "0.75" {
		t.Fatalf("consumers %+v", is.Consumers)
	}
	if !strings.Contains(is.Why, "hog1 (1.1 CPUs, 55% of the VM)") {
		t.Fatalf("why %s", is.Why)
	}
	// capped at the Mac's cores
	in.VM.CPUs, in.Mac.CPUs = 9, 10
	if is := find(t, Analyse(in), KindVMCPU); is.Apply.Body["cpus"] != 10 {
		t.Fatalf("cap %+v", is.Apply)
	}
	in.VM.CPUs = 10
	if is := find(t, Analyse(in), KindVMCPU); is.Apply.Type != "none" {
		t.Fatalf("no more cores %+v", is.Apply)
	}
	// too little history, or not busy enough
	in.VM.Window = 30 * time.Second
	none(t, Analyse(in), KindVMCPU)
	in.VM.Window, in.VM.CPUPct = 2*time.Minute, 70
	none(t, Analyse(in), KindVMCPU)
	in.VM.Sampled, in.VM.CPUPct = false, 99
	none(t, Analyse(in), KindVMCPU)
}

func TestVMHighMemory(t *testing.T) {
	in := base()
	in.VM.Sampled, in.VM.MemTotal, in.VM.MemUsed = true, 1900*MiB, 1850*MiB
	in.Containers = []Container{{ID: "a", Name: "db", State: "running", MemLimit: 0, Usage: &Usage{MemNow: 900 * MiB, MemPeak: 950 * MiB}}}
	is := find(t, Analyse(in), KindVMMemory)
	if is.Severity != "bad" || is.Apply.Type != "vm" || is.Apply.Body["memoryGiB"] != 4 {
		t.Fatalf("got %+v", is)
	}
	if len(is.Consumers) != 1 || is.Consumers[0].Suggest != "1.5g" {
		t.Fatalf("consumers %+v", is.Consumers)
	}
	// capped at half the Mac
	in.VM.Memory, in.Mac.Memory = 15*GiB, 32*GiB
	if is := find(t, Analyse(in), KindVMMemory); is.Apply.Body["memoryGiB"] != 16 {
		t.Fatalf("cap %+v", is.Apply)
	}
	in.VM.MemUsed = 1500 * MiB
	none(t, Analyse(in), KindVMMemory)
}

func TestJobOOM(t *testing.T) {
	in := base()
	in.JobOOMs = []JobOOM{{Job: "shop/unit-tests", Project: "shop", Service: "unit-tests", Count: 3, Last: now.Add(-2 * time.Hour)}}
	is := find(t, Analyse(in), KindJobOOM)
	if is.Apply.Type != "none" || is.Recommended["memory"] != "1g" || len(is.Links) != 1 || is.Links[0].Href != "#runs" {
		t.Fatalf("got %+v", is)
	}
	if !strings.Contains(is.Snippets[0].Text, "unit-tests:\n    mem_limit: 1g") || !strings.Contains(is.Why, "3 times") || !strings.Contains(is.Why, "2 hours ago") {
		t.Fatalf("got %+v", is)
	}
	// a container of the same service shows its limit: double it
	in.Containers = []Container{{ID: "x", Name: "shop-unit-tests-1", Project: "shop", Service: "unit-tests", State: "exited", MemLimit: 256 * MiB}}
	if is := find(t, Analyse(in), KindJobOOM); is.Recommended["memory"] != "512m" {
		t.Fatalf("got %+v", is.Recommended)
	}
	// that container itself killed for memory: one issue, not two, and it names the job
	in.Containers[0].OOMKilled = true
	issues := Analyse(in)
	none(t, issues, KindJobOOM)
	if is := find(t, issues, KindOOM); is.Target.Job != "shop/unit-tests" {
		t.Fatalf("target %+v", is.Target)
	}
	// a plain docker run: the job is the image without its digest
	in.JobOOMs = []JobOOM{{Job: "alpine", Image: "alpine", Count: 1, Last: now}}
	in.Containers = []Container{{ID: "y", Name: "hog", Image: "alpine@sha256:abc", State: "exited", OOMKilled: true, MemLimit: 16 * MiB}}
	issues = Analyse(in)
	none(t, issues, KindJobOOM)
	if is := find(t, issues, KindOOM); is.Target.Job != "alpine" {
		t.Fatalf("target %+v", is.Target)
	}
	// its limit raised since: no issue for it, and its runs are still covered
	in.Containers[0].OOMKilled, in.Containers[0].OOMHandled = false, true
	if issues := Analyse(in); len(issues) != 0 {
		t.Fatalf("want none, got %v", kinds(issues))
	}
}

func TestOrderAndEmpty(t *testing.T) {
	in := base()
	if got := Analyse(in); got == nil || len(got) != 0 {
		t.Fatalf("want an empty list, got %#v", got)
	}
	in.VM.Sampled, in.VM.CPUPct, in.VM.Window, in.VM.MemTotal, in.VM.MemUsed = true, 95, time.Minute, 1900*MiB, 100*MiB
	in.Containers = []Container{
		{ID: "a", Name: "spin", State: "running", CPUs: 0.5, Usage: &Usage{CPU: 0.5, Window: time.Minute}},
		{ID: "b", Name: "dead", State: "exited", OOMKilled: true, MemLimit: 64 * MiB},
	}
	issues := Analyse(in)
	if len(issues) != 3 || issues[0].Kind != KindOOM || issues[1].Kind != KindVMCPU || issues[2].Kind != KindCPULimit {
		t.Fatalf("order: %v", kinds(issues))
	}
	want := []string{"Stopped at its 64 MiB memory limit", "2 CPUs 95% busy on average over 60 seconds", "Using 0.5 CPUs of a 0.5 CPU limit (100%) over the last 60 seconds"}
	for i, is := range issues {
		if is.Summary != want[i] {
			t.Errorf("summary %d: %q, want %q", i, is.Summary, want[i])
		}
	}
}

func kinds(issues []Issue) []string {
	var out []string
	for _, is := range issues {
		out = append(out, is.Kind)
	}
	return out
}

func TestColimaCommand(t *testing.T) {
	if got := colimaCmd("default", "--cpus 4"); got != "colima stop && colima start --cpus 4" {
		t.Error(got)
	}
	if got := colimaCmd("work", "--memory 6"); got != "colima stop -p work && colima start -p work --memory 6" {
		t.Error(got)
	}
}

func TestDurTextUnits(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Second:                     "1 second",
		45 * time.Second:                "45 seconds",
		5 * time.Minute:                 "5 minutes",
		5 * time.Hour:                   "5 hours",
		3 * 24 * time.Hour:              "3 days",
		89*time.Minute + 59*time.Second: "90 minutes",
	} {
		if got := durText(d); got != want {
			t.Errorf("%v: got %q, want %q", d, got, want)
		}
	}
}
