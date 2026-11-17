package metrics

import (
	"sort"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

// Export is the snapshot as plain values, sorted for a stable report.
type Export struct {
	Configs []ConfigExport
	// Reachability is nil unless the config declares a reachability block.
	Reachability *ReachabilityExport
}

type ConfigExport struct {
	Name        string
	Running     bool
	XrayVersion string
	Cycle       CycleExport
	Targets     []TargetExport
	Resolvers   []ResolverExport
	UDPs        []UDPExport
	TCPs        []TCPExport
	// Entry is nil until a direct connect ran or when entry_tcp is off.
	Entry *EntryExport
}

type CycleExport struct {
	Done     bool
	At       time.Time
	Duration time.Duration
	Targets  int
}

// TargetExport keeps raw results plus whether each measurement ran. A seeded
// target has neither; an IP-literal target has no DNS.
type TargetExport struct {
	Target  string
	HasHTTP bool
	HTTP    probe.HTTPResult
	HasDNS  bool
	DNS     probe.DNSResult
}

type ResolverExport struct {
	Name       string
	Transports []TransportExport
}

type TransportExport struct {
	Transport string
	Result    probe.ResolverResult
}

// UDPExport keeps the raw burst result for one target and load pass.
type UDPExport struct {
	Target string
	Load   string
	Result probe.UDPResult
}

// TCPExport keeps the raw tunnel connect result for one target.
type TCPExport struct {
	Target string
	Result probe.TunnelTCPResult
}

// Export copies the snapshot under a read lock.
func (s *Snapshot) Export() Export {
	s.mu.RLock()
	defer s.mu.RUnlock()

	exp := Export{Configs: make([]ConfigExport, 0, len(s.configs)), Reachability: exportReachability(s.reachability)}
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
		Cycle: CycleExport{
			Done:     cfg.cycle.done,
			At:       cfg.cycle.at,
			Duration: cfg.cycle.dur,
			Targets:  cfg.cycle.targets,
		},
		Targets:   make([]TargetExport, 0, len(cfg.targets)),
		Resolvers: make([]ResolverExport, 0),
		UDPs:      make([]UDPExport, 0, len(cfg.udps)),
		TCPs:      make([]TCPExport, 0, len(cfg.tcps)),
	}

	if cfg.entry != nil {
		row.Entry = &EntryExport{Up: cfg.entry.up}
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

	for _, key := range sortedUDPKeys(cfg.udps) {
		row.UDPs = append(row.UDPs, UDPExport{Target: key.target, Load: key.load, Result: cfg.udps[key].result})
	}
	for _, target := range sortedKeys(cfg.tcps) {
		row.TCPs = append(row.TCPs, TCPExport{Target: target, Result: cfg.tcps[target].result})
	}

	return row
}
