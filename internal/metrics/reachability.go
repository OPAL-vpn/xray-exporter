package metrics

import (
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/reachability"
)

// ReachabilitySpec declares the direct probes. Setting one switches the reachability
// families on; the zero Snapshot exports none of them.
type ReachabilitySpec struct {
	Controls []config.Control
	EntryTCP bool
}

type controlKey struct {
	kind string
	name string
}

type controlState struct {
	class  string
	up     bool
	err    error
	lookup *float64
}

// entryState is one config's direct connect to its entry address.
type entryState struct {
	up     bool
	err    error
	lookup *float64
}

type reachabilityState struct {
	spec     ReachabilitySpec
	id       reachability.Identity
	hasID    bool
	controls map[controlKey]*controlState
}

// SetReachability declares the direct probes, keeping the results of any control
// that is still declared. nil switches the feature off.
func (s *Snapshot) SetReachability(spec *ReachabilitySpec) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if spec == nil {
		s.reachability = nil
		return
	}
	if s.reachability == nil {
		s.reachability = &reachabilityState{controls: make(map[controlKey]*controlState)}
	}
	v := s.reachability
	v.spec = *spec

	keep := make(map[controlKey]struct{}, len(spec.Controls))
	for _, c := range spec.Controls {
		key := controlKey{kind: c.Kind, name: c.Name}
		keep[key] = struct{}{}
		if state, ok := v.controls[key]; ok {
			state.class = c.Class
			continue
		}
		v.controls[key] = &controlState{class: c.Class}
	}
	for key := range v.controls {
		if _, ok := keep[key]; !ok {
			delete(v.controls, key)
		}
	}
}

// SetIdentity stores the egress network. A failed lookup stores nothing, so the
// last good answer stays.
func (s *Snapshot) SetIdentity(id reachability.Identity) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.reachability == nil {
		return
	}
	s.reachability.id = id
	s.reachability.hasID = true
}

// SetControl records a direct probe, keeping the last successful latency when
// it fails. A control SetReachability did not declare is ignored.
func (s *Snapshot) SetControl(kind, name string, up bool, rtt time.Duration, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.reachability == nil {
		return
	}
	state, ok := s.reachability.controls[controlKey{kind: kind, name: name}]
	if !ok {
		return
	}
	state.up = up
	state.err = err
	if up {
		setLatency(&state.lookup, rtt)
	}
}

// SetEntry records a config's direct connect to its entry address.
func (s *Snapshot) SetEntry(config string, up bool, rtt time.Duration, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, ok := s.configs[config]
	if !ok {
		return
	}
	if cfg.entry == nil {
		cfg.entry = &entryState{}
	}
	cfg.entry.up = up
	cfg.entry.err = err
	if up {
		setLatency(&cfg.entry.lookup, rtt)
	}
}

func sortedControlKeys(controls map[controlKey]*controlState) []controlKey {
	keys := make([]controlKey, 0, len(controls))
	for key := range controls {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].kind != keys[j].kind {
			return keys[i].kind < keys[j].kind
		}
		return keys[i].name < keys[j].name
	})
	return keys
}

func collectReachability(ch chan<- prometheus.Metric, v *reachabilityState) {
	if v.hasID {
		ch <- prometheus.MustNewConstMetric(networkInfo.desc, prometheus.GaugeValue, 1, v.id.ASName, v.id.Country)
	}
	for _, key := range sortedControlKeys(v.controls) {
		state := v.controls[key]
		labels := []string{key.kind, key.name, state.class}
		ch <- prometheus.MustNewConstMetric(controlUp.desc, prometheus.GaugeValue, boolValue(state.up), labels...)
		if state.lookup != nil {
			ch <- prometheus.MustNewConstMetric(controlSeconds.desc, prometheus.GaugeValue, *state.lookup, labels...)
		}
	}
}

func collectEntry(ch chan<- prometheus.Metric, name string, entry *entryState) {
	ch <- prometheus.MustNewConstMetric(entryTCPUp.desc, prometheus.GaugeValue, boolValue(entry != nil && entry.up), name)
	if entry != nil && entry.lookup != nil {
		ch <- prometheus.MustNewConstMetric(entryTCPSeconds.desc, prometheus.GaugeValue, *entry.lookup, name)
	}
}

// ReachabilityExport is the direct probes as plain values, for the one-shot report.
type ReachabilityExport struct {
	HasIdentity bool
	Identity    reachability.Identity
	Controls    []ControlExport
}

type ControlExport struct {
	Kind, Name, Class string
	Up                bool
}

func exportReachability(v *reachabilityState) *ReachabilityExport {
	if v == nil {
		return nil
	}
	out := &ReachabilityExport{HasIdentity: v.hasID, Identity: v.id}
	for _, key := range sortedControlKeys(v.controls) {
		state := v.controls[key]
		out.Controls = append(out.Controls, ControlExport{Kind: key.kind, Name: key.name, Class: state.class, Up: state.up})
	}
	return out
}

// EntryExport is a config's direct connect result.
type EntryExport struct {
	Up  bool
	Err error
}
