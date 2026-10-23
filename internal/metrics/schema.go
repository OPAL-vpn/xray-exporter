package metrics

import "github.com/prometheus/client_golang/prometheus"

// namespace prefixes every metric the exporter publishes.
const namespace = "xray"

// Transport values for the transport label. Only configured transports exist.
const (
	TransportDoH = "doh"
	TransportDoT = "dot"
)

// metricDef is one metric family: its name, its descriptor and, for a counter,
// the counter that renders it. A definition is built once and reused by every
// scrape, so a scrape allocates values, not schemas.
type metricDef struct {
	name    string
	desc    *prometheus.Desc
	counter prometheus.Counter
}

func newMetricDef(name, help string, labels ...string) *metricDef {
	return &metricDef{
		name: name,
		desc: prometheus.NewDesc(namespace+"_"+name, help, labels, nil),
	}
}

// newCounterDef builds a counter family. The counter renders itself, so
// snapshotCollector does not describe or collect it.
func newCounterDef(name, help string) *metricDef {
	counter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      name,
		Help:      help,
	})
	return &metricDef{name: name, desc: counter.Desc(), counter: counter}
}

var (
	probeUp = newMetricDef("probe_up",
		"Whether the HTTPS probe got a response from the target.",
		"config", "target")
	probeHTTPStatus = newMetricDef("probe_http_status",
		"HTTP status code of the last probe, 0 when no response arrived.",
		"config", "target")
	probeTLSOk = newMetricDef("probe_tls_ok",
		"Whether the target certificate chained to a trusted root and matched the host.",
		"config", "target")
	probeTTFB = newMetricDef("probe_ttfb_seconds",
		"Time to the first response byte of the last successful probe.",
		"config", "target")
	probeTotal = newMetricDef("probe_total_seconds",
		"Full request time of the last successful probe.",
		"config", "target")
	targetTimestamp = newMetricDef("target_timestamp_seconds",
		"When the target's DNS and HTTPS pair last completed.",
		"config", "target")
	dnsUp = newMetricDef("dns_up",
		"Whether the tunnel DNS lookup got an answer.",
		"config", "target")
	dnsLookup = newMetricDef("dns_lookup_seconds",
		"Round trip of the last successful tunnel DNS lookup.",
		"config", "target")
	resolverUp = newMetricDef("resolver_up",
		"Whether the DoH or DoT resolver answered through the tunnel.",
		"config", "resolver", "transport")
	resolverLookup = newMetricDef("resolver_lookup_seconds",
		"Round trip of the last successful resolver query.",
		"config", "resolver", "transport")
	resolverTimestamp = newMetricDef("resolver_timestamp_seconds",
		"When the resolver was last attempted.",
		"config", "resolver", "transport")
	egressVerified = newMetricDef("egress_verified",
		"Whether the config's traffic leaves through its tunnel.",
		"config")
	egressIPInfo = newMetricDef("egress_ip_info",
		"The egress address the tunnel was observed to use.",
		"config", "ip")
	egressTimestamp = newMetricDef("egress_timestamp_seconds",
		"When egress was last verified.",
		"config")
	configRunning = newMetricDef("config_running",
		"Whether the config's xray instance is up.",
		"config")
	configInfo = newMetricDef("config_info",
		"xray version of the running instance.",
		"config", "xray_version")
	cycleTimestamp = newMetricDef("cycle_timestamp_seconds",
		"When the last measurement cycle finished.",
		"config")
	cycleDuration = newMetricDef("cycle_duration_seconds",
		"Wall time of the last measurement cycle.",
		"config")
	cycleTargets = newMetricDef("cycle_targets_total",
		"Targets attempted by the last measurement cycle.",
		"config")
	reloadTotal = newCounterDef("exporter_config_reload_total",
		"Configuration reloads applied.")
	reloadErrors = newCounterDef("exporter_config_reload_errors_total",
		"Configuration reloads that failed.")
	buildInfo = newMetricDef("build_info",
		"Build version and Go version of the exporter.",
		"version", "go_version")
)

// allMetrics is every metric family this package emits, in AllNames order.
var allMetrics = []*metricDef{
	probeUp, probeHTTPStatus, probeTLSOk, probeTTFB, probeTotal,
	targetTimestamp, dnsUp, dnsLookup,
	resolverUp, resolverLookup, resolverTimestamp,
	egressVerified, egressIPInfo, egressTimestamp,
	configRunning, configInfo,
	cycleTimestamp, cycleDuration, cycleTargets,
	reloadTotal, reloadErrors,
	buildInfo,
}

// AllNames returns every metric name the package can emit, so the dashboard
// can be checked against the exporter instead of against memory.
func AllNames() []string {
	names := make([]string, len(allMetrics))
	for i, metric := range allMetrics {
		names[i] = namespace + "_" + metric.name
	}
	return names
}
