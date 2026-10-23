package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

func TestDNSSeries(t *testing.T) {
	snap := New()
	snap.Seed("nl1", []string{"a.test"}, nil, seedTime)

	snap.SetDNS("nl1", "a.test", probe.DNSResult{Up: true, RTT: 45 * time.Millisecond}, secondTime)
	snap.SetDNS("nl1", "a.test", probe.DNSResult{Err: errors.New("timeout")}, thirdTime)
	samples := gatherSamples(t, snap)

	targetLabels := labels("config", "nl1", "target", "a.test")
	if got := mustSample(t, samples, "xray_dns_up", targetLabels); got != 0 {
		t.Errorf("xray_dns_up = %v, want 0 after a failure", got)
	}
	if got := mustSample(t, samples, "xray_dns_lookup_seconds", targetLabels); got != 0.045 {
		t.Errorf("xray_dns_lookup_seconds = %v, want the last successful 0.045", got)
	}
	if got := mustSample(t, samples, "xray_target_timestamp_seconds", targetLabels); got != timestampSeconds(thirdTime) {
		t.Errorf("xray_target_timestamp_seconds = %v, want %v", got, timestampSeconds(thirdTime))
	}
}

func TestSkipDNSRemovesSeries(t *testing.T) {
	snap := New()
	snap.Seed("nl1", []string{"1.1.1.1", "a.test"}, nil, seedTime)
	snap.SetDNS("nl1", "1.1.1.1", probe.DNSResult{Up: true, RTT: 10 * time.Millisecond}, secondTime)
	snap.SetDNS("nl1", "a.test", probe.DNSResult{Up: true, RTT: 20 * time.Millisecond}, secondTime)
	snap.SetHTTP("nl1", "1.1.1.1", probe.HTTPResult{Up: true, Status: 200, TTFB: 10 * time.Millisecond, Total: 20 * time.Millisecond}, secondTime)

	snap.SkipDNS("nl1", "1.1.1.1")
	samples := gatherSamples(t, snap)

	ipLabels := labels("config", "nl1", "target", "1.1.1.1")
	mustNoSample(t, samples, "xray_dns_up", ipLabels)
	mustNoSample(t, samples, "xray_dns_lookup_seconds", ipLabels)
	if got := mustSample(t, samples, "xray_probe_up", ipLabels); got != 1 {
		t.Errorf("xray_probe_up for the IP literal = %v, want 1", got)
	}
	if got := mustSample(t, samples, "xray_probe_ttfb_seconds", ipLabels); got != 0.01 {
		t.Errorf("xray_probe_ttfb_seconds for the IP literal = %v, want 0.01", got)
	}

	hostLabels := labels("config", "nl1", "target", "a.test")
	if got := mustSample(t, samples, "xray_dns_up", hostLabels); got != 1 {
		t.Errorf("xray_dns_up for the hostname = %v, want 1", got)
	}
	if got := mustSample(t, samples, "xray_dns_lookup_seconds", hostLabels); got != 0.02 {
		t.Errorf("xray_dns_lookup_seconds for the hostname = %v, want 0.02", got)
	}

	rows := snap.Export()
	target := findTarget(t, rows, "nl1", "1.1.1.1")
	if !target.HasHTTP {
		t.Error("Export target 1.1.1.1 HasHTTP = false, want true")
	}
	if target.HasDNS {
		t.Error("Export target 1.1.1.1 HasDNS = true after SkipDNS, want false")
	}
}
