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
	cycle     cycleState
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
	}
}
