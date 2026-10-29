package app

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
	"github.com/OPAL-vpn/xray-exporter/internal/vantage"
	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
)

// lookupInterval is how often the egress network is looked up. A provider
// changes rarely, so this is a courtesy to ipinfo.io, not a tuning knob.
const lookupInterval = time.Hour

// lookuper detects the egress network; tests replace it.
type lookuper interface {
	Lookup(ctx context.Context) (vantage.Identity, error)
}

// newLookuper builds the detector; a test swaps it to avoid the network.
var newLookuper = func() lookuper { return vantage.New() }

// vantageSpec turns the block into the snapshot's view of it, or nil when the
// config has none.
func vantageSpec(cfg *config.ExporterConfig) *metrics.VantageSpec {
	if cfg.Vantage == nil {
		return nil
	}
	return &metrics.VantageSpec{Controls: cfg.Vantage.Controls.All(), EntryTCP: cfg.Vantage.EntryTCP}
}

// vantageConfig returns the current block, nil when the feature is off.
func (a *application) vantageConfig() *config.Vantage {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.exporterConfig.Vantage
}

// wakeVantage makes the loop look up now; a full buffer drops the wake.
func (a *application) wakeVantage() {
	select {
	case a.vantageWake <- struct{}{}:
	default:
	}
}

// vantageLoop looks the egress network up at startup, on every reload and then
// every lookupInterval. It never blocks the first probe cycle.
func (a *application) vantageLoop(ctx context.Context) {
	detector := newLookuper()
	ticker := time.NewTicker(lookupInterval)
	defer ticker.Stop()
	for {
		if a.vantageConfig() != nil {
			a.lookupVantage(ctx, detector)
		}
		select {
		case <-ctx.Done():
			return
		case <-a.vantageWake:
		case <-ticker.C:
		}
	}
}

// lookupVantage runs one detection. A failure keeps the last good identity.
func (a *application) lookupVantage(ctx context.Context, detector lookuper) {
	id, err := detector.Lookup(ctx)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		a.logger.Warn("egress lookup failed", "error", err)
		return
	}
	if before := a.snap.Export().Vantage; before != nil && before.HasIdentity && before.Identity != id {
		a.logger.Warn("egress network changed", "as_name", id.ASName, "country", id.Country,
			"previous_as_name", before.Identity.ASName, "previous_country", before.Identity.Country)
	} else {
		a.logger.Info("egress network", "as_name", id.ASName, "country", id.Country)
	}
	a.snap.SetIdentity(id)
}

// controls runs the direct probes of the vantage block, bounded by the worker
// count. With the entry probes they are the only traffic besides the config
// fetch and the lookup that leaves outside a tunnel.
func (a *application) controls(ctx context.Context) {
	v := a.vantageConfig()
	if v == nil {
		return
	}
	list := v.Controls.All()
	timeout := a.env.ControlTimeout

	run(ctx, a.env.Workers, len(list), func(i int) {
		c := list[i]
		up, rtt, err := runControl(ctx, c, timeout)
		if ctx.Err() != nil {
			return
		}
		a.snap.SetControl(c.Kind, c.Name, up, rtt, err)
		attrs := []any{"kind", c.Kind, "control", c.Name, "class", c.Class}
		if up {
			a.logger.Debug("control succeeded", attrs...)
			return
		}
		a.logger.Warn("control failed", append(attrs, "err", err)...)
	})
}

func runControl(ctx context.Context, c config.Control, timeout time.Duration) (bool, time.Duration, error) {
	switch c.Kind {
	case config.ControlHTTPS:
		res := probe.ProbeHTTPS(ctx, probe.DirectDialer{Timeout: timeout}, c.Target, timeout, nil)
		return res.Up, res.Total, res.Err
	case config.ControlTCP:
		res := probe.ProbeTCP(ctx, c.Target, timeout)
		return res.Up, res.RTT, res.Err
	default:
		res := probe.ProbeSystemDNS(ctx, c.Target, timeout)
		return res.Up, res.RTT, res.Err
	}
}

// entries connects directly to each config's entry address when the block asks
// for it. The address never reaches a label or a log line.
func (a *application) entries(ctx context.Context, names []string) {
	v := a.vantageConfig()
	if v == nil || !v.EntryTCP {
		return
	}
	timeout := a.env.ControlTimeout

	run(ctx, a.env.Workers, len(names), func(i int) {
		name := names[i]
		addr, ok := a.entryFor(name)
		if !ok {
			a.snap.SetEntry(name, false, 0, xraycfg.ErrNoEntry)
			a.logger.Warn("entry probe skipped", "config", name, "err", xraycfg.ErrNoEntry)
			return
		}
		res := probe.ProbeTCP(ctx, addr, timeout)
		if ctx.Err() != nil {
			return
		}
		if res.Up {
			a.snap.SetEntry(name, true, res.RTT, nil)
			a.logger.Debug("entry reachable", "config", name)
			return
		}
		reason := dialReason(res.Err)
		a.snap.SetEntry(name, false, 0, reason)
		a.logger.Warn("entry unreachable", "config", name, "err", reason)
	})
}

// dialReason keeps the cause of a failed connect and drops the address, which
// the error text repeats.
func dialReason(err error) error {
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err
	}
	return errors.New("connect failed")
}

// entryFor returns the entry address recorded for a config at seed time.
func (a *application) entryFor(name string) (string, bool) {
	a.entryMu.RLock()
	defer a.entryMu.RUnlock()
	addr, ok := a.entryAddrs[name]
	return addr, ok
}

// recordEntries parses every config's entry address; a config without one gets
// no entry and its probe reports the reason.
func (a *application) recordEntries(cfgs []xraycfg.Config) {
	addrs := make(map[string]string, len(cfgs))
	for _, cfg := range cfgs {
		if addr, err := xraycfg.Entry(cfg.Raw); err == nil {
			addrs[cfg.Name] = addr
		}
	}
	a.entryMu.Lock()
	a.entryAddrs = addrs
	a.entryMu.Unlock()
}

// run executes fn for 0..n-1 on at most workers goroutines.
func run(ctx context.Context, workers, n int, fn func(int)) {
	if n == 0 {
		return
	}
	workers = max(1, min(workers, n))
	queue := make(chan int, n)
	for i := 0; i < n; i++ {
		queue <- i
	}
	close(queue)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := range queue {
				if ctx.Err() != nil {
					return
				}
				fn(i)
			}
		}()
	}
	wg.Wait()
}
