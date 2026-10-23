package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/miekg/dns"
)

// ErrIPLiteral reports a DNS probe target that is already an address literal:
// there is nothing to look up, so the probe would measure a tautology.
var ErrIPLiteral = errors.New("ip literal")

// dnsUDPMessageSize is the read buffer for one tunnel lookup. The query carries
// no EDNS0 option, so the answer is a single small datagram; anything larger
// than this is not a reply to it.
const dnsUDPMessageSize = 4096

// DNSResult is the outcome of one tunnel DNS lookup.
type DNSResult struct {
	// Up is true when a DNS message came back and unpacked, whatever its rcode:
	// NXDOMAIN still proves the resolver answered.
	Up bool
	// RTT is the round trip to the reply, 0 when nothing answered.
	RTT time.Duration
	// Err is why the lookup did not produce an answer, nil when Up.
	Err error
}

// ProbeTunnelDNS sends an A query for domain to the UDP resolver at server and
// waits for the reply. server is the config's dokodemo-door inbound, so the
// query leaves through the tunnel. An address literal has nothing to resolve
// and is rejected before a packet is sent.
func ProbeTunnelDNS(ctx context.Context, server, domain string, timeout time.Duration) DNSResult {
	name := hostOnly(domain)
	if net.ParseIP(name) != nil {
		return DNSResult{Err: fmt.Errorf("%w: %s", ErrIPLiteral, name)}
	}

	query := newQuery(name)
	packed, err := query.Pack()
	if err != nil {
		return DNSResult{Err: fmt.Errorf("dns query pack: %w", err)}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "udp", server)
	if err != nil {
		return DNSResult{Err: fmt.Errorf("dns dial %s: %w", server, err)}
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return DNSResult{Err: fmt.Errorf("dns deadline: %w", err)}
		}
	}

	start := time.Now()
	if _, err := conn.Write(packed); err != nil {
		return DNSResult{Err: fmt.Errorf("dns write: %w", err)}
	}

	buf := make([]byte, dnsUDPMessageSize)
	n, err := conn.Read(buf)
	if err != nil {
		return DNSResult{Err: fmt.Errorf("dns read: %w", err)}
	}
	rtt := time.Since(start)

	var reply dns.Msg
	if err := reply.Unpack(buf[:n]); err != nil {
		return DNSResult{Err: fmt.Errorf("dns unpack: %w", err)}
	}

	return DNSResult{Up: true, RTT: rtt}
}
