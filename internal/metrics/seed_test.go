package metrics

import (
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

func TestSeedPublishesZerosAndNoLatency(t *testing.T) {
	snap := New()
	snap.Seed("nl1", []string{"a.test", "b.test"}, []config.Resolver{
		{Name: "cf", DoHURL: "https://cf.test/dns-query", DoTHost: "cf.test"},
	}, seedTime)
	samples := gatherSamples(t, snap)

	for _, target := range []string{"a.test", "b.test"} {
		lbls := labels("config", "nl1", "target", target)
		if got := mustSample(t, samples, "xray_probe_up", lbls); got != 0 {
			t.Errorf("xray_probe_up%v = %v, want 0", lbls, got)
		}
		if got := mustSample(t, samples, "xray_dns_up", lbls); got != 0 {
			t.Errorf("xray_dns_up%v = %v, want 0", lbls, got)
		}
		if got := mustSample(t, samples, "xray_probe_http_status", lbls); got != 0 {
			t.Errorf("xray_probe_http_status%v = %v, want 0", lbls, got)
		}
		if got := mustSample(t, samples, "xray_probe_tls_ok", lbls); got != 0 {
			t.Errorf("xray_probe_tls_ok%v = %v, want 0", lbls, got)
		}
		if got := mustSample(t, samples, "xray_target_timestamp_seconds", lbls); got != timestampSeconds(seedTime) {
			t.Errorf("xray_target_timestamp_seconds%v = %v, want %v", lbls, got, timestampSeconds(seedTime))
		}
	}

	if got := mustSample(t, samples, "xray_config_running", labels("config", "nl1")); got != 0 {
		t.Errorf("xray_config_running = %v, want 0", got)
	}
	if got := mustSample(t, samples, "xray_egress_verified", labels("config", "nl1")); got != 0 {
		t.Errorf("xray_egress_verified = %v, want 0", got)
	}

	for _, name := range []string{
		"xray_probe_ttfb_seconds",
		"xray_probe_total_seconds",
		"xray_dns_lookup_seconds",
		"xray_resolver_lookup_seconds",
	} {
		if got := samplesNamed(samples, name); len(got) != 0 {
			t.Errorf("%s has %d series after Seed, want none: %v", name, len(got), got)
		}
	}

	// No verification or cycle has happened yet, and no xray version is known.
	for _, name := range []string{
		"xray_egress_ip_info",
		"xray_egress_timestamp_seconds",
		"xray_config_info",
		"xray_cycle_timestamp_seconds",
		"xray_cycle_duration_seconds",
		"xray_cycle_targets_total",
	} {
		if got := samplesNamed(samples, name); len(got) != 0 {
			t.Errorf("%s has %d series after Seed, want none: %v", name, len(got), got)
		}
	}

	// Every seeded resolver transport exists with up=0 and the seed time.
	for _, transport := range []string{"doh", "dot"} {
		resolverLabels := labels("config", "nl1", "resolver", "cf", "transport", transport)
		if got := mustSample(t, samples, "xray_resolver_up", resolverLabels); got != 0 {
			t.Errorf("xray_resolver_up %s = %v, want 0", transport, got)
		}
		if got := mustSample(t, samples, "xray_resolver_timestamp_seconds", resolverLabels); got != timestampSeconds(seedTime) {
			t.Errorf("xray_resolver_timestamp_seconds %s = %v, want %v", transport, got, timestampSeconds(seedTime))
		}
	}
}

func TestDropRemovesSeries(t *testing.T) {
	snap := New()
	snap.Seed("nl1", []string{"a.test"}, []config.Resolver{resolverDoH("cf", "https://cf.test/dns-query")}, seedTime)
	snap.Seed("de1", []string{"a.test"}, nil, seedTime)
	snap.SetHTTP("nl1", "a.test", probe.HTTPResult{Up: true, Status: 200, TTFB: time.Millisecond, Total: 2 * time.Millisecond}, secondTime)

	snap.Drop("nl1")
	samples := gatherSamples(t, snap)

	for _, s := range samples {
		if s.labels["config"] == "nl1" {
			t.Errorf("%s%v still present after Drop", s.name, s.labels)
		}
	}
	if got := mustSample(t, samples, "xray_probe_up", labels("config", "de1", "target", "a.test")); got != 0 {
		t.Errorf("xray_probe_up{config=de1} = %v, want 0", got)
	}
}

func TestSeedReconcilesWithoutOrphans(t *testing.T) {
	snap := New()
	snap.Seed("nl1", []string{"a.test", "b.test"}, []config.Resolver{resolverDoH("cf", "https://cf.test/dns-query")}, seedTime)
	snap.SetHTTP("nl1", "b.test", probe.HTTPResult{Up: true, Status: 200, TTFB: 90 * time.Millisecond, Total: 100 * time.Millisecond}, secondTime)

	snap.Seed("nl1", []string{"b.test", "c.test"}, []config.Resolver{resolverDoH("quad9", "https://quad9.test/dns-query")}, thirdTime)
	samples := gatherSamples(t, snap)

	if got := samplesNamed(samples, "xray_probe_up"); len(got) != 2 {
		t.Errorf("xray_probe_up has %d series after reseed, want 2: %v", len(got), got)
	}
	for _, s := range samples {
		if s.labels["target"] == "a.test" {
			t.Errorf("%s%v survived removal from Seed", s.name, s.labels)
		}
		if s.labels["resolver"] == "cf" {
			t.Errorf("%s%v survived removal from Seed", s.name, s.labels)
		}
	}

	// Existing state keeps its measurement instead of being reset.
	if got := mustSample(t, samples, "xray_probe_up", labels("config", "nl1", "target", "b.test")); got != 1 {
		t.Errorf("xray_probe_up b.test = %v, want the preserved 1", got)
	}
	if got := mustSample(t, samples, "xray_probe_ttfb_seconds", labels("config", "nl1", "target", "b.test")); got != 0.09 {
		t.Errorf("xray_probe_ttfb_seconds b.test = %v, want the preserved 0.09", got)
	}
	if got := mustSample(t, samples, "xray_probe_up", labels("config", "nl1", "target", "c.test")); got != 0 {
		t.Errorf("xray_probe_up c.test = %v, want the seeded 0", got)
	}
}

func TestLateResultsDoNotResurrectRemovedEntities(t *testing.T) {
	snap := New()
	snap.Seed("nl1", []string{"a.test", "b.test"}, nil, seedTime)
	snap.Drop("nl1")

	// A result from a cycle that was interrupted by the reload must be dropped.
	snap.SetHTTP("nl1", "a.test", probe.HTTPResult{Up: true, Status: 200, TTFB: time.Millisecond, Total: 2 * time.Millisecond}, secondTime)
	snap.SetDNS("nl1", "a.test", probe.DNSResult{Up: true, RTT: time.Millisecond}, secondTime)
	snap.SetConfig("nl1", true, "v26.10.21")

	snap.Seed("de1", []string{"a.test", "b.test"}, nil, seedTime)
	snap.Seed("de1", []string{"b.test"}, nil, thirdTime)
	snap.SetHTTP("de1", "a.test", probe.HTTPResult{Up: true, Status: 200, TTFB: time.Millisecond, Total: 2 * time.Millisecond}, secondTime)

	samples := gatherSamples(t, snap)
	for _, s := range samples {
		if s.labels["config"] == "nl1" {
			t.Errorf("%s%v resurrected a dropped config", s.name, s.labels)
		}
		if s.labels["target"] == "a.test" {
			t.Errorf("%s%v resurrected a target removed from Seed", s.name, s.labels)
		}
	}
}
