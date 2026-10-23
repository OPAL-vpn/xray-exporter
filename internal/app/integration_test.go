package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
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

// TestFreedomInstanceServesACycle pins the whole probe path the spec's
// verification section asks for: a freedom instance, a local HTTPS target and
// one measurement cycle that publishes the target's series with probe_up = 1,
// which means the HTTPS probe left through the injected socks inbound.
//
// The target is the loopback address and port of the test server: a target host
// has no port of its own, so this is the only way to point a cycle at a local
// server without binding 443.
func TestFreedomInstanceServesACycle(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	host := strings.TrimPrefix(target.URL, "https://")

	upstream := xraycfg.Upstream{Host: "127.0.0.1", Port: testDNSServer(t)}
	cfg := xraycfg.Config{Name: "freedom", Raw: []byte(freedomConfig)}
	manager := testManagerFor(t, upstream, cfg)

	a := &application{
		env: Env{
			Workers:         2,
			ProbeTimeout:    5 * time.Second,
			ResolverTimeout: 5 * time.Second,
		},
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		snap:    metrics.New(),
		targets: &config.Targets{Targets: []config.Target{{Host: host}}},
		manager: manager,
		configs: []xraycfg.Config{cfg},
	}
	a.snap.SetBuildInfo("test", "go-test")
	a.snap.Seed("freedom", []string{host}, nil, time.Now())
	a.snap.SetConfig("freedom", true, manager.Instances()[0].Version)

	a.cycle(context.Background(), time.Minute)

	exp := a.snap.Export()
	if len(exp.Configs) != 1 || len(exp.Configs[0].Targets) != 1 {
		t.Fatalf("snapshot = %+v, want one config with one target", exp)
	}
	row := exp.Configs[0].Targets[0]

	if got := gather(t, a.snap)["xray_probe_up,config=freedom,target="+host]; got != 1 {
		t.Fatalf("xray_probe_up{target=%s} = %v, want 1 (probe: up=%v status=%d err=%v)",
			host, got, row.HTTP.Up, row.HTTP.Status, row.HTTP.Err)
	}
}

// gather renders the snapshot into a name-and-labels keyed map, the way a
// Prometheus scrape would read it.
func gather(t *testing.T, snap *metrics.Snapshot) map[string]float64 {
	t.Helper()

	registry := prometheus.NewRegistry()
	registry.MustRegister(snap.Collector())
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	values := make(map[string]float64)
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			key := family.GetName()
			for _, pair := range metric.GetLabel() {
				key += "," + pair.GetName() + "=" + pair.GetValue()
			}
			values[key] = metric.GetGauge().GetValue()
		}
	}
	return values
}
