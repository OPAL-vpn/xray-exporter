package app

import (
	"context"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/sampler"
)

func (a *application) cycle(ctx context.Context, deadline time.Duration) {
	jobs := a.jobs()
	targets, resolvers, probeDomain := a.inputs()

	s := sampler.New(a.snap, sampler.Probers{}, sampler.Options{
		Workers:         a.env.Workers,
		ProbeTimeout:    a.env.ProbeTimeout,
		ResolverTimeout: a.env.ResolverTimeout,
		Deadline:        deadline,
		ProbeDomain:     probeDomain,
	})

	start := time.Now()
	s.Cycle(ctx, jobs, targets, resolvers)
	if ctx.Err() != nil {
		// A reload or shutdown cut this pass short. The configs it never reached
		// still carry the previous pass's records, so neither a completion nor an
		// overrun can be attributed to them.
		return
	}
	elapsed := time.Since(start)

	exp := a.snap.Export()
	a.logProbes(exp, jobs)
	for _, cfg := range exp.Configs {
		if hasJob(jobs, cfg.Name) && overran(cfg.Cycle, deadline) {
			a.logger.Warn("cycle overrun", "config", cfg.Name, "duration", cfg.Cycle.Duration)
		}
	}
	a.logger.Info("cycle complete", "duration", elapsed, "configs", len(jobs))
}

// logProbes reports the probe results the cycle left in the snapshot: one WARN
// per failed entity with the reason the probe retained, one DEBUG per success.
// This is the only place the reason behind an up=0 gauge becomes visible; the

func (a *application) logProbes(exp metrics.Export, jobs []sampler.Job) {
	for _, cfg := range exp.Configs {
		if !hasJob(jobs, cfg.Name) {
			continue
		}
		for _, target := range cfg.Targets {
			if target.HasDNS {
				a.logProbe("dns", cfg.Name, "target", target.Target, target.DNS.Up, target.DNS.Err)
			}
			if target.HasHTTP {
				a.logProbe("https", cfg.Name, "target", target.Target, target.HTTP.Up, target.HTTP.Err)
			}
		}
		for _, resolver := range cfg.Resolvers {
			for _, transport := range resolver.Transports {
				a.logProbe(transport.Transport, cfg.Name, "resolver", resolver.Name, transport.Result.Up, transport.Result.Err)
			}
		}
	}
}

// logProbe writes one probe outcome. kind names the measurement — the resolver
// transport for a resolver — so two probes of one entity stay distinguishable,

func (a *application) logProbe(kind, config, label, name string, up bool, err error) {
	attrs := []any{"config", config, "probe", kind, label, name}
	if up {
		a.logger.Debug("probe succeeded", attrs...)
		return
	}
	if err != nil {
		attrs = append(attrs, "err", err)
	}
	a.logger.Warn("probe failed", attrs...)
}

func (a *application) startCycle(ctx context.Context, deadline time.Duration) bool {
	if !a.cycleMu.TryLock() {
		a.logger.Warn("cycle skipped: still running")
		return false
	}

	cycleCtx, cancel := context.WithCancel(ctx)
	a.cancelMu.Lock()
	a.cancel = cancel
	a.cancelMu.Unlock()

	go func() {
		defer a.cycleMu.Unlock()
		defer cancel()
		a.cycle(cycleCtx, deadline)
	}()
	return true
}

// interruptCycle cancels a running cycle and waits for it to stop, so a reload
// sees no result from the configuration it is replacing. Results published
// before the interruption stay as the last measurement and the reload's

func (a *application) interruptCycle() {
	a.cancelMu.Lock()
	cancel := a.cancel
	a.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}

	a.cycleMu.Lock()
	a.cycleMu.Unlock()
}

// serve starts the HTTP listener and the signal loop. SIGHUP reloads; SIGTERM

func (a *application) jobs() []sampler.Job {
	instances := a.instances()
	jobs := make([]sampler.Job, 0, len(instances))
	for _, inst := range instances {
		jobs = append(jobs, sampler.Job{Name: inst.Name, SocksPort: inst.SocksPort, DNSPort: inst.DNSPort})
	}
	return jobs
}

func (a *application) inputs() ([]config.Target, []config.Resolver, string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.targets.Targets, a.targets.Resolvers, a.targets.DNSProbeDomain
}

// overran reports whether a config's cycle used its entire deadline, which

func hasJob(jobs []sampler.Job, name string) bool {
	for _, job := range jobs {
		if job.Name == name {
			return true
		}
	}
	return false
}
