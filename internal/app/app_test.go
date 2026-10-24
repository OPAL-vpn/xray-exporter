package app

import (
	"bytes"
	"context"
	"errors"
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
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
	"github.com/OPAL-vpn/xray-exporter/internal/sampler"
	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
	"github.com/OPAL-vpn/xray-exporter/internal/xraylog"
	"github.com/OPAL-vpn/xray-exporter/internal/xrayproc"
)

// testManager returns a manager with no instances.
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

func TestReloadEmptyConfigDirectoryDropsSeries(t *testing.T) {
	targetsPath := filepath.Join(t.TempDir(), "targets.yaml")
	targetsBody := "dns_probe_domain: example.com\ndns_upstream: 9.9.9.9:53\ntargets:\n  - host: example.com\n"
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
		},
		logger:   logger,
		snap:     metrics.New(),
		targets:  &config.Targets{DNSProbeDomain: "example.com", DNSUpstream: "9.9.9.9:53", Targets: []config.Target{{Host: "example.com"}}},
		upstream: xraycfg.Upstream{Host: "9.9.9.9", Port: 53},
		manager:  testManager(logger),
		configs:  []xraycfg.Config{{Name: "nl1"}},
		seeded:   map[string]struct{}{"nl1": {}},
	}
	a.snap.SetBuildInfo("test", "go-test")
	a.snap.Seed("nl1", []string{"example.com"}, nil, time.Now())
	a.snap.SetConfig("nl1", true, "26.1.1")

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

// An interrupted pass logs neither completion nor overrun for unvisited jobs.
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

// logProbes is the only view of why an up gauge is 0: one WARN per failure, one
// DEBUG per success, nothing for a skipped config.
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

// A failed start leaves exactly one ERROR line.
func TestApplyLogsAStartFailureOnce(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	a := &application{logger: logger, snap: metrics.New(), manager: testManager(logger)}
	t.Cleanup(func() {
		if err := a.manager.Close(); err != nil {
			t.Errorf("Manager.Close: %v", err)
		}
		// Leave xray's process-global handler discarding.
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
