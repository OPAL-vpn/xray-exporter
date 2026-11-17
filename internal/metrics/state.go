package metrics

import (
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

type configState struct {
	running   bool
	version   string
	targets   map[string]*targetState
	resolvers map[resolverKey]*resolverState
	udps      map[udpKey]*udpState
	tcps      map[string]*tcpState
	udpLoad   *udpLoadState
	cycle     cycleState
	entry     *entryState
}

// targetState keeps raw results for the report. The latency pointers stay nil
// until a successful measurement, so the gauge is absent rather than zero.
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

type resolverKey struct {
	name      string
	transport string
}

type resolverState struct {
	at     time.Time
	result probe.ResolverResult
	lookup *float64
}

// udpKey identifies one UDP series: the same target is measured idle and,
// when under-load is configured, loaded.
type udpKey struct {
	target string
	load   string
}

// udpState keeps the raw burst result for the report. The gauge pointers stay
// nil until a successful probe, so the gauges are absent rather than zero.
type udpState struct {
	at     time.Time
	result probe.UDPResult
	loss   *float64
	rtt    *float64
	rttMax *float64
	jitter *float64
}

// tcpState keeps the raw connect result for the report; connect stays nil
// until a successful connect.
type tcpState struct {
	result  probe.TunnelTCPResult
	connect *float64
}

// udpLoadState is nil until the first loaded pass reports.
type udpLoadState struct {
	at time.Time
	up bool
}

type cycleState struct {
	done    bool
	at      time.Time
	dur     time.Duration
	targets int
}

func newConfigState() *configState {
	return &configState{
		targets:   make(map[string]*targetState),
		resolvers: make(map[resolverKey]*resolverState),
		udps:      make(map[udpKey]*udpState),
		tcps:      make(map[string]*tcpState),
	}
}
