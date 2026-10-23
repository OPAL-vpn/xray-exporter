package metrics

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/Anton-beep/xray-exporter/internal/config"
	"github.com/Anton-beep/xray-exporter/internal/egress"
	"github.com/Anton-beep/xray-exporter/internal/probe"
)

// sample is one gathered series: its family name, labels and value.
type sample struct {
	name   string
	labels map[string]string
	value  float64
}

// labels builds a label set from alternating name/value pairs.
func labels(pairs ...string) map[string]string {
	if len(pairs)%2 != 0 {
		panic("labels: odd number of arguments")
	}
	out := make(map[string]string, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		out[pairs[i]] = pairs[i+1]
	}
	return out
}

// gatherSamples renders the snapshot through a fresh registry, the way
// Prometheus sees it, and flattens the result into samples.
func gatherSamples(t *testing.T, snap *Snapshot) []sample {
	t.Helper()

	reg := prometheus.NewRegistry()
	reg.MustRegister(snap.Collector())
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	var out []sample
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			lbls := make(map[string]string, len(metric.GetLabel()))
			for _, pair := range metric.GetLabel() {
				lbls[pair.GetName()] = pair.GetValue()
			}
			out = append(out, sample{name: family.GetName(), labels: lbls, value: sampleValue(metric)})
		}
	}
	return out
}

func sampleValue(metric *dto.Metric) float64 {
	switch {
	case metric.GetGauge() != nil:
		return metric.GetGauge().GetValue()
	case metric.GetCounter() != nil:
		return metric.GetCounter().GetValue()
	case metric.GetUntyped() != nil:
		return metric.GetUntyped().GetValue()
	default:
		return math.NaN()
	}
}

// findSample returns the value of the series with exactly this name and label
// set. It fails when more than one series matches.
func findSample(t *testing.T, samples []sample, name string, want map[string]string) (float64, bool) {
	t.Helper()

	var found []sample
	for _, s := range samples {
		if s.name == name && sameLabels(s.labels, want) {
			found = append(found, s)
		}
	}
	if len(found) > 1 {
		t.Fatalf("%s%v: %d series, want at most one", name, want, len(found))
	}
	if len(found) == 0 {
		return 0, false
	}
	return found[0].value, true
}

// mustSample fails the test when the series is absent.
func mustSample(t *testing.T, samples []sample, name string, want map[string]string) float64 {
	t.Helper()

	got, ok := findSample(t, samples, name, want)
	if !ok {
		t.Fatalf("%s%v: series missing", name, want)
	}
	return got
}

// mustNoSample fails the test when the series is present.
func mustNoSample(t *testing.T, samples []sample, name string, want map[string]string) {
	t.Helper()

	if got, ok := findSample(t, samples, name, want); ok {
		t.Fatalf("%s%v: series present with %v, want absent", name, want, got)
	}
}

func sameLabels(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for name, value := range want {
		if got[name] != value {
			return false
		}
	}
	return true
}

func samplesNamed(samples []sample, name string) []sample {
	var out []sample
	for _, s := range samples {
		if s.name == name {
			out = append(out, s)
		}
	}
	return out
}

func timestampSeconds(at time.Time) float64 {
	return float64(at.UnixNano()) / 1e9
}

func durationSeconds(d time.Duration) float64 {
	return float64(d) / float64(time.Second)
}

var (
	seedTime   = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	secondTime = seedTime.Add(time.Minute)
	thirdTime  = seedTime.Add(2 * time.Minute)
)

func resolverDoH(name, url string) config.Resolver {
	return config.Resolver{Name: name, DoHURL: url}
}

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

func TestExport(t *testing.T) {
	snap := New()
	snap.Seed("nl1", []string{"a.test", "b.test"}, []config.Resolver{
		{Name: "cf", DoHURL: "https://cf.test/dns-query", DoTHost: "cf.test"},
	}, seedTime)
	snap.SetConfig("nl1", true, "v26.10.21")
	snap.SetEgress("nl1", egress.Result{IP: "203.0.113.7", Verified: true}, secondTime)
	snap.SetCycle("nl1", secondTime, 900*time.Millisecond, 2)

	httpResult := probe.HTTPResult{Up: true, Status: 200, TLSVerified: true, TTFB: 120 * time.Millisecond, Total: 300 * time.Millisecond}
	snap.SetHTTP("nl1", "a.test", httpResult, secondTime)
	snap.SetDNS("nl1", "a.test", probe.DNSResult{Up: true, RTT: 45 * time.Millisecond}, secondTime)
	dnsErr := errors.New("dns read: timeout")
	snap.SetDNS("nl1", "b.test", probe.DNSResult{Err: dnsErr}, secondTime)
	snap.SetResolver("nl1", "cf", "doh", probe.ResolverResult{Up: true, RTT: 50 * time.Millisecond}, secondTime)
	snap.SetResolver("nl1", "cf", "dot", probe.ResolverResult{Err: errors.New("handshake")}, secondTime)

	exp := snap.Export()
	if len(exp.Configs) != 1 {
		t.Fatalf("Export has %d configs, want 1", len(exp.Configs))
	}
	cfg := exp.Configs[0]
	if cfg.Name != "nl1" || !cfg.Running || cfg.XrayVersion != "v26.10.21" {
		t.Errorf("config row = %+v, want nl1 running with version v26.10.21", cfg)
	}
	if cfg.Egress.IP != "203.0.113.7" || !cfg.Egress.Verified {
		t.Errorf("egress row = %+v, want 203.0.113.7 verified", cfg.Egress)
	}
	if !cfg.Cycle.Done || cfg.Cycle.Duration != 900*time.Millisecond || cfg.Cycle.Targets != 2 {
		t.Errorf("cycle row = %+v, want a completed 900ms cycle over 2 targets", cfg.Cycle)
	}

	a := findTarget(t, exp, "nl1", "a.test")
	if !a.HasHTTP || a.HTTP.Status != 200 || a.HTTP.TTFB != 120*time.Millisecond {
		t.Errorf("target a.test HTTP row = %+v, want the raw successful result", a.HTTP)
	}
	if !a.HasDNS || !a.DNS.Up || a.DNS.RTT != 45*time.Millisecond {
		t.Errorf("target a.test DNS row = %+v, want the raw successful result", a.DNS)
	}

	b := findTarget(t, exp, "nl1", "b.test")
	if b.HasHTTP {
		t.Error("target b.test HasHTTP = true, want false before any HTTPS measurement")
	}
	if !b.HasDNS || b.DNS.Up {
		t.Errorf("target b.test DNS row = %+v, want a failed measurement", b.DNS)
	}
	if !errors.Is(b.DNS.Err, dnsErr) {
		t.Errorf("target b.test DNS err = %v, want %v", b.DNS.Err, dnsErr)
	}

	resolver := findResolver(t, exp, "nl1", "cf")
	transports := make(map[string]probe.ResolverResult, len(resolver.Transports))
	for _, tr := range resolver.Transports {
		transports[tr.Transport] = tr.Result
	}
	if got, ok := transports["doh"]; !ok || !got.Up || got.RTT != 50*time.Millisecond {
		t.Errorf("resolver cf doh = %+v, want the raw successful result", got)
	}
	if got, ok := transports["dot"]; !ok || got.Up {
		t.Errorf("resolver cf dot = %+v, want a failed result", got)
	}
}

func findTarget(t *testing.T, exp Export, configName, target string) TargetExport {
	t.Helper()

	for _, cfg := range exp.Configs {
		if cfg.Name != configName {
			continue
		}
		for _, row := range cfg.Targets {
			if row.Target == target {
				return row
			}
		}
		t.Fatalf("config %q has no target row %q", configName, target)
	}
	t.Fatalf("Export has no config %q", configName)
	return TargetExport{}
}

func findResolver(t *testing.T, exp Export, configName, resolver string) ResolverExport {
	t.Helper()

	for _, cfg := range exp.Configs {
		if cfg.Name != configName {
			continue
		}
		for _, row := range cfg.Resolvers {
			if row.Name == resolver {
				return row
			}
		}
		t.Fatalf("config %q has no resolver row %q", configName, resolver)
	}
	t.Fatalf("Export has no config %q", configName)
	return ResolverExport{}
}

func TestAllNamesCoverEverything(t *testing.T) {
	snap := New()
	snap.Seed("nl1", []string{"a.test"}, []config.Resolver{
		{Name: "cf", DoHURL: "https://cf.test/dns-query", DoTHost: "cf.test"},
	}, seedTime)
	snap.SetConfig("nl1", true, "v26.10.21")
	snap.SetBuildInfo("v1", "go1.27.1")
	snap.SetEgress("nl1", egress.Result{IP: "203.0.113.7", Verified: true}, secondTime)
	snap.SetCycle("nl1", secondTime, time.Second, 1)
	snap.SetHTTP("nl1", "a.test", probe.HTTPResult{Up: true, Status: 200, TLSVerified: true, TTFB: time.Millisecond, Total: 2 * time.Millisecond}, secondTime)
	snap.SetDNS("nl1", "a.test", probe.DNSResult{Up: true, RTT: time.Millisecond}, secondTime)
	snap.SetResolver("nl1", "cf", "doh", probe.ResolverResult{Up: true, RTT: time.Millisecond}, secondTime)
	snap.SetResolver("nl1", "cf", "dot", probe.ResolverResult{Up: true, RTT: time.Millisecond}, secondTime)
	snap.IncReload()
	snap.IncReloadError()

	samples := gatherSamples(t, snap)
	emitted := make(map[string]struct{})
	for _, s := range samples {
		if strings.HasPrefix(s.name, "xray_") {
			emitted[s.name] = struct{}{}
		}
	}

	declared := make(map[string]struct{}, len(AllNames()))
	for _, name := range AllNames() {
		declared[name] = struct{}{}
	}

	for name := range emitted {
		if _, ok := declared[name]; !ok {
			t.Errorf("emitted metric %q is missing from AllNames", name)
		}
	}
	for name := range declared {
		if _, ok := emitted[name]; !ok {
			t.Errorf("AllNames lists %q but a fully populated snapshot did not emit it", name)
		}
	}

	if got, want := len(AllNames()), 22; got != want {
		t.Errorf("AllNames has %d names, want the %d in the spec", got, want)
	}
	prometheusName := regexp.MustCompile(`^xray_[a-z0-9_]+$`)
	for _, name := range AllNames() {
		if !prometheusName.MatchString(name) {
			t.Errorf("AllNames entry %q is not an xray_ Prometheus metric name", name)
		}
	}
}

func TestConcurrentSetAndCollect(t *testing.T) {
	snap := New()
	snap.Seed("nl1", []string{"a.test"}, nil, seedTime)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				snap.SetHTTP("nl1", "a.test", probe.HTTPResult{Up: j%2 == 0, Status: 200, TTFB: time.Millisecond, Total: 2 * time.Millisecond}, time.Now())
				snap.SetDNS("nl1", "a.test", probe.DNSResult{Up: true, RTT: time.Millisecond}, time.Now())
			}
		}()
	}
	// Scrapes happen on the test goroutine so a Gather failure still fails the
	// test, and run while the setters write.
	for j := 0; j < 100; j++ {
		_ = gatherSamples(t, snap)
		_ = snap.Export()
	}
	wg.Wait()
}

// String keeps failures readable when a sample set is printed.
func (s sample) String() string {
	return fmt.Sprintf("%s%v=%v", s.name, s.labels, s.value)
}
