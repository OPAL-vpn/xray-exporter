package metrics

import (
	"testing"
	"time"
)

func TestCycleSeries(t *testing.T) {
	snap := New()
	snap.Seed("nl1", []string{"a.test"}, nil, seedTime)

	snap.SetCycle("nl1", secondTime, 1500*time.Millisecond, 1)
	samples := gatherSamples(t, snap)

	if got := mustSample(t, samples, "xray_cycle_timestamp_seconds", labels("config", "nl1")); got != timestampSeconds(secondTime) {
		t.Errorf("xray_cycle_timestamp_seconds = %v, want %v", got, timestampSeconds(secondTime))
	}
	if got := mustSample(t, samples, "xray_cycle_duration_seconds", labels("config", "nl1")); got != 1.5 {
		t.Errorf("xray_cycle_duration_seconds = %v, want 1.5", got)
	}
	if got := mustSample(t, samples, "xray_cycle_targets_total", labels("config", "nl1")); got != 1 {
		t.Errorf("xray_cycle_targets_total = %v, want 1", got)
	}
}
