// Package metrics keeps the last probe results as a Prometheus snapshot.
// Setters record measurements; the collector renders them at scrape time.
package metrics

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
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

	fetches     prometheus.Counter
	fetchErrors prometheus.Counter
	fetchAt     time.Time
	hasFetch    bool
	source      string

	// reachability is nil unless the config declares a reachability block.
	reachability *reachabilityState

	collector prometheus.Collector
}

// New returns an empty snapshot.
func New() *Snapshot {
	s := &Snapshot{
		configs:      make(map[string]*configState),
		reloads:      reloadTotal.newCounter(),
		reloadErrors: reloadErrors.newCounter(),
		fetches:      fetchTotal.newCounter(),
		fetchErrors:  fetchErrors.newCounter(),
	}
	s.collector = combinedCollector{&snapshotCollector{snapshot: s}, s.reloads, s.reloadErrors, s.fetches, s.fetchErrors}
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

// SeedUDP reconciles a config's declared UDP series: every target is paired
// with every load pass, existing measurements are kept, and target/load pairs
// no longer declared are removed.
func (s *Snapshot) SeedUDP(config string, targets []config.UDPTarget, loads []string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, ok := s.configs[config]
	if !ok {
		cfg = newConfigState()
		s.configs[config] = cfg
	}

	keep := make(map[udpKey]struct{}, len(targets)*len(loads))
	for _, target := range targets {
		for _, load := range loads {
			key := udpKey{target: target.Name, load: load}
			keep[key] = struct{}{}
			if _, ok := cfg.udps[key]; !ok {
				cfg.udps[key] = &udpState{at: now}
			}
		}
	}
	for key := range cfg.udps {
		if _, ok := keep[key]; !ok {
			delete(cfg.udps, key)
		}
	}

	// A removed under-load pass must not keep reporting generator health for
	// a run that no longer happens.
	underLoad := false
	for _, load := range loads {
		if load == "loaded" {
			underLoad = true
			break
		}
	}
	if !underLoad {
		cfg.udpLoad = nil
	}
}

// SeedTCP reconciles a config's declared TCP targets: existing measurements
// are kept, targets no longer present are removed.
func (s *Snapshot) SeedTCP(config string, targets []config.TCPTarget) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, ok := s.configs[config]
	if !ok {
		cfg = newConfigState()
		s.configs[config] = cfg
	}

	keep := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		keep[target.Name] = struct{}{}
		if _, ok := cfg.tcps[target.Name]; !ok {
			cfg.tcps[target.Name] = &tcpState{}
		}
	}
	for target := range cfg.tcps {
		if _, ok := keep[target]; !ok {
			delete(cfg.tcps, target)
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

// SetUDP records a burst, keeping the last successful gauges when it fails.
func (s *Snapshot) SetUDP(config, target, load string, r probe.UDPResult, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, ok := s.configs[config]
	if !ok {
		return
	}
	state, ok := cfg.udps[udpKey{target: target, load: load}]
	if !ok {
		return
	}
	state.at = at
	state.result = r
	if !r.Up {
		return
	}
	// A successful burst always has replies: loss and jitter are meaningful
	// even at zero, while the RTT gauges keep the shared zero guard.
	loss := lossRatio(r)
	state.loss = &loss
	setLatency(&state.rtt, r.Median)
	setLatency(&state.rttMax, r.Max)
	jitter := r.Jitter.Seconds()
	state.jitter = &jitter
}

// SetUDPLoad records whether the last loaded pass moved bytes; the family is
// created on the first call because it only exists with under-load configured.
// The timestamp is accepted for the uniform setter shape but not exposed: the
// spec has no load-timestamp family.
func (s *Snapshot) SetUDPLoad(config string, up bool, _ time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, ok := s.configs[config]
	if !ok {
		return
	}
	cfg.udpLoad = &udpLoadState{up: up}
}

// SetTCP records a connect, keeping the last successful connect time when it
// fails.
func (s *Snapshot) SetTCP(config, target string, r probe.TunnelTCPResult, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, ok := s.configs[config]
	if !ok {
		return
	}
	state, ok := cfg.tcps[target]
	if !ok {
		return
	}
	state.result = r
	if r.Up {
		setLatency(&state.connect, r.Connect)
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

func (s *Snapshot) IncFetch() {
	s.fetches.Inc()
}

func (s *Snapshot) IncFetchError() {
	s.fetchErrors.Inc()
}

// SetFetchSuccess records when the config source last returned a usable set; a
// failed fetch leaves the previous success time in place.
func (s *Snapshot) SetFetchSuccess(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.fetchAt = at
	s.hasFetch = true
}

func (s *Snapshot) SetSource(kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.source = kind
}

// Collector renders the last snapshot without waiting for a measurement.
func (s *Snapshot) Collector() prometheus.Collector {
	return s.collector
}

// lossRatio is the share of sent datagrams that went unanswered.
func lossRatio(r probe.UDPResult) float64 {
	if r.Sent <= 0 {
		return 0
	}
	return 1 - float64(r.Replies)/float64(r.Sent)
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
