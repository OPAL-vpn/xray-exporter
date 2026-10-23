// Package metrics holds the last result of every probe as a Prometheus
// snapshot: setters record measurements, the collector renders them at scrape
// time, and Collect does no work beyond reading the snapshot under a lock.
package metrics

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/egress"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

// Snapshot is the last result of every measurement, safe for concurrent
// setters and scrapes. Seed declares which entities exist, the setters only
// update declared entities, and Drop removes them.
type Snapshot struct {
	mu      sync.RWMutex
	configs map[string]*configState

	buildVersion   string
	buildGoVersion string

	reloads      prometheus.Counter
	reloadErrors prometheus.Counter
	collector    prometheus.Collector
}

// New returns an empty snapshot: no config has a series until Seed.
func New() *Snapshot {
	s := &Snapshot{
		configs:      make(map[string]*configState),
		reloads:      reloadTotal.counter,
		reloadErrors: reloadErrors.counter,
	}
	s.collector = combinedCollector{&snapshotCollector{snapshot: s}, s.reloads, s.reloadErrors}
	return s
}

// Seed declares the entities a config is expected to measure. Every target and
// every configured resolver transport gets its series before the first probe,
// with up 0 and the given time as its timestamp. Entities no longer present are
// removed; existing entities keep their last measurement, so a reload never
// produces an orphan or loses history. Setters only update entities Seed
// declared, so a result that arrives after a reload is discarded instead of
// resurrecting a removed series.
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

// resolverTransports lists the transports a resolver is configured for.
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

// Drop removes every series belonging to a config.
func (s *Snapshot) Drop(config string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.configs, config)
}

// SetConfig records whether the config's instance runs and, when known, which
// xray version it runs. An empty version leaves a previously known one in
// place: a failed start has no new version to report.
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

// SetBuildInfo records the exporter's own build identity.
func (s *Snapshot) SetBuildInfo(version, goVersion string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.buildVersion = version
	s.buildGoVersion = goVersion
}

// SetEgress records one egress verification. A failed check moves the time and
// the verdict but keeps the last observed address, the way a failed probe keeps
// its last latency.
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

// SetCycle records a completed measurement cycle.
func (s *Snapshot) SetCycle(config string, at time.Time, dur time.Duration, targets int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, ok := s.configs[config]
	if !ok {
		return
	}
	cfg.cycle = cycleState{done: true, at: at, dur: dur, targets: targets}
}

// SetHTTP records an HTTPS probe. A failed probe clears up/status/tls and moves
// the timestamp but leaves the last successful latencies in place.
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

// SetDNS records a tunnel DNS lookup. A failed lookup moves the timestamp but
// leaves the last successful round trip in place.
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

// SkipDNS removes a target's DNS series entirely. An address literal has
// nothing to resolve, so publishing a permanently failing or zero lookup would
// describe a measurement that never happened.
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

// SetResolver records one DoH or DoT probe. A failed probe moves the timestamp
// but leaves the last successful round trip in place.
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

// target returns the state for target, or nil when Seed did not declare it.
// The caller holds the write lock.
func (s *Snapshot) target(config, target string) *targetState {
	cfg, ok := s.configs[config]
	if !ok {
		return nil
	}
	return cfg.targets[target]
}

// IncReload records one applied configuration reload.
func (s *Snapshot) IncReload() {
	s.reloads.Inc()
}

// IncReloadError records one configuration reload that failed.
func (s *Snapshot) IncReloadError() {
	s.reloadErrors.Inc()
}

// Collector returns the collector to register with a Prometheus registry. It
// renders the last snapshot; it never waits for a measurement.
func (s *Snapshot) Collector() prometheus.Collector {
	return s.collector
}

// setLatency stores d only when the probe's timer actually ran. A successful
// probe that reports a zero duration must not publish a zero: it would chart
// as infinitely fast and poison averages. The last real measurement stays in
// place, the same way a failed probe leaves it in place.
func setLatency(dst **float64, d time.Duration) {
	if d <= 0 {
		return
	}
	seconds := d.Seconds()
	*dst = &seconds
}
