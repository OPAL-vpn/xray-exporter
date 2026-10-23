package metrics

import (
	"sort"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

// Export is the snapshot as plain values, for the one-shot report. Configs,
// targets and resolvers are sorted by name so a report is stable.
type Export struct {
	Configs []ConfigExport
}

// ConfigExport is one config's state.
type ConfigExport struct {
	Name        string
	Running     bool
	XrayVersion string
	Egress      EgressExport
	Cycle       CycleExport
	Targets     []TargetExport
	Resolvers   []ResolverExport
}

// EgressExport is the last egress verification. IP is empty and Verified false
// when the check has not run or could not fetch an address.
type EgressExport struct {
	IP       string
	Verified bool
}

// CycleExport is the last completed measurement cycle.
type CycleExport struct {
	Done     bool
	At       time.Time
	Duration time.Duration
	Targets  int
}

// TargetExport is one (config, target) row. HasHTTP and HasDNS report whether
// that measurement ever ran; a target that has only been seeded carries the
// zero result with the flag false, and a target skipped as an address literal
// never has DNS.
type TargetExport struct {
	Target  string
	HasHTTP bool
	HTTP    probe.HTTPResult
	HasDNS  bool
	DNS     probe.DNSResult
}

// ResolverExport is one resolver's configured transports.
type ResolverExport struct {
	Name       string
	Transports []TransportExport
}

// TransportExport is the last result for one resolver transport.
type TransportExport struct {
	Transport string
	Result    probe.ResolverResult
}

// Export copies the snapshot out from under the lock.
func (s *Snapshot) Export() Export {
	s.mu.RLock()
	defer s.mu.RUnlock()

	exp := Export{Configs: make([]ConfigExport, 0, len(s.configs))}
	for _, name := range sortedKeys(s.configs) {
		exp.Configs = append(exp.Configs, exportConfig(name, s.configs[name]))
	}
	return exp
}

func exportConfig(name string, cfg *configState) ConfigExport {
	row := ConfigExport{
		Name:        name,
		Running:     cfg.running,
		XrayVersion: cfg.version,
		Egress:      EgressExport{IP: cfg.egress.ip, Verified: cfg.egress.verified},
		Cycle: CycleExport{
			Done:     cfg.cycle.done,
			At:       cfg.cycle.at,
			Duration: cfg.cycle.dur,
			Targets:  cfg.cycle.targets,
		},
		Targets:   make([]TargetExport, 0, len(cfg.targets)),
		Resolvers: make([]ResolverExport, 0),
	}

	for _, target := range sortedKeys(cfg.targets) {
		t := cfg.targets[target]
		row.Targets = append(row.Targets, TargetExport{
			Target:  target,
			HasHTTP: t.hasHTTP,
			HTTP:    t.http,
			HasDNS:  t.hasDNS && !t.dnsSkipped,
			DNS:     t.dns,
		})
	}

	resolvers := make(map[string][]TransportExport, len(cfg.resolvers))
	for key, state := range cfg.resolvers {
		resolvers[key.name] = append(resolvers[key.name], TransportExport{
			Transport: key.transport,
			Result:    state.result,
		})
	}
	for _, resolver := range sortedKeys(resolvers) {
		transports := resolvers[resolver]
		sort.Slice(transports, func(i, j int) bool { return transports[i].Transport < transports[j].Transport })
		row.Resolvers = append(row.Resolvers, ResolverExport{Name: resolver, Transports: transports})
	}

	return row
}
