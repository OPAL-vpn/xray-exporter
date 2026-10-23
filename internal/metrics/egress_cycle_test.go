package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/egress"
)

func TestEgressAndCycleSeries(t *testing.T) {
	snap := New()
	snap.Seed("nl1", []string{"a.test"}, nil, seedTime)

	snap.SetEgress("nl1", egress.Result{IP: "203.0.113.7", Verified: true}, secondTime)
	snap.SetCycle("nl1", secondTime, 1500*time.Millisecond, 1)
	samples := gatherSamples(t, snap)

	if got := mustSample(t, samples, "xray_egress_verified", labels("config", "nl1")); got != 1 {
		t.Errorf("xray_egress_verified = %v, want 1", got)
	}
	if got := mustSample(t, samples, "xray_egress_ip_info", labels("config", "nl1", "ip", "203.0.113.7")); got != 1 {
		t.Errorf("xray_egress_ip_info = %v, want 1", got)
	}
	if got := mustSample(t, samples, "xray_egress_timestamp_seconds", labels("config", "nl1")); got != timestampSeconds(secondTime) {
		t.Errorf("xray_egress_timestamp_seconds = %v, want %v", got, timestampSeconds(secondTime))
	}
	if got := mustSample(t, samples, "xray_cycle_timestamp_seconds", labels("config", "nl1")); got != timestampSeconds(secondTime) {
		t.Errorf("xray_cycle_timestamp_seconds = %v, want %v", got, timestampSeconds(secondTime))
	}
	if got := mustSample(t, samples, "xray_cycle_duration_seconds", labels("config", "nl1")); got != 1.5 {
		t.Errorf("xray_cycle_duration_seconds = %v, want 1.5", got)
	}
	if got := mustSample(t, samples, "xray_cycle_targets_total", labels("config", "nl1")); got != 1 {
		t.Errorf("xray_cycle_targets_total = %v, want 1", got)
	}

	// A later failed egress check keeps the last IP but flips the verdict.
	snap.SetEgress("nl1", egress.Result{Err: errors.New("baseline fetch failed")}, thirdTime)
	samples = gatherSamples(t, snap)
	if got := mustSample(t, samples, "xray_egress_verified", labels("config", "nl1")); got != 0 {
		t.Errorf("xray_egress_verified after failure = %v, want 0", got)
	}
	if got := mustSample(t, samples, "xray_egress_timestamp_seconds", labels("config", "nl1")); got != timestampSeconds(thirdTime) {
		t.Errorf("xray_egress_timestamp_seconds after failure = %v, want %v", got, timestampSeconds(thirdTime))
	}
	if got := mustSample(t, samples, "xray_egress_ip_info", labels("config", "nl1", "ip", "203.0.113.7")); got != 1 {
		t.Errorf("xray_egress_ip_info after failure = %v, want the last observed address", got)
	}
}

func TestConfigSeries(t *testing.T) {
	snap := New()
	snap.Seed("nl1", []string{"a.test"}, nil, seedTime)

	snap.SetConfig("nl1", true, "v26.10.21")
	samples := gatherSamples(t, snap)

	if got := mustSample(t, samples, "xray_config_running", labels("config", "nl1")); got != 1 {
		t.Errorf("xray_config_running = %v, want 1", got)
	}
	if got := mustSample(t, samples, "xray_config_info", labels("config", "nl1", "xray_version", "v26.10.21")); got != 1 {
		t.Errorf("xray_config_info = %v, want 1", got)
	}

	snap.SetConfig("nl1", false, "")
	samples = gatherSamples(t, snap)
	if got := mustSample(t, samples, "xray_config_running", labels("config", "nl1")); got != 0 {
		t.Errorf("xray_config_running after stop = %v, want 0", got)
	}
	// The version is not forgotten by a later failure that has none to report.
	if got := mustSample(t, samples, "xray_config_info", labels("config", "nl1", "xray_version", "v26.10.21")); got != 1 {
		t.Errorf("xray_config_info after stop = %v, want 1", got)
	}
}
