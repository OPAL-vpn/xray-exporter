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
	snap.SetCycle("nl1", secondTime, time.Second, 1)
	snap.SetHTTP("nl1", "a.test", probe.HTTPResult{Up: true, Status: 200, TLSVerified: true, TTFB: time.Millisecond, Total: 2 * time.Millisecond}, secondTime)
	snap.SetDNS("nl1", "a.test", probe.DNSResult{Up: true, RTT: time.Millisecond}, secondTime)
	snap.SetResolver("nl1", "cf", "doh", probe.ResolverResult{Up: true, RTT: time.Millisecond}, secondTime)
	snap.SetResolver("nl1", "cf", "dot", probe.ResolverResult{Up: true, RTT: time.Millisecond}, secondTime)
	snap.IncReload()
	snap.IncReloadError()
	snap.IncFetch()
	snap.IncFetchError()
	snap.SetFetchSuccess(secondTime)
	snap.SetSource("remnawave")
	snap.SetReachability(&ReachabilitySpec{
		Controls: []config.Control{{Kind: config.ControlTCP, Name: "cf", Class: config.ClassForeign, Target: "1.1.1.1:443"}},
		EntryTCP: true,
	})
	snap.SetIdentity(egress.Identity{ASName: "PJSC MegaFon", Country: "RU"}, secondTime)
	snap.SetControl(config.ControlTCP, "cf", true, time.Millisecond)
	snap.SetEntry("nl1", true, time.Millisecond)
	snap.SeedUDP("nl1", []config.UDPTarget{{Name: "tg", Kind: "stun", Addr: "203.0.113.10:3478"}}, []string{"idle", "loaded"}, seedTime)
	snap.SeedTCP("nl1", []config.TCPTarget{{Name: "dc4", Addr: "149.154.167.51:443"}}, seedTime)
	snap.SetUDP("nl1", "tg", "idle", probe.UDPResult{Up: true, Sent: 2, Replies: 2, Median: time.Millisecond, Max: 2 * time.Millisecond, Jitter: time.Millisecond}, secondTime)
	snap.SetUDPLoad("nl1", true, secondTime)
	snap.SetTCP("nl1", "dc4", probe.TunnelTCPResult{Up: true, Connect: time.Millisecond}, secondTime)

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

	if got, want := len(AllNames()), 38; got != want {
		t.Errorf("AllNames has %d names, want the %d in the spec", got, want)
	}
	prometheusName := regexp.MustCompile(`^xray_[a-z0-9_]+$`)
	for _, name := range AllNames() {
		if !prometheusName.MatchString(name) {
			t.Errorf("AllNames entry %q is not an xray_ Prometheus metric name", name)
		}
	}
}
