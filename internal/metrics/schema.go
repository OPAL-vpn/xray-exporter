package metrics

import "github.com/prometheus/client_golang/prometheus"

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
		"When the target's DNS and HTTPS pair last completed.",
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
