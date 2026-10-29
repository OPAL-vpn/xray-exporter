package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/egress"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
	"github.com/OPAL-vpn/xray-exporter/internal/sampler"
	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
)

const (
	// lookupInterval is how often the egress network is looked up. A provider
	// changes rarely, so this is a courtesy to ipinfo.io, not a tuning knob.
	lookupInterval = time.Hour
	// lookupRetry replaces it after a failed lookup, so a transient failure at
	// startup does not leave the network unknown for an hour.
	lookupRetry = time.Minute
)

// lookuper detects the egress network; tests replace it.
type lookuper interface {
	Lookup(ctx context.Context) (egress.Identity, error)
}

// reachabilitySpec turns the block into the snapshot's view of it, or nil when
// the config has none.
func reachabilitySpec(cfg *config.ExporterConfig) *metrics.ReachabilitySpec {
	if cfg.Reachability == nil {
		return nil
	}
	return &metrics.ReachabilitySpec{Controls: cfg.Reachability.Controls.All(), EntryTCP: cfg.Reachability.EntryTCP}
}

// reachabilityConfig returns the current block, nil when the feature is off.
func (a *application) reachabilityConfig() *config.Reachability {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.exporterConfig.Reachability
}

// wakeEgress makes the loop look up now; a full buffer drops the wake.
func (a *application) wakeEgress() {
	select {
	case a.egressWake <- struct{}{}:
	default:
	}
}

// egressLoop looks the egress network up at startup, on every reload and then
// every lookupInterval (lookupRetry after a failure). It never blocks the first
// probe cycle.
func (a *application) egressLoop(ctx context.Context, detector lookuper) {
	for {
		wait := lookupInterval
		if a.reachabilityConfig() != nil && !a.lookupEgress(ctx, detector) {
			wait = lookupRetry
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-a.egressWake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// lookupEgress runs one detection and reports whether it produced an answer. A
// failure keeps the last good identity.
func (a *application) lookupEgress(ctx context.Context, detector lookuper) bool {
	id, err := detector.Lookup(ctx)
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		a.logger.Warn("egress lookup failed", "error", err)
		return false
	}
	if previous, had := a.snap.SetIdentity(id); had && previous != id {
		a.logger.Warn("egress network changed", "as_name", id.ASName, "country", id.Country,
			"previous_as_name", previous.ASName, "previous_country", previous.Country)
	} else {
		a.logger.Info("egress network", "as_name", id.ASName, "country", id.Country)
	}
	return true
}

// probeDirect runs the reachability probes beside the tunnel probes, so a slow
// or blackholed route cannot delay them. The returned channel closes when both
// stages are done.
func (a *application) probeDirect(ctx context.Context, configs []string) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.probeControls(ctx)
		a.probeEntries(ctx, configs)
	}()
	return done
}

// probeControls runs the block's direct probes, bounded by the worker count.
// Like probeEntries, it sends traffic outside a tunnel.
func (a *application) probeControls(ctx context.Context) {
	rc := a.reachabilityConfig()
	if rc == nil {
		return
	}
	controls := rc.Controls.All()
	timeout := a.env.ControlTimeout

	sampler.Each(ctx, a.env.Workers, len(controls), func(ctx context.Context, i int) {
		c := controls[i]
		up, rtt, err := runControl(ctx, c, timeout)
		if ctx.Err() != nil {
			return
		}
		a.snap.SetControl(c.Kind, c.Name, up, rtt)
		attrs := []any{"kind", c.Kind, "control", c.Name, "class", c.Class}
		if up {
			a.logger.Debug("control succeeded", attrs...)
			return
		}
		a.logger.Warn("control failed", append(attrs, "error", err)...)
	})
}

func runControl(ctx context.Context, c config.Control, timeout time.Duration) (up bool, rtt time.Duration, err error) {
	switch c.Kind {
	case config.ControlHTTPS:
		res := probe.ProbeHTTPS(ctx, probe.DirectDialer{}, c.Target, timeout, nil)
		return res.Up, res.Total, res.Err
	case config.ControlTCP:
		res := probe.ProbeTCP(ctx, c.Target, timeout)
		return res.Up, res.RTT, res.Err
	case config.ControlDNS:
		res := probe.ProbeSystemDNS(ctx, c.Target, timeout)
		return res.Up, res.RTT, res.Err
	}
	return false, 0, fmt.Errorf("unknown control kind %q", c.Kind)
}

// probeEntries connects directly to each config's entry address when the block
// asks for it. A config with no known entry is skipped here and was logged when
// its address was read. The address never reaches a label or a log line.
func (a *application) probeEntries(ctx context.Context, configs []string) {
	rc := a.reachabilityConfig()
	if rc == nil || !rc.EntryTCP {
		return
	}
	timeout := a.env.ControlTimeout

	sampler.Each(ctx, a.env.Workers, len(configs), func(ctx context.Context, i int) {
		name := configs[i]
		addr, ok := a.entryAddr(name)
		if !ok {
			return
		}
		res := probe.ProbeTCP(ctx, addr, timeout)
		if ctx.Err() != nil {
			return
		}
		a.snap.SetEntry(name, res.Up, res.RTT)
		if res.Up {
			a.logger.Debug("entry reachable", "config", name)
			return
		}
		a.logger.Warn("entry unreachable", "config", name, "error", dialReason(res.Err))
	})
}

// dialReason keeps the cause of a failed connect and drops the address and host
// name, which the error text repeats.
func dialReason(err error) error {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return errors.New(dnsErr.Err)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		return opErr.Err
	}
	return errors.New("connect failed")
}

// entryAddr returns the entry address read for a config by recordEntries.
func (a *application) entryAddr(name string) (string, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	addr, ok := a.entryAddrs[name]
	return addr, ok
}

// recordEntries reads every config's entry address when entry_tcp is on, and
// logs each config that has none once, here, rather than on every cycle.
func (a *application) recordEntries(cfgs []xraycfg.Config) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.entryAddrs = nil
	if rc := a.exporterConfig.Reachability; rc == nil || !rc.EntryTCP {
		return
	}
	a.entryAddrs = make(map[string]string, len(cfgs))
	for _, cfg := range cfgs {
		addr, err := xraycfg.Entry(cfg.Raw)
		if err != nil {
			a.logger.Warn("entry address unknown, config not probed", "config", cfg.Name, "error", err)
			continue
		}
		a.entryAddrs[cfg.Name] = addr
	}
}
