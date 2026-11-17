package probe_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

func TestProbeTunnelTCPConnects(t *testing.T) {
	socks := newTestSocksServer(t, discardListener(t))
	dialer := probe.NewSocksDialer(socks.Addr(), 2*time.Second)

	res := probe.ProbeTunnelTCP(context.Background(), dialer, "example.test:443", 2*time.Second)
	if !res.Up || res.Err != nil {
		t.Fatalf("connect: %+v, want up", res)
	}
	if res.Connect <= 0 {
		t.Errorf("connect time = %v, want > 0", res.Connect)
	}

	requests := socks.Requests()
	if len(requests) != 1 {
		t.Fatalf("proxy saw %d CONNECT requests, want 1", len(requests))
	}
	if got := requests[0]; got.Host != "example.test" || got.Port != 443 {
		t.Errorf("target = %s:%d, want example.test:443", got.Host, got.Port)
	}
}

func TestProbeTunnelTCPRefused(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := listener.Addr().String()
	listener.Close()

	// No upstream override: the proxy dials the requested address itself and
	// answers with a failure reply when nothing listens there.
	socks := newTestSocksServer(t, "")
	dialer := probe.NewSocksDialer(socks.Addr(), 2*time.Second)

	res := probe.ProbeTunnelTCP(context.Background(), dialer, closed, 2*time.Second)
	if res.Up || res.Err == nil {
		t.Fatalf("closed port: %+v, want a failure with an error", res)
	}
	if !strings.Contains(res.Err.Error(), "socks connect") {
		t.Errorf("error = %q, want it to name the failed socks connect", res.Err)
	}
	if res.Connect != 0 {
		t.Errorf("connect time = %v on failure, want 0", res.Connect)
	}
}
