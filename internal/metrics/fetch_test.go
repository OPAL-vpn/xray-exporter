package metrics

import "testing"

func TestFetchCountersAndTimestamp(t *testing.T) {
	snap := New()

	snap.IncFetch()
	snap.IncFetch()
	snap.IncFetchError()
	samples := gatherSamples(t, snap)

	if got := mustSample(t, samples, "xray_exporter_config_fetch_total", map[string]string{}); got != 2 {
		t.Errorf("xray_exporter_config_fetch_total = %v, want 2", got)
	}
	if got := mustSample(t, samples, "xray_exporter_config_fetch_errors_total", map[string]string{}); got != 1 {
		t.Errorf("xray_exporter_config_fetch_errors_total = %v, want 1", got)
	}
	// No fetch has succeeded yet, so there is no success time to report.
	if got := samplesNamed(samples, "xray_exporter_config_fetch_timestamp_seconds"); len(got) != 0 {
		t.Errorf("xray_exporter_config_fetch_timestamp_seconds = %v before the first success, want absent", got)
	}
}

func TestFetchTimestampAfterSuccess(t *testing.T) {
	snap := New()
	snap.SetFetchSuccess(secondTime)

	samples := gatherSamples(t, snap)
	if got := mustSample(t, samples, "xray_exporter_config_fetch_timestamp_seconds", map[string]string{}); got != timestampSeconds(secondTime) {
		t.Errorf("xray_exporter_config_fetch_timestamp_seconds = %v, want %v", got, timestampSeconds(secondTime))
	}
}

func TestSourceInfo(t *testing.T) {
	snap := New()

	samples := gatherSamples(t, snap)
	if got := samplesNamed(samples, "xray_exporter_config_source_info"); len(got) != 0 {
		t.Errorf("xray_exporter_config_source_info = %v before SetSource, want absent", got)
	}

	snap.SetSource("remnawave")
	samples = gatherSamples(t, snap)
	if got := mustSample(t, samples, "xray_exporter_config_source_info", labels("type", "remnawave")); got != 1 {
		t.Errorf("xray_exporter_config_source_info{type=\"remnawave\"} = %v, want 1", got)
	}

	snap.SetSource("directory")
	samples = gatherSamples(t, snap)
	if got := mustSample(t, samples, "xray_exporter_config_source_info", labels("type", "directory")); got != 1 {
		t.Errorf("xray_exporter_config_source_info{type=\"directory\"} = %v, want 1", got)
	}
	if series := samplesNamed(samples, "xray_exporter_config_source_info"); len(series) != 1 {
		t.Errorf("xray_exporter_config_source_info has %d series after a source change, want 1: %v", len(series), series)
	}
}
