package sampler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

func TestWorkersBound(t *testing.T) {
	const (
		targets = 20
		workers = 4
	)
	hosts := make([]config.Target, targets)
	names := make([]string, targets)
	for i := range hosts {
		hosts[i] = config.Target{Host: "t" + string(rune('a'+i)) + ".test"}
		names[i] = hosts[i].Host
	}

	snap := metrics.New()
	snap.Seed("nl1", names, nil, seedTime)

	fake := &fakeProbers{
		httpResult: probe.HTTPResult{Up: true, Status: 200},
		dnsResult:  probe.DNSResult{Up: true, RTT: time.Millisecond},
		httpDelay:  20 * time.Millisecond,
		dnsDelay:   20 * time.Millisecond,
	}
	opts := testOptions("example.com")
	opts.Workers = workers
	s := New(snap, fake.probers(), opts)

	s.Cycle(context.Background(),
		[]Job{{Name: "nl1", SocksPort: 1080, DNSPort: 5353}},
		hosts, nil,
	)

	if got := atomic.LoadInt32(&fake.maxInflight); got > workers {
		t.Errorf("observed concurrency = %d, want at most %d", got, workers)
	}
	if got := atomic.LoadInt32(&fake.maxInflight); got < 2 {
		t.Errorf("observed concurrency = %d, want the pool to actually run probes in parallel", got)
	}
}
