// Package app wires the exporter into a process: inputs, xray instances,
// measurement cycles, /metrics, reloads and the one-shot report.
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

	a, err := newApplication(env, opts, logger)
	if err != nil {
		return err
	}
	defer a.close()

	if opts.Once {
		return a.once(ctx)
	}
	return a.serve(ctx)
}

// application is the process state. mu guards exporterConfig, manager and
// upstream; a reload interrupts the running cycle before writing them.
type application struct {
	env        Env
	opts       Options
	logger     *slog.Logger
	xrayLogger *slog.Logger
	logFile    io.Closer

	snap *metrics.Snapshot

	source xraycfg.Source
	policy sourcePolicy
	// sourceConfig is the block that built source; a reload compares it to the
	// new one to decide whether the source must be rebuilt. refreshMu guards it.
	sourceConfig *config.XrayConfigSource

	mu             sync.RWMutex
	exporterConfig *config.ExporterConfig
	manager        *xrayproc.Manager
	upstream       xraycfg.Upstream

	// configs and seeded are the reconciled set and the names the snapshot
	// declares; they are written only while holding refreshMu.
	configs []xraycfg.Config
	seeded  map[string]struct{}

	// refreshMu serializes refreshes, which interrupt and restart the cycle.
	refreshMu sync.Mutex
	// refreshWake interrupts a parked refresh loop; buffered 1 so a wake-up is
	// never lost.
	refreshWake chan struct{}

	cycleMu  sync.Mutex
	cancelMu sync.Mutex
	cancel   context.CancelFunc
}

// newApplication loads the inputs, fetches the config set and starts every
// instance.
func newApplication(env Env, opts Options, logger *slog.Logger) (*application, error) {
	exporterConfig, upstream, err := loadExporterConfig(env.ConfigFile)
	if err != nil {
		return nil, err
	}
	source, policy, err := newSource(exporterConfig, env.ConfigDir)
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
		env:            env,
		opts:           opts,
		logger:         logger,
		xrayLogger:     xrayLogger,
		logFile:        logFile,
		snap:           metrics.New(),
		exporterConfig: exporterConfig,
		upstream:       upstream,
		source:         source,
		policy:         policy,
		sourceConfig:   exporterConfig.XrayConfigSource,
		seeded:         make(map[string]struct{}),
		refreshWake:    make(chan struct{}, 1),
	}
	a.manager = xrayproc.NewManager(upstream, env.XrayLogLevel, xrayLogger)
	a.snap.SetBuildInfo(opts.Version, runtime.Version())
	a.snap.SetSource(source.Kind())
	a.logger.Info("config source", "type", source.Kind(), "host", sourceHost(source))

	// The first fetch has no request of its own to cancel; the source bounds it.
	cfgs, err := a.initialConfigs(context.Background())
	if err != nil {
		a.close()
		return nil, err
	}

	a.configs = cfgs
	a.apply(cfgs)
	a.seed(time.Now())
	return a, nil
}

func loadExporterConfig(path string) (*config.ExporterConfig, xraycfg.Upstream, error) {
	exporterConfig, err := config.Load(path)
	if err != nil {
		return nil, xraycfg.Upstream{}, err
	}
	upstream, err := xraycfg.UpstreamFrom(exporterConfig.DNSUpstream)
	if err != nil {
		return nil, xraycfg.Upstream{}, err
	}
	return exporterConfig, upstream, nil
}

// close stops the cycle and instances, then releases the log file.
func (a *application) close() {
	a.interruptCycle()

	// A refresh in flight still applies instances; the manager must outlive it.
	a.refreshMu.Lock()
	a.refreshMu.Unlock()

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
