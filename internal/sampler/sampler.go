// Package sampler runs one measurement cycle over a set of configs: every
// target through its tunnel, every configured resolver transport, all recorded
// into a metrics snapshot.
package sampler

import (
	"context"
	"crypto/x509"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/netutil"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

// Job is one config measured by a cycle: its metric label and the loopback
// ports its running xray instance listens on.
type Job struct {
	Name      string
	SocksPort int
	DNSPort   int
}

// Options bounds a cycle. Workers is the probe concurrency within one job;
// ProbeTimeout and ResolverTimeout bound a single probe; Deadline bounds a
// whole job's cycle. The app always passes half the interval as Deadline, so
// the zero case (no per-job deadline, parent context only) exists for tests.
type Options struct {
	Workers         int
	ProbeTimeout    time.Duration
	ResolverTimeout time.Duration
	Deadline        time.Duration
	ProbeDomain     string
}

// Probers are the measurement functions a cycle calls. A nil field in New is
// filled with the real implementation, so a test can replace only what it
// observes.
type Probers struct {
	HTTP func(ctx context.Context, d probe.Dialer, host string, timeout time.Duration, roots *x509.CertPool) probe.HTTPResult
	DNS  func(ctx context.Context, server, domain string, timeout time.Duration) probe.DNSResult
	DoH  func(ctx context.Context, d probe.Dialer, url, domain string, timeout time.Duration, roots *x509.CertPool) probe.ResolverResult
	DoT  func(ctx context.Context, d probe.Dialer, host, domain string, timeout time.Duration, roots *x509.CertPool) probe.ResolverResult
	// Dialer builds the dialer every probe leaves through. A test replaces it
	// to see the address a cycle dials; nil becomes probe.NewSocksDialer.
	Dialer DialerFactory
}

// DialerFactory builds a dialer for the tunnel's loopback socks address. The
// sampler calls it once per probe kind, so the timeout it passes is the one the
// probe is bounded by.
type DialerFactory func(addr string, timeout time.Duration) probe.Dialer

// Sampler measures jobs and records their results in a snapshot.
type Sampler struct {
	snap    *metrics.Snapshot
	probers Probers
	opts    Options
}

// New returns a sampler. Nil probers become the real implementations; they are
// passed system roots (nil) so a tunnel-injected CA is not trusted by accident.
func New(snap *metrics.Snapshot, p Probers, o Options) *Sampler {
	if p.HTTP == nil {
		p.HTTP = probe.ProbeHTTPS
	}
	if p.DNS == nil {
		p.DNS = probe.ProbeTunnelDNS
	}
	if p.DoH == nil {
		p.DoH = probe.ProbeDoH
	}
	if p.DoT == nil {
		p.DoT = probe.ProbeDoT
	}
	if p.Dialer == nil {
		p.Dialer = probe.NewSocksDialer
	}
	return &Sampler{snap: snap, probers: p, opts: o}
}

// Cycle measures every job, one job at a time. Within a job targets and then
// resolvers share a worker pool, and the job's own deadline bounds the whole
// thing. Results are published as they arrive, so an overrun still leaves the
// completed measurements visible.
func (s *Sampler) Cycle(ctx context.Context, jobs []Job, targets []config.Target, resolvers []config.Resolver) {
	for _, job := range jobs {
		s.cycle(ctx, job, targets, resolvers)
	}
}

// cycle measures one job's targets and resolvers.
func (s *Sampler) cycle(ctx context.Context, job Job, targets []config.Target, resolvers []config.Resolver) {
	start := time.Now()
	if s.opts.Deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.opts.Deadline)
		defer cancel()
	}

	var attempted int64
	s.each(ctx, len(targets), func(ctx context.Context, i int) {
		if s.measureTarget(ctx, job, targets[i].Host) {
			atomic.AddInt64(&attempted, 1)
		}
	})
	s.each(ctx, len(resolvers), func(ctx context.Context, i int) {
		s.measureResolver(ctx, job, resolvers[i])
	})

	s.snap.SetCycle(job.Name, time.Now(), time.Since(start), int(atomic.LoadInt64(&attempted)))
}

// measureTarget runs the DNS and HTTPS probes for one target. It reports
// whether the target was attempted; a target the deadline reached first is
// left at its seeded state rather than recorded as a failed probe. Both results
// are published together under one timestamp: the target's timestamp means the
// DNS+HTTPS pair completed, so advancing it on the DNS half alone would pair a
// fresh attempt with the previous cycle's status.
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

// measureResolver runs every transport the resolver is configured for. A
// deadline that lands mid-probe discards that result, like a target's.
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

// each runs fn over n indexes on a worker pool of Options.Workers goroutines.
func (s *Sampler) each(ctx context.Context, n int, fn func(context.Context, int)) {
	workers := s.opts.Workers
	if workers < 1 {
		workers = 1
	}
	if workers > n {
		workers = n
	}

	queue := make(chan int, n)
	for i := 0; i < n; i++ {
		queue <- i
	}
	close(queue)

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for i := range queue {
				fn(ctx, i)
			}
		}()
	}
	wg.Wait()
}

// isIPLiteral reports whether a target needs no DNS lookup. A configured target
// is a host with an optional path, and an IPv6 literal arrives bracketed, so
// probe.HostOnly settles the form before the address is parsed.
func isIPLiteral(host string) bool {
	return net.ParseIP(probe.HostOnly(host)) != nil
}
