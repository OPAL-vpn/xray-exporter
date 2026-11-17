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

	entryTCP := s.reachability != nil && s.reachability.spec.EntryTCP
	for _, name := range sortedKeys(s.configs) {
		collectConfig(ch, name, s.configs[name])
		if entryTCP {
			collectEntry(ch, name, s.configs[name].entry)
		}
	}
	if s.reachability != nil {
		collectReachability(ch, s.reachability)
	}

	if s.hasFetch {
		ch <- prometheus.MustNewConstMetric(fetchTimestamp.desc, prometheus.GaugeValue, unixSeconds(s.fetchAt))
	}
	if s.source != "" {
		ch <- prometheus.MustNewConstMetric(sourceInfo.desc, prometheus.GaugeValue, 1, s.source)
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

	for _, key := range sortedUDPKeys(cfg.udps) {
		collectUDP(ch, name, key, cfg.udps[key])
	}
	for _, target := range sortedKeys(cfg.tcps) {
		collectTCP(ch, name, target, cfg.tcps[target])
	}
	if cfg.udpLoad != nil {
		ch <- prometheus.MustNewConstMetric(udpLoadUp.desc, prometheus.GaugeValue, boolValue(cfg.udpLoad.up), name)
	}
}

func collectUDP(ch chan<- prometheus.Metric, config string, key udpKey, state *udpState) {
	labels := []string{config, key.target, key.load}

	ch <- prometheus.MustNewConstMetric(udpUp.desc, prometheus.GaugeValue, boolValue(state.result.Up), labels...)
	if state.loss != nil {
		ch <- prometheus.MustNewConstMetric(udpLossRatio.desc, prometheus.GaugeValue, *state.loss, labels...)
	}
	if state.rtt != nil {
		ch <- prometheus.MustNewConstMetric(udpRTT.desc, prometheus.GaugeValue, *state.rtt, labels...)
	}
	if state.rttMax != nil {
		ch <- prometheus.MustNewConstMetric(udpRTTMax.desc, prometheus.GaugeValue, *state.rttMax, labels...)
	}
	if state.jitter != nil {
		ch <- prometheus.MustNewConstMetric(udpJitter.desc, prometheus.GaugeValue, *state.jitter, labels...)
	}
	ch <- prometheus.MustNewConstMetric(udpTimestamp.desc, prometheus.GaugeValue, unixSeconds(state.at), labels...)
}

func collectTCP(ch chan<- prometheus.Metric, config, target string, state *tcpState) {
	labels := []string{config, target}

	ch <- prometheus.MustNewConstMetric(tcpUp.desc, prometheus.GaugeValue, boolValue(state.result.Up), labels...)
	if state.connect != nil {
		ch <- prometheus.MustNewConstMetric(tcpSeconds.desc, prometheus.GaugeValue, *state.connect, labels...)
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

// sortedUDPKeys keeps a scrape stable: target name, then load.
func sortedUDPKeys(m map[udpKey]*udpState) []udpKey {
	keys := make([]udpKey, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].target != keys[j].target {
			return keys[i].target < keys[j].target
		}
		return keys[i].load < keys[j].load
	})
	return keys
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
