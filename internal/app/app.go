// Package app wires the exporter into a process: inputs, xray instances,
// measurement cycles, egress checks, /metrics, reloads and the one-shot report.
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
	// Once runs one cycle, writes the report and exits without serving.
	Once bool
	// Out is the report path; empty writes to stdout.
	Out string
	// Deadline bounds one config's cycle in once mode; zero means DefaultDeadline.
	Deadline time.Duration
	// Version is stamped into xray_build_info; empty means dev.
	Version string
}

// Run starts the exporter and blocks until ctx is cancelled or a signal
// arrives. Setup errors are fatal; a broken xray config is published as
// running=0.
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

// application is the process state. mu guards targets, manager, verifier and
// upstream; a reload interrupts the running cycle before writing them.
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

	// configs and seeded belong to the reload goroutine.
	configs []xraycfg.Config
	seeded  map[string]struct{}

	cycleMu  sync.Mutex
	cancelMu sync.Mutex
	cancel   context.CancelFunc
}

// newApplication loads the inputs and starts every instance. The egress
// baseline is fetched eagerly so a bad echo URL fails in the first log line.
func newApplication(ctx context.Context, env Env, opts Options, logger *slog.Logger) (*application, error) {
	targets, upstream, err := loadTargets(env.TargetsFile)
	if err != nil {
		return nil, err
	}
	cfgs, err := xraycfg.Discover(env.ConfigDir)
	if err != nil {
		return nil, err
	}

	// xray reads its geo assets from this variable itself; keep the parsed value
	// authoritative.
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

// close stops the cycle and instances, then releases the log file.
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

func (a *application) instances() []*xrayproc.Instance {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.manager.Instances()
}
