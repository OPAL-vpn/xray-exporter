package sampler

import (
	"context"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

func TestDeadline(t *testing.T) {
	const (
		target = "slow.test"
		others = 2
	)
	targets := []config.Target{{Host: target}}
	names := []string{target}
	for i := 0; i < others; i++ {
		host := "later" + string(rune('a'+i)) + ".test"
		targets = append(targets, config.Target{Host: host})
		names = append(names, host)
	}

	snap := metrics.New()
	snap.Seed("nl1", names, nil, seedTime)

	fake := &fakeProbers{
		httpResult: probe.HTTPResult{Up: true, Status: 200},
		dnsResult:  probe.DNSResult{Up: true, RTT: time.Millisecond},
		httpDelay:  time.Second,
	}
	opts := testOptions("example.com")
	opts.Workers = 1
	opts.Deadline = 100 * time.Millisecond
	s := New(snap, fake.probers(), opts)

	start := time.Now()
	s.Cycle(context.Background(),
		[]Job{{Name: "nl1", SocksPort: 1080, DNSPort: 5353}},
		targets, nil,
	)
	elapsed := time.Since(start)

	if elapsed > 300*time.Millisecond {
		t.Errorf("Cycle took %v, want it bounded near the 100ms deadline", elapsed)
	}

	values := snapshot(t, snap)
	seedSeconds := float64(seedTime.UnixNano()) / 1e9
	if got := mustValue(t, values, "xray_target_timestamp_seconds", "config", "nl1", "target", target); got != seedSeconds {
		t.Errorf("xray_target_timestamp_seconds{target=%s} = %v, want the seeded time %v: the deadline cut the HTTPS half of the pair", target, got, seedSeconds)
	}
	for _, host := range names[1:] {
		if got := mustValue(t, values, "xray_target_timestamp_seconds", "config", "nl1", "target", host); got != seedSeconds {
			t.Errorf("xray_target_timestamp_seconds{target=%s} = %v, want the seeded time for a target never measured", host, got)
		}
	}
	if got := mustValue(t, values, "xray_cycle_targets_total", "config", "nl1"); got != 1 {
		t.Errorf("xray_cycle_targets_total = %v, want 1 attempted", got)
	}
}
