package app

import (
	"context"
	"errors"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/egress"
	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
	"github.com/OPAL-vpn/xray-exporter/internal/xrayproc"
)

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
