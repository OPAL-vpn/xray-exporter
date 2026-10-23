package metrics

import (
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

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
