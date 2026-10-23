//go:build integration

package tests

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
	"github.com/OPAL-vpn/xray-exporter/internal/xrayproc"
)

// testRecord is the address the local resolver answers with. It is in the
// documentation range, so no real answer can be mistaken for it.
const testRecord = "198.51.100.7"

// freedomConfig is a complete client config whose only outbound is freedom: the
// probe path is real, but no remote server is involved.
const freedomConfig = `{"outbounds":[{"protocol":"freedom","settings":{}}]}`

// testDNSServer serves testRecord for every A query on a loopback UDP port and
// returns that port. Answers are fixed, so a lookup that returns the record
// proves it reached this server — and therefore left through the injected
// dokodemo-door inbound rather than through a system resolver.
func testDNSServer(t *testing.T) int {
	t.Helper()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback UDP: %v", err)
	}

	started := make(chan struct{})
	server := &dns.Server{
		PacketConn: pc,
		Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
			reply := new(dns.Msg)
			reply.SetReply(r)
			if len(r.Question) > 0 && r.Question[0].Qtype == dns.TypeA {
				reply.Answer = append(reply.Answer, &dns.A{
					Hdr: dns.RR_Header{
						Name:   r.Question[0].Name,
						Rrtype: dns.TypeA,
						Class:  dns.ClassINET,
						Ttl:    60,
					},
					A: net.ParseIP(testRecord),
				})
			}
			_ = w.WriteMsg(reply)
		}),
		NotifyStartedFunc: func() { close(started) },
	}
	go func() { _ = server.ActivateAndServe() }()
	<-started
	t.Cleanup(func() {
		if err := server.Shutdown(); err != nil {
			t.Errorf("dns server shutdown: %v", err)
		}
	})

	return pc.LocalAddr().(*net.UDPAddr).Port
}

// testManagerFor starts one freedom instance against upstream and returns the
// manager that owns it.
func testManagerFor(t *testing.T, upstream xraycfg.Upstream, cfg xraycfg.Config) *xrayproc.Manager {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := xrayproc.NewManager(upstream, "warning", logger)
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("Manager.Close: %v", err)
		}
	})

	if _, _, errs := manager.Apply([]xraycfg.Config{cfg}); len(errs) != 0 {
		t.Fatalf("Apply: %v", errs)
	}
	return manager
}

// loopback renders an instance port as an address on the loopback interface.
func loopback(port int) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

// TestInjectedDNSInboundAnswersThroughTheTunnel pins the dokodemo-door inbound
// the exporter injects: a query sent to the instance's DNS port must reach the
// configured upstream and come back with its answer. Nothing else in the suite
// exercises that inbound, so a wiring defect would surface only at deployment.
func TestInjectedDNSInboundAnswersThroughTheTunnel(t *testing.T) {
	upstream := xraycfg.Upstream{Host: "127.0.0.1", Port: testDNSServer(t)}
	inst := testManagerFor(t, upstream, xraycfg.Config{Name: "freedom", Raw: []byte(freedomConfig)}).Instances()[0]

	addr := loopback(inst.DNSPort)
	const domain = "example.test"

	res := probe.ProbeTunnelDNS(context.Background(), addr, domain, 5*time.Second)
	if !res.Up {
		t.Fatalf("ProbeTunnelDNS(%s, %s): Up = false, Err = %v", addr, domain, res.Err)
	}

	// ProbeTunnelDNS reports up and round trip by design; the same port answers
	// a full exchange below, which is what proves the reply is the local record.
	client := &dns.Client{Net: "udp", Timeout: 5 * time.Second}
	query := new(dns.Msg)
	query.SetQuestion(dns.Fqdn(domain), dns.TypeA)
	reply, _, err := client.Exchange(query, addr)
	if err != nil {
		t.Fatalf("dns exchange through %s: %v", addr, err)
	}
	if len(reply.Answer) != 1 {
		t.Fatalf("answer = %v, want the local A record", reply.Answer)
	}
	record, ok := reply.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("answer = %T, want *dns.A", reply.Answer[0])
	}
	if got := record.A.String(); got != testRecord {
		t.Errorf("answer address = %s, want %s from the local upstream", got, testRecord)
	}
}
