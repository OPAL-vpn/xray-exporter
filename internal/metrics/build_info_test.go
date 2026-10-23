package metrics

import (
	"testing"
)

func TestSetBuildInfo(t *testing.T) {
	snap := New()
	snap.SetBuildInfo("v1", "go1.27.1")
	samples := gatherSamples(t, snap)

	if got := mustSample(t, samples, "xray_build_info", labels("version", "v1", "go_version", "go1.27.1")); got != 1 {
		t.Errorf("xray_build_info = %v, want 1", got)
	}
}

func TestReloadCounters(t *testing.T) {
	snap := New()
	samples := gatherSamples(t, snap)
	if got := mustSample(t, samples, "xray_exporter_config_reload_total", map[string]string{}); got != 0 {
		t.Errorf("xray_exporter_config_reload_total = %v, want 0", got)
	}

	snap.IncReload()
	snap.IncReload()
	snap.IncReloadError()
	samples = gatherSamples(t, snap)
	if got := mustSample(t, samples, "xray_exporter_config_reload_total", map[string]string{}); got != 2 {
		t.Errorf("xray_exporter_config_reload_total = %v, want 2", got)
	}
	if got := mustSample(t, samples, "xray_exporter_config_reload_errors_total", map[string]string{}); got != 1 {
		t.Errorf("xray_exporter_config_reload_errors_total = %v, want 1", got)
	}
}

// TestReloadCountersArePerSnapshot pins that a snapshot starts at zero even
// after another snapshot counted reloads; the counters are state, not schema.
func TestReloadCountersArePerSnapshot(t *testing.T) {
	first := New()
	first.IncReload()

	second := New()
	if got := mustSample(t, gatherSamples(t, second), "xray_exporter_config_reload_total", map[string]string{}); got != 0 {
		t.Errorf("fresh snapshot reload counter = %v, want 0", got)
	}
}
