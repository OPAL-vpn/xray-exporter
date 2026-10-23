package metrics

import (
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// combinedCollector renders the snapshot and reload counters as one collector.
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

type snapshotCollector struct {
	snapshot *Snapshot
}

// Describe skips counter families; they describe themselves.
func (c *snapshotCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, metric := range allMetrics {
		if !metric.counter {
			ch <- metric.desc
		}
	}
}

func (c *snapshotCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.snapshot

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, name := range sortedKeys(s.configs) {
		collectConfig(ch, name, s.configs[name])
	}

	if s.buildVersion != "" || s.buildGoVersion != "" {
		ch <- prometheus.MustNewConstMetric(buildInfo.desc, prometheus.GaugeValue, 1, s.buildVersion, s.buildGoVersion)
	}
}

func collectConfig(ch chan<- prometheus.Metric, name string, cfg *configState) {
	ch <- prometheus.MustNewConstMetric(configRunning.desc, prometheus.GaugeValue, boolValue(cfg.running), name)
	if cfg.version != "" {
		ch <- prometheus.MustNewConstMetric(configInfo.desc, prometheus.GaugeValue, 1, name, cfg.version)
	}

	ch <- prometheus.MustNewConstMetric(egressVerified.desc, prometheus.GaugeValue, boolValue(cfg.egress.verified), name)
	if !cfg.egress.at.IsZero() {
		ch <- prometheus.MustNewConstMetric(egressTimestamp.desc, prometheus.GaugeValue, unixSeconds(cfg.egress.at), name)
	}
	if cfg.egress.ip != "" {
		ch <- prometheus.MustNewConstMetric(egressIPInfo.desc, prometheus.GaugeValue, 1, name, cfg.egress.ip)
	}

	if cfg.cycle.done {
		ch <- prometheus.MustNewConstMetric(cycleTimestamp.desc, prometheus.GaugeValue, unixSeconds(cfg.cycle.at), name)
		ch <- prometheus.MustNewConstMetric(cycleDuration.desc, prometheus.GaugeValue, cfg.cycle.dur.Seconds(), name)
		ch <- prometheus.MustNewConstMetric(cycleTargets.desc, prometheus.GaugeValue, float64(cfg.cycle.targets), name)
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
		ch <- prometheus.MustNewConstMetric(resolverUp.desc, prometheus.GaugeValue, boolValue(state.result.Up), labels...)
		if state.lookup != nil {
			ch <- prometheus.MustNewConstMetric(resolverLookup.desc, prometheus.GaugeValue, *state.lookup, labels...)
		}
		ch <- prometheus.MustNewConstMetric(resolverTimestamp.desc, prometheus.GaugeValue, unixSeconds(state.at), labels...)
	}
}

func collectTarget(ch chan<- prometheus.Metric, config, target string, t *targetState) {
	labels := []string{config, target}

	ch <- prometheus.MustNewConstMetric(probeUp.desc, prometheus.GaugeValue, boolValue(t.http.Up), labels...)
	ch <- prometheus.MustNewConstMetric(probeHTTPStatus.desc, prometheus.GaugeValue, float64(t.http.Status), labels...)
	ch <- prometheus.MustNewConstMetric(probeTLSOk.desc, prometheus.GaugeValue, boolValue(t.http.TLSVerified), labels...)
	if t.ttfb != nil {
		ch <- prometheus.MustNewConstMetric(probeTTFB.desc, prometheus.GaugeValue, *t.ttfb, labels...)
	}
	if t.total != nil {
		ch <- prometheus.MustNewConstMetric(probeTotal.desc, prometheus.GaugeValue, *t.total, labels...)
	}
	ch <- prometheus.MustNewConstMetric(targetTimestamp.desc, prometheus.GaugeValue, unixSeconds(t.at), labels...)

	if t.dnsSkipped {
		return
	}
	ch <- prometheus.MustNewConstMetric(dnsUp.desc, prometheus.GaugeValue, boolValue(t.dns.Up), labels...)
	if t.dnsLookup != nil {
		ch <- prometheus.MustNewConstMetric(dnsLookup.desc, prometheus.GaugeValue, *t.dnsLookup, labels...)
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

// sortedKeys keeps a scrape stable.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
