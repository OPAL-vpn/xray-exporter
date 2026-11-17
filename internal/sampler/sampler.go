// Package sampler runs one measurement cycle over a set of configs and records
// the results in a metrics snapshot.
package sampler

import (
	"context"
	"crypto/x509"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

// Job is one config's metric label and loopback ports.
type Job struct {
	Name      string
	SocksPort int
	DNSPort   int
}

// Inputs are one cycle's configured measurement targets. UnderLoadURL adds the
// UDP loaded pass; empty disables it.
type Inputs struct {
	Targets      []config.Target
	Resolvers    []config.Resolver
	UDPTargets   []config.UDPTarget
	TCPTargets   []config.TCPTarget
	UnderLoadURL string
}

// Options bounds a cycle. Deadline zero means the parent context only.
type Options struct {
	Workers         int
	ProbeTimeout    time.Duration
	ResolverTimeout time.Duration
	Deadline        time.Duration
	ProbeDomain     string
}

// Probers are the measurement functions; nil fields use the real implementations.
type Probers struct {
	HTTP func(ctx context.Context, d probe.Dialer, host string, timeout time.Duration, roots *x509.CertPool) probe.HTTPResult
	DNS  func(ctx context.Context, server, domain string, timeout time.Duration) probe.DNSResult
	DoH  func(ctx context.Context, d probe.Dialer, url, domain string, timeout time.Duration, roots *x509.CertPool) probe.ResolverResult
	DoT  func(ctx context.Context, d probe.Dialer, host, domain string, timeout time.Duration, roots *x509.CertPool) probe.ResolverResult
	// STUN takes the socks address: the burst probe owns its UDP relay.
	STUN     func(ctx context.Context, socksAddr, target string, count int, spacing, timeout time.Duration) probe.UDPResult
	TCP      func(ctx context.Context, d probe.Dialer, addr string, timeout time.Duration) probe.TunnelTCPResult
	Download func(ctx context.Context, d probe.Dialer, url string) (int64, error)
	// Dialer builds the dialer every probe leaves through.
	Dialer DialerFactory
}

// DialerFactory builds a dialer for the tunnel's socks address.
type DialerFactory func(addr string, timeout time.Duration) probe.Dialer

// Sampler measures jobs into a snapshot.
type Sampler struct {
	snap    *metrics.Snapshot
	probers Probers
	opts    Options
}

// New fills nil probers with the real implementations. They use system roots,
// so a tunnel-injected CA is never trusted.
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
	if p.STUN == nil {
		p.STUN = probe.ProbeSTUN
	}
	if p.TCP == nil {
		p.TCP = probe.ProbeTunnelTCP
	}
	if p.Download == nil {
		p.Download = probe.DownloadBytes
	}
	if p.Dialer == nil {
		p.Dialer = probe.NewSocksDialer
	}
	return &Sampler{snap: snap, probers: p, opts: o}
}

// Cycle measures every job in turn; within a job, each pass runs through the
// worker pool in order. Results are published as they arrive.
func (s *Sampler) Cycle(ctx context.Context, jobs []Job, in Inputs) {
	for _, job := range jobs {
		s.cycle(ctx, job, in)
	}
}
