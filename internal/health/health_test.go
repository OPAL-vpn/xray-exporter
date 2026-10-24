package health

import (
	"strings"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
)

func TestOverran(t *testing.T) {
	const deadline = 150 * time.Second

	cases := []struct {
		name  string
		cycle metrics.CycleExport
		want  bool
	}{
		{name: "deadline cut the cycle", cycle: metrics.CycleExport{Done: true, Duration: deadline}, want: true},
		{name: "cycle finished early", cycle: metrics.CycleExport{Done: true, Duration: 2 * time.Second}, want: false},
		{name: "cycle never ran", cycle: metrics.CycleExport{}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Overran(tc.cycle, deadline); got != tc.want {
				t.Errorf("Overran(%+v, %s) = %v, want %v", tc.cycle, deadline, got, tc.want)
			}
		})
	}

	if Overran(metrics.CycleExport{Done: true, Duration: time.Hour}, 0) {
		t.Error("Overran with no deadline = true, want false")
	}
}

func TestUnhealthy(t *testing.T) {
	const deadline = 150 * time.Second

	healthy := metrics.ConfigExport{
		Name:    "nl1",
		Running: true,
		Cycle:   metrics.CycleExport{Done: true, Duration: 2 * time.Second},
	}
	if reasons := Reasons(metrics.Export{Configs: []metrics.ConfigExport{healthy}}, deadline); len(reasons) != 0 {
		t.Errorf("Reasons(healthy config) = %v, want no reasons", reasons)
	}

	broken := metrics.ConfigExport{
		Name:  "nl1",
		Cycle: metrics.CycleExport{},
	}
	reasons := Reasons(metrics.Export{Configs: []metrics.ConfigExport{broken}}, deadline)
	joined := strings.Join(reasons, "; ")
	for _, want := range []string{"not running", "cycle did not complete"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Reasons(broken config) = %v, want it to mention %q", reasons, want)
		}
	}

	if reasons := Reasons(metrics.Export{}, deadline); len(reasons) == 0 {
		t.Error("Reasons(empty export) = no reasons, want one for the absence of configs")
	}
}

// TestUnhealthyReportsATruncatedCycle pins that a cycle cut short is incomplete.
func TestUnhealthyReportsATruncatedCycle(t *testing.T) {
	const deadline = 150 * time.Second
	now := time.Now()

	snap := metrics.New()
	snap.Seed("nl1", []string{"a.test"}, nil, now)
	snap.SetConfig("nl1", true, "26.1.1")

	snap.SetCycle("nl1", now, deadline, 1)
	reasons := Reasons(snap.Export(), deadline)
	if len(reasons) != 1 || reasons[0] != "nl1 cycle did not complete" {
		t.Fatalf("Reasons(truncated cycle) = %v, want [nl1 cycle did not complete]", reasons)
	}

	snap.SetCycle("nl1", now, deadline-time.Second, 1)
	if reasons := Reasons(snap.Export(), deadline); len(reasons) != 0 {
		t.Errorf("Reasons(completed cycle) = %v, want no reasons", reasons)
	}
}
