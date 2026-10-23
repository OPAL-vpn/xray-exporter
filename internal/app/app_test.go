package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	clog "github.com/xtls/xray-core/common/log"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/egress"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
	"github.com/OPAL-vpn/xray-exporter/internal/sampler"
	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
	"github.com/OPAL-vpn/xray-exporter/internal/xraylog"
	"github.com/OPAL-vpn/xray-exporter/internal/xrayproc"
)

// testManager returns a manager with no instances, which every cycle and
// reload path under test can use without starting xray.
func testManager(logger *slog.Logger) *xrayproc.Manager {
	return xrayproc.NewManager(xraycfg.Upstream{Host: "9.9.9.9", Port: 53}, "warning", logger)
}

func TestHandlerServesMetricsAndHealthz(t *testing.T) {
	a := &application{snap: metrics.New()}
	a.snap.SetBuildInfo("test", "go-test")

	srv := httptest.NewServer(a.handler())
	defer srv.Close()

	status, body := get(t, srv.URL+"/metrics")
	if status != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200", status)
	}
	if !strings.Contains(body, "xray_") {
		t.Errorf("/metrics body has no xray_ metric name:\n%s", body)
	}

	status, body = get(t, srv.URL+"/healthz")
	if status != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200", status)
	}
	if strings.TrimSpace(body) != "ok" {
		t.Errorf("/healthz body = %q, want %q", body, "ok")
	}

	if status, _ = get(t, srv.URL+"/nope"); status != http.StatusNotFound {
		t.Errorf("/nope status = %d, want 404: the mux must not fall through to a handler", status)
	}
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp.StatusCode, string(body)
}

func TestMirrorHandlerWritesConsoleAndFile(t *testing.T) {
	var console, file bytes.Buffer
	logger := slog.New(mirrorHandler{
		console: slog.NewTextHandler(&console, &slog.HandlerOptions{Level: slog.LevelInfo}),
		file:    slog.NewTextHandler(&file, &slog.HandlerOptions{Level: slog.LevelDebug}),
	})

	logger.Info("instance started", "config", "nl1")
	logger.Debug("xray heartbeat")

	for name, buf := range map[string]*bytes.Buffer{"console": &console, "file": &file} {
		out := buf.String()
		if !strings.Contains(out, "instance started") || !strings.Contains(out, "config=nl1") {
			t.Errorf("%s got %q, want the message and its attributes", name, out)
		}
	}

	// The console keeps its own level: only the file mirrors the debug stream.
	if strings.Contains(console.String(), "xray heartbeat") {
		t.Errorf("console got the debug line, want its info filter to hold:\n%s", console.String())
	}
	if !strings.Contains(file.String(), "xray heartbeat") {
		t.Errorf("file missed the debug line, want it to keep the mirrored stream:\n%s", file.String())
	}
}

// TestReloadEmptyConfigDirectoryDropsSeries covers the reload after the last
// config file was deleted: a readable but empty directory is authoritative, so
// the instance stops and its series disappear instead of surviving as a stale
// "keeping previous" set.
func TestReloadEmptyConfigDirectoryDropsSeries(t *testing.T) {
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "203.0.113.7")
	}))
	defer echo.Close()

	targetsPath := filepath.Join(t.TempDir(), "targets.yaml")
	targetsBody := fmt.Sprintf("egress_check_url: %s\ndns_probe_domain: example.com\ndns_upstream: 9.9.9.9:53\ntargets:\n  - host: example.com\n", echo.URL)
	if err := os.WriteFile(targetsPath, []byte(targetsBody), 0o644); err != nil {
		t.Fatalf("write targets: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &application{
		env: Env{
			ConfigDir:    t.TempDir(), // readable, and the last config was deleted
			TargetsFile:  targetsPath,
			Interval:     time.Hour,
			Workers:      1,
			ProbeTimeout: time.Second,
			EgressTTL:    time.Minute,
		},
		logger:   logger,
		snap:     metrics.New(),
		targets:  &config.Targets{EgressCheckURL: echo.URL, DNSProbeDomain: "example.com", DNSUpstream: "9.9.9.9:53", Targets: []config.Target{{Host: "example.com"}}},
		verifier: egress.New(echo.URL, time.Minute, time.Second),
		upstream: xraycfg.Upstream{Host: "9.9.9.9", Port: 53},
		manager:  testManager(logger),
		configs:  []xraycfg.Config{{Name: "nl1"}},
		seeded:   map[string]struct{}{"nl1": {}},
	}
	a.snap.SetBuildInfo("test", "go-test")
	a.snap.Seed("nl1", []string{"example.com"}, nil, time.Now())
	a.snap.SetConfig("nl1", true, "26.1.1")
	a.snap.SetEgress("nl1", egress.Result{IP: "203.0.113.7", Verified: true}, time.Now())

	a.reload(context.Background())
	defer a.interruptCycle()

	if configs := a.snap.Export().Configs; len(configs) != 0 {
		t.Fatalf("after reload with an empty config directory, configs = %+v, want none", configs)
	}

	rec := httptest.NewRecorder()
	a.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "xray_exporter_config_reload_total 1") {
		t.Errorf("reload counter did not move, want an applied reload:\n%s", body)
	}
	if !strings.Contains(body, "xray_exporter_config_reload_errors_total 0") {
		t.Errorf("empty directory was counted as a reload error:\n%s", body)
	}
}

// TestCycleInterruptedPassLogsNeitherOutcome covers the reload's interrupted
// pass: it reached only some of its jobs, so it reports neither a completion
// nor an overrun for the rest.
func TestCycleInterruptedPassLogsNeitherOutcome(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	a := &application{
		env: Env{
			Workers:         1,
			ProbeTimeout:    time.Second,
			ResolverTimeout: time.Second,
		},
		logger:  logger,
		snap:    metrics.New(),
		targets: &config.Targets{},
		manager: testManager(logger),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.cycle(ctx, time.Second)

	for _, want := range []string{"cycle complete", "cycle overrun"} {
		if strings.Contains(logs.String(), want) {
			t.Errorf("interrupted pass logged %q, want neither outcome:\n%s", want, logs.String())
		}
	}
}

// TestLogProbesReportsEachOutcome covers the operator's only view of why an up
// gauge is 0: one WARN per failed entity with the retained reason, one DEBUG per
// success, and nothing for a config the cycle did not run.
func TestLogProbesReportsEachOutcome(t *testing.T) {
	var logs bytes.Buffer
	a := &application{logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}

	exp := metrics.Export{Configs: []metrics.ConfigExport{
		{
			Name: "nl1",
			Targets: []metrics.TargetExport{
				{
					Target:  "down.test",
					HasHTTP: true, HTTP: probe.HTTPResult{Err: errors.New("tls handshake refused")},
					HasDNS: true, DNS: probe.DNSResult{Err: errors.New("dns read: timeout")},
				},
				{
					Target:  "up.test",
					HasHTTP: true, HTTP: probe.HTTPResult{Up: true, Status: http.StatusOK},
					HasDNS: true, DNS: probe.DNSResult{Up: true},
				},
			},
			Resolvers: []metrics.ResolverExport{{
				Name: "cf",
				Transports: []metrics.TransportExport{{
					Transport: metrics.TransportDoH,
					Result:    probe.ResolverResult{Err: errors.New("doh status 500")},
				}},
			}},
		},
		{
			Name:    "gone",
			Targets: []metrics.TargetExport{{Target: "stale.test", HasHTTP: true, HTTP: probe.HTTPResult{Err: errors.New("stale")}}},
		},
	}}

	a.logProbes(exp, []sampler.Job{{Name: "nl1"}})

	out := logs.String()
	for _, want := range []string{
		`level=WARN msg="probe failed" config=nl1 probe=dns target=down.test err="dns read: timeout"`,
		`level=WARN msg="probe failed" config=nl1 probe=https target=down.test err="tls handshake refused"`,
		`level=WARN msg="probe failed" config=nl1 probe=doh resolver=cf err="doh status 500"`,
		`level=DEBUG msg="probe succeeded" config=nl1 probe=dns target=up.test`,
		`level=DEBUG msg="probe succeeded" config=nl1 probe=https target=up.test`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log does not contain %q:\n%s", want, out)
		}
	}
	if got := strings.Count(out, "level=WARN"); got != 3 {
		t.Errorf("got %d WARN lines, want one per failed entity transport (3):\n%s", got, out)
	}
	if strings.Contains(out, "config=gone") {
		t.Errorf("a config the cycle did not run was logged:\n%s", out)
	}
}

// TestApplyLogsAStartFailureOnce pins that a config that cannot start leaves one
// ERROR line: Start already names the config and xray's own cause.
func TestApplyLogsAStartFailureOnce(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	a := &application{logger: logger, snap: metrics.New(), manager: testManager(logger)}
	t.Cleanup(func() {
		if err := a.manager.Close(); err != nil {
			t.Errorf("Manager.Close: %v", err)
		}
		// Apply installs xray's process-global handler; leave it discarding.
		xraylog.Install(testDiscardLogger(), clog.Severity_Unknown)
	})

	_, _, accepted := a.apply([]xraycfg.Config{{Name: "broken", Raw: []byte(`{"outbounds":[{"protocol":"nope"}]}`)}})
	if accepted {
		t.Fatal("apply accepted a batch with a config that cannot start")
	}

	out := logs.String()
	if got := strings.Count(out, "level=ERROR"); got != 1 {
		t.Errorf("start failure logged %d times, want 1:\n%s", got, out)
	}
	if !strings.Contains(out, "config=broken") {
		t.Errorf("log does not name the config:\n%s", out)
	}
}

func testDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
