// Package app wires the exporter's parts into a running process: it loads the
// environment and inputs, starts the xray instances, runs measurement cycles on
// a timer, verifies egress, serves /metrics and /healthz, reloads on SIGHUP and
// renders the one-shot report.
package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/egress"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
	"github.com/OPAL-vpn/xray-exporter/internal/xrayproc"
)

// DefaultDeadline is the one-shot deadline when Options.Deadline is zero.
const DefaultDeadline = 5 * time.Minute

// Options are the command-line flags Run acts on.
type Options struct {
	// Once runs one measurement cycle, writes the report and returns instead of
	// serving. No listener is started in this mode.
	Once bool
	// Out is the one-shot report path. Empty writes the report to stdout.
	Out string
	// Deadline bounds one config's cycle in once mode. Once mode has no interval
	// to halve, so the operator supplies the bound. Zero means DefaultDeadline.
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
