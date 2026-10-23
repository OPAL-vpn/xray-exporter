package sampler

import (
	"context"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/netutil"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

func TestCycleSeedsAndMeasures(t *testing.T) {
	snap := metrics.New()
	snap.Seed("nl1", []string{"a.test", "b.test"}, []config.Resolver{
		{Name: "cf", DoHURL: "https://cf.test/dns-query", DoTHost: "cf.test"},
	}, seedTime)

	fake := &fakeProbers{
		httpResult:     probe.HTTPResult{Up: true, Status: 200, TLSVerified: true, TTFB: 10 * time.Millisecond, Total: 20 * time.Millisecond},
		dnsResult:      probe.DNSResult{Up: true, RTT: 5 * time.Millisecond},
		resolverResult: probe.ResolverResult{Up: true, RTT: 7 * time.Millisecond},
	}
	s := New(snap, fake.probers(), testOptions("example.com"))

	s.Cycle(context.Background(),
		[]Job{{Name: "nl1", SocksPort: 1080, DNSPort: 5353}},
		[]config.Target{{Host: "a.test"}, {Host: "b.test"}},
		[]config.Resolver{{Name: "cf", DoHURL: "https://cf.test/dns-query", DoTHost: "cf.test"}},
	)

	fake.mu.Lock()
	httpHosts := append([]string(nil), fake.httpHosts...)
	dnsCalls := append([]dnsCall(nil), fake.dnsCalls...)
	dohCalls := append([]resolverCall(nil), fake.dohCalls...)
	dotCalls := append([]resolverCall(nil), fake.dotCalls...)
	fake.mu.Unlock()

	if len(httpHosts) != 2 {
		t.Errorf("HTTP called %d times (%v), want 2", len(httpHosts), httpHosts)
	}
	if len(dnsCalls) != 2 {
		t.Errorf("DNS called %d times (%v), want 2", len(dnsCalls), dnsCalls)
	}
	for _, call := range dnsCalls {
		if call.server != "127.0.0.1:5353" {
			t.Errorf("DNS server = %q, want the job's dokodemo-door port", call.server)
		}
	}
	if len(dohCalls) != 1 || dohCalls[0].addr != "https://cf.test/dns-query" || dohCalls[0].domain != "example.com" {
		t.Errorf("DoH calls = %v, want one for the resolver's URL and the probe domain", dohCalls)
	}
	if len(dotCalls) != 1 || dotCalls[0].addr != "cf.test" || dotCalls[0].domain != "example.com" {
		t.Errorf("DoT calls = %v, want one for the resolver's host and the probe domain", dotCalls)
	}

	values := snapshot(t, snap)
	for _, target := range []string{"a.test", "b.test"} {
		if got := mustValue(t, values, "xray_probe_up", "config", "nl1", "target", target); got != 1 {
			t.Errorf("xray_probe_up{target=%s} = %v, want 1", target, got)
		}
		if got := mustValue(t, values, "xray_dns_up", "config", "nl1", "target", target); got != 1 {
			t.Errorf("xray_dns_up{target=%s} = %v, want 1", target, got)
		}
		if got := mustValue(t, values, "xray_probe_ttfb_seconds", "config", "nl1", "target", target); got != 0.01 {
			t.Errorf("xray_probe_ttfb_seconds{target=%s} = %v, want 0.01", target, got)
		}
	}
	for _, transport := range []string{metrics.TransportDoH, metrics.TransportDoT} {
		if got := mustValue(t, values, "xray_resolver_up", "config", "nl1", "resolver", "cf", "transport", transport); got != 1 {
			t.Errorf("xray_resolver_up{%s} = %v, want 1", transport, got)
		}
	}

	if got := mustValue(t, values, "xray_cycle_targets_total", "config", "nl1"); got != 2 {
		t.Errorf("xray_cycle_targets_total = %v, want 2", got)
	}
	if got := mustValue(t, values, "xray_cycle_timestamp_seconds", "config", "nl1"); got <= float64(seedTime.Unix()) {
		t.Errorf("xray_cycle_timestamp_seconds = %v, want after %v", got, seedTime.Unix())
	}
}

func TestCycleBuildsSocksDialerFromJob(t *testing.T) {
	const socksPort = 1080

	snap := metrics.New()
	snap.Seed("nl1", []string{"a.test"}, []config.Resolver{
		{Name: "cf", DoHURL: "https://cf.test/dns-query"},
	}, seedTime)

	fake := &fakeProbers{
		httpResult:     probe.HTTPResult{Up: true, Status: 200},
		dnsResult:      probe.DNSResult{Up: true, RTT: time.Millisecond},
		resolverResult: probe.ResolverResult{Up: true, RTT: time.Millisecond},
	}

	var (
		addresses []string
		built     []probe.Dialer
	)
	p := fake.probers()
	p.Dialer = func(addr string, timeout time.Duration) probe.Dialer {
		d := probe.NewSocksDialer(addr, timeout)
		addresses = append(addresses, addr)
		built = append(built, d)
		return d
	}
	s := New(snap, p, testOptions("example.com"))

	s.Cycle(context.Background(),
		[]Job{{Name: "nl1", SocksPort: socksPort, DNSPort: 5353}},
		[]config.Target{{Host: "a.test"}},
		[]config.Resolver{{Name: "cf", DoHURL: "https://cf.test/dns-query"}},
	)

	want := netutil.LoopbackAddr(socksPort)
	if len(addresses) != 2 {
		t.Fatalf("dialer factory called %d times (%v), want 2: one target and one resolver dialer", len(addresses), addresses)
	}
	for _, addr := range addresses {
		if addr != want {
			t.Errorf("dialer factory address = %q, want %q", addr, want)
		}
	}

	fake.mu.Lock()
	httpDialers := append([]probe.Dialer(nil), fake.httpDialers...)
	resolverDialers := append([]probe.Dialer(nil), fake.resolverDialers...)
	fake.mu.Unlock()

	if len(httpDialers) != 1 || httpDialers[0] != built[0] {
		t.Errorf("HTTPS prober got %v, want the factory-built dialer", httpDialers)
	}
	if len(resolverDialers) != 1 || resolverDialers[0] != built[1] {
		t.Errorf("resolver prober got %v, want the factory-built dialer", resolverDialers)
	}
}
