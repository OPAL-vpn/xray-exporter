package app

import (
	"bytes"
	"context"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
	"github.com/OPAL-vpn/xray-exporter/internal/xrayproc"
)

// refreshLoop re-fetches the source every interval it reports. A source that
// never polls (a directory) parks instead of spinning, until ctx ends or a wake
// arrives.
func (a *application) refreshLoop(ctx context.Context) {
	for {
		d := a.source.Refresh()
		if d <= 0 {
			select {
			case <-ctx.Done():
				return
			case <-a.refreshWake:
			}
			continue
		}

		timer := time.NewTimer(d)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-a.refreshWake:
			timer.Stop()
		case <-timer.C:
			a.refresh(ctx)
		}
	}
}

// refresh reconciles the instances against one fetched set. An unchanged set
// is left alone: only a missing instance restarts it, and only a changed set
// interrupts the running cycle.
func (a *application) refresh(ctx context.Context) {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()

	next, err := a.fetchSource(ctx)
	if err != nil {
		if a.policy.failClosed {
			a.failClosed(time.Now(), err)
			return
		}
		a.logger.Error("config refresh failed, keeping previous", "error", err)
		return
	}

	now := time.Now()
	same := sameConfigs(a.configs, next)
	if same && a.allRunning(next) {
		a.logger.Debug("config refresh unchanged", "configs", len(next))
		return
	}
	if same {
		// The set did not change, so this is not a reload; the snapshot's running
		// flags are the only thing to repair.
		a.apply(next)
		a.seed(now)
		a.startCycleIfIdle(ctx, a.env.Interval/2)
		return
	}

	a.interruptCycle()
	a.configs = next
	_, stopped, accepted := a.apply(next)
	// A stopped instance's measurements describe a path that no longer exists.
	for _, name := range stopped {
		a.snap.Drop(name)
	}
	a.seed(now)
	a.startCycle(ctx, a.env.Interval/2)

	// A fetched set is authoritative: changing it is a reload even when one of
	// its configs fails to start, which is a result rather than a reload error.
	if a.policy.failClosed || accepted {
		a.snap.IncReload()
	} else {
		a.snap.IncReloadError()
	}
	a.logger.Info("config refresh applied", "configs", len(next), "stopped", stopped)
}

// failClosed stops every known config after a fetch failed. The names stay
// declared so running=0 is visible; their bytes are never started again.
func (a *application) failClosed(now time.Time, cause error) {
	a.interruptCycle()
	a.apply(nil)

	a.mu.RLock()
	targets := a.targets
	a.mu.RUnlock()

	hosts := make([]string, len(targets.Targets))
	for i, target := range targets.Targets {
		hosts[i] = target.Host
	}

	known := make(map[string]struct{}, len(a.configs))
	for _, cfg := range a.configs {
		a.snap.Drop(cfg.Name)
		a.snap.Seed(cfg.Name, hosts, targets.Resolvers, now)
		a.snap.SetConfig(cfg.Name, false, "")
		known[cfg.Name] = struct{}{}
	}
	a.seeded = known

	a.logger.Error("config fetch failed; stopped configs", "configs", len(a.configs), "error", cause)
}

// sameConfigs reports whether next carries the same names and bytes as current.
// Sources return sorted sets, so position is comparable.
func sameConfigs(current, next []xraycfg.Config) bool {
	if len(current) != len(next) {
		return false
	}
	for i := range current {
		if current[i].Name != next[i].Name || !bytes.Equal(current[i].Raw, next[i].Raw) {
			return false
		}
	}
	return true
}

// allRunning reports whether every config in cfgs has a running instance.
func (a *application) allRunning(cfgs []xraycfg.Config) bool {
	running := make(map[string]struct{}, len(cfgs))
	for _, inst := range a.instances() {
		running[inst.Name] = struct{}{}
	}
	for _, cfg := range cfgs {
		if _, ok := running[cfg.Name]; !ok {
			return false
		}
	}
	return true
}

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
