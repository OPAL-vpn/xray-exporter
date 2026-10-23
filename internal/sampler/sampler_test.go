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

	"github.com/Anton-beep/xray-exporter/internal/config"
	"github.com/Anton-beep/xray-exporter/internal/metrics"
	"github.com/Anton-beep/xray-exporter/internal/probe"
)

var seedTime = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

// fakeProbers records every probe it serves and answers with the configured
// result after an optional delay. The delay honours ctx, the way the real
// probers do, so a deadline test does not have to wait it out.
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

type dnsCall struct {
	server string
	domain string
}

type resolverCall struct {
	addr   string
	domain string
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

// sleep waits for d and reports whether it finished before ctx was cancelled.
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

// snapshot reads a value out of the snapshot the way Prometheus would.
func snapshot(t *testing.T, snap *metrics.Snapshot) map[string]float64 {
	t.Helper()

	reg := prometheus.NewRegistry()
	reg.MustRegister(snap.Collector())
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
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
	return out
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

	want := loopback(socksPort)
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

func TestIPLiteralSkipsDNS(t *testing.T) {
	snap := metrics.New()
	targets := []config.Target{{Host: "1.1.1.1"}, {Host: "9.9.9.9/status"}}
	snap.Seed("nl1", []string{targets[0].Host, targets[1].Host}, nil, seedTime)

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
	if got := mustValue(t, values, "xray_target_timestamp_seconds", "config", "nl1", "target", target); got <= float64(seedTime.Unix()) {
		t.Errorf("xray_target_timestamp_seconds{target=%s} = %v, want advanced by the DNS measurement", target, got)
	}
	for _, host := range names[1:] {
		if got := mustValue(t, values, "xray_target_timestamp_seconds", "config", "nl1", "target", host); got != float64(seedTime.UnixNano())/1e9 {
			t.Errorf("xray_target_timestamp_seconds{target=%s} = %v, want the seeded time for a target never measured", host, got)
		}
	}
	if got := mustValue(t, values, "xray_cycle_targets_total", "config", "nl1"); got != 1 {
		t.Errorf("xray_cycle_targets_total = %v, want 1 attempted", got)
	}
}

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
