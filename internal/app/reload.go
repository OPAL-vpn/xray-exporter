package app

import (
	"context"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/xrayproc"
)

// reload reconciles each input independently; a rejected file keeps its previous
// state. A changed DNS upstream restarts every instance because the injected
// dokodemo-door inbound carries the address.
func (a *application) reload(ctx context.Context) {
	a.interruptCycle()

	applied := true
	restartedAll := false

	if targets, upstream, err := loadExporterConfig(a.env.ConfigFile); err != nil {
		applied = false
		a.logger.Error("reload: exporter config rejected, keeping previous", "error", err)
	} else {
		a.mu.Lock()
		restartedAll = upstream != a.upstream
		a.upstream = upstream
		a.targets = targets
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

	fetched, err := a.fetchSource(ctx)
	switch {
	case err != nil:
		applied = false
		a.logger.Error("reload: config source rejected, keeping previous", "error", err)
	case len(fetched) == 0:
		// A readable but empty set is authoritative: every config left.
		a.configs = nil
		a.logger.Info("reload: config source empty, stopping every config")
	default:
		a.configs = fetched
	}

	_, stopped, accepted := a.apply(a.configs)
	if !accepted {
		applied = false
	}

	// A stopped or restarted instance's measurements describe a path that no
	// longer exists; an unchanged config keeps its last values.
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

	if applied {
		a.snap.IncReload()
	} else {
		a.snap.IncReloadError()
	}
	a.logger.Info("configuration reloaded", "configs", len(a.configs), "applied", applied)

	a.startCycle(ctx, a.env.Interval/2)
}
