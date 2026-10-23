// Package app wires the exporter's parts into a running process: it loads the
// environment and inputs, starts the xray instances, runs measurement cycles on
// a timer, verifies egress, serves /metrics and /healthz, reloads on SIGHUP and
// renders the one-shot report.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Anton-beep/xray-exporter/internal/config"
	"github.com/Anton-beep/xray-exporter/internal/egress"
	"github.com/Anton-beep/xray-exporter/internal/metrics"
	"github.com/Anton-beep/xray-exporter/internal/probe"
	"github.com/Anton-beep/xray-exporter/internal/report"
	"github.com/Anton-beep/xray-exporter/internal/sampler"
	"github.com/Anton-beep/xray-exporter/internal/xraycfg"
	"github.com/Anton-beep/xray-exporter/internal/xrayproc"
)

// Options are the command-line flags Run acts on.
type Options struct {
	// Once runs one measurement cycle, writes the report and returns instead of
	// serving. No listener is started in this mode.
	Once bool
	// Out is the one-shot report path. Empty writes the report to stdout.
	Out string
	// Deadline bounds one config's cycle in once mode. Once mode has no interval
	// to halve, so the operator supplies the bound. Zero means 5m.
	Deadline time.Duration
	// Version is the build version stamped into xray_build_info. Empty is dev.
	Version string
}

// Run starts the exporter and blocks until ctx is cancelled or a termination
// signal arrives. Every returned error is fatal: a rejected targets file, an
// unreadable config directory, a listener that cannot bind. A single broken
// xray config is not fatal; it is logged and published as running=0.
func Run(ctx context.Context, env Env, opts Options, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}

	a, err := newApplication(ctx, env, opts, logger)
	if err != nil {
		return err
	}
	defer a.close()

	if opts.Once {
		return a.once(ctx)
	}
	return a.serve(ctx)
}

// application is the process's state. targets, manager, verifier and upstream
// are read by a background cycle and written by a reload, so the mutex guards
// them; a reload interrupts the cycle before writing.
type application struct {
	env        Env
	opts       Options
	logger     *slog.Logger
	xrayLogger *slog.Logger
	logFile    io.Closer

	snap *metrics.Snapshot

	mu       sync.RWMutex
	targets  *config.Targets
	manager  *xrayproc.Manager
	verifier *egress.Verifier
	upstream xraycfg.Upstream

	// configs and seeded belong to the goroutine that runs the reload loop.
	configs []xraycfg.Config
	seeded  map[string]struct{}

	cycleMu  sync.Mutex
	cancelMu sync.Mutex
	cancel   context.CancelFunc
}

// newApplication loads the inputs and starts every xray instance. The egress
// baseline is fetched eagerly, so a misconfigured echo URL is visible in the
// first log line rather than at the first verdict.
func newApplication(ctx context.Context, env Env, opts Options, logger *slog.Logger) (*application, error) {
	targets, upstream, err := loadTargets(env.TargetsFile)
	if err != nil {
		return nil, err
	}
	cfgs, err := xraycfg.Discover(env.ConfigDir)
	if err != nil {
		return nil, err
	}

	// xray reads its geo assets from this variable itself; make the parsed value
	// authoritative so the exporter's own default is the one in effect.
	if err := os.Setenv("XRAY_LOCATION_ASSET", env.XrayAssetDir); err != nil {
		return nil, fmt.Errorf("XRAY_LOCATION_ASSET: %w", err)
	}
	xrayLogger, logFile, err := xrayLoggerFor(env, logger)
	if err != nil {
		return nil, err
	}

	a := &application{
		env:        env,
		opts:       opts,
		logger:     logger,
		xrayLogger: xrayLogger,
		logFile:    logFile,
		snap:       metrics.New(),
		targets:    targets,
		verifier:   egress.New(targets.EgressCheckURL, env.EgressTTL, env.ProbeTimeout),
		upstream:   upstream,
		configs:    cfgs,
		seeded:     make(map[string]struct{}, len(cfgs)),
	}
	a.manager = xrayproc.NewManager(upstream, env.XrayLogLevel, xrayLogger)
	a.snap.SetBuildInfo(opts.Version, runtime.Version())

	a.apply(cfgs)
	a.seed(time.Now())
	a.verifyEgress(ctx)
	return a, nil
}

// loadTargets reads the YAML and parses the resolver address the injected DNS
// inbound forwards to.
func loadTargets(path string) (*config.Targets, xraycfg.Upstream, error) {
	targets, err := config.Load(path)
	if err != nil {
		return nil, xraycfg.Upstream{}, err
	}
	upstream, err := xraycfg.UpstreamFrom(targets.DNSUpstream)
	if err != nil {
		return nil, xraycfg.Upstream{}, err
	}
	return targets, upstream, nil
}

// apply reconciles the manager against cfgs and logs what changed. Startup and
// reload share it so both report the same way. It returns the names it started
// and stopped, and whether every config was accepted.
func (a *application) apply(cfgs []xraycfg.Config) (started, stopped []string, ok bool) {
	started, stopped, errs := a.manager.Apply(cfgs)
	for _, err := range errs {
		// A config that failed to start already logged its name and xray's own
		// cause; only the failures the manager did not report belong here.
		var startErr *xrayproc.StartError
		if errors.As(err, &startErr) {
			continue
		}
		a.logger.Error("xray config failed", "error", err)
	}
	if len(started) > 0 || len(stopped) > 0 {
		a.logger.Info("xray configs applied", "started", started, "stopped", stopped)
	}
	return started, stopped, len(errs) == 0
}

// seed declares every discovered config and its entities in the snapshot. A
// config whose instance failed to start is still declared, with running=0, so
// its series exist instead of appearing only once it recovers. Configs removed
// from the directory lose their series here.
func (a *application) seed(now time.Time) {
	a.mu.RLock()
	targets := a.targets
	a.mu.RUnlock()

	hosts := make([]string, len(targets.Targets))
	for i, target := range targets.Targets {
		hosts[i] = target.Host
	}

	running := make(map[string]string, len(a.configs))
	for _, inst := range a.instances() {
		running[inst.Name] = inst.Version
	}

	keep := make(map[string]struct{}, len(a.configs))
	for _, cfg := range a.configs {
		keep[cfg.Name] = struct{}{}
		a.snap.Seed(cfg.Name, hosts, targets.Resolvers, now)
	}
	for name, version := range running {
		a.snap.SetConfig(name, true, version)
	}
	for name := range keep {
		if _, ok := running[name]; !ok {
			a.snap.SetConfig(name, false, "")
		}
	}
	for name := range a.seeded {
		if _, ok := keep[name]; !ok {
			a.snap.Drop(name)
		}
	}
	a.seeded = keep
}

// verifyEgress fetches the direct baseline once and then checks every running
// config's tunnel against it. A config whose instance is not running has no
// path to check, so its verdict is cleared instead of keeping the last green
// one from before it stopped.
func (a *application) verifyEgress(ctx context.Context) {
	a.mu.RLock()
	verifier := a.verifier
	a.mu.RUnlock()

	if _, err := verifier.Baseline(ctx); err != nil {
		a.logger.Warn("egress baseline unavailable", "error", err)
	}

	instances := make(map[string]*xrayproc.Instance, len(a.configs))
	for _, inst := range a.instances() {
		instances[inst.Name] = inst
	}

	for _, cfg := range a.configs {
		inst, running := instances[cfg.Name]
		if !running {
			a.snap.SetEgress(cfg.Name, egress.Result{}, time.Now())
			continue
		}

		result := verifier.Check(ctx, probe.NewSocksDialer(loopback(inst.SocksPort), a.env.ProbeTimeout))
		a.snap.SetEgress(inst.Name, result, time.Now())
		switch {
		case result.Verified:
			a.logger.Debug("egress verified", "config", inst.Name, "ip", result.IP)
		case result.Err != nil:
			a.logger.Warn("egress check failed", "config", inst.Name, "error", result.Err)
		default:
			a.logger.Error("egress not verified", "config", inst.Name, "ip", result.IP)
		}
	}
}

// cycle runs one measurement pass over every running instance and logs its
// duration. A config whose cycle used its whole deadline was cut short by it,
// which the sampler cannot report because it has no logger.
func (a *application) cycle(ctx context.Context, deadline time.Duration) {
	jobs := a.jobs()
	targets, resolvers, probeDomain := a.inputs()

	s := sampler.New(a.snap, sampler.Probers{}, sampler.Options{
		Workers:         a.env.Workers,
		ProbeTimeout:    a.env.ProbeTimeout,
		ResolverTimeout: a.env.ResolverTimeout,
		Deadline:        deadline,
		ProbeDomain:     probeDomain,
	})

	start := time.Now()
	s.Cycle(ctx, jobs, targets, resolvers)
	if ctx.Err() != nil {
		// A reload or shutdown cut this pass short. The configs it never reached
		// still carry the previous pass's records, so neither a completion nor an
		// overrun can be attributed to them.
		return
	}
	elapsed := time.Since(start)

	exp := a.snap.Export()
	a.logProbes(exp, jobs)
	for _, cfg := range exp.Configs {
		if hasJob(jobs, cfg.Name) && overran(cfg.Cycle, deadline) {
			a.logger.Warn("cycle overrun", "config", cfg.Name, "duration", cfg.Cycle.Duration)
		}
	}
	a.logger.Info("cycle complete", "duration", elapsed, "configs", len(jobs))
}

// logProbes reports the probe results the cycle left in the snapshot: one WARN
// per failed entity with the reason the probe retained, one DEBUG per success.
// This is the only place the reason behind an up=0 gauge becomes visible; the
// sampler keeps it in the snapshot and has no logger of its own.
func (a *application) logProbes(exp metrics.Export, jobs []sampler.Job) {
	for _, cfg := range exp.Configs {
		if !hasJob(jobs, cfg.Name) {
			continue
		}
		for _, target := range cfg.Targets {
			if target.HasDNS {
				a.logProbe("dns", cfg.Name, "target", target.Target, target.DNS.Up, target.DNS.Err)
			}
			if target.HasHTTP {
				a.logProbe("https", cfg.Name, "target", target.Target, target.HTTP.Up, target.HTTP.Err)
			}
		}
		for _, resolver := range cfg.Resolvers {
			for _, transport := range resolver.Transports {
				a.logProbe(transport.Transport, cfg.Name, "resolver", resolver.Name, transport.Result.Up, transport.Result.Err)
			}
		}
	}
}

// logProbe writes one probe outcome. probe names the measurement — the resolver
// transport for a resolver — so two probes of one entity stay distinguishable.
func (a *application) logProbe(probe, config, entity, name string, up bool, err error) {
	attrs := []any{"config", config, "probe", probe, entity, name}
	if up {
		a.logger.Debug("probe succeeded", attrs...)
		return
	}
	if err != nil {
		attrs = append(attrs, "err", err)
	}
	a.logger.Warn("probe failed", attrs...)
}

// startCycle runs one cycle in the background unless one is still running.
func (a *application) startCycle(ctx context.Context, deadline time.Duration) bool {
	if !a.cycleMu.TryLock() {
		a.logger.Warn("cycle skipped: still running")
		return false
	}

	cycleCtx, cancel := context.WithCancel(ctx)
	a.cancelMu.Lock()
	a.cancel = cancel
	a.cancelMu.Unlock()

	go func() {
		defer a.cycleMu.Unlock()
		defer cancel()
		a.cycle(cycleCtx, deadline)
	}()
	return true
}

// interruptCycle cancels a running cycle and waits for it to stop, so a reload
// sees no result from the configuration it is replacing. Results published
// before the interruption stay as the last measurement and the reload's
// immediate cycle replaces them.
func (a *application) interruptCycle() {
	a.cancelMu.Lock()
	cancel := a.cancel
	a.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}

	a.cycleMu.Lock()
	a.cycleMu.Unlock()
}

// serve starts the HTTP listener and the signal loop. SIGHUP reloads; SIGTERM
// and SIGINT stop the cycle, the listener and the instances.
func (a *application) serve(ctx context.Context) error {
	listener, err := net.Listen("tcp", a.env.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", a.env.ListenAddr, err)
	}

	server := &http.Server{Handler: a.handler(), ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	a.logger.Info("metrics server listening", "addr", listener.Addr().String())

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)

	ticker := time.NewTicker(a.env.Interval)
	defer ticker.Stop()

	// A first cycle at startup means the first scrape does not wait a whole
	// interval for measurements.
	a.startCycle(runCtx, a.env.Interval/2)

	for {
		select {
		case <-runCtx.Done():
			a.logger.Info("shutting down")
			return a.stop(server)
		case sig := <-signals:
			if sig == syscall.SIGHUP {
				a.logger.Info("reloading configuration")
				a.reload(runCtx)
				continue
			}
			a.logger.Info("shutting down", "signal", sig.String())
			cancel()
			return a.stop(server)
		case <-ticker.C:
			a.startCycle(runCtx, a.env.Interval/2)
		case err := <-serveErr:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			a.logger.Error("metrics server stopped", "error", err)
			cancel()
			a.stop(server)
			return fmt.Errorf("serve %s: %w", a.env.ListenAddr, err)
		}
	}
}

// handler builds the routes the listener serves: the Prometheus scrape endpoint
// over the snapshot and a liveness probe. It is separate from serve so the
// wiring can be exercised without binding a port.
func (a *application) handler() http.Handler {
	registry := prometheus.NewRegistry()
	registry.MustRegister(a.snap.Collector())

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})
	return mux
}

// stop ends the in-flight cycle, closes every xray instance and shuts the
// listener down with a 5 s bound.
func (a *application) stop(server *http.Server) error {
	a.interruptCycle()

	if err := a.manager.Close(); err != nil {
		a.logger.Error("closing xray instances", "error", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		a.logger.Warn("http server shutdown", "error", err)
	}
	return nil
}

// reload applies a SIGHUP. Each piece is reconciled independently: a rejected
// YAML file or config directory keeps its previous state and logs, and the
// rest of the reload still applies. A changed DNS upstream restarts every
// instance, because the injected dokodemo-door inbound carries the address.
func (a *application) reload(ctx context.Context) {
	a.interruptCycle()

	applied := true
	restartedAll := false

	if targets, upstream, err := loadTargets(a.env.TargetsFile); err != nil {
		applied = false
		a.logger.Error("reload: targets file rejected, keeping previous", "error", err)
	} else {
		a.mu.Lock()
		restartedAll = upstream != a.upstream
		a.upstream = upstream
		a.targets = targets
		a.verifier = egress.New(targets.EgressCheckURL, a.env.EgressTTL, a.env.ProbeTimeout)
		a.mu.Unlock()

		if restartedAll {
			a.mu.Lock()
			previous := a.manager
			a.manager = xrayproc.NewManager(upstream, a.env.XrayLogLevel, a.xrayLogger)
			a.mu.Unlock()
			if err := previous.Close(); err != nil {
				a.logger.Error("closing xray instances", "error", err)
			}
		}
	}

	discovered, err := xraycfg.Discover(a.env.ConfigDir)
	switch {
	case errors.Is(err, xraycfg.ErrNoConfigs):
		// The directory was read and is empty, so it is authoritative: every
		// config left it, and their instances and series go with them.
		a.configs = nil
		a.logger.Info("reload: config directory empty, stopping every config")
	case err != nil:
		applied = false
		a.logger.Error("reload: config directory rejected, keeping previous", "error", err)
	default:
		a.configs = discovered
	}

	_, stopped, accepted := a.apply(a.configs)
	if !accepted {
		applied = false
	}

	// A measurement belongs to the instance that produced it. A stopped
	// instance either left the directory or was restarted against a different
	// tunnel, so its values describe a path that no longer exists and are
	// dropped instead of being published for the new one. An unchanged config
	// keeps its last values: they still describe the live tunnel, and the
	// immediate cycle below refreshes them.
	if restartedAll {
		for name := range a.seeded {
			a.snap.Drop(name)
		}
	} else {
		for _, name := range stopped {
			a.snap.Drop(name)
		}
	}

	a.seed(time.Now())
	a.verifyEgress(ctx)

	if applied {
		a.snap.IncReload()
	} else {
		a.snap.IncReloadError()
	}
	a.logger.Info("configuration reloaded", "configs", len(a.configs), "applied", applied)

	a.startCycle(ctx, a.env.Interval/2)
}

// once runs a single cycle, writes the report and fails unless every config is
// running, every egress is verified and every cycle completed.
func (a *application) once(ctx context.Context) error {
	deadline := a.opts.Deadline
	if deadline <= 0 {
		deadline = 5 * time.Minute
	}

	a.cycle(ctx, deadline)

	if err := a.writeReport(); err != nil {
		return err
	}
	if reasons := unhealthy(a.snap.Export(), deadline); len(reasons) > 0 {
		return fmt.Errorf("one-shot check failed: %s", strings.Join(reasons, "; "))
	}
	return nil
}

// writeReport renders the snapshot to opts.Out, or to stdout when it is empty.
// A file is created mode 0600; an existing file is narrowed to it, because the
// flag promises the mode, not just the creation mode.
func (a *application) writeReport() error {
	exp := a.snap.Export()
	if a.opts.Out == "" {
		return report.Render(os.Stdout, exp)
	}

	f, err := os.OpenFile(a.opts.Out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", a.opts.Out, err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("chmod %s: %w", a.opts.Out, err)
	}
	if err := report.Render(f, exp); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// close releases the process's resources: any running cycle, the xray
// instances and the mirrored log file, in that order.
func (a *application) close() {
	a.interruptCycle()

	if err := a.manager.Close(); err != nil {
		a.logger.Error("closing xray instances", "error", err)
	}
	if a.logFile != nil {
		if err := a.logFile.Close(); err != nil {
			a.logger.Warn("closing log file", "error", err)
		}
	}
}

// instances is the running instances under the state lock.
func (a *application) instances() []*xrayproc.Instance {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.manager.Instances()
}

// jobs is the sampler's view of the running instances.
func (a *application) jobs() []sampler.Job {
	instances := a.instances()
	jobs := make([]sampler.Job, 0, len(instances))
	for _, inst := range instances {
		jobs = append(jobs, sampler.Job{Name: inst.Name, SocksPort: inst.SocksPort, DNSPort: inst.DNSPort})
	}
	return jobs
}

// inputs is the targets and resolvers a cycle measures.
func (a *application) inputs() ([]config.Target, []config.Resolver, string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.targets.Targets, a.targets.Resolvers, a.targets.DNSProbeDomain
}

// overran reports whether a config's cycle used its entire deadline, which
// means the deadline cut it short rather than the probes finishing.
func overran(cycle metrics.CycleExport, deadline time.Duration) bool {
	return cycle.Done && deadline > 0 && cycle.Duration >= deadline
}

// unhealthy lists why a one-shot run exits non-zero. A cycle the deadline cut
// short counts as incomplete: the sampler stamps every pass it started as done,
// so only the duration against the deadline tells a truncated pass from a
// finished one — and a pre-deploy gate must not pass on a truncated one.
func unhealthy(exp metrics.Export, deadline time.Duration) []string {
	if len(exp.Configs) == 0 {
		return []string{"no configs"}
	}

	var reasons []string
	for _, cfg := range exp.Configs {
		if !cfg.Running {
			reasons = append(reasons, cfg.Name+" not running")
		}
		if !cfg.Egress.Verified {
			reasons = append(reasons, cfg.Name+" egress not verified")
		}
		if !cfg.Cycle.Done || overran(cfg.Cycle, deadline) {
			reasons = append(reasons, cfg.Name+" cycle did not complete")
		}
	}
	return reasons
}

// hasJob reports whether a cycle just measured the named config.
func hasJob(jobs []sampler.Job, name string) bool {
	for _, job := range jobs {
		if job.Name == name {
			return true
		}
	}
	return false
}

// loopback renders a job port as an address on the loopback interface.
func loopback(port int) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

// xrayLoggerFor returns the logger xray's stream is routed through, mirroring
// the same lines into XRAY_LOG_FILE when it is set. One file holds the whole
// process's stream because xray's log carries no instance identity.
func xrayLoggerFor(env Env, logger *slog.Logger) (*slog.Logger, io.Closer, error) {
	if env.XrayLogFile == "" {
		return logger, nil, nil
	}

	f, err := os.OpenFile(env.XrayLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, nil, fmt.Errorf("XRAY_LOG_FILE: %w", err)
	}
	mirrored := slog.New(mirrorHandler{
		console: logger.Handler(),
		// The bridge already applied XRAY_LOG_LEVEL, so the file keeps whatever
		// the process emits instead of filtering it a second time.
		file: slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}),
	})
	return mirrored, f, nil
}

// mirrorHandler writes every record to two handlers, the console and the
// XRAY_LOG_FILE mirror.
type mirrorHandler struct {
	console slog.Handler
	file    slog.Handler
}

func (m mirrorHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return m.console.Enabled(ctx, level) || m.file.Enabled(ctx, level)
}

func (m mirrorHandler) Handle(ctx context.Context, record slog.Record) error {
	var err error
	// Each handler keeps its own level filter: Enabled is asked once for the
	// pair, so Handle must not hand a record to a handler that refused it.
	if m.console.Enabled(ctx, record.Level) {
		err = m.console.Handle(ctx, record)
	}
	if m.file.Enabled(ctx, record.Level) {
		if fileErr := m.file.Handle(ctx, record); err == nil {
			err = fileErr
		}
	}
	return err
}

func (m mirrorHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return mirrorHandler{console: m.console.WithAttrs(attrs), file: m.file.WithAttrs(attrs)}
}

func (m mirrorHandler) WithGroup(name string) slog.Handler {
	return mirrorHandler{console: m.console.WithGroup(name), file: m.file.WithGroup(name)}
}
