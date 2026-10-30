package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/egress"
)

// This file holds everything the optional reachability block exports. That
// includes xray_network_info: it is the label set every other reachability
// series is read against, so it lives with them.

// ReachabilitySpec declares the direct probes. Setting one switches the
// reachability families on; the zero Snapshot exports none of them.
type ReachabilitySpec struct {
	Controls []config.Control
	EntryTCP bool
}

// controlState is one declared control; its map key is kind/name.
type controlState struct {
	kind, name, class string
	up                bool
	seconds           *float64
}

// entryState is one config's direct connect to its entry address.
type entryState struct {
	up      bool
	seconds *float64
}

type reachabilityState struct {
	spec     ReachabilitySpec
	id       egress.Identity
	idAt     time.Time
	hasID    bool
	controls map[string]*controlState
}

func controlKey(kind, name string) string { return kind + "/" + name }

// SetReachability declares the direct probes, keeping the results of any
// control that is still declared. nil switches the feature off.
func (s *Snapshot) SetReachability(spec *ReachabilitySpec) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if spec == nil {
		s.reachability = nil
		return
	}
	if s.reachability == nil {
		s.reachability = &reachabilityState{controls: make(map[string]*controlState)}
	}
	rs := s.reachability
	rs.spec = *spec

	keep := make(map[string]struct{}, len(spec.Controls))
	for _, c := range spec.Controls {
		key := controlKey(c.Kind, c.Name)
		keep[key] = struct{}{}
		if state, ok := rs.controls[key]; ok {
			state.class = c.Class
			continue
		}
		rs.controls[key] = &controlState{kind: c.Kind, name: c.Name, class: c.Class}
	}
	for key := range rs.controls {
		if _, ok := keep[key]; !ok {
			delete(rs.controls, key)
		}
	}
}

// SetIdentity stores the egress network and when it was read, and returns the
// previous one, if any. A failed lookup stores nothing, so the last good answer
// and its timestamp stay.
func (s *Snapshot) SetIdentity(id egress.Identity, at time.Time) (previous egress.Identity, had bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rs := s.reachability
	if rs == nil {
		return egress.Identity{}, false
	}
	previous, had = rs.id, rs.hasID
	rs.id, rs.idAt, rs.hasID = id, at, true
	return previous, had
}

// SetControl records a direct probe, keeping the last successful latency when
// it fails. A control SetReachability did not declare is ignored.
func (s *Snapshot) SetControl(kind, name string, up bool, rtt time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.reachability == nil {
		return
	}
	state, ok := s.reachability.controls[controlKey(kind, name)]
	if !ok {
		return
	}
	state.up = up
	if up {
		setLatency(&state.seconds, rtt)
	}
}

// SetEntry records a config's direct connect to its entry address.
func (s *Snapshot) SetEntry(config string, up bool, rtt time.Duration) {
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
	if up {
		setLatency(&cfg.entry.seconds, rtt)
	}
}

func collectReachability(ch chan<- prometheus.Metric, rs *reachabilityState) {
	if rs.hasID {
		ch <- prometheus.MustNewConstMetric(networkInfo.desc, prometheus.GaugeValue, 1, rs.id.ASName, rs.id.Country)
		ch <- prometheus.MustNewConstMetric(networkTimestamp.desc, prometheus.GaugeValue, unixSeconds(rs.idAt))
	}
	for _, key := range sortedKeys(rs.controls) {
		state := rs.controls[key]
		labels := []string{state.kind, state.name, state.class}
		ch <- prometheus.MustNewConstMetric(controlUp.desc, prometheus.GaugeValue, boolValue(state.up), labels...)
		if state.seconds != nil {
			ch <- prometheus.MustNewConstMetric(controlSeconds.desc, prometheus.GaugeValue, *state.seconds, labels...)
		}
	}
}

// collectEntry emits a config's entry series; a config never probed reads 0.
func collectEntry(ch chan<- prometheus.Metric, name string, entry *entryState) {
	if entry == nil {
		entry = &entryState{}
	}
	ch <- prometheus.MustNewConstMetric(entryTCPUp.desc, prometheus.GaugeValue, boolValue(entry.up), name)
	if entry.seconds != nil {
		ch <- prometheus.MustNewConstMetric(entryTCPSeconds.desc, prometheus.GaugeValue, *entry.seconds, name)
	}
}

// ReachabilityExport is the direct probes as plain values, for the one-shot
// report.
type ReachabilityExport struct {
	HasIdentity bool
	Identity    egress.Identity
	Controls    []ControlExport
}

type ControlExport struct {
	Kind, Name, Class string
	Up                bool
}

func exportReachability(rs *reachabilityState) *ReachabilityExport {
	if rs == nil {
		return nil
	}
	out := &ReachabilityExport{HasIdentity: rs.hasID, Identity: rs.id}
	for _, key := range sortedKeys(rs.controls) {
		state := rs.controls[key]
		out.Controls = append(out.Controls, ControlExport{Kind: state.kind, Name: state.name, Class: state.class, Up: state.up})
	}
	return out
}

// EntryExport is a config's direct connect result.
type EntryExport struct {
	Up bool
}
