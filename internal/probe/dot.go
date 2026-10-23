package probe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/miekg/dns"
)

// dotPort is the IANA port for DNS over TLS, RFC 7858.
const dotPort = "853"

// ProbeDoT queries domain over TLS to dotHost:853 through d, with dotHost as
// the SNI name: the name is resolved on the far side of the tunnel, which is
// the property being measured. roots overrides the system roots when non-nil.
// The exchange is length-prefixed as RFC 7858 requires.
func ProbeDoT(ctx context.Context, d Dialer, dotHost, domain string, timeout time.Duration, roots *x509.CertPool) ResolverResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	query := newQuery(hostOnly(domain))
	packed, err := query.Pack()
	if err != nil {
		return ResolverResult{Err: fmt.Errorf("dot query pack: %w", err)}
	}

	start := time.Now()

	addr := net.JoinHostPort(dotHost, dotPort)
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return ResolverResult{Err: fmt.Errorf("dot dial %s: %w", addr, err)}
	}
	defer raw.Close()

	if deadline, ok := ctx.Deadline(); ok {
		if err := raw.SetDeadline(deadline); err != nil {
			return ResolverResult{Err: fmt.Errorf("dot deadline: %w", err)}
		}
	}

	conn := tls.Client(raw, &tls.Config{ServerName: dotHost, RootCAs: roots})
	if err := conn.HandshakeContext(ctx); err != nil {
		return ResolverResult{Err: fmt.Errorf("dot handshake: %w", err)}
	}

	frame := make([]byte, 2+len(packed))
	binary.BigEndian.PutUint16(frame, uint16(len(packed)))
	copy(frame[2:], packed)
	if _, err := conn.Write(frame); err != nil {
		return ResolverResult{Err: fmt.Errorf("dot write: %w", err)}
	}

	var length [2]byte
	if _, err := io.ReadFull(conn, length[:]); err != nil {
		return ResolverResult{Err: fmt.Errorf("dot read: %w", err)}
	}
	replyBytes := make([]byte, binary.BigEndian.Uint16(length[:]))
	if _, err := io.ReadFull(conn, replyBytes); err != nil {
		return ResolverResult{Err: fmt.Errorf("dot read: %w", err)}
	}
	rtt := time.Since(start)

	var reply dns.Msg
	if err := reply.Unpack(replyBytes); err != nil {
		return ResolverResult{Err: fmt.Errorf("dot unpack: %w", err)}
	}

	return ResolverResult{Up: true, RTT: rtt}
}
