package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/reachability"
)

func reachabilitySpec() *ReachabilitySpec {
	return &ReachabilitySpec{
		Controls: []config.Control{
			{Kind: config.ControlHTTPS, Name: "ya", Class: config.ClassDomestic, Target: "ya.ru"},
			{Kind: config.ControlTCP, Name: "cf", Class: config.ClassForeign, Target: "1.1.1.1:443"},
		},
	}
}

func TestReachabilityOffExportsNothing(t *testing.T) {
	snap := New()
	snap.Seed("nl1", []string{"a.test"}, nil, seedTime)
	for _, s := range gatherSamples(t, snap) {
		for _, prefix := range []string{"xray_network_", "xray_control_", "xray_entry_"} {
			if len(s.name) >= len(prefix) && s.name[:len(prefix)] == prefix {
				t.Errorf("%s emitted without a reachability block", s.name)
			}
		}
	}
}

func TestControlsSeededDown(t *testing.T) {
	snap := New()
	snap.SetReachability(reachabilitySpec())
	samples := gatherSamples(t, snap)

	if n := len(samplesNamed(samples, "xray_network_info")); n != 0 {
		t.Errorf("network_info has %d series before a lookup, want 0", n)
	}
	for _, c := range []map[string]string{
		labels("kind", "https", "name", "ya", "class", "domestic"),
		labels("kind", "tcp", "name", "cf", "class", "foreign"),
	} {
		if got := mustSample(t, samples, "xray_control_up", c); got != 0 {
			t.Errorf("control_up%v = %v, want 0", c, got)
		}
		mustNoSample(t, samples, "xray_control_seconds", c)
	}
}

func TestNetworkInfoKeepsLastIdentity(t *testing.T) {
	snap := New()
	snap.SetReachability(reachabilitySpec())
	snap.SetIdentity(reachability.Identity{ASName: "PJSC MegaFon", Country: "RU"})
	snap.SetIdentity(reachability.Identity{ASName: "MTS PJSC", Country: "RU"})
	infos := samplesNamed(gatherSamples(t, snap), "xray_network_info")

	if len(infos) != 1 || infos[0].value != 1 ||
		infos[0].labels["as_name"] != "MTS PJSC" || infos[0].labels["country"] != "RU" {
		t.Errorf("network_info = %+v, want one series for the latest identity", infos)
	}
}

func TestControlKeepsLastLatencyOnFailure(t *testing.T) {
	snap := New()
	snap.SetReachability(reachabilitySpec())
	key := labels("kind", "tcp", "name", "cf", "class", "foreign")

	snap.SetControl(config.ControlTCP, "cf", true, 30*time.Millisecond, nil)
	snap.SetControl(config.ControlTCP, "cf", false, 0, errors.New("timeout"))
	samples := gatherSamples(t, snap)

	if got := mustSample(t, samples, "xray_control_up", key); got != 0 {
		t.Errorf("control_up = %v, want 0", got)
	}
	if got := mustSample(t, samples, "xray_control_seconds", key); got != durationSeconds(30*time.Millisecond) {
		t.Errorf("control_seconds = %v, want the last success", got)
	}
}

func TestReseedDropsRemovedControls(t *testing.T) {
	snap := New()
	snap.SetReachability(reachabilitySpec())
	spec := reachabilitySpec()
	spec.Controls = spec.Controls[:1]
	snap.SetReachability(spec)
	samples := gatherSamples(t, snap)

	mustNoSample(t, samples, "xray_control_up", labels("kind", "tcp", "name", "cf", "class", "foreign"))
	mustSample(t, samples, "xray_control_up", labels("kind", "https", "name", "ya", "class", "domestic"))

	snap.SetReachability(nil)
	if n := len(samplesNamed(gatherSamples(t, snap), "xray_control_up")); n != 0 {
		t.Errorf("control_up has %d series after the block was removed, want 0", n)
	}
}

func TestEntryTCPOnlyWhenEnabled(t *testing.T) {
	spec := reachabilitySpec()
	snap := New()
	snap.Seed("nl1", []string{"a.test"}, nil, seedTime)
	snap.SetReachability(spec)
	mustNoSample(t, gatherSamples(t, snap), "xray_entry_tcp_up", labels("config", "nl1"))

	spec.EntryTCP = true
	snap.SetReachability(spec)
	samples := gatherSamples(t, snap)
	if got := mustSample(t, samples, "xray_entry_tcp_up", labels("config", "nl1")); got != 0 {
		t.Errorf("entry_tcp_up = %v before a probe, want 0", got)
	}
	mustNoSample(t, samples, "xray_entry_tcp_seconds", labels("config", "nl1"))

	snap.SetEntry("nl1", true, 20*time.Millisecond, nil)
	samples = gatherSamples(t, snap)
	if got := mustSample(t, samples, "xray_entry_tcp_up", labels("config", "nl1")); got != 1 {
		t.Errorf("entry_tcp_up = %v, want 1", got)
	}
	if got := mustSample(t, samples, "xray_entry_tcp_seconds", labels("config", "nl1")); got != durationSeconds(20*time.Millisecond) {
		t.Errorf("entry_tcp_seconds = %v, want 0.02", got)
	}

	snap.Drop("nl1")
	mustNoSample(t, gatherSamples(t, snap), "xray_entry_tcp_up", labels("config", "nl1"))
}
