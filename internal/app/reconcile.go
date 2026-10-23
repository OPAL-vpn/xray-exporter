package app

import (
	"context"
	"errors"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/egress"
	"github.com/OPAL-vpn/xray-exporter/internal/netutil"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
	"github.com/OPAL-vpn/xray-exporter/internal/xrayproc"
)

// apply reconciles the manager against cfgs and logs what changed.
func (a *application) apply(cfgs []xraycfg.Config) (started, stopped []string, ok bool) {
	started, stopped, errs := a.manager.Apply(cfgs)
	for _, err := range errs {
		// StartError already logged the failure; log only manager-level errors.
		var startErr *xrayproc.StartError
		if errors.As(err, &startErr) {
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

// verifyEgress checks every running config; a stopped config loses its verdict
// rather than keeping the last green one.
func (a *application) verifyEgress(ctx context.Context) {
	a.mu.RLock()
	verifier := a.verifier
	a.mu.RUnlock()

	if _, err := verifier.Baseline(ctx); err != nil {
		a.logger.Warn("egress baseline unavailable", "error", err)
	}

	instances := make(map[string]*xrayproc.Instance, len(a.configs))
	for _, inst := range a.instances() {
		instances[inst.Name] = inst
	}

	for _, cfg := range a.configs {
		inst, running := instances[cfg.Name]
		if !running {
			a.snap.SetEgress(cfg.Name, egress.Result{}, time.Now())
			continue
		}

		result := verifier.Check(ctx, probe.NewSocksDialer(netutil.LoopbackAddr(inst.SocksPort), a.env.ProbeTimeout))
		a.snap.SetEgress(inst.Name, result, time.Now())
		switch {
		case result.Verified:
			a.logger.Debug("egress verified", "config", inst.Name, "ip", result.IP)
		case result.Err != nil:
			a.logger.Warn("egress check failed", "config", inst.Name, "error", result.Err)
		default:
			a.logger.Error("egress not verified", "config", inst.Name, "ip", result.IP)
		}
	}
}
