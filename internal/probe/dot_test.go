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
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

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
		// A client rejecting our certificate hangs up here; that is a verdict.
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

	// nil roots mean the system pool, which cannot contain the test CA.
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
