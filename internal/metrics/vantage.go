package metrics

import (
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/vantage"
)

// VantageSpec declares the direct probes. Setting one switches the vantage
// families on; the zero Snapshot exports none of them.
type VantageSpec struct {
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

type vantageState struct {
	spec     VantageSpec
	id       vantage.Identity
	hasID    bool
	controls map[controlKey]*controlState
}

// SetVantage declares the direct probes, keeping the results of any control
// that is still declared. nil switches the feature off.
func (s *Snapshot) SetVantage(spec *VantageSpec) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if spec == nil {
		s.vantage = nil
		return
	}
	if s.vantage == nil {
		s.vantage = &vantageState{controls: make(map[controlKey]*controlState)}
	}
	v := s.vantage
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
func (s *Snapshot) SetIdentity(id vantage.Identity) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.vantage == nil {
		return
	}
	s.vantage.id = id
	s.vantage.hasID = true
}

// SetControl records a direct probe, keeping the last successful latency when
// it fails. A control SetVantage did not declare is ignored.
func (s *Snapshot) SetControl(kind, name string, up bool, rtt time.Duration, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.vantage == nil {
		return
	}
	state, ok := s.vantage.controls[controlKey{kind: kind, name: name}]
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

func collectVantage(ch chan<- prometheus.Metric, v *vantageState) {
	if v.hasID {
		ch <- prometheus.MustNewConstMetric(vantageInfo.desc, prometheus.GaugeValue, 1, v.id.ASName, v.id.Country)
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

// VantageExport is the direct probes as plain values, for the one-shot report.
type VantageExport struct {
	HasIdentity bool
	Identity    vantage.Identity
	Controls    []ControlExport
}

type ControlExport struct {
	Kind, Name, Class string
	Up                bool
}

func exportVantage(v *vantageState) *VantageExport {
	if v == nil {
		return nil
	}
	out := &VantageExport{HasIdentity: v.hasID, Identity: v.id}
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
