package probe

import (
	"context"
	"fmt"
	"net"
	"time"
)

// DirectDialer leaves through the host's own route and resolver. It is for the
// reachability controls only: a tunnel probe must never use it.
type DirectDialer struct {
	Timeout time.Duration
}

// DialContext opens a connection straight from this host.
func (d DirectDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: d.Timeout}
	return dialer.DialContext(ctx, network, addr)
}

// TCPResult is the outcome of one direct connect.
type TCPResult struct {
	Up  bool
	RTT time.Duration
	Err error
}

// ProbeTCP connects to addr and closes at once. It says nothing about what the
// peer would serve: it separates a blocked address or port from a blocked name
// or protocol.
func ProbeTCP(ctx context.Context, addr string, timeout time.Duration) TCPResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	conn, err := DirectDialer{Timeout: timeout}.DialContext(ctx, "tcp", addr)
	if err != nil {
		return TCPResult{Err: err}
	}
	rtt := time.Since(start)
	_ = conn.Close()
	return TCPResult{Up: true, RTT: rtt}
}

// ProbeSystemDNS resolves host with the host's own resolver, as an application
// on this machine would. Up means at least one address came back.
func ProbeSystemDNS(ctx context.Context, host string, timeout time.Duration) DNSResult {
	name := HostOnly(host)
	if net.ParseIP(name) != nil {
		return DNSResult{Err: fmt.Errorf("%w: %s", ErrIPLiteral, name)}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, name)
	if err != nil {
		return DNSResult{Err: err}
	}
	if len(addrs) == 0 {
		return DNSResult{Err: fmt.Errorf("dns: no addresses for %s", name)}
	}
	return DNSResult{Up: true, RTT: time.Since(start)}
}
