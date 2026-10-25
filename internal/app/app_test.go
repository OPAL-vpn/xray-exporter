package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
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

// exporterConfigFile writes a minimal exporter config; sourceBlock is appended
// verbatim so a test can name an xray_config_source.
func exporterConfigFile(t *testing.T, sourceBlock string) string {
	t.Helper()

	body := "dns_probe_domain: example.com\ndns_upstream: 9.9.9.9:53\ntargets:\n  - host: example.com\n" + sourceBlock
	path := filepath.Join(t.TempDir(), "xray-exporter-config.yml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write exporter config: %v", err)
	}
	return path
}

// startupEnv is the smallest Env newApplication needs; XrayAssetDir is a fresh
// directory so the process environment points somewhere harmless.
func startupEnv(t *testing.T, configFile, configDir string) Env {
	t.Helper()
	return Env{
		ConfigDir:       configDir,
		ConfigFile:      configFile,
		Interval:        time.Hour,
		Workers:         1,
		ProbeTimeout:    time.Second,
		ResolverTimeout: time.Second,
		XrayLogLevel:    "warning",
		XrayAssetDir:    t.TempDir(),
	}
}

func remnawaveSourceBlock(url string) string {
	return "xray_config_source:\n  type: remnawave\n  url: " + url + "\n  refresh: 0\n"
}

// panel serves body on every request and returns its base URL; a non-200 status
// exercises a failed fetch.
func panel(t *testing.T, status int, body string) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status != http.StatusOK {
			http.Error(w, "panel detail", status)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// snapValue renders the snapshot the way the collector does and returns one
// series; ok is false when the series is absent.
func snapValue(t *testing.T, snap *metrics.Snapshot, name string, labelPairs ...string) (float64, bool) {
	t.Helper()

	reg := prometheus.NewRegistry()
	reg.MustRegister(snap.Collector())
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	want := make(map[string]string, len(labelPairs)/2)
	for i := 0; i < len(labelPairs); i += 2 {
		want[labelPairs[i]] = labelPairs[i+1]
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			got := make(map[string]string, len(metric.GetLabel()))
			for _, pair := range metric.GetLabel() {
				got[pair.GetName()] = pair.GetValue()
			}
			if !maps.Equal(got, want) {
				continue
			}
			if metric.GetCounter() != nil {
				return metric.GetCounter().GetValue(), true
			}
			return metric.GetGauge().GetValue(), true
		}
	}
	return 0, false
}

func TestNewSourceDefaultsToDirectory(t *testing.T) {
	src, policy, err := newSource(&config.ExporterConfig{}, "/config/xray")
	if err != nil {
		t.Fatalf("newSource: %v", err)
	}

	if got, want := src.Kind(), "directory"; got != want {
		t.Errorf("Kind() = %q, want %q", got, want)
	}
	dir, ok := src.(xraycfg.Directory)
	if !ok {
		t.Fatalf("source is %T, want xraycfg.Directory", src)
	}
	if got, want := dir.Path, "/config/xray"; got != want {
		t.Errorf("Path = %q, want the config dir %q", got, want)
	}
	if !policy.strictStartup {
		t.Error("strictStartup = false, want an empty directory to be fatal at startup")
	}
	if policy.failClosed {
		t.Error("failClosed = true, want a directory to keep its previous set")
	}
	if got, want := policy.startLogLevel, slog.LevelError; got != want {
		t.Errorf("startLogLevel = %v, want %v", got, want)
	}
}

func TestNewSourceDirectoryPath(t *testing.T) {
	cfg := &config.ExporterConfig{XrayConfigSource: &config.XrayConfigSource{Type: "directory", Path: "/etc/xray"}}

	src, policy, err := newSource(cfg, "/config/xray")
	if err != nil {
		t.Fatalf("newSource: %v", err)
	}

	dir, ok := src.(xraycfg.Directory)
	if !ok {
		t.Fatalf("source is %T, want xraycfg.Directory", src)
	}
	if got, want := dir.Path, "/etc/xray"; got != want {
		t.Errorf("Path = %q, want the block path %q", got, want)
	}
	if !policy.strictStartup || policy.failClosed {
		t.Errorf("policy = %+v, want the directory policy", policy)
	}
}

func TestNewSourceRemnawave(t *testing.T) {
	cfg := &config.ExporterConfig{XrayConfigSource: &config.XrayConfigSource{
		Type:     "remnawave",
		URL:      "https://panel.example.com/api/sub/abc",
		Interval: 5 * time.Minute,
	}}

	src, policy, err := newSource(cfg, "/config/xray")
	if err != nil {
		t.Fatalf("newSource: %v", err)
	}

	if got, want := src.Kind(), "remnawave"; got != want {
		t.Errorf("Kind() = %q, want %q", got, want)
	}
	if got, want := src.Refresh(), 5*time.Minute; got != want {
		t.Errorf("Refresh() = %s, want %s", got, want)
	}
	if !policy.failClosed {
		t.Error("failClosed = false, want a panel to fail closed")
	}
	if policy.strictStartup {
		t.Error("strictStartup = true, want a failed first fetch to be soft")
	}
	if got, want := policy.startLogLevel, slog.LevelWarn; got != want {
		t.Errorf("startLogLevel = %v, want %v", got, want)
	}
}

func TestStartupFetchesRemnawaveConfigs(t *testing.T) {
	panelURL := panel(t, http.StatusOK, `[{"remarks":"local","outbounds":[{"protocol":"freedom","settings":{}}]}]`)
	env := startupEnv(t, exporterConfigFile(t, remnawaveSourceBlock(panelURL)), t.TempDir())

	a, err := newApplication(env, Options{Version: "test"}, testDiscardLogger())
	if err != nil {
		t.Fatalf("newApplication: %v", err)
	}
	defer a.close()

	configs := a.snap.Export().Configs
	if len(configs) != 1 {
		t.Fatalf("configs = %+v, want the one fetched config", configs)
	}
	if got, want := configs[0].Name, "local"; got != want {
		t.Errorf("config name = %q, want %q", got, want)
	}
	if !configs[0].Running {
		t.Error("config running = false, want the fetched freedom config started")
	}
	if got := len(a.instances()); got != 1 {
		t.Errorf("instances = %d, want 1", got)
	}

	if got, ok := snapValue(t, a.snap, "xray_exporter_config_source_info", "type", "remnawave"); !ok || got != 1 {
		t.Errorf("xray_exporter_config_source_info{type=\"remnawave\"} = %v (present %v), want 1", got, ok)
	}
	if got, ok := snapValue(t, a.snap, "xray_exporter_config_fetch_total"); !ok || got != 1 {
		t.Errorf("xray_exporter_config_fetch_total = %v (present %v), want 1", got, ok)
	}
	if got, ok := snapValue(t, a.snap, "xray_exporter_config_fetch_errors_total"); !ok || got != 0 {
		t.Errorf("xray_exporter_config_fetch_errors_total = %v (present %v), want 0", got, ok)
	}
	if _, ok := snapValue(t, a.snap, "xray_exporter_config_fetch_timestamp_seconds"); !ok {
		t.Error("xray_exporter_config_fetch_timestamp_seconds is absent after a successful fetch")
	}
}

func TestStartupFetchFailureStartsWithoutConfigs(t *testing.T) {
	panelURL := panel(t, http.StatusInternalServerError, "")
	env := startupEnv(t, exporterConfigFile(t, remnawaveSourceBlock(panelURL)), t.TempDir())

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	a, err := newApplication(env, Options{Version: "test"}, logger)
	if err != nil {
		t.Fatalf("newApplication: %v", err)
	}
	defer a.close()

	if configs := a.snap.Export().Configs; len(configs) != 0 {
		t.Errorf("configs = %+v, want none after a failed first fetch", configs)
	}
	if got := len(a.instances()); got != 0 {
		t.Errorf("instances = %d, want 0", got)
	}

	if got, ok := snapValue(t, a.snap, "xray_exporter_config_source_info", "type", "remnawave"); !ok || got != 1 {
		t.Errorf("xray_exporter_config_source_info{type=\"remnawave\"} = %v (present %v), want 1", got, ok)
	}
	if got, ok := snapValue(t, a.snap, "xray_exporter_config_fetch_errors_total"); !ok || got != 1 {
		t.Errorf("xray_exporter_config_fetch_errors_total = %v (present %v), want 1", got, ok)
	}
	if got := strings.Count(logs.String(), "level=ERROR"); got != 1 {
		t.Errorf("ERROR lines = %d, want one for the soft fetch failure:\n%s", got, logs.String())
	}
}

func TestOnceFailsWhenFetchFails(t *testing.T) {
	panelURL := panel(t, http.StatusInternalServerError, "")
	env := startupEnv(t, exporterConfigFile(t, remnawaveSourceBlock(panelURL)), t.TempDir())
	reportPath := filepath.Join(t.TempDir(), "report.txt")

	err := Run(context.Background(), env, Options{Once: true, Out: reportPath, Version: "test"}, testDiscardLogger())
	if err == nil {
		t.Fatal("Run with --once and a failed fetch: want an error")
	}
	if !strings.Contains(err.Error(), "unexpected status") {
		t.Errorf("error = %q, want the panel status", err)
	}
	if _, statErr := os.Stat(reportPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("report file exists after a failed startup fetch: %v", statErr)
	}
}

func TestStartupEmptyDirectoryIsFatal(t *testing.T) {
	dir := t.TempDir()
	env := startupEnv(t, exporterConfigFile(t, ""), dir)

	a, err := newApplication(env, Options{Version: "test"}, testDiscardLogger())
	if err == nil {
		a.close()
		t.Fatal("newApplication with an empty config directory: want an error")
	}
	if !errors.Is(err, xraycfg.ErrNoConfigs) {
		t.Errorf("error = %v, want it to wrap ErrNoConfigs", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error = %q, want it to name the config directory %q", err, dir)
	}
}

func TestEmptyDirectoryFetchIsNotAnError(t *testing.T) {
	a := &application{
		logger: testDiscardLogger(),
		snap:   metrics.New(),
		source: xraycfg.Directory{Path: t.TempDir()},
		policy: directoryPolicy(),
	}

	cfgs, err := a.fetchSource(context.Background())
	if err != nil {
		t.Fatalf("fetchSource: %v", err)
	}
	if cfgs == nil || len(cfgs) != 0 {
		t.Errorf("configs = %#v, want an empty, non-nil set", cfgs)
	}

	if got, ok := snapValue(t, a.snap, "xray_exporter_config_fetch_total"); !ok || got != 1 {
		t.Errorf("xray_exporter_config_fetch_total = %v (present %v), want 1", got, ok)
	}
	if got, ok := snapValue(t, a.snap, "xray_exporter_config_fetch_errors_total"); !ok || got != 0 {
		t.Errorf("xray_exporter_config_fetch_errors_total = %v (present %v), want 0", got, ok)
	}
	if got, ok := snapValue(t, a.snap, "xray_exporter_config_fetch_timestamp_seconds"); !ok || got <= 0 {
		t.Errorf("xray_exporter_config_fetch_timestamp_seconds = %v (present %v), want a success time", got, ok)
	}
}

func TestHandlerServesMetricsAndHealthz(t *testing.T) {
	a := &application{snap: metrics.New(), source: xraycfg.Directory{}, policy: directoryPolicy()}
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

func TestReloadUsesDirectorySource(t *testing.T) {
	targetsPath := filepath.Join(t.TempDir(), "xray-exporter-config.yml")
	targetsBody := "dns_probe_domain: example.com\ndns_upstream: 9.9.9.9:53\ntargets:\n  - host: example.com\n"
	if err := os.WriteFile(targetsPath, []byte(targetsBody), 0o644); err != nil {
		t.Fatalf("write targets: %v", err)
	}

	dir := t.TempDir() // readable, and the last config was deleted
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &application{
		env: Env{
			ConfigDir:    dir,
			ConfigFile:   targetsPath,
			Interval:     time.Hour,
			Workers:      1,
			ProbeTimeout: time.Second,
		},
		logger:   logger,
		snap:     metrics.New(),
		targets:  &config.ExporterConfig{DNSProbeDomain: "example.com", DNSUpstream: "9.9.9.9:53", Targets: []config.Target{{Host: "example.com"}}},
		upstream: xraycfg.Upstream{Host: "9.9.9.9", Port: 53},
		manager:  testManager(logger),
		source:   xraycfg.Directory{Path: dir},
		policy:   directoryPolicy(),
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
	// An empty directory is a successful read: it advances the fetch timestamp
	// instead of counting a fetch error.
	if got, ok := snapValue(t, a.snap, "xray_exporter_config_fetch_total"); !ok || got != 1 {
		t.Errorf("xray_exporter_config_fetch_total = %v (present %v), want 1", got, ok)
	}
	if got, ok := snapValue(t, a.snap, "xray_exporter_config_fetch_errors_total"); !ok || got != 0 {
		t.Errorf("xray_exporter_config_fetch_errors_total = %v (present %v), want 0", got, ok)
	}
	if _, ok := snapValue(t, a.snap, "xray_exporter_config_fetch_timestamp_seconds"); !ok {
		t.Error("xray_exporter_config_fetch_timestamp_seconds is absent after an empty-directory read")
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
		source:  xraycfg.Directory{},
		policy:  directoryPolicy(),
		targets: &config.ExporterConfig{},
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
	a := &application{
		logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		source: xraycfg.Directory{},
		policy: directoryPolicy(),
	}

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
	a := &application{logger: logger, snap: metrics.New(), manager: testManager(logger), source: xraycfg.Directory{}, policy: directoryPolicy()}
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

// freedomEntry is one panel config with the given remarks and a freedom
// outbound, which always starts.
func freedomEntry(remark string) string {
	return `{"remarks":"` + remark + `","outbounds":[{"protocol":"freedom","settings":{}}]}`
}

// panelEntries renders a subscription body from raw panel entries.
func panelEntries(entries ...string) string {
	return "[" + strings.Join(entries, ",") + "]"
}

// refreshPanel serves a status and body a test replaces between refreshes.
type refreshPanel struct {
	mu     sync.Mutex
	status int
	body   string
}

func (p *refreshPanel) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	status, body := p.status, p.body
	p.mu.Unlock()

	if status != http.StatusOK {
		http.Error(w, "panel detail", status)
		return
	}
	_, _ = io.WriteString(w, body)
}

func (p *refreshPanel) set(status int, body string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status, p.body = status, body
}

// refreshApp starts an application on a Remnawave source serving body.
func refreshApp(t *testing.T, logger *slog.Logger, body string) (*application, *refreshPanel) {
	t.Helper()

	p := &refreshPanel{status: http.StatusOK, body: body}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)

	env := startupEnv(t, exporterConfigFile(t, remnawaveSourceBlock(srv.URL)), t.TempDir())
	a, err := newApplication(env, Options{Version: "test"}, logger)
	if err != nil {
		t.Fatalf("newApplication: %v", err)
	}
	t.Cleanup(a.close)
	return a, p
}

// instancePort returns the named instance's socks port, or 0 when it has none.
func instancePort(a *application, name string) int {
	for _, inst := range a.instances() {
		if inst.Name == name {
			return inst.SocksPort
		}
	}
	return 0
}

// configRunning reports a config's exported running flag and whether it is
// declared at all.
func configRunning(t *testing.T, snap *metrics.Snapshot, name string) (running, declared bool) {
	t.Helper()

	for _, cfg := range snap.Export().Configs {
		if cfg.Name == name {
			return cfg.Running, true
		}
	}
	return false, false
}

// counter returns a counter value, failing when the family is absent.
func counter(t *testing.T, snap *metrics.Snapshot, name string) float64 {
	t.Helper()

	got, ok := snapValue(t, snap, name)
	if !ok {
		t.Fatalf("%s is absent", name)
	}
	return got
}

func TestRefreshLoopParksAndStopsWithContext(t *testing.T) {
	a := &application{
		logger:      testDiscardLogger(),
		snap:        metrics.New(),
		source:      xraycfg.Directory{},
		policy:      directoryPolicy(),
		refreshWake: make(chan struct{}, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.refreshLoop(ctx)
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("refreshLoop still parked after the context was cancelled")
	}
}

func TestRefreshChangedSetCountsReload(t *testing.T) {
	a, p := refreshApp(t, testDiscardLogger(), panelEntries(freedomEntry("local")))
	defer a.interruptCycle()

	p.set(http.StatusOK, panelEntries(freedomEntry("other")))
	a.refresh(context.Background())

	if got := len(a.instances()); got != 1 {
		t.Fatalf("instances = %d, want the one config of the new set", got)
	}
	if got := instancePort(a, "other"); got == 0 {
		t.Error("the changed config has no instance")
	}
	if running, declared := configRunning(t, a.snap, "other"); !declared || !running {
		t.Errorf("config other running = %v (declared %v), want a started config", running, declared)
	}
	if _, declared := configRunning(t, a.snap, "local"); declared {
		t.Error("the replaced config still has series")
	}

	if got, want := counter(t, a.snap, "xray_exporter_config_reload_total"), 1.0; got != want {
		t.Errorf("reload_total = %v, want %v", got, want)
	}
	if got, want := counter(t, a.snap, "xray_exporter_config_reload_errors_total"), 0.0; got != want {
		t.Errorf("reload_errors_total = %v, want %v", got, want)
	}
}

func TestRefreshUnchangedSetLeavesInstances(t *testing.T) {
	a, _ := refreshApp(t, testDiscardLogger(), panelEntries(freedomEntry("local")))
	defer a.interruptCycle()

	before := instancePort(a, "local")
	if before == 0 {
		t.Fatal("startup did not start the config")
	}

	a.refresh(context.Background())
	a.refresh(context.Background())

	if got := instancePort(a, "local"); got != before {
		t.Errorf("socks port = %d, want the unchanged instance %d", got, before)
	}
	if got := len(a.instances()); got != 1 {
		t.Errorf("instances = %d, want 1", got)
	}
	if got, want := counter(t, a.snap, "xray_exporter_config_reload_total"), 0.0; got != want {
		t.Errorf("reload_total = %v, want %v: an unchanged set is not a reload", got, want)
	}
	if got, want := counter(t, a.snap, "xray_exporter_config_reload_errors_total"), 0.0; got != want {
		t.Errorf("reload_errors_total = %v, want %v", got, want)
	}
	if got, want := counter(t, a.snap, "xray_exporter_config_fetch_total"), 3.0; got != want {
		t.Errorf("fetch_total = %v, want %v: every refresh fetches", got, want)
	}
}

func TestRefreshFetchFailureFailsClosed(t *testing.T) {
	var logs bytes.Buffer
	a, p := refreshApp(t, slog.New(slog.NewTextHandler(&logs, nil)), panelEntries(freedomEntry("local")))
	defer a.interruptCycle()

	fetchedAt, ok := snapValue(t, a.snap, "xray_exporter_config_fetch_timestamp_seconds")
	if !ok {
		t.Fatal("the startup fetch has no timestamp")
	}

	p.set(http.StatusInternalServerError, "")
	a.refresh(context.Background())

	if got := len(a.instances()); got != 0 {
		t.Errorf("instances = %d, want every instance stopped", got)
	}
	if running, declared := configRunning(t, a.snap, "local"); !declared {
		t.Error("config local lost its series, want it declared with running=0")
	} else if running {
		t.Error("config local running = true, want false after a failed fetch")
	}
	if got, ok := snapValue(t, a.snap, "xray_probe_up", "config", "local", "target", "example.com"); !ok || got != 0 {
		t.Errorf("probe_up{config=\"local\"} = %v (present %v), want 0", got, ok)
	}
	if got, want := counter(t, a.snap, "xray_exporter_config_fetch_errors_total"), 1.0; got != want {
		t.Errorf("fetch_errors_total = %v, want %v", got, want)
	}
	if got, ok := snapValue(t, a.snap, "xray_exporter_config_fetch_timestamp_seconds"); !ok || got != fetchedAt {
		t.Errorf("fetch timestamp = %v (present %v), want the previous success %v", got, ok, fetchedAt)
	}
	if got, want := counter(t, a.snap, "xray_exporter_config_reload_total"), 0.0; got != want {
		t.Errorf("reload_total = %v, want %v: a failed fetch is not a reload", got, want)
	}
	if got, want := counter(t, a.snap, "xray_exporter_config_reload_errors_total"), 0.0; got != want {
		t.Errorf("reload_errors_total = %v, want %v", got, want)
	}

	out := logs.String()
	if got, want := strings.Count(out, "level=ERROR"), 1; got != want {
		t.Errorf("ERROR lines = %d, want %d fail-closed line:\n%s", got, want, out)
	}
	for _, want := range []string{"config fetch failed", "configs=1", "unexpected status"} {
		if !strings.Contains(out, want) {
			t.Errorf("log does not contain %q:\n%s", want, out)
		}
	}
}

func TestRefreshRecoveryIdenticalSet(t *testing.T) {
	body := panelEntries(freedomEntry("local"))
	a, p := refreshApp(t, testDiscardLogger(), body)
	defer a.interruptCycle()

	p.set(http.StatusInternalServerError, "")
	a.refresh(context.Background())
	if got := len(a.instances()); got != 0 {
		t.Fatalf("instances = %d after the failed fetch, want 0", got)
	}

	p.set(http.StatusOK, body)
	a.refresh(context.Background())

	if got := len(a.instances()); got != 1 {
		t.Fatalf("instances = %d, want the identical set started again", got)
	}
	if running, declared := configRunning(t, a.snap, "local"); !declared || !running {
		t.Errorf("config local running = %v (declared %v), want it started again", running, declared)
	}
	if got, want := counter(t, a.snap, "xray_exporter_config_reload_total"), 0.0; got != want {
		t.Errorf("reload_total = %v, want %v: recovery is not a changed set", got, want)
	}
	if got, want := counter(t, a.snap, "xray_exporter_config_reload_errors_total"), 0.0; got != want {
		t.Errorf("reload_errors_total = %v, want %v", got, want)
	}
	if got, want := counter(t, a.snap, "xray_exporter_config_fetch_errors_total"), 1.0; got != want {
		t.Errorf("fetch_errors_total = %v, want %v", got, want)
	}
}

func TestRefreshChangedSetStartFailureCountsReloadNotError(t *testing.T) {
	var logs bytes.Buffer
	a, p := refreshApp(t, slog.New(slog.NewTextHandler(&logs, nil)), panelEntries(freedomEntry("local")))
	defer a.interruptCycle()

	// "x" is not a config object: it is named config-1 and cannot start.
	p.set(http.StatusOK, panelEntries(`"x"`, freedomEntry("ok")))
	a.refresh(context.Background())

	if running, declared := configRunning(t, a.snap, "config-1"); !declared {
		t.Fatal("config-1 is not declared, want it kept with running=0")
	} else if running {
		t.Error("config-1 running = true, want false: it cannot start")
	}
	if running, declared := configRunning(t, a.snap, "ok"); !declared || !running {
		t.Errorf("config ok running = %v (declared %v), want a started config", running, declared)
	}
	if got, want := counter(t, a.snap, "xray_exporter_config_reload_total"), 1.0; got != want {
		t.Errorf("reload_total = %v, want %v: the set changed even though one start failed", got, want)
	}
	if got, want := counter(t, a.snap, "xray_exporter_config_reload_errors_total"), 0.0; got != want {
		t.Errorf("reload_errors_total = %v, want %v: a failed start is a result, not a reload error", got, want)
	}

	out := logs.String()
	for _, want := range []string{"xray instance failed to start", "subsystem=xray", "config=config-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("log does not contain %q:\n%s", want, out)
		}
	}
	if got, want := strings.Count(out, "level=WARN msg=\"xray instance failed to start\""), 1; got != want {
		t.Errorf("start failures logged at WARN %d times, want %d at the panel policy level:\n%s", got, want, out)
	}
}

func TestRefreshEmptyArrayStopsAllWithoutFetchError(t *testing.T) {
	a, p := refreshApp(t, testDiscardLogger(), panelEntries(freedomEntry("local")))
	defer a.interruptCycle()

	before, ok := snapValue(t, a.snap, "xray_exporter_config_fetch_timestamp_seconds")
	if !ok {
		t.Fatal("the startup fetch has no timestamp")
	}

	p.set(http.StatusOK, "[]")
	a.refresh(context.Background())

	if configs := a.snap.Export().Configs; len(configs) != 0 {
		t.Errorf("configs = %+v, want none after an empty set", configs)
	}
	if got := len(a.instances()); got != 0 {
		t.Errorf("instances = %d, want 0", got)
	}
	if got, want := counter(t, a.snap, "xray_exporter_config_fetch_errors_total"), 0.0; got != want {
		t.Errorf("fetch_errors_total = %v, want %v: an empty array is a successful set", got, want)
	}
	if got, want := counter(t, a.snap, "xray_exporter_config_reload_total"), 1.0; got != want {
		t.Errorf("reload_total = %v, want %v", got, want)
	}
	if got, want := counter(t, a.snap, "xray_exporter_config_reload_errors_total"), 0.0; got != want {
		t.Errorf("reload_errors_total = %v, want %v", got, want)
	}
	after, ok := snapValue(t, a.snap, "xray_exporter_config_fetch_timestamp_seconds")
	if !ok || after <= before {
		t.Errorf("fetch timestamp = %v (present %v), want it past the previous success %v", after, ok, before)
	}
}

func TestRefreshUnchangedMissingInstanceRetries(t *testing.T) {
	a, _ := refreshApp(t, testDiscardLogger(), panelEntries(freedomEntry("a"), freedomEntry("b")))
	defer a.interruptCycle()

	if got := len(a.instances()); got != 2 {
		t.Fatalf("instances = %d, want both configs started", got)
	}
	keptPort := instancePort(a, "a")
	if keptPort == 0 {
		t.Fatal("config a has no instance")
	}

	// Drop b's instance without touching the config set.
	kept := make([]xraycfg.Config, 0, 1)
	for _, cfg := range a.configs {
		if cfg.Name == "a" {
			kept = append(kept, cfg)
		}
	}
	if _, stopped, errs := a.manager.Apply(kept); len(errs) != 0 || len(stopped) != 1 {
		t.Fatalf("dropping b: stopped = %v, errors = %v, want b stopped", stopped, errs)
	}

	a.refresh(context.Background())

	if got := len(a.instances()); got != 2 {
		t.Fatalf("instances = %d, want the missing instance restarted", got)
	}
	if got := instancePort(a, "a"); got != keptPort {
		t.Errorf("config a socks port = %d, want the running instance %d kept", got, keptPort)
	}
	if got := instancePort(a, "b"); got == 0 {
		t.Error("config b has no instance after the retry")
	}
	if running, declared := configRunning(t, a.snap, "b"); !declared || !running {
		t.Errorf("config b running = %v (declared %v), want it running again", running, declared)
	}
	if got, want := counter(t, a.snap, "xray_exporter_config_reload_total"), 0.0; got != want {
		t.Errorf("reload_total = %v, want %v: the set did not change", got, want)
	}
	if got, want := counter(t, a.snap, "xray_exporter_config_reload_errors_total"), 0.0; got != want {
		t.Errorf("reload_errors_total = %v, want %v", got, want)
	}
}

func testDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
