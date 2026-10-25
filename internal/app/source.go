package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/remnawave"
	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
)

// sourcePolicy is the behaviour that differs between a local directory and a
// Remnawave panel.
type sourcePolicy struct {
	// failClosed stops every config when a refresh fetch fails.
	failClosed bool
	// strictStartup makes a failed or empty first fetch fatal.
	strictStartup bool
	// startLogLevel is how a config that fails to start is logged.
	startLogLevel slog.Level
}

// newSource selects the config source named by cfg. configDir is the default
// directory when the block names no path.
func newSource(cfg *config.ExporterConfig, configDir string) (xraycfg.Source, sourcePolicy, error) {
	if cfg.XrayConfigSource == nil || cfg.XrayConfigSource.Type == "directory" {
		path := configDir
		if cfg.XrayConfigSource != nil && cfg.XrayConfigSource.Path != "" {
			path = cfg.XrayConfigSource.Path
		}
		return xraycfg.Directory{Path: path}, directoryPolicy(), nil
	}

	src, err := remnawave.New(cfg.XrayConfigSource.URL, cfg.XrayConfigSource.Interval)
	if err != nil {
		return nil, sourcePolicy{}, err
	}
	return src, remnawavePolicy(), nil
}

// directoryPolicy is hand-written configs: a broken deployment at startup, and a
// failed start is an operator error worth the ERROR level.
func directoryPolicy() sourcePolicy {
	return sourcePolicy{strictStartup: true, startLogLevel: slog.LevelError}
}

// remnawavePolicy is panel-provided configs: a failed fetch or start is the
// panel's state rather than a deployment error, so the exporter fails closed.
func remnawavePolicy() sourcePolicy {
	return sourcePolicy{failClosed: true, startLogLevel: slog.LevelWarn}
}

// fetchSource fetches one complete set, counting the attempt. A readable but
// empty directory is a successful empty set: it advances the success time and
// is not a fetch error.
func (a *application) fetchSource(ctx context.Context) ([]xraycfg.Config, error) {
	a.snap.IncFetch()
	cfgs, err := a.source.Fetch(ctx)
	if errors.Is(err, xraycfg.ErrNoConfigs) {
		a.snap.SetFetchSuccess(time.Now())
		return []xraycfg.Config{}, nil
	}
	if err != nil {
		a.snap.IncFetchError()
		return nil, err
	}
	a.snap.SetFetchSuccess(time.Now())
	return cfgs, nil
}

// initialConfigs fetches the first set. A directory source must yield at least
// one config; a Remnawave failure is soft unless the process runs once.
func (a *application) initialConfigs(ctx context.Context) ([]xraycfg.Config, error) {
	cfgs, err := a.fetchSource(ctx)
	if err != nil {
		if a.policy.strictStartup || a.opts.Once {
			return nil, err
		}
		a.logger.Error("config fetch failed; starting with no configs", "error", err)
		return nil, nil
	}
	// strictStartup is only set for a directory source, where an empty
	// directory is a broken deployment rather than an empty set.
	if len(cfgs) == 0 && a.policy.strictStartup {
		return nil, fmt.Errorf("%s: no configs: %w", a.source.(xraycfg.Directory).Path, xraycfg.ErrNoConfigs)
	}
	return cfgs, nil
}
