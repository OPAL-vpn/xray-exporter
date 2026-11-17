package app

import (
	"bytes"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
	"github.com/OPAL-vpn/xray-exporter/internal/sampler"
	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
)

// A cycle's inputs carry the configured UDP and TCP targets and the under-load
// URL; without the block the loaded pass stays off.
func TestInputsCarriesUDPAndTCPBlocks(t *testing.T) {
	udp := []config.UDPTarget{{Name: "tg-stun-1", Kind: "stun", Addr: "203.0.113.10:3478"}}
	tcp := []config.TCPTarget{{Name: "mtproto-dc4", Addr: "149.154.167.51:443"}}

	a := &application{exporterConfig: &config.ExporterConfig{
		DNSProbeDomain: "example.com",
		UDPTargets:     udp,
		TCPTargets:     tcp,
		UDPUnderLoad:   &config.UDPUnderLoad{URL: "https://mirror.example.org/20mb.bin"},
	}}

	in, domain := a.inputs()
	if domain != "example.com" {
		t.Errorf("probe domain = %q, want example.com", domain)
	}
	if !reflect.DeepEqual(in.UDPTargets, udp) {
		t.Errorf("inputs UDP targets = %+v, want %+v", in.UDPTargets, udp)
	}
	if !reflect.DeepEqual(in.TCPTargets, tcp) {
		t.Errorf("inputs TCP targets = %+v, want %+v", in.TCPTargets, tcp)
	}
	if want := "https://mirror.example.org/20mb.bin"; in.UnderLoadURL != want {
		t.Errorf("UnderLoadURL = %q, want %q", in.UnderLoadURL, want)
	}

	bare := &application{exporterConfig: &config.ExporterConfig{}}
	in, _ = bare.inputs()
	if in.UnderLoadURL != "" {
		t.Errorf("UnderLoadURL = %q without udp_under_load, want empty", in.UnderLoadURL)
	}
}

// Seeding declares a config's UDP series for every load pass and its TCP
// series; failClosed's re-seed keeps them so up=0 stays visible.
func TestSeedDeclaresUDPAndTCPSeries(t *testing.T) {
	udp := []config.UDPTarget{{Name: "tg-stun-1", Kind: "stun", Addr: "203.0.113.10:3478"}}
	tcp := []config.TCPTarget{{Name: "mtproto-dc4", Addr: "149.154.167.51:443"}}

	for _, tc := range []struct {
		name      string
		underLoad *config.UDPUnderLoad
		wantLoads []string
	}{
		{name: "idle only", wantLoads: []string{metrics.LoadIdle}},
		{
			name:      "under load",
			underLoad: &config.UDPUnderLoad{URL: "https://mirror.example.org/20mb.bin"},
			wantLoads: []string{metrics.LoadIdle, metrics.LoadLoaded},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := seedApp(t, udp, tcp, tc.underLoad)

			a.seed(time.Now())
			assertDeclared(t, a.snap, udp, tcp, tc.wantLoads)

			// A fetch failure stops every config but must leave its series
			// declared: an up=0 gauge is the visible result.
			a.failClosed(time.Now(), errors.New("panel unavailable"))
			assertDeclared(t, a.snap, udp, tcp, tc.wantLoads)
		})
	}
}

// seedApp is the smallest application seed and failClosed need: one declared
// config, no running instances.
func seedApp(t *testing.T, udp []config.UDPTarget, tcp []config.TCPTarget, underLoad *config.UDPUnderLoad) *application {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	return &application{
		logger:  logger,
		snap:    metrics.New(),
		manager: testManager(logger),
		exporterConfig: &config.ExporterConfig{
			UDPTargets:   udp,
			TCPTargets:   tcp,
			UDPUnderLoad: underLoad,
		},
		configs: []xraycfg.Config{{Name: "nl1"}},
		seeded:  make(map[string]struct{}),
	}
}

// assertDeclared checks one config's exported UDP rows (one per target and
// load) and TCP rows, and that no loaded pass has reported yet.
func assertDeclared(t *testing.T, snap *metrics.Snapshot, udp []config.UDPTarget, tcp []config.TCPTarget, wantLoads []string) {
	t.Helper()

	exp := snap.Export()
	if len(exp.Configs) != 1 || exp.Configs[0].Name != "nl1" {
		t.Fatalf("configs = %+v, want one config named nl1", exp.Configs)
	}
	cfg := exp.Configs[0]

	loads := make(map[string][]string, len(udp))
	for _, row := range cfg.UDPs {
		loads[row.Target] = append(loads[row.Target], row.Load)
	}
	if len(loads) != len(udp) {
		t.Errorf("declared %d UDP targets, want %d: %+v", len(loads), len(udp), cfg.UDPs)
	}
	for _, target := range udp {
		if got := loads[target.Name]; !reflect.DeepEqual(got, wantLoads) {
			t.Errorf("UDP target %q loads = %v, want %v", target.Name, got, wantLoads)
		}
	}

	if len(cfg.TCPs) != len(tcp) {
		t.Fatalf("declared %d TCP targets, want %d: %+v", len(cfg.TCPs), len(tcp), cfg.TCPs)
	}
	for i, row := range cfg.TCPs {
		if row.Target != tcp[i].Name {
			t.Errorf("TCP target %d = %q, want %q", i, row.Target, tcp[i].Name)
		}
	}

	if cfg.LoadUp != nil {
		t.Errorf("LoadUp = %v before any loaded pass, want nil", *cfg.LoadUp)
	}
}

// logProbes reports every UDP row that is not up, every failed TCP row and a
// loaded pass that moved no bytes.
func TestLogProbesReportsUDPFailures(t *testing.T) {
	var logs bytes.Buffer
	a := &application{
		logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}

	loadDown := false
	exp := metrics.Export{Configs: []metrics.ConfigExport{{
		Name: "nl1",
		UDPs: []metrics.UDPExport{
			{Target: "tg-stun-1", Load: metrics.LoadIdle, Result: probe.UDPResult{Err: errors.New("relay: timeout")}},
			{Target: "tg-stun-1", Load: metrics.LoadLoaded, Result: probe.UDPResult{Up: true, Sent: 2, Replies: 2}},
		},
		TCPs: []metrics.TCPExport{
			{Target: "mtproto-dc4", Result: probe.TunnelTCPResult{Err: errors.New("connect refused")}},
		},
		LoadUp: &loadDown,
	}}}

	a.logProbes(exp, []sampler.Job{{Name: "nl1"}})

	out := logs.String()
	for _, want := range []string{
		`level=WARN msg="probe failed" config=nl1 probe=udp target=tg-stun-1 load=idle err="relay: timeout"`,
		`level=WARN msg="probe failed" config=nl1 probe=tcp target=mtproto-dc4 err="connect refused"`,
		`level=WARN msg="under-load download moved no bytes" config=nl1`,
		`level=DEBUG msg="probe succeeded" config=nl1 probe=udp target=tg-stun-1 load=loaded`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log does not contain %q:\n%s", want, out)
		}
	}
	if got := strings.Count(out, "level=WARN"); got != 3 {
		t.Errorf("got %d WARN lines, want one per failure (3):\n%s", got, out)
	}
}
