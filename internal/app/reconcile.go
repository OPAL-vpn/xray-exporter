package app

import (
	"context"
	"errors"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
	"github.com/OPAL-vpn/xray-exporter/internal/xrayproc"
)

// apply reconciles the manager against cfgs and logs what changed. A config
// that fails to start is a published result, logged at the source's level.
func (a *application) apply(cfgs []xraycfg.Config) (started, stopped []string, ok bool) {
	started, stopped, errs := a.manager.Apply(cfgs)
	for _, err := range errs {
		var startErr *xrayproc.StartError
		if errors.As(err, &startErr) {
			a.logger.Log(context.Background(), a.policy.startLogLevel, "xray instance failed to start",
				"subsystem", "xray", "config", startErr.Config, "error", startErr.Err)
			continue
		}
		a.logger.Error("xray config failed", "error", err)
	}
	if len(started) > 0 || len(stopped) > 0 {
		a.logger.Info("xray configs applied", "started", started, "stopped", stopped)
	}
	return started, stopped, len(errs) == 0
}

// seed declares every config and its entities. Failed starts stay declared with
// running=0, and configs removed from the directory lose their series.
func (a *application) seed(now time.Time) {
	a.mu.RLock()
	targets := a.targets
	a.mu.RUnlock()

	hosts := make([]string, len(targets.Targets))
	for i, target := range targets.Targets {
		hosts[i] = target.Host
	}

	running := make(map[string]string, len(a.configs))
	for _, inst := range a.instances() {
		running[inst.Name] = inst.Version
	}

	keep := make(map[string]struct{}, len(a.configs))
	for _, cfg := range a.configs {
		keep[cfg.Name] = struct{}{}
		a.snap.Seed(cfg.Name, hosts, targets.Resolvers, now)
	}
	for name, version := range running {
		a.snap.SetConfig(name, true, version)
	}
	for name := range keep {
		if _, ok := running[name]; !ok {
			a.snap.SetConfig(name, false, "")
		}
	}
	for name := range a.seeded {
		if _, ok := keep[name]; !ok {
			a.snap.Drop(name)
		}
	}
	a.seeded = keep
}
