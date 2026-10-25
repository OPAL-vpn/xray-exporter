package app

import (
	"bytes"
	"context"
	"reflect"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
	"github.com/OPAL-vpn/xray-exporter/internal/xrayproc"
)

// refreshLoop re-fetches the source every interval it reports. A source that
// never polls (a directory) parks instead of spinning, until ctx ends or a wake
// arrives.
func (a *application) refreshLoop(ctx context.Context) {
	for {
		// A reload swaps the source, so its interval is read under the lock that
		// guards the swap.
		a.refreshMu.Lock()
		d := a.source.Refresh()
		a.refreshMu.Unlock()

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

	// Shutdown waits for a fetch already in flight, not for one ordered after it:
	// a cancelled run must not publish instances on a closed manager.
	if ctx.Err() != nil {
		return
	}

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
	// next tick.
	defer a.startCycle(ctx, a.env.Interval/2)

	a.interruptCycle()

	targets, upstream, err := loadExporterConfig(a.env.ConfigFile)
	if err != nil {
		a.logger.Error("reload: exporter config rejected, keeping previous", "error", err)
		a.snap.IncReloadError()
		return
	}

	// Build the new source before publishing anything, so a rejected block leaves
	// the inputs it replaced untouched.
	source, policy := a.source, a.policy
	sourceChanged := !reflect.DeepEqual(targets.XrayConfigSource, a.sourceConfig)
	if sourceChanged {
		if source, policy, err = newSource(targets, a.env.ConfigDir); err != nil {
			a.logger.Error("reload: config source rejected, keeping previous", "error", err)
			a.snap.IncReloadError()
			return
		}
	}

	restartedAll := a.applyInputs(targets, upstream)
	if sourceChanged {
		a.source = source
		a.policy = policy
		a.sourceConfig = targets.XrayConfigSource
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
			return
		}
		a.logger.Error("reload: config fetch failed, keeping previous", "error", err)
		if restartedAll {
			// The kept configs still carry the DNS upstream and the manager is new,
			// so start them rather than leave the exporter with no instances.
			a.apply(a.configs)
			a.seed(now)
		}
		return
	}

	a.configs = fetched
	if len(fetched) == 0 {
		// A readable but empty set is authoritative: every config left.
		a.logger.Info("reload: config source empty, stopping every config")
	}
	_, stopped, accepted := a.apply(fetched)
	// A stopped instance's measurements describe a path that no longer exists.
	for _, name := range stopped {
		a.snap.Drop(name)
	}
	a.seed(now)

	// A fetched set is authoritative: changing it is a reload even when one of
	// its configs fails to start. For a panel that is a result; a directory is
	// hand-written, so there it is a reload error.
	applied := a.policy.failClosed || accepted
	if applied {
		a.snap.IncReload()
	} else {
		a.snap.IncReloadError()
	}
	a.logger.Info("configuration reloaded", "configs", len(fetched), "applied", applied)
}

// applyInputs publishes the reloaded exporter config and reports whether the
// DNS upstream changed. A changed upstream replaces the manager, because the
// injected dokodemo-door inbound of every instance carries the address.
func (a *application) applyInputs(targets *config.ExporterConfig, upstream xraycfg.Upstream) bool {
	a.mu.Lock()
	restartedAll := upstream != a.upstream
	a.upstream = upstream
	a.targets = targets
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

// wakeRefresh interrupts a parked refresh loop. A wake-up is advisory, so a
// full buffer drops it rather than blocking the reload.
func (a *application) wakeRefresh() {
	select {
	case a.refreshWake <- struct{}{}:
	default:
	}
}
