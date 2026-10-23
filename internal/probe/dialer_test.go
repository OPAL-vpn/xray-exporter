package probe_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

// discardListener accepts and closes, giving the proxy a reachable target.
func discardListener(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen discard: %v", err)
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
}

func TestSocksDialerUsesDomainAddress(t *testing.T) {
	socks := newTestSocksServer(t, discardListener(t))
	dialer := probe.NewSocksDialer(socks.Addr(), 2*time.Second)

	conn, err := dialer.DialContext(context.Background(), "tcp", "example.test:443")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	conn.Close()

	requests := socks.Requests()
	if len(requests) != 1 {
		t.Fatalf("proxy saw %d CONNECT requests, want 1", len(requests))
	}
	got := requests[0]
	if got.ATYP != testATYPDomain {
		t.Errorf("address type = %#x, want domain (%#x); the name was resolved locally", got.ATYP, testATYPDomain)
	}
	if got.Host != "example.test" || got.Port != 443 {
		t.Errorf("target = %s:%d, want example.test:443", got.Host, got.Port)
	}
}

func TestSocksDialerSendsIPv4Literal(t *testing.T) {
	socks := newTestSocksServer(t, discardListener(t))
	dialer := probe.NewSocksDialer(socks.Addr(), 2*time.Second)

	conn, err := dialer.DialContext(context.Background(), "tcp", "127.0.0.1:8443")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	conn.Close()

	requests := socks.Requests()
	if len(requests) != 1 {
		t.Fatalf("proxy saw %d CONNECT requests, want 1", len(requests))
	}
	got := requests[0]
	if got.ATYP != testATYPIPv4 {
		t.Errorf("address type = %#x, want IPv4 (%#x)", got.ATYP, testATYPIPv4)
	}
	if got.Host != "127.0.0.1" || got.Port != 8443 {
		t.Errorf("target = %s:%d, want 127.0.0.1:8443", got.Host, got.Port)
	}
}

func TestSocksDialerErrorsOnRefused(t *testing.T) {
	socks := newTestSocksServer(t, "")
	socks.setFailCode(0x05)

	dialer := probe.NewSocksDialer(socks.Addr(), 2*time.Second)
	conn, err := dialer.DialContext(context.Background(), "tcp", "example.test:443")
	if err == nil {
		conn.Close()
		t.Fatal("DialContext succeeded, want error")
	}
	if !strings.Contains(err.Error(), "socks") {
		t.Errorf("error = %q, want it to mention socks", err)
	}
	if !strings.Contains(err.Error(), "5") {
		t.Errorf("error = %q, want the reply code", err)
	}
}
