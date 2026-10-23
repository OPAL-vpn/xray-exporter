// Package metrics holds the last result of every probe as a Prometheus
// snapshot: setters record measurements, the collector renders them at scrape
// time, and Collect does no work beyond reading the snapshot under a lock.
package metrics

import (
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Anton-beep/xray-exporter/internal/config"
	"github.com/Anton-beep/xray-exporter/internal/egress"
	"github.com/Anton-beep/xray-exporter/internal/probe"
)

// namespace prefixes every metric the exporter publishes.
const namespace = "xray"

// Transport values for the transport label. Only configured transports exist.
const (
	TransportDoH = "doh"
	TransportDoT = "dot"
)

// The descriptions are built once and reused by every const metric: a scrape
// allocates values, not schemas.
var (
	descProbeUp = prometheus.NewDesc(
		namespace+"_probe_up",
		"Whether the HTTPS probe got a response from the target.",
		[]string{"config", "target"}, nil,
	)
	descProbeHTTPStatus = prometheus.NewDesc(
		namespace+"_probe_http_status",
		"HTTP status code of the last probe, 0 when no response arrived.",
		[]string{"config", "target"}, nil,
	)
	descProbeTLSOk = prometheus.NewDesc(
		namespace+"_probe_tls_ok",
		"Whether the target certificate chained to a trusted root and matched the host.",
		[]string{"config", "target"}, nil,
	)
	descProbeTTFB = prometheus.NewDesc(
		namespace+"_probe_ttfb_seconds",
		"Time to the first response byte of the last successful probe.",
		[]string{"config", "target"}, nil,
	)
	descProbeTotal = prometheus.NewDesc(
		namespace+"_probe_total_seconds",
		"Full request time of the last successful probe.",
		[]string{"config", "target"}, nil,
	)
	descTargetTimestamp = prometheus.NewDesc(
		namespace+"_target_timestamp_seconds",
		"When the target's DNS and HTTPS pair last attempted.",
		[]string{"config", "target"}, nil,
	)
	descDNSUp = prometheus.NewDesc(
		namespace+"_dns_up",
		"Whether the tunnel DNS lookup got an answer.",
		[]string{"config", "target"}, nil,
	)
	descDNSLookup = prometheus.NewDesc(
		namespace+"_dns_lookup_seconds",
		"Round trip of the last successful tunnel DNS lookup.",
		[]string{"config", "target"}, nil,
	)
	descResolverUp = prometheus.NewDesc(
		namespace+"_resolver_up",
		"Whether the DoH or DoT resolver answered through the tunnel.",
		[]string{"config", "resolver", "transport"}, nil,
	)
	descResolverLookup = prometheus.NewDesc(
		namespace+"_resolver_lookup_seconds",
		"Round trip of the last successful resolver query.",
		[]string{"config", "resolver", "transport"}, nil,
	)
	descResolverTimestamp = prometheus.NewDesc(
		namespace+"_resolver_timestamp_seconds",
		"When the resolver was last attempted.",
		[]string{"config", "resolver", "transport"}, nil,
	)
	descEgressVerified = prometheus.NewDesc(
		namespace+"_egress_verified",
		"Whether the config's traffic leaves through its tunnel.",
		[]string{"config"}, nil,
	)
	descEgressIPInfo = prometheus.NewDesc(
		namespace+"_egress_ip_info",
		"The egress address the tunnel was observed to use.",
		[]string{"config", "ip"}, nil,
	)
	descEgressTimestamp = prometheus.NewDesc(
		namespace+"_egress_timestamp_seconds",
		"When egress was last verified.",
		[]string{"config"}, nil,
	)
	descConfigRunning = prometheus.NewDesc(
		namespace+"_config_running",
		"Whether the config's xray instance is up.",
		[]string{"config"}, nil,
	)
	descConfigInfo = prometheus.NewDesc(
		namespace+"_config_info",
		"xray version of the running instance.",
		[]string{"config", "xray_version"}, nil,
	)
	descCycleTimestamp = prometheus.NewDesc(
		namespace+"_cycle_timestamp_seconds",
		"When the last measurement cycle finished.",
		[]string{"config"}, nil,
	)
	descCycleDuration = prometheus.NewDesc(
		namespace+"_cycle_duration_seconds",
		"Wall time of the last measurement cycle.",
		[]string{"config"}, nil,
	)
	descCycleTargets = prometheus.NewDesc(
		namespace+"_cycle_targets_total",
		"Targets attempted by the last measurement cycle.",
		[]string{"config"}, nil,
	)
	descBuildInfo = prometheus.NewDesc(
		namespace+"_build_info",
		"Build version and Go version of the exporter.",
		[]string{"version", "go_version"}, nil,
	)
)

// allNames is every metric name this package emits, in emission order.
var allNames = []string{
	namespace + "_probe_up",
	namespace + "_probe_http_status",
	namespace + "_probe_tls_ok",
	namespace + "_probe_ttfb_seconds",
	namespace + "_probe_total_seconds",
	namespace + "_target_timestamp_seconds",
	namespace + "_dns_up",
	namespace + "_dns_lookup_seconds",
	namespace + "_resolver_up",
	namespace + "_resolver_lookup_seconds",
	namespace + "_resolver_timestamp_seconds",
	namespace + "_egress_verified",
	namespace + "_egress_ip_info",
	namespace + "_egress_timestamp_seconds",
	namespace + "_config_running",
	namespace + "_config_info",
	namespace + "_cycle_timestamp_seconds",
	namespace + "_cycle_duration_seconds",
	namespace + "_cycle_targets_total",
	namespace + "_exporter_config_reload_total",
	namespace + "_exporter_config_reload_errors_total",
	namespace + "_build_info",
}

// AllNames returns every metric name the package can emit, so the dashboard
// can be checked against the exporter instead of against memory.
func AllNames() []string {
	return append([]string(nil), allNames...)
}

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
		configs: make(map[string]*configState),
		reloads: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "exporter_config_reload_total",
			Help:      "Configuration reloads applied.",
		}),
		reloadErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "exporter_config_reload_errors_total",
			Help:      "Configuration reloads that failed.",
		}),
	}
	s.collector = combinedCollector{&snapshotCollector{snapshot: s}, s.reloads, s.reloadErrors}
	return s
}

// configState is everything known about one named xray config.
type configState struct {
	running   bool
	version   string
	targets   map[string]*targetState
	resolvers map[resolverKey]*resolverState
	egress    egressState
	cycle     cycleState
}

// targetState is one (config, target) pair. The raw results are kept for the
// one-shot report; the latency fields stay nil until a successful measurement,
// so the gauge is absent rather than zero.
type targetState struct {
	at         time.Time
	http       probe.HTTPResult
	hasHTTP    bool
	ttfb       *float64
	total      *float64
	dns        probe.DNSResult
	hasDNS     bool
	dnsLookup  *float64
	dnsSkipped bool
}

// resolverKey identifies one (config, resolver, transport) pair.
type resolverKey struct {
	name      string
	transport string
}

type resolverState struct {
	at     time.Time
	result probe.ResolverResult
	lookup *float64
}

type egressState struct {
	at       time.Time
	ip       string
	verified bool
}

type cycleState struct {
	done    bool
	at      time.Time
	dur     time.Duration
	targets int
}

// newConfigState returns the empty state Seed fills in.
func newConfigState() *configState {
	return &configState{
		targets:   make(map[string]*targetState),
		resolvers: make(map[resolverKey]*resolverState),
	}
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
		t.ttfb = durationPtr(r.TTFB)
		t.total = durationPtr(r.Total)
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
		t.dnsLookup = durationPtr(r.RTT)
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
		state.lookup = durationPtr(r.RTT)
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

// combinedCollector renders the snapshot and the reload counters as one
// collector, so a registry only has to know Snapshot.Collector.
type combinedCollector []prometheus.Collector

func (c combinedCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, collector := range c {
		collector.Describe(ch)
	}
}

func (c combinedCollector) Collect(ch chan<- prometheus.Metric) {
	for _, collector := range c {
		collector.Collect(ch)
	}
}

// snapshotCollector turns the snapshot into metric families.
type snapshotCollector struct {
	snapshot *Snapshot
}

// Describe reports the schema of every family the collector can produce.
func (c *snapshotCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{
		descProbeUp, descProbeHTTPStatus, descProbeTLSOk, descProbeTTFB,
		descProbeTotal, descTargetTimestamp, descDNSUp, descDNSLookup,
		descResolverUp, descResolverLookup, descResolverTimestamp,
		descEgressVerified, descEgressIPInfo, descEgressTimestamp,
		descConfigRunning, descConfigInfo, descCycleTimestamp,
		descCycleDuration, descCycleTargets, descBuildInfo,
	} {
		ch <- desc
	}
}

// Collect renders the snapshot under a read lock.
func (c *snapshotCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.snapshot

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, name := range sortedKeys(s.configs) {
		collectConfig(ch, name, s.configs[name])
	}

	if s.buildVersion != "" || s.buildGoVersion != "" {
		ch <- prometheus.MustNewConstMetric(descBuildInfo, prometheus.GaugeValue, 1, s.buildVersion, s.buildGoVersion)
	}
}

func collectConfig(ch chan<- prometheus.Metric, name string, cfg *configState) {
	ch <- prometheus.MustNewConstMetric(descConfigRunning, prometheus.GaugeValue, boolValue(cfg.running), name)
	if cfg.version != "" {
		ch <- prometheus.MustNewConstMetric(descConfigInfo, prometheus.GaugeValue, 1, name, cfg.version)
	}

	ch <- prometheus.MustNewConstMetric(descEgressVerified, prometheus.GaugeValue, boolValue(cfg.egress.verified), name)
	if !cfg.egress.at.IsZero() {
		ch <- prometheus.MustNewConstMetric(descEgressTimestamp, prometheus.GaugeValue, unixSeconds(cfg.egress.at), name)
	}
	if cfg.egress.ip != "" {
		ch <- prometheus.MustNewConstMetric(descEgressIPInfo, prometheus.GaugeValue, 1, name, cfg.egress.ip)
	}

	if cfg.cycle.done {
		ch <- prometheus.MustNewConstMetric(descCycleTimestamp, prometheus.GaugeValue, unixSeconds(cfg.cycle.at), name)
		ch <- prometheus.MustNewConstMetric(descCycleDuration, prometheus.GaugeValue, cfg.cycle.dur.Seconds(), name)
		ch <- prometheus.MustNewConstMetric(descCycleTargets, prometheus.GaugeValue, float64(cfg.cycle.targets), name)
	}

	for _, target := range sortedKeys(cfg.targets) {
		collectTarget(ch, name, target, cfg.targets[target])
	}

	resolverKeys := make([]resolverKey, 0, len(cfg.resolvers))
	for key := range cfg.resolvers {
		resolverKeys = append(resolverKeys, key)
	}
	sort.Slice(resolverKeys, func(i, j int) bool {
		if resolverKeys[i].name != resolverKeys[j].name {
			return resolverKeys[i].name < resolverKeys[j].name
		}
		return resolverKeys[i].transport < resolverKeys[j].transport
	})
	for _, key := range resolverKeys {
		state := cfg.resolvers[key]
		labels := []string{name, key.name, key.transport}
		ch <- prometheus.MustNewConstMetric(descResolverUp, prometheus.GaugeValue, boolValue(state.result.Up), labels...)
		if state.lookup != nil {
			ch <- prometheus.MustNewConstMetric(descResolverLookup, prometheus.GaugeValue, *state.lookup, labels...)
		}
		ch <- prometheus.MustNewConstMetric(descResolverTimestamp, prometheus.GaugeValue, unixSeconds(state.at), labels...)
	}
}

func collectTarget(ch chan<- prometheus.Metric, config, target string, t *targetState) {
	labels := []string{config, target}

	ch <- prometheus.MustNewConstMetric(descProbeUp, prometheus.GaugeValue, boolValue(t.http.Up), labels...)
	ch <- prometheus.MustNewConstMetric(descProbeHTTPStatus, prometheus.GaugeValue, float64(t.http.Status), labels...)
	ch <- prometheus.MustNewConstMetric(descProbeTLSOk, prometheus.GaugeValue, boolValue(t.http.TLSVerified), labels...)
	if t.ttfb != nil {
		ch <- prometheus.MustNewConstMetric(descProbeTTFB, prometheus.GaugeValue, *t.ttfb, labels...)
	}
	if t.total != nil {
		ch <- prometheus.MustNewConstMetric(descProbeTotal, prometheus.GaugeValue, *t.total, labels...)
	}
	ch <- prometheus.MustNewConstMetric(descTargetTimestamp, prometheus.GaugeValue, unixSeconds(t.at), labels...)

	if t.dnsSkipped {
		return
	}
	ch <- prometheus.MustNewConstMetric(descDNSUp, prometheus.GaugeValue, boolValue(t.dns.Up), labels...)
	if t.dnsLookup != nil {
		ch <- prometheus.MustNewConstMetric(descDNSLookup, prometheus.GaugeValue, *t.dnsLookup, labels...)
	}
}

func boolValue(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

func durationPtr(d time.Duration) *float64 {
	seconds := d.Seconds()
	return &seconds
}

func unixSeconds(t time.Time) float64 {
	return float64(t.UnixNano()) / 1e9
}

// sortedKeys returns a map's keys in order, so a scrape is stable.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
