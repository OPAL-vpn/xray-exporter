package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

func TestExport(t *testing.T) {
	snap := New()
	snap.Seed("nl1", []string{"a.test", "b.test"}, []config.Resolver{
		{Name: "cf", DoHURL: "https://cf.test/dns-query", DoTHost: "cf.test"},
	}, seedTime)
	snap.SetConfig("nl1", true, "v26.10.21")
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
