package metrics

import "github.com/prometheus/client_golang/prometheus"

const namespace = "xray"

// Transport values for the transport label.
const (
	TransportDoH = "doh"
	TransportDoT = "dot"
)

// metricDef is one metric family, built once and reused across scrapes.
type metricDef struct {
	name    string
	help    string
	desc    *prometheus.Desc
	counter bool
}

func newMetricDef(name, help string, labels ...string) *metricDef {
	return &metricDef{
		name: name,
		help: help,
		desc: prometheus.NewDesc(namespace+"_"+name, help, labels, nil),
	}
}

// newCounterDef defines a counter family; each snapshot builds its own counter.
func newCounterDef(name, help string) *metricDef {
	return &metricDef{name: name, help: help, counter: true}
}

func (m *metricDef) newCounter() prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      m.name,
		Help:      m.help,
	})
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
	fetchTotal = newCounterDef("exporter_config_fetch_total",
		"Config source fetch attempts.")
	fetchErrors = newCounterDef("exporter_config_fetch_errors_total",
		"Config source fetches that returned no usable set.")
	fetchTimestamp = newMetricDef("exporter_config_fetch_timestamp_seconds",
		"When the last config source fetch succeeded.")
	sourceInfo = newMetricDef("exporter_config_source_info",
		"The configured config source type.",
		"type")
	buildInfo = newMetricDef("build_info",
		"Build version and Go version of the exporter.",
		"version", "go_version")

	networkInfo = newMetricDef("network_info",
		"The network the host egresses from, as ipinfo.io reports it.",
		"as_name", "country")
	networkTimestamp = newMetricDef("network_timestamp_seconds",
		"When the egress network was last looked up successfully.")
	controlUp = newMetricDef("control_up",
		"Whether a direct control probe succeeded, outside any tunnel.",
		"kind", "name", "class")
	controlSeconds = newMetricDef("control_seconds",
		"Round trip of the last successful control probe.",
		"kind", "name", "class")
	entryTCPUp = newMetricDef("entry_tcp_up",
		"Whether a direct TCP connect to the config's entry address succeeded.",
		"config")
	entryTCPSeconds = newMetricDef("entry_tcp_seconds",
		"Connect time of the last successful direct connect to the entry address.",
		"config")
)

// allMetrics is every family, in AllNames order.
var allMetrics = []*metricDef{
	probeUp, probeHTTPStatus, probeTLSOk, probeTTFB, probeTotal,
	targetTimestamp, dnsUp, dnsLookup,
	resolverUp, resolverLookup, resolverTimestamp,
	configRunning, configInfo,
	cycleTimestamp, cycleDuration, cycleTargets,
	reloadTotal, reloadErrors,
	fetchTotal, fetchErrors, fetchTimestamp, sourceInfo,
	buildInfo,
	networkInfo, networkTimestamp, controlUp, controlSeconds,
	entryTCPUp, entryTCPSeconds,
}

// AllNames lists every metric name.
func AllNames() []string {
	names := make([]string, len(allMetrics))
	for i, metric := range allMetrics {
		names[i] = namespace + "_" + metric.name
	}
	return names
}
