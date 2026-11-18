package docker

import (
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	good := map[string]int64{
		"256m": 256 << 20, "1g": 1 << 30, "1G": 1 << 30, "1.5g": 3 << 29, "512MiB": 512 << 20,
		"512mb": 512 << 20, "1024k": 1 << 20, "1048576": 1 << 20, " 2g ": 2 << 30, "100b": 100, "1t": 1 << 40,
	}
	for in, want := range good {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "-1g", "1x", "1 g b", "g", "1e9", "ib", "5ib", "1.2.3m"} {
		if _, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) should fail", in)
		}
	}
}

func TestParseCPUs(t *testing.T) {
	good := map[string]int64{"0.5": 5e8, "2": 2e9, "0.01": 1e7, "1.25": 125e7}
	for in, want := range good {
		got, err := ParseCPUs(in)
		if err != nil || got != want {
			t.Errorf("ParseCPUs(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "0.001", "-1", "two", "NaN", "Inf", "2000"} {
		if _, err := ParseCPUs(in); err == nil {
			t.Errorf("ParseCPUs(%q) should fail", in)
		}
	}
}

func ptr(s string) *string { return &s }

func TestUpdateBody(t *testing.T) {
	none := &Details{Restart: "no"}
	b, err := UpdateBody(none, LimitsRequest{Memory: "512m", CPUs: "0.5", Restart: ptr("unless-stopped")})
	if err != nil {
		t.Fatal(err)
	}
	if b["Memory"] != int64(512<<20) || b["MemorySwap"] != int64(1<<30) || b["NanoCpus"] != int64(5e8) {
		t.Fatalf("body %v", b)
	}
	if rp := b["RestartPolicy"].(map[string]any); rp["Name"] != "unless-stopped" {
		t.Fatalf("restart %v", rp)
	}

	// unlimited swap stays unlimited
	b, _ = UpdateBody(&Details{MemorySwap: -1}, LimitsRequest{Memory: "1g"})
	if b["MemorySwap"] != int64(-1) {
		t.Fatalf("swap %v", b["MemorySwap"])
	}

	// a quota-limited container gets its quota changed, not NanoCpus
	b, _ = UpdateBody(&Details{CPUQuota: 50000, CPUPeriod: 100000}, LimitsRequest{CPUs: "1.5"})
	if b["CpuQuota"] != int64(150000) || b["NanoCpus"] != nil {
		t.Fatalf("quota body %v", b)
	}

	// same restart policy: nothing to send
	if _, err := UpdateBody(&Details{Restart: "always"}, LimitsRequest{Restart: ptr("always")}); err != ErrNoChange {
		t.Fatalf("want ErrNoChange, got %v", err)
	}
	b, _ = UpdateBody(&Details{Restart: "no"}, LimitsRequest{Restart: ptr("on-failure"), MaxRetries: 3})
	if rp := b["RestartPolicy"].(map[string]any); rp["MaximumRetryCount"] != 3 {
		t.Fatalf("retries %v", rp)
	}

	bad := []struct {
		cur  *Details
		req  LimitsRequest
		want string
	}{
		{none, LimitsRequest{Memory: "1m"}, "smallest"},
		{none, LimitsRequest{Memory: "lots"}, "not a size"},
		{none, LimitsRequest{Memory: "256m", MemoryReservation: "512m"}, "must not be more"},
		{&Details{Memory: 256 << 20}, LimitsRequest{MemoryReservation: "1g"}, "must not be more"},
		{&Details{MemoryReservation: 512 << 20}, LimitsRequest{Memory: "256m"}, "less than the memory reservation"},
		{none, LimitsRequest{CPUs: "0"}, "at least"},
		{none, LimitsRequest{Restart: ptr("sometimes")}, "use one of"},
		{none, LimitsRequest{Restart: ptr("always"), MaxRetries: 2}, "only apply"},
		{none, LimitsRequest{}, "nothing"},
	}
	for _, c := range bad {
		if _, err := UpdateBody(c.cur, c.req); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("UpdateBody(%+v) err = %v; want %q", c.req, err, c.want)
		}
	}
}

func TestHealthAndExit(t *testing.T) {
	for in, want := range map[string]string{
		"Up 2 minutes (healthy)": "healthy", "Up 1 second (unhealthy)": "unhealthy",
		"Up 3 seconds (health: starting)": "starting", "Up 2 hours": "", "Exited (0) 1 minute ago": "",
	} {
		if got := HealthFromStatus(in); got != want {
			t.Errorf("HealthFromStatus(%q) = %q", in, got)
		}
	}
	if ExitCodeFromStatus("Exited (137) 5 minutes ago") != 137 || ExitCodeFromStatus("Up 1 minute") != -1 {
		t.Error("ExitCodeFromStatus")
	}
}

func TestHumanSize(t *testing.T) {
	for in, want := range map[int64]string{512 << 20: "512m", 3 << 29: "1.5g", 1 << 30: "1g", 100: "100", 10 << 30: "10g"} {
		if got := HumanSize(in); got != want {
			t.Errorf("HumanSize(%d) = %q; want %q", in, got, want)
		}
	}
}

func TestSplitStamp(t *testing.T) {
	ts, text, ok := SplitStamp("2026-11-18T10:00:00.5+02:00 hello world")
	if !ok || ts != "2026-11-18T08:00:00.500000000Z" || text != "hello world" {
		t.Fatalf("got %q %q %v", ts, text, ok)
	}
	if _, text, ok := SplitStamp("no stamp here"); ok || text != "no stamp here" {
		t.Fatal("line without a stamp")
	}
	if ts, text, ok := SplitStamp("2026-11-18T10:00:00Z"); !ok || text != "" || ts == "" {
		t.Fatal("empty line with a stamp")
	}
}
