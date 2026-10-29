package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/egress"
)

func testSpec() *ReachabilitySpec {
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
	snap.SetConfig("nl1", true, "v1")
	samples := gatherSamples(t, snap)
	if len(samplesNamed(samples, "xray_config_running")) == 0 {
		t.Fatal("the snapshot emitted nothing, so the check below proves nothing")
	}
	for _, s := range samples {
		for _, prefix := range []string{"xray_network_", "xray_control_", "xray_entry_"} {
			if strings.HasPrefix(s.name, prefix) {
				t.Errorf("%s emitted without a reachability block", s.name)
			}
		}
	}
}

func TestControlsSeededDown(t *testing.T) {
	snap := New()
	snap.SetReachability(testSpec())
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
	snap.SetReachability(testSpec())
	if _, had := snap.SetIdentity(egress.Identity{ASName: "PJSC MegaFon", Country: "RU"}); had {
		t.Error("the first identity reported a previous one")
	}
	if prev, had := snap.SetIdentity(egress.Identity{ASName: "MTS PJSC", Country: "RU"}); !had || prev.ASName != "PJSC MegaFon" {
		t.Errorf("previous = %+v, %v; want the first identity", prev, had)
	}
	infos := samplesNamed(gatherSamples(t, snap), "xray_network_info")

	if len(infos) != 1 || infos[0].value != 1 ||
		infos[0].labels["as_name"] != "MTS PJSC" || infos[0].labels["country"] != "RU" {
		t.Errorf("network_info = %+v, want one series for the latest identity", infos)
	}
}

func TestControlKeepsLastLatencyOnFailure(t *testing.T) {
	snap := New()
	snap.SetReachability(testSpec())
	key := labels("kind", "tcp", "name", "cf", "class", "foreign")

	snap.SetControl(config.ControlTCP, "cf", true, 30*time.Millisecond)
	snap.SetControl(config.ControlTCP, "cf", false, 0)
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
	snap.SetReachability(testSpec())
	spec := testSpec()
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
	spec := testSpec()
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

	snap.SetEntry("nl1", true, 20*time.Millisecond)
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

func TestReDeclareKeepsControlResults(t *testing.T) {
	snap := New()
	snap.SetReachability(testSpec())
	snap.SetControl(config.ControlTCP, "cf", true, 30*time.Millisecond)

	snap.SetReachability(testSpec())
	key := labels("kind", "tcp", "name", "cf", "class", "foreign")
	samples := gatherSamples(t, snap)
	if got := mustSample(t, samples, "xray_control_up", key); got != 1 {
		t.Errorf("control_up = %v after a reload that kept the control, want 1", got)
	}
	mustSample(t, samples, "xray_control_seconds", key)
}
