package sampler

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/netutil"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

// The load label values. A series exists only for the loads the snapshot
// seeded, and SetUDP ignores any other value, so these must match the seeding
// side exactly.
const (
	loadIdle   = "idle"
	loadLoaded = "loaded"
)

func (s *Sampler) cycle(ctx context.Context, job Job, in Inputs) {
	start := time.Now()
	if s.opts.Deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.opts.Deadline)
		defer cancel()
	}

	var attempted int64
	s.each(ctx, len(in.Targets), func(ctx context.Context, i int) {
		if s.measureTarget(ctx, job, in.Targets[i].Host) {
			atomic.AddInt64(&attempted, 1)
		}
	})
	s.each(ctx, len(in.Resolvers), func(ctx context.Context, i int) {
		s.measureResolver(ctx, job, in.Resolvers[i])
	})
	s.each(ctx, len(in.UDPTargets), func(ctx context.Context, i int) {
		s.measureUDP(ctx, job, in.UDPTargets[i], loadIdle, "")
	})
	if in.UnderLoadURL != "" {
		s.each(ctx, len(in.UDPTargets), func(ctx context.Context, i int) {
			s.measureUDP(ctx, job, in.UDPTargets[i], loadLoaded, in.UnderLoadURL)
		})
	}
	s.each(ctx, len(in.TCPTargets), func(ctx context.Context, i int) {
		s.measureTCP(ctx, job, in.TCPTargets[i])
	})

	s.snap.SetCycle(job.Name, time.Now(), time.Since(start), int(atomic.LoadInt64(&attempted)))
}

// measureTarget records DNS and HTTP together: the target timestamp means the
// pair completed. A target the deadline reached first stays seeded.
func (s *Sampler) measureTarget(ctx context.Context, job Job, host string) bool {
	if ctx.Err() != nil {
		return false
	}

	literal := isIPLiteral(host)
	var dns probe.DNSResult
	if !literal {
		dns = s.probers.DNS(ctx, netutil.LoopbackAddr(job.DNSPort), host, s.opts.ProbeTimeout)
		if ctx.Err() != nil {
			return true
		}
	}

	d := s.probers.Dialer(netutil.LoopbackAddr(job.SocksPort), s.opts.ProbeTimeout)
	r := s.probers.HTTP(ctx, d, host, s.opts.ProbeTimeout, nil)
	if ctx.Err() != nil {
		return true
	}

	at := time.Now()
	if literal {
		s.snap.SkipDNS(job.Name, host)
	} else {
		s.snap.SetDNS(job.Name, host, dns, at)
	}
	s.snap.SetHTTP(job.Name, host, r, at)
	return true
}

// measureResolver records each configured transport; a deadline mid-probe
// discards the result.
func (s *Sampler) measureResolver(ctx context.Context, job Job, resolver config.Resolver) {
	if ctx.Err() != nil {
		return
	}

	d := s.probers.Dialer(netutil.LoopbackAddr(job.SocksPort), s.opts.ResolverTimeout)
	if resolver.DoHURL != "" {
		r := s.probers.DoH(ctx, d, resolver.DoHURL, s.opts.ProbeDomain, s.opts.ResolverTimeout, nil)
		if ctx.Err() == nil {
			s.snap.SetResolver(job.Name, resolver.Name, metrics.TransportDoH, r, time.Now())
		}
	}
	if resolver.DoTHost != "" && ctx.Err() == nil {
		r := s.probers.DoT(ctx, d, resolver.DoTHost, s.opts.ProbeDomain, s.opts.ResolverTimeout, nil)
		if ctx.Err() == nil {
			s.snap.SetResolver(job.Name, resolver.Name, metrics.TransportDoT, r, time.Now())
		}
	}
}

// measureUDP records one STUN burst, idle or under load. A deadline mid-burst
// discards it like measureResolver does, and a loaded pass whose download moved
// no bytes is discarded too: an unloaded sample would read as no congestion.
// The discarded pass publishes nothing, so the previous sample and its
// timestamp stay and staleness remains visible.
func (s *Sampler) measureUDP(ctx context.Context, job Job, t config.UDPTarget, load, underLoadURL string) {
	if ctx.Err() != nil {
		return
	}

	timeout := t.TimeoutDur
	if timeout == 0 {
		timeout = s.opts.ProbeTimeout
	}

	var bytes int64
	stop := func() {}
	if load == loadLoaded {
		stop = s.startDownload(ctx, job, underLoadURL, &bytes)
	}

	r := s.probers.STUN(ctx, netutil.LoopbackAddr(job.SocksPort), t.Addr, t.Count, t.SpacingDur, timeout)
	stop()

	if ctx.Err() != nil {
		return
	}

	at := time.Now()
	if load == loadLoaded && bytes == 0 {
		s.snap.SetUDPLoad(job.Name, false, at)
		return
	}
	s.snap.SetUDP(job.Name, t.Name, load, r, at)
	if load == loadLoaded {
		s.snap.SetUDPLoad(job.Name, true, at)
	}
}

// startDownload keeps a bulk GET in flight for the whole burst: it restarts the
// download whenever the file ends early, so the burst is never left unloaded.
// Each loaded burst owns its download because the worker pool runs bursts in
// parallel. It returns once the download goroutine is committed to its first
// GET, so the burst starts with the transfer under way; the returned stop
// cancels the transfer and waits for the byte count.
func (s *Sampler) startDownload(ctx context.Context, job Job, url string, bytes *int64) (stop func()) {
	dctx, cancel := context.WithCancel(ctx)
	d := s.probers.Dialer(netutil.LoopbackAddr(job.SocksPort), s.opts.ProbeTimeout)

	started := make(chan struct{})
	var once sync.Once
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for dctx.Err() == nil {
			once.Do(func() { close(started) })
			n, err := s.probers.Download(dctx, d, url)
			atomic.AddInt64(bytes, n)
			if err != nil {
				// A dead URL is not worth retrying inside the burst.
				return
			}
			if n == 0 {
				// An empty body would restart forever, one socks connection per
				// iteration: stop instead of spinning.
				return
			}
		}
	}()

	select {
	case <-started:
	case <-dctx.Done():
	}

	return func() {
		cancel()
		wg.Wait()
	}
}

// measureTCP records a tunnel CONNECT; a deadline mid-probe discards the result.
func (s *Sampler) measureTCP(ctx context.Context, job Job, t config.TCPTarget) {
	if ctx.Err() != nil {
		return
	}

	d := s.probers.Dialer(netutil.LoopbackAddr(job.SocksPort), s.opts.ProbeTimeout)
	r := s.probers.TCP(ctx, d, t.Addr, s.opts.ProbeTimeout)
	if ctx.Err() == nil {
		s.snap.SetTCP(job.Name, t.Name, r, time.Now())
	}
}
