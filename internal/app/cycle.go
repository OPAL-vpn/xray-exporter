package app

import (
	"context"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/health"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/sampler"
)

// cycle runs one measurement pass and logs its result.
func (a *application) cycle(ctx context.Context, deadline time.Duration) {
	jobs := a.jobs()
	in, probeDomain := a.inputs()

	// The direct probes run beside the tunnel probes; the cycle waits for both.
	direct := a.probeDirect(ctx, jobNames(jobs))

	s := sampler.New(a.snap, sampler.Probers{}, sampler.Options{
		Workers:         a.env.Workers,
		ProbeTimeout:    a.env.ProbeTimeout,
		ResolverTimeout: a.env.ResolverTimeout,
		Deadline:        deadline,
		ProbeDomain:     probeDomain,
	})

	start := time.Now()
	s.Cycle(ctx, jobs, in)
	<-direct
	if ctx.Err() != nil {
		// A reload or shutdown cut this pass short; configs it never reached still
		// carry the previous pass's records.
		return
	}
	elapsed := time.Since(start)

	exp := a.snap.Export()
	a.logProbes(exp, jobs)
	for _, cfg := range exp.Configs {
		if hasJob(jobs, cfg.Name) && health.Overran(cfg.Cycle, deadline) {
			a.logger.Warn("cycle overrun", "config", cfg.Name, "duration", cfg.Cycle.Duration)
		}
	}
	a.logger.Info("cycle complete", "duration", elapsed, "configs", len(jobs))
}

// logProbes logs why each probe failed; this is the only place the error behind
// an up=0 gauge is visible.
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
		for _, udp := range cfg.UDPs {
			if !udp.Measured {
				continue
			}
			a.logProbe("udp", cfg.Name, "target", udp.Target, udp.Result.Up, udp.Result.Err, "load", udp.Load)
		}
		for _, tcp := range cfg.TCPs {
			if !tcp.Measured {
				continue
			}
			a.logProbe("tcp", cfg.Name, "target", tcp.Target, tcp.Result.Up, tcp.Result.Err)
		}
		if cfg.LoadUp != nil && !*cfg.LoadUp {
			a.logger.Warn("under-load download moved no bytes", "config", cfg.Name)
		}
	}
}

// logProbe logs one entity's outcome; extra attrs follow the entity's label
// and name.
func (a *application) logProbe(kind, config, label, name string, up bool, err error, extra ...any) {
	attrs := []any{"config", config, "probe", kind, label, name}
	attrs = append(attrs, extra...)
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

// startCycleIfIdle starts a pass only when none holds the cycle lock, so a
// refresh that repairs a missing instance reports no skip and interrupts no
// measurement.
func (a *application) startCycleIfIdle(ctx context.Context, deadline time.Duration) {
	if !a.cycleMu.TryLock() {
		return
	}
	a.cycleMu.Unlock()
	a.startCycle(ctx, deadline)
}

// interruptCycle cancels and waits for the running cycle, so a reload cannot
// publish results from the config it is replacing.
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

func (a *application) jobs() []sampler.Job {
	instances := a.instances()
	jobs := make([]sampler.Job, 0, len(instances))
	for _, inst := range instances {
		jobs = append(jobs, sampler.Job{Name: inst.Name, SocksPort: inst.SocksPort, DNSPort: inst.DNSPort})
	}
	return jobs
}

// inputs returns one cycle's configured targets; the probe domain stays a
// separate return because the sampler takes it as an option.
func (a *application) inputs() (sampler.Inputs, string) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	cfg := a.exporterConfig
	in := sampler.Inputs{
		Targets:    cfg.Targets,
		Resolvers:  cfg.Resolvers,
		UDPTargets: cfg.UDPTargets,
		TCPTargets: cfg.TCPTargets,
	}
	if cfg.UDPUnderLoad != nil {
		in.UnderLoadURL = cfg.UDPUnderLoad.URL
	}
	return in, cfg.DNSProbeDomain
}

func jobNames(jobs []sampler.Job) []string {
	names := make([]string, len(jobs))
	for i, job := range jobs {
		names[i] = job.Name
	}
	return names
}

func hasJob(jobs []sampler.Job, name string) bool {
	for _, job := range jobs {
		if job.Name == name {
			return true
		}
	}
	return false
}
