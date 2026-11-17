package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

func stunTarget(name, addr string) config.UDPTarget {
	return config.UDPTarget{Name: name, Kind: "stun", Addr: addr}
}

func tcpTarget(name, addr string) config.TCPTarget {
	return config.TCPTarget{Name: name, Addr: addr}
}

func TestSeedUDPDeclaresSeries(t *testing.T) {
	snap := New()
	snap.SeedUDP("nl1", []config.UDPTarget{stunTarget("tg", "203.0.113.10:3478")}, []string{"idle", "loaded"}, seedTime)
	samples := gatherSamples(t, snap)

	for _, load := range []string{"idle", "loaded"} {
		lbls := labels("config", "nl1", "target", "tg", "load", load)
		if got := mustSample(t, samples, "xray_udp_up", lbls); got != 0 {
			t.Errorf("xray_udp_up %s = %v, want 0", load, got)
		}
		if got := mustSample(t, samples, "xray_udp_timestamp_seconds", lbls); got != timestampSeconds(seedTime) {
			t.Errorf("xray_udp_timestamp_seconds %s = %v, want %v", load, got, timestampSeconds(seedTime))
		}
	}

	// Latency gauges appear only after a success.
	for _, name := range []string{
		"xray_udp_loss_ratio",
		"xray_udp_rtt_seconds",
		"xray_udp_rtt_max_seconds",
		"xray_udp_jitter_seconds",
	} {
		if got := samplesNamed(samples, name); len(got) != 0 {
			t.Errorf("%s has %d series after SeedUDP, want none: %v", name, len(got), got)
		}
	}
}

func TestSeedUDPRemovesDroppedTargets(t *testing.T) {
	snap := New()
	snap.SeedUDP("nl1", []config.UDPTarget{
		stunTarget("a", "a.test:3478"),
		stunTarget("b", "b.test:3478"),
	}, []string{"idle", "loaded"}, seedTime)

	// Re-seed with b idle only: target a and every loaded pair disappear.
	snap.SeedUDP("nl1", []config.UDPTarget{stunTarget("b", "b.test:3478")}, []string{"idle"}, secondTime)
	samples := gatherSamples(t, snap)

	if got := samplesNamed(samples, "xray_udp_up"); len(got) != 1 {
		t.Fatalf("xray_udp_up has %d series after reseed, want 1: %v", len(got), got)
	}
	if got := mustSample(t, samples, "xray_udp_up", labels("config", "nl1", "target", "b", "load", "idle")); got != 0 {
		t.Errorf("xray_udp_up b/idle = %v, want the seeded 0", got)
	}
	for _, s := range samples {
		if s.labels["target"] == "a" {
			t.Errorf("%s%v survived removal from SeedUDP", s.name, s.labels)
		}
		if s.labels["load"] == "loaded" {
			t.Errorf("%s%v survived removal of the loaded pass", s.name, s.labels)
		}
	}
}

func TestSeedTCPRemovesDroppedTargets(t *testing.T) {
	snap := New()
	snap.SeedTCP("nl1", []config.TCPTarget{tcpTarget("dc4", "149.154.167.51:443")})
	snap.SetTCP("nl1", "dc4", probe.TunnelTCPResult{Up: true, Connect: 20 * time.Millisecond}, secondTime)

	snap.SeedTCP("nl1", []config.TCPTarget{tcpTarget("dc5", "149.154.167.50:443")})
	samples := gatherSamples(t, snap)

	for _, s := range samples {
		if s.labels["target"] == "dc4" {
			t.Errorf("%s%v survived removal from SeedTCP", s.name, s.labels)
		}
	}
	dc5 := labels("config", "nl1", "target", "dc5")
	if got := mustSample(t, samples, "xray_tcp_up", dc5); got != 0 {
		t.Errorf("xray_tcp_up dc5 = %v, want the seeded 0", got)
	}
	mustNoSample(t, samples, "xray_tcp_seconds", dc5)
}

func TestSetUDPFailureKeepsLastLatency(t *testing.T) {
	snap := New()
	snap.SeedUDP("nl1", []config.UDPTarget{stunTarget("tg", "203.0.113.10:3478")}, []string{"idle"}, seedTime)
	snap.SetUDP("nl1", "tg", "idle", probe.UDPResult{
		Up:      true,
		Sent:    4,
		Replies: 3,
		Median:  30 * time.Millisecond,
		Max:     40 * time.Millisecond,
		Jitter:  5 * time.Millisecond,
	}, secondTime)
	snap.SetUDP("nl1", "tg", "idle", probe.UDPResult{Sent: 4, Err: errors.New("timeout")}, thirdTime)

	samples := gatherSamples(t, snap)
	lbls := labels("config", "nl1", "target", "tg", "load", "idle")
	if got := mustSample(t, samples, "xray_udp_up", lbls); got != 0 {
		t.Errorf("xray_udp_up after failure = %v, want 0", got)
	}
	for name, want := range map[string]float64{
		"xray_udp_loss_ratio":      0.25,
		"xray_udp_rtt_seconds":     0.03,
		"xray_udp_rtt_max_seconds": 0.04,
		"xray_udp_jitter_seconds":  0.005,
	} {
		if got := mustSample(t, samples, name, lbls); got != want {
			t.Errorf("%s after failure = %v, want the last successful %v", name, got, want)
		}
	}
	if got := mustSample(t, samples, "xray_udp_timestamp_seconds", lbls); got != timestampSeconds(thirdTime) {
		t.Errorf("xray_udp_timestamp_seconds after failure = %v, want %v", got, timestampSeconds(thirdTime))
	}

	// A single reply is still a success: jitter is a meaningful zero and is
	// published, unlike a zero RTT which the shared guard would hide.
	snap.SetUDP("nl1", "tg", "idle", probe.UDPResult{Up: true, Sent: 1, Replies: 1, Median: 8 * time.Millisecond}, thirdTime)
	samples = gatherSamples(t, snap)
	if got := mustSample(t, samples, "xray_udp_jitter_seconds", lbls); got != 0 {
		t.Errorf("xray_udp_jitter_seconds after a single-reply success = %v, want 0", got)
	}
	if got := mustSample(t, samples, "xray_udp_rtt_seconds", lbls); got != 0.008 {
		t.Errorf("xray_udp_rtt_seconds after a single-reply success = %v, want 0.008", got)
	}
}

func TestSetUDPIgnoresUndeclared(t *testing.T) {
	snap := New()
	snap.SeedUDP("nl1", []config.UDPTarget{stunTarget("tg", "203.0.113.10:3478")}, []string{"idle"}, seedTime)

	success := probe.UDPResult{Up: true, Sent: 2, Replies: 2, Median: 10 * time.Millisecond, Max: 12 * time.Millisecond}
	snap.SetUDP("nl1", "other", "idle", success, secondTime) // undeclared target
	snap.SetUDP("nl1", "tg", "loaded", success, secondTime)  // undeclared load
	snap.SetUDP("de1", "tg", "idle", success, secondTime)    // undeclared config
	samples := gatherSamples(t, snap)

	if got := mustSample(t, samples, "xray_udp_up", labels("config", "nl1", "target", "tg", "load", "idle")); got != 0 {
		t.Errorf("xray_udp_up tg/idle = %v, want the seeded 0", got)
	}
	mustNoSample(t, samples, "xray_udp_up", labels("config", "nl1", "target", "other", "load", "idle"))
	mustNoSample(t, samples, "xray_udp_up", labels("config", "nl1", "target", "tg", "load", "loaded"))
	for _, s := range samples {
		if s.labels["config"] == "de1" {
			t.Errorf("%s%v created a series for an undeclared config", s.name, s.labels)
		}
	}
	for _, name := range []string{
		"xray_udp_loss_ratio",
		"xray_udp_rtt_seconds",
		"xray_udp_rtt_max_seconds",
		"xray_udp_jitter_seconds",
	} {
		if got := samplesNamed(samples, name); len(got) != 0 {
			t.Errorf("%s has %d series after ignored setters, want none: %v", name, len(got), got)
		}
	}
}

func TestSetUDPLoad(t *testing.T) {
	snap := New()
	snap.SeedUDP("nl1", []config.UDPTarget{stunTarget("tg", "203.0.113.10:3478")}, []string{"idle", "loaded"}, seedTime)

	lbls := labels("config", "nl1")
	mustNoSample(t, gatherSamples(t, snap), "xray_udp_load_up", lbls)

	snap.SetUDPLoad("nl1", true, secondTime)
	if got := mustSample(t, gatherSamples(t, snap), "xray_udp_load_up", lbls); got != 1 {
		t.Errorf("xray_udp_load_up after a live download = %v, want 1", got)
	}

	snap.SetUDPLoad("nl1", false, thirdTime)
	if got := mustSample(t, gatherSamples(t, snap), "xray_udp_load_up", lbls); got != 0 {
		t.Errorf("xray_udp_load_up after a dead download = %v, want 0", got)
	}

	// An undeclared config is a no-op, like every other setter.
	snap.SetUDPLoad("de1", true, thirdTime)
	for _, s := range gatherSamples(t, snap) {
		if s.labels["config"] == "de1" {
			t.Errorf("%s%v created a series for an undeclared config", s.name, s.labels)
		}
	}
}

func TestSeedUDPClearsLoadWhenUnderLoadRemoved(t *testing.T) {
	snap := New()
	snap.SeedUDP("nl1", []config.UDPTarget{stunTarget("tg", "203.0.113.10:3478")}, []string{"idle", "loaded"}, seedTime)
	snap.SetUDPLoad("nl1", true, secondTime)

	snap.SeedUDP("nl1", []config.UDPTarget{stunTarget("tg", "203.0.113.10:3478")}, []string{"idle"}, thirdTime)

	mustNoSample(t, gatherSamples(t, snap), "xray_udp_load_up", labels("config", "nl1"))
	if cfg := snap.Export().Configs[0]; cfg.LoadUp != nil {
		t.Errorf("ConfigExport.LoadUp = %v after under-load was removed, want nil", *cfg.LoadUp)
	}
}

func TestSetTCPKeepsLastConnect(t *testing.T) {
	snap := New()
	snap.SeedTCP("nl1", []config.TCPTarget{tcpTarget("dc4", "149.154.167.51:443")})

	lbls := labels("config", "nl1", "target", "dc4")
	if got := mustSample(t, gatherSamples(t, snap), "xray_tcp_up", lbls); got != 0 {
		t.Errorf("xray_tcp_up after SeedTCP = %v, want 0", got)
	}
	mustNoSample(t, gatherSamples(t, snap), "xray_tcp_seconds", lbls)

	snap.SetTCP("nl1", "dc4", probe.TunnelTCPResult{Up: true, Connect: 25 * time.Millisecond}, secondTime)
	samples := gatherSamples(t, snap)
	if got := mustSample(t, samples, "xray_tcp_up", lbls); got != 1 {
		t.Errorf("xray_tcp_up after success = %v, want 1", got)
	}
	if got := mustSample(t, samples, "xray_tcp_seconds", lbls); got != 0.025 {
		t.Errorf("xray_tcp_seconds after success = %v, want 0.025", got)
	}

	snap.SetTCP("nl1", "dc4", probe.TunnelTCPResult{Err: errors.New("refused")}, thirdTime)
	samples = gatherSamples(t, snap)
	if got := mustSample(t, samples, "xray_tcp_up", lbls); got != 0 {
		t.Errorf("xray_tcp_up after failure = %v, want 0", got)
	}
	if got := mustSample(t, samples, "xray_tcp_seconds", lbls); got != 0.025 {
		t.Errorf("xray_tcp_seconds after failure = %v, want the last successful 0.025", got)
	}

	// An undeclared target is a no-op.
	snap.SetTCP("nl1", "other", probe.TunnelTCPResult{Up: true, Connect: time.Millisecond}, thirdTime)
	mustNoSample(t, gatherSamples(t, snap), "xray_tcp_up", labels("config", "nl1", "target", "other"))
}

func TestExportUDPAndTCP(t *testing.T) {
	snap := New()
	snap.SeedUDP("nl1", []config.UDPTarget{
		stunTarget("b", "b.test:3478"),
		stunTarget("a", "a.test:3478"),
	}, []string{"loaded", "idle"}, seedTime)
	snap.SeedTCP("nl1", []config.TCPTarget{
		tcpTarget("z", "z.test:443"),
		tcpTarget("m", "m.test:443"),
	})

	udpResult := probe.UDPResult{Up: true, Sent: 2, Replies: 2, Median: 10 * time.Millisecond}
	snap.SetUDP("nl1", "a", "idle", udpResult, secondTime)
	tcpResult := probe.TunnelTCPResult{Up: true, Connect: 20 * time.Millisecond}
	snap.SetTCP("nl1", "m", tcpResult, secondTime)

	// A first failure on a fresh row still counts as measured: only seeding
	// leaves Measured false, and an unmeasured row is never logged as a failure.
	snap.SetUDP("nl1", "b", "idle", probe.UDPResult{Err: errors.New("relay: timeout")}, secondTime)
	snap.SetTCP("nl1", "z", probe.TunnelTCPResult{Err: errors.New("connect refused")}, secondTime)

	exp := snap.Export()
	if len(exp.Configs) != 1 {
		t.Fatalf("Export has %d configs, want 1", len(exp.Configs))
	}
	cfg := exp.Configs[0]
	if cfg.LoadUp != nil {
		t.Errorf("LoadUp = %v before any loaded pass, want nil", *cfg.LoadUp)
	}

	var udpKeys []string
	for _, row := range cfg.UDPs {
		udpKeys = append(udpKeys, row.Target+"/"+row.Load)
	}
	wantUDP := []string{"a/idle", "a/loaded", "b/idle", "b/loaded"}
	if len(udpKeys) != len(wantUDP) {
		t.Fatalf("UDP rows = %v, want %v", udpKeys, wantUDP)
	}
	for i, want := range wantUDP {
		if udpKeys[i] != want {
			t.Errorf("UDP row %d = %q, want %q", i, udpKeys[i], want)
		}
	}
	if got := cfg.UDPs[0].Result; got != udpResult {
		t.Errorf("UDP a/idle result = %+v, want the raw result %+v", got, udpResult)
	}
	for _, row := range cfg.UDPs {
		want := row.Target == "a" && row.Load == "idle" ||
			row.Target == "b" && row.Load == "idle"
		if row.Measured != want {
			t.Errorf("UDP %s/%s Measured = %v, want %v", row.Target, row.Load, row.Measured, want)
		}
		if row.Target == "b" && row.Load == "idle" && row.Result.Up {
			t.Errorf("UDP b/idle Up = true, want the first failure recorded as down")
		}
	}

	var tcpNames []string
	for _, row := range cfg.TCPs {
		tcpNames = append(tcpNames, row.Target)
	}
	if len(tcpNames) != 2 || tcpNames[0] != "m" || tcpNames[1] != "z" {
		t.Errorf("TCP rows = %v, want [m z]", tcpNames)
	}
	if got := cfg.TCPs[0].Result; got != tcpResult {
		t.Errorf("TCP m result = %+v, want the raw result %+v", got, tcpResult)
	}
	for _, row := range cfg.TCPs {
		want := row.Target == "m" || row.Target == "z"
		if row.Measured != want {
			t.Errorf("TCP %s Measured = %v, want %v", row.Target, row.Measured, want)
		}
		if row.Target == "z" && row.Result.Up {
			t.Errorf("TCP z Up = true, want the first failure recorded as down")
		}
	}

	snap.SetUDPLoad("nl1", true, secondTime)
	cfg = snap.Export().Configs[0]
	if cfg.LoadUp == nil || !*cfg.LoadUp {
		t.Errorf("LoadUp = %v after a live loaded pass, want true", cfg.LoadUp)
	}
}
