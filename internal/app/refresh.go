package app

import (
	"bytes"
	"context"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
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

// wakeRefresh interrupts a parked refresh loop. A wake-up is advisory, so a
// full buffer drops it rather than blocking the reload.
func (a *application) wakeRefresh() {
	select {
	case a.refreshWake <- struct{}{}:
	default:
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
	stopped, _ := a.applySet(next, now)
	a.startCycle(ctx, a.env.Interval/2)
	a.logger.Info("config refresh applied", "configs", len(next), "stopped", stopped)
}

// failClosed stops every known config after a fetch failed. The names stay
// declared so running=0 is visible; their bytes are never started again.
func (a *application) failClosed(now time.Time, cause error) {
	a.interruptCycle()
	a.apply(nil)

	hosts, resolvers := a.declared()

	known := make(map[string]struct{}, len(a.configs))
	for _, cfg := range a.configs {
		a.snap.Drop(cfg.Name)
		a.snap.Seed(cfg.Name, hosts, resolvers, now)
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
