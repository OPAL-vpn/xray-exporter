// Package metrics keeps the last probe results as a Prometheus snapshot.
// Setters record measurements; the collector renders them at scrape time.
package metrics

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/egress"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

// Snapshot holds the last result of every measurement and is safe for
// concurrent setters and scrapes. Setters ignore entities Seed did not declare.
type Snapshot struct {
	mu      sync.RWMutex
	configs map[string]*configState

	buildVersion   string
	buildGoVersion string

	reloads      prometheus.Counter
	reloadErrors prometheus.Counter
	collector    prometheus.Collector
}

// New returns an empty snapshot.
func New() *Snapshot {
	s := &Snapshot{
		configs:      make(map[string]*configState),
		reloads:      reloadTotal.newCounter(),
		reloadErrors: reloadErrors.newCounter(),
	}
	s.collector = combinedCollector{&snapshotCollector{snapshot: s}, s.reloads, s.reloadErrors}
	return s
}

// Seed reconciles a config's declared targets and resolver transports: existing
// measurements are kept, entities no longer present are removed. Setters only
// update declared entities, so a late result cannot resurrect a removed series.
func (s *Snapshot) Seed(config string, targets []string, resolvers []config.Resolver, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, ok := s.configs[config]
	if !ok {
		cfg = newConfigState()
		s.configs[config] = cfg
	}

	keepTargets := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		keepTargets[target] = struct{}{}
		if _, ok := cfg.targets[target]; !ok {
			cfg.targets[target] = &targetState{at: now}
		}
	}
	for target := range cfg.targets {
		if _, ok := keepTargets[target]; !ok {
			delete(cfg.targets, target)
		}
	}

	keepResolvers := make(map[resolverKey]struct{}, 2*len(resolvers))
	for _, resolver := range resolvers {
		for _, transport := range resolverTransports(resolver) {
			key := resolverKey{name: resolver.Name, transport: transport}
			keepResolvers[key] = struct{}{}
			if _, ok := cfg.resolvers[key]; !ok {
				cfg.resolvers[key] = &resolverState{at: now}
			}
		}
	}
	for key := range cfg.resolvers {
		if _, ok := keepResolvers[key]; !ok {
			delete(cfg.resolvers, key)
		}
	}
}

func resolverTransports(resolver config.Resolver) []string {
	var transports []string
	if resolver.DoHURL != "" {
		transports = append(transports, TransportDoH)
	}
	if resolver.DoTHost != "" {
		transports = append(transports, TransportDoT)
	}
	return transports
}

func (s *Snapshot) Drop(config string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.configs, config)
}

// SetConfig records whether the instance runs. An empty version keeps the last
// known one, because a failed start has none to report.
func (s *Snapshot) SetConfig(config string, running bool, version string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, ok := s.configs[config]
	if !ok {
		return
	}
	cfg.running = running
	if version != "" {
		cfg.version = version
	}
}

func (s *Snapshot) SetBuildInfo(version, goVersion string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.buildVersion = version
	s.buildGoVersion = goVersion
}

// SetEgress records a verdict, keeping the last observed address when the check
// fails.
func (s *Snapshot) SetEgress(config string, r egress.Result, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, ok := s.configs[config]
	if !ok {
		return
	}
	cfg.egress.at = at
	cfg.egress.verified = r.Verified
	if r.IP != "" {
		cfg.egress.ip = r.IP
	}
}

func (s *Snapshot) SetCycle(config string, at time.Time, dur time.Duration, targets int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, ok := s.configs[config]
	if !ok {
		return
	}
	cfg.cycle = cycleState{done: true, at: at, dur: dur, targets: targets}
}

// SetHTTP records a probe, keeping the last successful latencies when it fails.
func (s *Snapshot) SetHTTP(config, target string, r probe.HTTPResult, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t := s.target(config, target)
	if t == nil {
		return
	}
	t.at = at
	t.http = r
	t.hasHTTP = true
	if r.Up {
		setLatency(&t.ttfb, r.TTFB)
		setLatency(&t.total, r.Total)
	}
}

// SetDNS records a lookup, keeping the last successful round trip when it fails.
func (s *Snapshot) SetDNS(config, target string, r probe.DNSResult, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t := s.target(config, target)
	if t == nil {
		return
	}
	t.at = at
	t.dns = r
	t.hasDNS = true
	if r.Up {
		setLatency(&t.dnsLookup, r.RTT)
	}
}

// SkipDNS removes a target's DNS series; an address literal has nothing to
// resolve.
func (s *Snapshot) SkipDNS(config, target string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t := s.target(config, target)
	if t == nil {
		return
	}
	t.dnsSkipped = true
	t.dns = probe.DNSResult{}
	t.hasDNS = false
	t.dnsLookup = nil
}

// SetResolver records a probe, keeping the last successful round trip when it
// fails.
func (s *Snapshot) SetResolver(config, resolver, transport string, r probe.ResolverResult, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, ok := s.configs[config]
	if !ok {
		return
	}
	state, ok := cfg.resolvers[resolverKey{name: resolver, transport: transport}]
	if !ok {
		return
	}
	state.at = at
	state.result = r
	if r.Up {
		setLatency(&state.lookup, r.RTT)
	}
}

// target returns nil when Seed did not declare it. The caller holds the write
// lock.
func (s *Snapshot) target(config, target string) *targetState {
	cfg, ok := s.configs[config]
	if !ok {
		return nil
	}
	return cfg.targets[target]
}

func (s *Snapshot) IncReload() {
	s.reloads.Inc()
}

func (s *Snapshot) IncReloadError() {
	s.reloadErrors.Inc()
}

// Collector renders the last snapshot without waiting for a measurement.
func (s *Snapshot) Collector() prometheus.Collector {
	return s.collector
}

// setLatency ignores a zero duration: publishing it would chart as infinitely
// fast and poison averages.
func setLatency(dst **float64, d time.Duration) {
	if d <= 0 {
		return
	}
	seconds := d.Seconds()
	*dst = &seconds
}
