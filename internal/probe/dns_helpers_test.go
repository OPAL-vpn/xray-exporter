package probe_test

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/miekg/dns"
)

// redirectDialer is a direct Dialer that records every address the probe asked
// for and dials target instead when one is set. That lets a probe request its
// production address (dot.test:853) while the test serves a loopback listener.
type redirectDialer struct {
	target string

	mu       sync.Mutex
	requests []string
}

func (d *redirectDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d.mu.Lock()
	d.requests = append(d.requests, addr)
	d.mu.Unlock()

	var dialer net.Dialer
	if d.target != "" {
		return dialer.DialContext(ctx, network, d.target)
	}
	return dialer.DialContext(ctx, network, addr)
}

// Requests returns the addresses the probe asked for, in arrival order.
func (d *redirectDialer) Requests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.requests...)
}

// dnsAnswer packs a reply to query carrying one A record for addr.
func dnsAnswer(t *testing.T, query *dns.Msg, addr string) []byte {
	t.Helper()

	reply := new(dns.Msg)
	reply.SetReply(query)
	reply.Answer = append(reply.Answer, &dns.A{
		Hdr: dns.RR_Header{
			Name:   query.Question[0].Name,
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    60,
		},
		A: net.ParseIP(addr).To4(),
	})

	packed, err := reply.Pack()
	if err != nil {
		t.Fatalf("pack test answer: %v", err)
	}
	return packed
}
