package app

import (
	"context"
	"reflect"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
	"github.com/OPAL-vpn/xray-exporter/internal/xrayproc"
)

// reload re-reads the exporter config and the source it names, then reconciles
// the instances against one fetched set. A rejected input keeps its previous
// state. It is a sibling of refresh, not a caller: refreshMu serializes them,
// and the whole body runs under it because every input it publishes is read by
// a fetch.
func (a *application) reload(ctx context.Context) {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()

	// Shutdown waits for a reload already in flight, not for one ordered after
	// it: a cancelled run must not publish instances on a closed manager.
	if ctx.Err() != nil {
		return
	}

	// A reload always restarts the measurement, even when it rejects an input:
	// interrupting the cycle must not leave the exporter unmeasured until the
	// next tick. A fetch that failed closed is the exception: that state starts
	// no cycle until a fetch succeeds again.
	failedClosed := false
	defer func() {
		if failedClosed || ctx.Err() != nil {
			return
		}
		a.startCycle(ctx, a.env.Interval/2)
	}()

	a.interruptCycle()

	exporterConfig, upstream, err := loadExporterConfig(a.env.ConfigFile)
	if err != nil {
		a.logger.Error("reload: exporter config rejected, keeping previous", "error", err)
		a.snap.IncReloadError()
		return
	}

	// Build the new source before publishing anything, so a rejected block leaves
	// the inputs it replaced untouched.
	source, policy := a.source, a.policy
	sourceChanged := !reflect.DeepEqual(exporterConfig.XrayConfigSource, a.sourceConfig)
	if sourceChanged {
		if source, policy, err = newSource(exporterConfig, a.env.ConfigDir); err != nil {
			a.logger.Error("reload: config source rejected, keeping previous", "error", err)
			a.snap.IncReloadError()
			return
		}
	}

	restartedAll := a.applyInputs(exporterConfig, upstream)
	if sourceChanged {
		a.source = source
		a.policy = policy
		a.sourceConfig = exporterConfig.XrayConfigSource
		a.snap.SetSource(source.Kind())
		// A parked loop reads the old interval; wake it so the new source's own
		// interval is used from here on.
		a.wakeRefresh()
		a.logger.Info("reload: config source changed", "type", source.Kind())
	}
	// A replaced manager has no instances, and every measurement through the old
	// ones describes a path that no longer exists.
	if restartedAll {
		for name := range a.seeded {
			a.snap.Drop(name)
		}
	}

	now := time.Now()
	fetched, err := a.fetchSource(ctx)
	if err != nil {
		a.snap.IncReloadError()
		if a.policy.failClosed {
			a.failClosed(now, err)
			failedClosed = true
			return
		}
		a.logger.Error("reload: config fetch failed, keeping previous", "error", err)
		if restartedAll {
			// The kept configs still carry the DNS upstream and the manager is new,
			// so start them rather than leave the exporter with no instances.
			a.interruptCycle()
			a.apply(a.configs)
			a.seed(now)
		}
		return
	}

	if len(fetched) == 0 {
		// A readable but empty set is authoritative: every config left.
		a.logger.Info("reload: config source empty, stopping every config")
	}
	// The fetch can take up to its own timeout, so a ticker-started cycle may
	// have begun while the instances were replaced; interrupt it before applying
	// so a reload still leaves no cycle measuring the old set.
	a.interruptCycle()
	_, applied := a.applySet(fetched, now)
	a.logger.Info("configuration reloaded", "configs", len(fetched), "applied", applied)
}

// applyInputs publishes the reloaded exporter config and reports whether the
// DNS upstream changed. A changed upstream replaces the manager, because the
// injected dokodemo-door inbound of every instance carries the address.
func (a *application) applyInputs(exporterConfig *config.ExporterConfig, upstream xraycfg.Upstream) bool {
	a.mu.Lock()
	restartedAll := upstream != a.upstream
	a.upstream = upstream
	a.exporterConfig = exporterConfig
	a.snap.SetVantage(vantageSpec(exporterConfig))
	a.wakeVantage()
	var previous *xrayproc.Manager
	if restartedAll {
		previous = a.manager
		a.manager = xrayproc.NewManager(upstream, a.env.XrayLogLevel, a.xrayLogger)
	}
	a.mu.Unlock()

	if previous == nil {
		return false
	}
	if err := previous.Close(); err != nil {
		a.logger.Error("closing xray instances", "error", err)
	}
	return true
}
