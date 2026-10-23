package probe_test

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

// testUDPServer serves DNS on a loopback port; a nil handler never answers.
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
		{name: "bracketed ipv6", domain: "[2606:4700:4700::1111]"},
		{name: "bracketed ipv6 with path", domain: "[2606:4700:4700::1111]/health"},
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
