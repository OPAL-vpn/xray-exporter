package metrics

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/egress"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

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
