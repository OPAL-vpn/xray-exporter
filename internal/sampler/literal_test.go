package sampler

import (
	"context"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

func TestIPLiteralSkipsDNS(t *testing.T) {
	snap := metrics.New()
	targets := []config.Target{
		{Host: "1.1.1.1"},
		{Host: "9.9.9.9/status"},
		{Host: "[2606:4700:4700::1111]"},
		{Host: "[2606:4700:4700::1111]/status"},
	}
	names := make([]string, len(targets))
	for i, target := range targets {
		names[i] = target.Host
	}
	snap.Seed("nl1", names, nil, seedTime)

	fake := &fakeProbers{
		httpResult: probe.HTTPResult{Up: true, Status: 200},
		dnsResult:  probe.DNSResult{Up: true, RTT: time.Millisecond},
	}
	s := New(snap, fake.probers(), testOptions("example.com"))

	s.Cycle(context.Background(),
		[]Job{{Name: "nl1", SocksPort: 1080, DNSPort: 5353}},
		targets, nil,
	)

	fake.mu.Lock()
	dnsCalls := len(fake.dnsCalls)
	httpHosts := append([]string(nil), fake.httpHosts...)
	fake.mu.Unlock()

	if dnsCalls != 0 {
		t.Errorf("DNS called %d times for address literals, want 0", dnsCalls)
	}
	if len(httpHosts) != len(targets) {
		t.Errorf("HTTPS called %d times (%v), want %d", len(httpHosts), httpHosts, len(targets))
	}

	values := snapshot(t, snap)
	for _, target := range targets {
		if got := mustValue(t, values, "xray_probe_up", "config", "nl1", "target", target.Host); got != 1 {
			t.Errorf("xray_probe_up{target=%s} = %v, want 1", target.Host, got)
		}
	}
}

func TestIPLiteralPublishesNoDNSUp(t *testing.T) {
	const target = "1.1.1.1"

	snap := metrics.New()
	snap.Seed("nl1", []string{target}, nil, seedTime)

	fake := &fakeProbers{
		httpResult: probe.HTTPResult{Up: true, Status: 200},
		dnsResult:  probe.DNSResult{Up: true, RTT: time.Millisecond},
	}
	s := New(snap, fake.probers(), testOptions("example.com"))

	s.Cycle(context.Background(),
		[]Job{{Name: "nl1", SocksPort: 1080, DNSPort: 5353}},
		[]config.Target{{Host: target}}, nil,
	)

	values := snapshot(t, snap)
	mustAbsent(t, values, "xray_dns_up", "config", "nl1", "target", target)
	mustAbsent(t, values, "xray_dns_lookup_seconds", "config", "nl1", "target", target)
}
