package app

import (
	"context"
	"errors"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
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

// declared returns the hosts and the resolvers every config's series carries.
func (a *application) declared() (hosts []string, resolvers []config.Resolver) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	exporterConfig := a.exporterConfig
	hosts = make([]string, len(exporterConfig.Targets))
	for i, target := range exporterConfig.Targets {
		hosts[i] = target.Host
	}
	return hosts, exporterConfig.Resolvers
}

// seed declares every config and its entities. Failed starts stay declared with
// running=0, and configs removed from the set lose their series.
func (a *application) seed(now time.Time) {
	hosts, resolvers := a.declared()

	running := make(map[string]string, len(a.configs))
	for _, inst := range a.instances() {
		running[inst.Name] = inst.Version
	}

	keep := make(map[string]struct{}, len(a.configs))
	for _, cfg := range a.configs {
		keep[cfg.Name] = struct{}{}
		a.snap.Seed(cfg.Name, hosts, resolvers, now)
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

// applySet makes fetched the reconciled set: it applies every config, removes
// the measurements of what stopped and declares the result. It reports the
// stopped names and whether the set counts as an applied reload. A fetched set
// is authoritative, so changing it is a reload even when one of its configs
// fails to start: for a panel that is a result, while a directory is
// hand-written and a failed start there is a reload error.
func (a *application) applySet(fetched []xraycfg.Config, now time.Time) (stopped []string, applied bool) {
	a.configs = fetched
	_, stopped, accepted := a.apply(fetched)
	// A stopped instance's measurements describe a path that no longer exists.
	for _, name := range stopped {
		a.snap.Drop(name)
	}
	a.seed(now)

	applied = a.policy.failClosed || accepted
	if applied {
		a.snap.IncReload()
	} else {
		a.snap.IncReloadError()
	}
	return stopped, applied
}
