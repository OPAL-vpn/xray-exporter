package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

func TestResolverTransports(t *testing.T) {
	snap := New()
	snap.Seed("nl1", []string{"a.test"}, []config.Resolver{
		{Name: "both", DoHURL: "https://both.test/dns-query", DoTHost: "both.test"},
		resolverDoH("doh-only", "https://doh.test/dns-query"),
	}, seedTime)

	snap.SetResolver("nl1", "both", "doh", probe.ResolverResult{Up: true, RTT: 50 * time.Millisecond}, secondTime)
	snap.SetResolver("nl1", "both", "dot", probe.ResolverResult{Up: true, RTT: 70 * time.Millisecond}, secondTime)
	snap.SetResolver("nl1", "doh-only", "doh", probe.ResolverResult{Up: true, RTT: 30 * time.Millisecond}, secondTime)
	samples := gatherSamples(t, snap)

	for _, transport := range []string{"doh", "dot"} {
		lbls := labels("config", "nl1", "resolver", "both", "transport", transport)
		if got := mustSample(t, samples, "xray_resolver_up", lbls); got != 1 {
			t.Errorf("xray_resolver_up both/%s = %v, want 1", transport, got)
		}
	}
	if got := mustSample(t, samples, "xray_resolver_lookup_seconds", labels("config", "nl1", "resolver", "both", "transport", "doh")); got != 0.05 {
		t.Errorf("xray_resolver_lookup_seconds both/doh = %v, want 0.05", got)
	}
	if got := mustSample(t, samples, "xray_resolver_lookup_seconds", labels("config", "nl1", "resolver", "both", "transport", "dot")); got != 0.07 {
		t.Errorf("xray_resolver_lookup_seconds both/dot = %v, want 0.07", got)
	}

	var transports []string
	for _, s := range samplesNamed(samples, "xray_resolver_up") {
		if s.labels["resolver"] == "doh-only" {
			transports = append(transports, s.labels["transport"])
		}
	}
	if len(transports) != 1 || transports[0] != "doh" {
		t.Errorf("doh-only transports = %v, want exactly [doh]", transports)
	}

	// A failed lookup leaves the last successful round trip in place.
	snap.SetResolver("nl1", "doh-only", "doh", probe.ResolverResult{Err: errors.New("refused")}, thirdTime)
	samples = gatherSamples(t, snap)
	lbls := labels("config", "nl1", "resolver", "doh-only", "transport", "doh")
	if got := mustSample(t, samples, "xray_resolver_up", lbls); got != 0 {
		t.Errorf("xray_resolver_up after failure = %v, want 0", got)
	}
	if got := mustSample(t, samples, "xray_resolver_lookup_seconds", lbls); got != 0.03 {
		t.Errorf("xray_resolver_lookup_seconds after failure = %v, want the last successful 0.03", got)
	}
	if got := mustSample(t, samples, "xray_resolver_timestamp_seconds", lbls); got != timestampSeconds(thirdTime) {
		t.Errorf("xray_resolver_timestamp_seconds = %v, want %v", got, timestampSeconds(thirdTime))
	}
}
