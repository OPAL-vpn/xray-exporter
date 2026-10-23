package sampler

import (
	"context"
	"crypto/x509"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

func TestTargetTimestampAdvances(t *testing.T) {
	const target = "a.test"

	snap := metrics.New()
	snap.Seed("nl1", []string{target}, nil, seedTime)
	snap.SetHTTP("nl1", target, probe.HTTPResult{Up: true, Status: 200}, seedTime)
	snap.SetDNS("nl1", target, probe.DNSResult{Up: true, RTT: time.Millisecond}, seedTime)

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
	if got := mustValue(t, values, "xray_target_timestamp_seconds", "config", "nl1", "target", target); got <= float64(seedTime.UnixNano())/1e9 {
		t.Errorf("xray_target_timestamp_seconds = %v, want after the seed time %v", got, seedTime.UnixNano()/1e9)
	}
}

// TestTargetTimestampWaitsForThePair pins that the timestamp is stamped after
// the HTTPS result exists, so a scrape between probes sees one pair.
func TestTargetTimestampWaitsForThePair(t *testing.T) {
	const target = "a.test"

	snap := metrics.New()
	snap.Seed("nl1", []string{target}, nil, seedTime)

	type reading struct {
		timestamp float64
		err       error
	}
	observed := make(chan reading, 1)
	fake := &fakeProbers{
		httpResult: probe.HTTPResult{Up: true, Status: 200},
		dnsResult:  probe.DNSResult{Up: true, RTT: time.Millisecond},
	}
	probers := fake.probers()
	http := probers.HTTP
	probers.HTTP = func(ctx context.Context, d probe.Dialer, host string, timeout time.Duration, roots *x509.CertPool) probe.HTTPResult {
		// A stamp taken before the pair completes would show here.
		values, err := gatherSnapshot(snap)
		got := reading{err: err}
		if err == nil {
			got.timestamp = values[series("xray_target_timestamp_seconds", "config", "nl1", "target", target)]
		}
		observed <- got
		return http(ctx, d, host, timeout, roots)
	}
	s := New(snap, probers, testOptions("example.com"))

	s.Cycle(context.Background(),
		[]Job{{Name: "nl1", SocksPort: 1080, DNSPort: 5353}},
		[]config.Target{{Host: target}}, nil,
	)

	got := <-observed
	if got.err != nil {
		t.Fatalf("gather during the HTTPS probe: %v", got.err)
	}
	if seedSeconds := float64(seedTime.UnixNano()) / 1e9; got.timestamp != seedSeconds {
		t.Errorf("xray_target_timestamp_seconds during the HTTPS probe = %v, want the seeded time %v", got.timestamp, seedSeconds)
	}
}
