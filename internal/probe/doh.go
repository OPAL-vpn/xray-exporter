package probe

import (
	"bytes"
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// dnsContentType is the DoH wire format, RFC 8484.
const dnsContentType = "application/dns-message"

// maxDNSMessageSize bounds a DNS message read from the wire.
const maxDNSMessageSize = 65535

// ProbeDoH POSTs an A query through d. A resolver is up only for a 200 with an
// application/dns-message body that unpacks and carries our message ID.
func ProbeDoH(ctx context.Context, d Dialer, dohURL, domain string, timeout time.Duration, roots *x509.CertPool) ResolverResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	query := newQuery(HostOnly(domain))
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

	// Read the body to the end so RTT covers the full exchange.
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
