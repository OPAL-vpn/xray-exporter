package probe

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// ErrIPLiteral reports a DNS probe target that is already an address literal:
// there is nothing to look up, so the probe would measure a tautology.
var ErrIPLiteral = errors.New("ip literal")

// dnsContentType is the wire format DoH carries, RFC 8484.
const dnsContentType = "application/dns-message"

// dotPort is the IANA port for DNS over TLS, RFC 7858.
const dotPort = "853"

// maxDNSMessageSize bounds a read from the wire: a DNS message cannot be
// longer, so a larger read is not a message.
const maxDNSMessageSize = 65535

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

// ResolverResult is the outcome of one DoH or DoT probe.
type ResolverResult struct {
	// Up is true when the resolver returned a DNS answer for our query.
	Up bool
	// RTT is the round trip including connection setup, 0 when the resolver is
	// down.
	RTT time.Duration
	// Err is why the resolver is down, nil when Up.
	Err error
}

// ProbeDoH POSTs an A query for domain to dohURL through d. A resolver is up
// only when it answers with 200, an application/dns-message body, a message
// that unpacks, and our own message ID: an error page or a cached reply from
// another query is not a resolver. roots overrides the system roots when
// non-nil.
func ProbeDoH(ctx context.Context, d Dialer, dohURL, domain string, timeout time.Duration, roots *x509.CertPool) ResolverResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	query := newQuery(hostOnly(domain))
	packed, err := query.Pack()
	if err != nil {
		return ResolverResult{Err: fmt.Errorf("doh query pack: %w", err)}
	}

	client := HTTPClient(d, timeout, roots)
	defer client.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dohURL, bytes.NewReader(packed))
	if err != nil {
		return ResolverResult{Err: err}
	}
	req.Header.Set("Content-Type", dnsContentType)
	req.Header.Set("Accept", dnsContentType)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return ResolverResult{Err: err}
	}
	defer resp.Body.Close()

	// Reading the body to the end is what makes RTT the full exchange; the
	// request context bounds a body that never finishes.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDNSMessageSize))
	if err != nil {
		return ResolverResult{Err: fmt.Errorf("doh read: %w", err)}
	}
	rtt := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		return ResolverResult{Err: fmt.Errorf("doh status %d", resp.StatusCode)}
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, dnsContentType) {
		return ResolverResult{Err: fmt.Errorf("doh content type %q", contentType)}
	}

	var reply dns.Msg
	if err := reply.Unpack(body); err != nil {
		return ResolverResult{Err: fmt.Errorf("doh unpack: %w", err)}
	}
	if reply.Id != query.Id {
		return ResolverResult{Err: fmt.Errorf("doh id %d, want %d", reply.Id, query.Id)}
	}

	return ResolverResult{Up: true, RTT: rtt}
}

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

// newQuery builds a recursive A query for name. SetQuestion assigns a random
// message ID, which the DoH check then compares the answer against.
func newQuery(name string) *dns.Msg {
	query := new(dns.Msg)
	query.SetQuestion(dns.Fqdn(name), dns.TypeA)
	return query
}

// hostOnly returns the name a DNS query asks for: the host without its /path
// and without the brackets an IPv6 literal carries in a URL, so
// "example.com/health" is queried as "example.com" and "[::1]/x" as "::1".
func hostOnly(host string) string {
	host = strings.TrimSpace(host)
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	return host
}
