package metrics

import (
	"sync"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

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
	// Scrape on the test goroutine so a Gather failure still fails the test.
	for j := 0; j < 100; j++ {
		_ = gatherSamples(t, snap)
		_ = snap.Export()
	}
	wg.Wait()
}
