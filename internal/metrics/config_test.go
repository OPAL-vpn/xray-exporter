package metrics

import "testing"

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
