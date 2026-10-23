package sampler

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/netutil"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

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
