package sampler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/netutil"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

const underLoadURL = "https://mirror.test/20mb.bin"

func udpTarget(name, addr string) config.UDPTarget {
	return config.UDPTarget{Name: name, Kind: "stun", Addr: addr, Count: 20, SpacingDur: 100 * time.Millisecond}
}

func seedSeconds() float64 {
	return float64(seedTime.UnixNano()) / 1e9
}

func TestCycleMeasuresUDPTargets(t *testing.T) {
	snap := metrics.New()
	snap.Seed("nl1", nil, nil, seedTime)
	snap.SeedUDP("nl1", []config.UDPTarget{udpTarget("tg-stun-1", "203.0.113.10:3478")}, []string{"idle"}, seedTime)

	fake := &fakeProbers{
		stunResult: probe.UDPResult{Up: true, Sent: 20, Replies: 20, Median: 30 * time.Millisecond, Max: 45 * time.Millisecond},
	}
	s := New(snap, fake.probers(), testOptions("example.com"))

	s.Cycle(context.Background(), []Job{{Name: "nl1", SocksPort: 1080}}, Inputs{
		UDPTargets: []config.UDPTarget{udpTarget("tg-stun-1", "203.0.113.10:3478")},
	})

	calls := fake.stunCallsSnapshot()
	if len(calls) != 1 {
		t.Fatalf("STUN called %d times (%+v), want 1: the idle pass only", len(calls), calls)
	}
	if got, want := calls[0].socksAddr, netutil.LoopbackAddr(1080); got != want {
		t.Errorf("STUN socks address = %q, want the job's socks inbound %q", got, want)
	}
	if calls[0].target != "203.0.113.10:3478" || calls[0].count != 20 || calls[0].spacing != 100*time.Millisecond {
		t.Errorf("STUN call = %+v, want the target's addr, count and spacing", calls[0])
	}
	if downloads := fake.downloadCallsSnapshot(); len(downloads) != 0 {
		t.Errorf("download called %d times (%v) without an under-load URL", len(downloads), downloads)
	}

	values := snapshot(t, snap)
	if got := mustValue(t, values, "xray_udp_up", "config", "nl1", "load", "idle", "target", "tg-stun-1"); got != 1 {
		t.Errorf("xray_udp_up{load=idle} = %v, want 1", got)
	}
	if got := mustValue(t, values, "xray_udp_rtt_seconds", "config", "nl1", "load", "idle", "target", "tg-stun-1"); got != 0.03 {
		t.Errorf("xray_udp_rtt_seconds{load=idle} = %v, want 0.03", got)
	}
	mustAbsent(t, values, "xray_udp_up", "config", "nl1", "load", "loaded", "target", "tg-stun-1")
	mustAbsent(t, values, "xray_udp_load_up", "config", "nl1")
}

func TestCycleMeasuresUDPLoadedPass(t *testing.T) {
	snap := metrics.New()
	snap.Seed("nl1", nil, nil, seedTime)
	snap.SeedUDP("nl1", []config.UDPTarget{udpTarget("tg-stun-1", "203.0.113.10:3478")}, []string{"idle", "loaded"}, seedTime)

	fake := &fakeProbers{
		stunResult:    probe.UDPResult{Up: true, Sent: 20, Replies: 19, Median: 90 * time.Millisecond, Max: 200 * time.Millisecond},
		downloadBytes: 1 << 20,
	}
	s := New(snap, fake.probers(), testOptions("example.com"))

	s.Cycle(context.Background(), []Job{{Name: "nl1", SocksPort: 1080}}, Inputs{
		UDPTargets:   []config.UDPTarget{udpTarget("tg-stun-1", "203.0.113.10:3478")},
		UnderLoadURL: underLoadURL,
	})

	if calls := fake.stunCallsSnapshot(); len(calls) != 2 {
		t.Errorf("STUN called %d times (%+v), want 2: idle and loaded", len(calls), calls)
	}
	downloads := fake.downloadCallsSnapshot()
	if len(downloads) == 0 || downloads[0] != underLoadURL {
		t.Errorf("download calls = %v, want the under-load URL", downloads)
	}

	values := snapshot(t, snap)
	for _, load := range []string{"idle", "loaded"} {
		if got := mustValue(t, values, "xray_udp_up", "config", "nl1", "load", load, "target", "tg-stun-1"); got != 1 {
			t.Errorf("xray_udp_up{load=%s} = %v, want 1", load, got)
		}
	}
	if got := mustValue(t, values, "xray_udp_load_up", "config", "nl1"); got != 1 {
		t.Errorf("xray_udp_load_up = %v, want 1: the download moved bytes", got)
	}
}

// TestMeasureUDPRestartsFinishedDownload pins that a file which ends early is
// fetched again: one GET can finish in a second and leave the rest of the burst
// unloaded.
func TestMeasureUDPRestartsFinishedDownload(t *testing.T) {
	snap := metrics.New()
	snap.Seed("nl1", nil, nil, seedTime)
	snap.SeedUDP("nl1", []config.UDPTarget{udpTarget("tg", "203.0.113.10:3478")}, []string{"idle", "loaded"}, seedTime)

	p := (&fakeProbers{}).probers()
	var downloads, stuns int32
	restarted := make(chan struct{})
	p.Download = func(context.Context, probe.Dialer, string) (int64, error) {
		if atomic.AddInt32(&downloads, 1) == 2 {
			close(restarted)
		}
		return 1, nil
	}
	p.STUN = func(context.Context, string, string, int, time.Duration, time.Duration) probe.UDPResult {
		// The idle burst runs first; only the loaded burst waits for the second GET.
		if atomic.AddInt32(&stuns, 1) == 2 {
			select {
			case <-restarted:
			case <-time.After(2 * time.Second):
			}
		}
		return probe.UDPResult{Up: true, Sent: 20, Replies: 20}
	}
	s := New(snap, p, testOptions("example.com"))

	s.Cycle(context.Background(), []Job{{Name: "nl1", SocksPort: 1080}}, Inputs{
		UDPTargets:   []config.UDPTarget{udpTarget("tg", "203.0.113.10:3478")},
		UnderLoadURL: underLoadURL,
	})

	if got := atomic.LoadInt32(&downloads); got < 2 {
		t.Errorf("download called %d times, want it restarted after the file ended", got)
	}
	values := snapshot(t, snap)
	if got := mustValue(t, values, "xray_udp_load_up", "config", "nl1"); got != 1 {
		t.Errorf("xray_udp_load_up = %v, want 1", got)
	}
}

func TestMeasureUDPDiscardsLoadedPassOnDeadDownload(t *testing.T) {
	snap := metrics.New()
	snap.Seed("nl1", nil, nil, seedTime)
	snap.SeedUDP("nl1", []config.UDPTarget{udpTarget("tg", "203.0.113.10:3478")}, []string{"idle", "loaded"}, seedTime)

	fake := &fakeProbers{
		stunResult:  probe.UDPResult{Up: true, Sent: 20, Replies: 20, Median: 20 * time.Millisecond},
		downloadErr: errors.New("download: connection refused"),
	}
	s := New(snap, fake.probers(), testOptions("example.com"))

	s.Cycle(context.Background(), []Job{{Name: "nl1", SocksPort: 1080}}, Inputs{
		UDPTargets:   []config.UDPTarget{udpTarget("tg", "203.0.113.10:3478")},
		UnderLoadURL: underLoadURL,
	})

	values := snapshot(t, snap)
	if got := mustValue(t, values, "xray_udp_up", "config", "nl1", "load", "idle", "target", "tg"); got != 1 {
		t.Errorf("xray_udp_up{load=idle} = %v, want 1: the idle pass is unaffected", got)
	}
	if got := mustValue(t, values, "xray_udp_timestamp_seconds", "config", "nl1", "load", "idle", "target", "tg"); got <= seedSeconds() {
		t.Errorf("xray_udp_timestamp_seconds{load=idle} = %v, want after the seed %v", got, seedSeconds())
	}
	if got := mustValue(t, values, "xray_udp_up", "config", "nl1", "load", "loaded", "target", "tg"); got != 0 {
		t.Errorf("xray_udp_up{load=loaded} = %v, want the seeded 0: a dead download discards the pass", got)
	}
	if got := mustValue(t, values, "xray_udp_timestamp_seconds", "config", "nl1", "load", "loaded", "target", "tg"); got != seedSeconds() {
		t.Errorf("xray_udp_timestamp_seconds{load=loaded} = %v, want the seeded %v: the previous sample stays stale", got, seedSeconds())
	}
	if got := mustValue(t, values, "xray_udp_load_up", "config", "nl1"); got != 0 {
		t.Errorf("xray_udp_load_up = %v, want 0: the download moved no bytes", got)
	}
}

func TestMeasureUDPDeadlineCutDiscards(t *testing.T) {
	snap := metrics.New()
	snap.Seed("nl1", nil, nil, seedTime)
	snap.SeedUDP("nl1", []config.UDPTarget{udpTarget("tg", "203.0.113.10:3478")}, []string{"idle", "loaded"}, seedTime)

	fake := &fakeProbers{
		stunResult:    probe.UDPResult{Up: true, Sent: 20, Replies: 20, Median: 20 * time.Millisecond},
		downloadBytes: 1 << 20,
		stunBlock:     true,
	}
	opts := testOptions("example.com")
	opts.Deadline = 100 * time.Millisecond
	s := New(snap, fake.probers(), opts)

	s.Cycle(context.Background(), []Job{{Name: "nl1", SocksPort: 1080}}, Inputs{
		UDPTargets:   []config.UDPTarget{udpTarget("tg", "203.0.113.10:3478")},
		UnderLoadURL: underLoadURL,
	})

	values := snapshot(t, snap)
	for _, load := range []string{"idle", "loaded"} {
		if got := mustValue(t, values, "xray_udp_up", "config", "nl1", "load", load, "target", "tg"); got != 0 {
			t.Errorf("xray_udp_up{load=%s} = %v, want the seeded 0: the deadline cut the burst", load, got)
		}
		if got := mustValue(t, values, "xray_udp_timestamp_seconds", "config", "nl1", "load", load, "target", "tg"); got != seedSeconds() {
			t.Errorf("xray_udp_timestamp_seconds{load=%s} = %v, want the seeded %v", load, got, seedSeconds())
		}
	}
	mustAbsent(t, values, "xray_udp_load_up", "config", "nl1")
}

func TestCycleMeasuresTCPTargets(t *testing.T) {
	const addr = "149.154.167.51:443"

	snap := metrics.New()
	snap.Seed("nl1", nil, nil, seedTime)
	snap.SeedTCP("nl1", []config.TCPTarget{{Name: "mtproto-dc4", Addr: addr}})

	fake := &fakeProbers{tcpResult: probe.TunnelTCPResult{Up: true, Connect: 12 * time.Millisecond}}
	opts := testOptions("example.com")
	s := New(snap, fake.probers(), opts)

	s.Cycle(context.Background(), []Job{{Name: "nl1", SocksPort: 1080}}, Inputs{
		TCPTargets: []config.TCPTarget{{Name: "mtproto-dc4", Addr: addr}},
	})

	calls := fake.tcpCallsSnapshot()
	if len(calls) != 1 || calls[0].addr != addr {
		t.Fatalf("TCP calls = %+v, want one for %q", calls, addr)
	}
	if calls[0].timeout != opts.ProbeTimeout {
		t.Errorf("TCP timeout = %v, want the probe timeout %v", calls[0].timeout, opts.ProbeTimeout)
	}

	values := snapshot(t, snap)
	if got := mustValue(t, values, "xray_tcp_up", "config", "nl1", "target", "mtproto-dc4"); got != 1 {
		t.Errorf("xray_tcp_up = %v, want 1", got)
	}
	if got := mustValue(t, values, "xray_tcp_seconds", "config", "nl1", "target", "mtproto-dc4"); got != 0.012 {
		t.Errorf("xray_tcp_seconds = %v, want 0.012", got)
	}
}

func TestCycleUsesConfiguredTimeout(t *testing.T) {
	snap := metrics.New()
	snap.Seed("nl1", nil, nil, seedTime)

	configured := udpTarget("configured", "a.test:3478")
	configured.TimeoutDur = 3 * time.Second
	targets := []config.UDPTarget{configured, udpTarget("default", "b.test:3478")}
	snap.SeedUDP("nl1", targets, []string{"idle"}, seedTime)

	fake := &fakeProbers{stunResult: probe.UDPResult{Up: true, Sent: 20, Replies: 20}}
	opts := testOptions("example.com")
	s := New(snap, fake.probers(), opts)

	s.Cycle(context.Background(), []Job{{Name: "nl1", SocksPort: 1080}}, Inputs{UDPTargets: targets})

	want := map[string]time.Duration{
		"a.test:3478": 3 * time.Second,
		"b.test:3478": opts.ProbeTimeout,
	}
	calls := fake.stunCallsSnapshot()
	if len(calls) != len(want) {
		t.Fatalf("STUN called %d times (%+v), want %d", len(calls), calls, len(want))
	}
	for _, call := range calls {
		if call.timeout != want[call.target] {
			t.Errorf("STUN timeout for %s = %v, want %v", call.target, call.timeout, want[call.target])
		}
	}
}
