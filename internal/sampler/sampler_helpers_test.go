package sampler

import (
	"context"
	"crypto/x509"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

var seedTime = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

type dnsCall struct {
	server string
	domain string
}

type resolverCall struct {
	addr   string
	domain string
}

// fakeProbers records probes and answers after an optional ctx-aware delay.
type fakeProbers struct {
	mu sync.Mutex

	httpHosts []string
	dnsCalls  []dnsCall
	dohCalls  []resolverCall
	dotCalls  []resolverCall

	httpDialers     []probe.Dialer
	resolverDialers []probe.Dialer

	httpResult     probe.HTTPResult
	dnsResult      probe.DNSResult
	resolverResult probe.ResolverResult
	httpDelay      time.Duration
	dnsDelay       time.Duration

	inflight    int32
	maxInflight int32
}

func (f *fakeProbers) probers() Probers {
	return Probers{HTTP: f.http, DNS: f.dns, DoH: f.doh, DoT: f.dot}
}

func (f *fakeProbers) http(ctx context.Context, d probe.Dialer, host string, _ time.Duration, _ *x509.CertPool) probe.HTTPResult {
	f.enter()
	defer f.leave()

	f.mu.Lock()
	f.httpHosts = append(f.httpHosts, host)
	f.httpDialers = append(f.httpDialers, d)
	f.mu.Unlock()

	if !sleep(ctx, f.httpDelay) {
		return probe.HTTPResult{Err: ctx.Err()}
	}
	return f.httpResult
}

func (f *fakeProbers) dns(ctx context.Context, server, domain string, _ time.Duration) probe.DNSResult {
	f.enter()
	defer f.leave()

	f.mu.Lock()
	f.dnsCalls = append(f.dnsCalls, dnsCall{server: server, domain: domain})
	f.mu.Unlock()

	if !sleep(ctx, f.dnsDelay) {
		return probe.DNSResult{Err: ctx.Err()}
	}
	return f.dnsResult
}

func (f *fakeProbers) doh(ctx context.Context, d probe.Dialer, url, domain string, _ time.Duration, _ *x509.CertPool) probe.ResolverResult {
	f.enter()
	defer f.leave()

	f.mu.Lock()
	f.dohCalls = append(f.dohCalls, resolverCall{addr: url, domain: domain})
	f.resolverDialers = append(f.resolverDialers, d)
	f.mu.Unlock()

	return f.resolverResult
}

func (f *fakeProbers) dot(ctx context.Context, d probe.Dialer, host, domain string, _ time.Duration, _ *x509.CertPool) probe.ResolverResult {
	f.enter()
	defer f.leave()

	f.mu.Lock()
	f.dotCalls = append(f.dotCalls, resolverCall{addr: host, domain: domain})
	f.resolverDialers = append(f.resolverDialers, d)
	f.mu.Unlock()

	return f.resolverResult
}

func (f *fakeProbers) enter() {
	n := atomic.AddInt32(&f.inflight, 1)
	for {
		max := atomic.LoadInt32(&f.maxInflight)
		if n <= max || atomic.CompareAndSwapInt32(&f.maxInflight, max, n) {
			return
		}
	}
}

func (f *fakeProbers) leave() {
	atomic.AddInt32(&f.inflight, -1)
}

func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func snapshot(t *testing.T, snap *metrics.Snapshot) map[string]float64 {
	t.Helper()

	out, err := gatherSnapshot(snap)
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return out
}

// gatherSnapshot renders every series; it returns the error for use in a prober.
func gatherSnapshot(snap *metrics.Snapshot) (map[string]float64, error) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(snap.Collector())
	families, err := reg.Gather()
	if err != nil {
		return nil, err
	}

	out := make(map[string]float64)
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			key := family.GetName()
			for _, pair := range metric.GetLabel() {
				key += "," + pair.GetName() + "=" + pair.GetValue()
			}
			out[key] = sampleValue(metric)
		}
	}
	return out, nil
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
		return 0
	}
}

func series(name string, pairs ...string) string {
	key := name
	for i := 0; i+1 < len(pairs); i += 2 {
		key += "," + pairs[i] + "=" + pairs[i+1]
	}
	return key
}

func mustValue(t *testing.T, values map[string]float64, name string, pairs ...string) float64 {
	t.Helper()

	key := series(name, pairs...)
	got, ok := values[key]
	if !ok {
		t.Fatalf("%s: series missing", key)
	}
	return got
}

func mustAbsent(t *testing.T, values map[string]float64, name string, pairs ...string) {
	t.Helper()

	key := series(name, pairs...)
	if got, ok := values[key]; ok {
		t.Fatalf("%s: series present with %v, want absent", key, got)
	}
}

func testOptions(probeDomain string) Options {
	return Options{
		Workers:         4,
		ProbeTimeout:    50 * time.Millisecond,
		ResolverTimeout: 50 * time.Millisecond,
		Deadline:        time.Second,
		ProbeDomain:     probeDomain,
	}
}
