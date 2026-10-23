package probe_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/Anton-beep/xray-exporter/internal/probe"
)

const testDNSContentType = "application/dns-message"

// testUDPServer starts a UDP DNS server on a loopback port, counts every packet
// that arrives, and hands each one to handler when it is not nil. A nil handler
// is a resolver that never answers.
func testUDPServer(t *testing.T, handler dns.Handler) (addr string, received func() int) {
	t.Helper()

	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen test dns: %v", err)
	}

	var count atomic.Int32
	srv := &dns.Server{
		PacketConn: packetConn,
		Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
			count.Add(1)
			if handler != nil {
				handler.ServeDNS(w, req)
			}
		}),
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.ActivateAndServe()
	}()
	t.Cleanup(func() {
		_ = srv.Shutdown()
		<-done
	})

	return packetConn.LocalAddr().String(), func() int { return int(count.Load()) }
}

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

// readDNSQuery unpacks the request body of a DoH POST.
func readDNSQuery(t *testing.T, r *http.Request) *dns.Msg {
	t.Helper()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("read doh request body: %v", err)
		return nil
	}

	query := new(dns.Msg)
	if err := query.Unpack(body); err != nil {
		t.Errorf("unpack doh request body: %v", err)
		return nil
	}
	if len(query.Question) != 1 {
		t.Errorf("doh request has %d questions, want 1", len(query.Question))
		return nil
	}
	return query
}

func TestProbeTunnelDNSNXDOMAIN(t *testing.T) {
	addr, _ := testUDPServer(t, dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		if len(req.Question) != 1 {
			t.Errorf("query has %d questions, want 1", len(req.Question))
		} else if got := req.Question[0]; got.Name != "does-not-exist.test." || got.Qtype != dns.TypeA {
			t.Errorf("question = %s %s, want does-not-exist.test. A", got.Name, dns.TypeToString[got.Qtype])
		}
		if err := w.WriteMsg(new(dns.Msg).SetRcode(req, dns.RcodeNameError)); err != nil {
			t.Errorf("write nxdomain reply: %v", err)
		}
	}))

	res := probe.ProbeTunnelDNS(context.Background(), addr, "does-not-exist.test", time.Second)

	if !res.Up {
		t.Fatalf("Up = false, Err = %v, want true: NXDOMAIN is still an answer", res.Err)
	}
	if res.Err != nil {
		t.Errorf("Err = %v, want nil", res.Err)
	}
	if res.RTT <= 0 {
		t.Errorf("RTT = %v, want > 0", res.RTT)
	}
}

func TestProbeTunnelDNSAnswer(t *testing.T) {
	addr, _ := testUDPServer(t, dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		if err := w.WriteMsg(new(dns.Msg).SetReply(req)); err != nil {
			t.Errorf("write reply: %v", err)
		}
	}))

	res := probe.ProbeTunnelDNS(context.Background(), addr, "example.test", time.Second)

	if !res.Up {
		t.Fatalf("Up = false, Err = %v", res.Err)
	}
	if res.RTT <= 0 {
		t.Errorf("RTT = %v, want > 0", res.RTT)
	}
}

func TestProbeTunnelDNSTimeout(t *testing.T) {
	addr, _ := testUDPServer(t, nil)

	start := time.Now()
	res := probe.ProbeTunnelDNS(context.Background(), addr, "example.test", 100*time.Millisecond)

	if res.Up {
		t.Error("Up = true, want false when the resolver never answers")
	}
	if res.Err == nil {
		t.Error("Err = nil, want a timeout error")
	}
	if res.RTT != 0 {
		t.Errorf("RTT = %v, want 0 when nothing answered", res.RTT)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("probe took %v, want it bounded by the 100ms timeout", elapsed)
	}
}

func TestProbeTunnelDNSIPLiteral(t *testing.T) {
	cases := []struct {
		name   string
		domain string
	}{
		{name: "ipv4", domain: "1.1.1.1"},
		{name: "ipv4 with path", domain: "1.1.1.1/resolve"},
		{name: "ipv6", domain: "2606:4700:4700::1111"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr, received := testUDPServer(t, nil)

			res := probe.ProbeTunnelDNS(context.Background(), addr, tc.domain, time.Second)

			if res.Up {
				t.Error("Up = true, want false for an IP literal")
			}
			if !errors.Is(res.Err, probe.ErrIPLiteral) {
				t.Errorf("Err = %v, want ErrIPLiteral", res.Err)
			}

			time.Sleep(100 * time.Millisecond)
			if n := received(); n != 0 {
				t.Errorf("server received %d queries, want 0: an IP literal must be rejected before sending", n)
			}
		})
	}
}

func TestProbeDoH(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != testDNSContentType {
			t.Errorf("Content-Type = %q, want %q", got, testDNSContentType)
		}

		query := readDNSQuery(t, r)
		if query == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", testDNSContentType)
		_, _ = w.Write(dnsAnswer(t, query, "192.0.2.10"))
	}))
	defer srv.Close()

	dialer := &redirectDialer{}
	res := probe.ProbeDoH(context.Background(), dialer, srv.URL+"/dns-query", "example.test", 2*time.Second)

	if !res.Up {
		t.Fatalf("Up = false, Err = %v", res.Err)
	}
	if res.Err != nil {
		t.Errorf("Err = %v, want nil", res.Err)
	}
	if res.RTT <= 0 {
		t.Errorf("RTT = %v, want > 0", res.RTT)
	}
	if requests := dialer.Requests(); len(requests) != 1 || requests[0] != srv.Listener.Addr().String() {
		t.Errorf("dialer saw %v, want the DoH host %s", requests, srv.Listener.Addr())
	}
}

func TestProbeDoHRejectsBadResponses(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "server error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				query := readDNSQuery(t, r)
				if query == nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				// A well-formed answer under a 500: only the status keeps this down.
				w.Header().Set("Content-Type", testDNSContentType)
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write(dnsAnswer(t, query, "192.0.2.10"))
			},
		},
		{
			name: "non-dns content type",
			handler: func(w http.ResponseWriter, r *http.Request) {
				query := readDNSQuery(t, r)
				if query == nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				// A well-formed answer under the wrong type: only the type keeps
				// this down.
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write(dnsAnswer(t, query, "192.0.2.10"))
			},
		},
		{
			name: "unparsable body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				readDNSQuery(t, r)
				w.Header().Set("Content-Type", testDNSContentType)
				_, _ = w.Write([]byte("garbage"))
			},
		},
		{
			name: "mismatched id",
			handler: func(w http.ResponseWriter, r *http.Request) {
				query := readDNSQuery(t, r)
				if query == nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				query.Id++
				w.Header().Set("Content-Type", testDNSContentType)
				_, _ = w.Write(dnsAnswer(t, query, "192.0.2.10"))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			res := probe.ProbeDoH(context.Background(), &redirectDialer{}, srv.URL+"/dns-query", "example.test", 2*time.Second)

			if res.Up {
				t.Error("Up = true, want false")
			}
			if res.Err == nil {
				t.Error("Err = nil, want the reason the resolver is down")
			}
		})
	}
}

// testCertificate returns a self-signed certificate for names and a pool that
// trusts it.
func testCertificate(t *testing.T, names ...string) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: names[0]},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	for _, name := range names {
		if ip := net.ParseIP(name); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, name)
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create test certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse test certificate: %v", err)
	}

	roots := x509.NewCertPool()
	roots.AddCert(leaf)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, roots
}

// serveDoT answers one length-prefixed DNS query per accepted connection.
func serveDoT(t *testing.T, listener net.Listener) {
	t.Helper()

	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go handleDoT(t, conn)
	}
}

func handleDoT(t *testing.T, conn net.Conn) {
	t.Helper()
	defer conn.Close()

	var head [2]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		// A client that rejects our certificate hangs up here; that is a
		// probe verdict, not a server failure.
		return
	}

	body := make([]byte, binary.BigEndian.Uint16(head[:]))
	if _, err := io.ReadFull(conn, body); err != nil {
		t.Errorf("dot test server: read query: %v", err)
		return
	}

	query := new(dns.Msg)
	if err := query.Unpack(body); err != nil {
		t.Errorf("dot test server: unpack query: %v", err)
		return
	}
	if len(query.Question) != 1 || query.Question[0].Qtype != dns.TypeA {
		t.Errorf("dot question = %+v, want one A question", query.Question)
		return
	}

	answer := dnsAnswer(t, query, "192.0.2.10")
	frame := make([]byte, 2+len(answer))
	binary.BigEndian.PutUint16(frame, uint16(len(answer)))
	copy(frame[2:], answer)

	if _, err := conn.Write(frame); err != nil {
		t.Errorf("dot test server: write answer: %v", err)
	}
}

// startDoTServer serves DoT on a loopback port with cert and returns its
// address.
func startDoTServer(t *testing.T, cert tls.Certificate) string {
	t.Helper()

	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("listen test dot: %v", err)
	}
	t.Cleanup(func() { listener.Close() })
	go serveDoT(t, listener)

	return listener.Addr().String()
}

func TestProbeDoT(t *testing.T) {
	cert, roots := testCertificate(t, "dot.test")
	addr := startDoTServer(t, cert)

	dialer := &redirectDialer{target: addr}
	res := probe.ProbeDoT(context.Background(), dialer, "dot.test", "example.test", 2*time.Second, roots)

	if !res.Up {
		t.Fatalf("Up = false, Err = %v", res.Err)
	}
	if res.Err != nil {
		t.Errorf("Err = %v, want nil", res.Err)
	}
	if res.RTT <= 0 {
		t.Errorf("RTT = %v, want > 0", res.RTT)
	}

	requests := dialer.Requests()
	if len(requests) != 1 {
		t.Fatalf("dialer saw %d dials, want 1", len(requests))
	}
	if requests[0] != "dot.test:853" {
		t.Errorf("dial address = %q, want dot.test:853", requests[0])
	}
}

func TestProbeDoTUntrustedCertificate(t *testing.T) {
	cert, _ := testCertificate(t, "dot.test")
	addr := startDoTServer(t, cert)

	// nil roots mean the system pool, which cannot contain the test CA: a
	// resolver whose certificate does not verify is down, as in the HTTPS probe.
	res := probe.ProbeDoT(context.Background(), &redirectDialer{target: addr}, "dot.test", "example.test", 2*time.Second, nil)

	if res.Up {
		t.Error("Up = true, want false for an unverifiable certificate")
	}
	if res.Err == nil {
		t.Error("Err = nil, want the certificate error")
	}
}

func TestProbeDoTDown(t *testing.T) {
	cases := []struct {
		name   string
		target func(t *testing.T) string
	}{
		{
			name: "connection refused",
			target: func(t *testing.T) string {
				// Reserve a port, then close it, so nothing listens there.
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatalf("reserve port: %v", err)
				}
				addr := listener.Addr().String()
				listener.Close()
				return addr
			},
		},
		{
			name: "plain tcp",
			target: func(t *testing.T) string {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatalf("listen plain tcp: %v", err)
				}
				t.Cleanup(func() { listener.Close() })
				go func() {
					for {
						conn, err := listener.Accept()
						if err != nil {
							return
						}
						conn.Close()
					}
				}()
				return listener.Addr().String()
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := probe.ProbeDoT(context.Background(), &redirectDialer{target: tc.target(t)}, "dot.test", "example.test", time.Second, nil)

			if res.Up {
				t.Error("Up = true, want false")
			}
			if res.Err == nil {
				t.Error("Err = nil, want a transport error")
			}
			if res.RTT != 0 {
				t.Errorf("RTT = %v, want 0 when the resolver is down", res.RTT)
			}
		})
	}
}
