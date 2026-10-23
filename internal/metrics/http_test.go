package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

func TestHTTPResultSeries(t *testing.T) {
	snap := New()
	snap.Seed("nl1", []string{"a.test"}, nil, seedTime)

	result := probe.HTTPResult{Up: true, Status: 200, TLSVerified: true, TTFB: 120 * time.Millisecond, Total: 300 * time.Millisecond}
	snap.SetHTTP("nl1", "a.test", result, secondTime)
	samples := gatherSamples(t, snap)

	targetLabels := labels("config", "nl1", "target", "a.test")
	checks := map[string]float64{
		"xray_probe_up":                 1,
		"xray_probe_http_status":        200,
		"xray_probe_tls_ok":             1,
		"xray_probe_ttfb_seconds":       0.12,
		"xray_probe_total_seconds":      0.3,
		"xray_target_timestamp_seconds": timestampSeconds(secondTime),
	}
	for name, want := range checks {
		if got := mustSample(t, samples, name, targetLabels); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}

func TestFailureKeepsLastLatency(t *testing.T) {
	snap := New()
	snap.Seed("nl1", []string{"a.test"}, nil, seedTime)

	success := probe.HTTPResult{Up: true, Status: 200, TLSVerified: true, TTFB: 120 * time.Millisecond, Total: 300 * time.Millisecond}
	snap.SetHTTP("nl1", "a.test", success, secondTime)
	snap.SetHTTP("nl1", "a.test", probe.HTTPResult{Err: errors.New("dial refused")}, thirdTime)
	samples := gatherSamples(t, snap)

	targetLabels := labels("config", "nl1", "target", "a.test")
	if got := mustSample(t, samples, "xray_probe_up", targetLabels); got != 0 {
		t.Errorf("xray_probe_up = %v, want 0", got)
	}
	if got := mustSample(t, samples, "xray_probe_http_status", targetLabels); got != 0 {
		t.Errorf("xray_probe_http_status = %v, want 0", got)
	}
	if got := mustSample(t, samples, "xray_probe_tls_ok", targetLabels); got != 0 {
		t.Errorf("xray_probe_tls_ok = %v, want 0", got)
	}
	if got := mustSample(t, samples, "xray_probe_ttfb_seconds", targetLabels); got != 0.12 {
		t.Errorf("xray_probe_ttfb_seconds = %v, want the last successful 0.12", got)
	}
	if got := mustSample(t, samples, "xray_probe_total_seconds", targetLabels); got != 0.3 {
		t.Errorf("xray_probe_total_seconds = %v, want the last successful 0.3", got)
	}
	if got := mustSample(t, samples, "xray_target_timestamp_seconds", targetLabels); got != timestampSeconds(thirdTime) {
		t.Errorf("xray_target_timestamp_seconds = %v, want the failed attempt time %v", got, timestampSeconds(thirdTime))
	}
}

func TestZeroLatencyNotPublished(t *testing.T) {
	snap := New()
	snap.Seed("nl1", []string{"a.test"}, []config.Resolver{resolverDoH("cf", "https://cf.test/dns-query")}, seedTime)

	// A successful probe with a zero timer must not become a latency sample.
	snap.SetHTTP("nl1", "a.test", probe.HTTPResult{Up: true, Status: 200}, secondTime)
	snap.SetDNS("nl1", "a.test", probe.DNSResult{Up: true}, secondTime)
	snap.SetResolver("nl1", "cf", "doh", probe.ResolverResult{Up: true}, secondTime)
	samples := gatherSamples(t, snap)

	targetLabels := labels("config", "nl1", "target", "a.test")
	resolverLabels := labels("config", "nl1", "resolver", "cf", "transport", "doh")
	for _, name := range []string{"xray_probe_ttfb_seconds", "xray_probe_total_seconds", "xray_dns_lookup_seconds"} {
		mustNoSample(t, samples, name, targetLabels)
	}
	mustNoSample(t, samples, "xray_resolver_lookup_seconds", resolverLabels)

	// A later zero-timer success also leaves the last real measurement alone.
	snap.SetHTTP("nl1", "a.test", probe.HTTPResult{Up: true, Status: 200, TTFB: 120 * time.Millisecond, Total: 300 * time.Millisecond}, thirdTime)
	snap.SetHTTP("nl1", "a.test", probe.HTTPResult{Up: true, Status: 200}, thirdTime)
	samples = gatherSamples(t, snap)
	if got := mustSample(t, samples, "xray_probe_ttfb_seconds", targetLabels); got != 0.12 {
		t.Errorf("xray_probe_ttfb_seconds = %v, want the last non-zero 0.12", got)
	}
	if got := mustSample(t, samples, "xray_probe_total_seconds", targetLabels); got != 0.3 {
		t.Errorf("xray_probe_total_seconds = %v, want the last non-zero 0.3", got)
	}
}
