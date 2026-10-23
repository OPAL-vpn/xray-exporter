// Package sampler runs one measurement cycle over a set of configs: every
// target through its tunnel, every configured resolver transport, all recorded
// into a metrics snapshot.
package sampler

import (
	"context"
	"crypto/x509"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
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
