package metrics

import (
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

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
