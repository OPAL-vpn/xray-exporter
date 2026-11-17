package report

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/egress"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

// export builds one config with a hostname target, an IP-literal target and a
// DoH resolver.
func export() metrics.Export {
	return metrics.Export{Configs: []metrics.ConfigExport{{
		Name:        "nl1",
		Running:     true,
		XrayVersion: "v26.10.21",
		Cycle:       metrics.CycleExport{Done: true, Duration: 2 * time.Second, Targets: 2},
		Targets: []metrics.TargetExport{
			{
				Target:  "a.test",
				HasHTTP: true,
				HTTP: probe.HTTPResult{
					Up:          true,
					Status:      200,
					TLSVerified: true,
					TTFB:        120 * time.Millisecond,
					Total:       200 * time.Millisecond,
				},
				HasDNS: true,
				DNS:    probe.DNSResult{Up: true, RTT: 50 * time.Millisecond},
			},
			{
				Target:  "1.1.1.1",
				HasHTTP: true,
				HTTP: probe.HTTPResult{
					Up:          true,
					Status:      200,
					TLSVerified: true,
					TTFB:        110 * time.Millisecond,
					Total:       150 * time.Millisecond,
				},
			},
		},
		Resolvers: []metrics.ResolverExport{{
			Name: "cloudflare",
			Transports: []metrics.TransportExport{{
				Transport: metrics.TransportDoH,
				Result:    probe.ResolverResult{Up: true, RTT: 50 * time.Millisecond},
			}},
		}},
	}}}
}

func render(t *testing.T, exp metrics.Export) string {
	t.Helper()

	var buf bytes.Buffer
	if err := Render(&buf, exp); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return buf.String()
}

func row(t *testing.T, out, name string) string {
	t.Helper()

	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == name {
			return line
		}
	}
	t.Fatalf("report has no row starting %q:\n%s", name, out)
	return ""
}

// rowFields returns the cells of the first line whose leading cells are want.
func rowFields(t *testing.T, out string, want ...string) []string {
	t.Helper()

	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= len(want) && slices.Equal(fields[:len(want)], want) {
			return fields
		}
	}
	t.Fatalf("report has no row starting %v:\n%s", want, out)
	return nil
}

func TestRender(t *testing.T) {
	out := render(t, export())

	if !strings.HasPrefix(out, "xray-exporter report ") {
		t.Errorf("report does not start with its header line:\n%s", out)
	}
	if !strings.Contains(out, "==== config: nl1 ====") {
		t.Errorf("report lacks the config header:\n%s", out)
	}
	if !strings.Contains(out, "xray: v26.10.21") {
		t.Errorf("report lacks the xray version:\n%s", out)
	}

	host := row(t, out, "a.test")
	if !strings.Contains(host, "200") {
		t.Errorf("a.test row = %q, want the HTTP status 200", host)
	}
	if !strings.Contains(host, "0.12s") || !strings.Contains(host, "0.20s") {
		t.Errorf("a.test row = %q, want the TTFB and total in seconds", host)
	}
	if !strings.Contains(host, "0.05s") {
		t.Errorf("a.test row = %q, want the DNS round trip in seconds", host)
	}

	literal := strings.Fields(row(t, out, "1.1.1.1"))
	if len(literal) == 0 || literal[len(literal)-1] != "-" {
		t.Errorf("1.1.1.1 row = %v, want its DNS cell to be -", literal)
	}
	if !strings.Contains(strings.Join(literal, " "), "200") {
		t.Errorf("1.1.1.1 row = %v, want the HTTP status 200", literal)
	}

	resolver := strings.Fields(row(t, out, "cloudflare"))
	if len(resolver) != 3 {
		t.Fatalf("cloudflare row = %v, want resolver, doh and dot cells", resolver)
	}
	if resolver[1] != "0.05s" || resolver[2] != "-" {
		t.Errorf("cloudflare row = %v, want the DoH round trip and no DoT cell", resolver)
	}
}

func TestRenderFailedTarget(t *testing.T) {
	exp := metrics.Export{Configs: []metrics.ConfigExport{{
		Name: "nl1",
		Targets: []metrics.TargetExport{{
			Target:  "down.test",
			HasHTTP: true,
			HTTP:    probe.HTTPResult{Err: probe.ErrIPLiteral},
			HasDNS:  true,
			DNS:     probe.DNSResult{Err: probe.ErrIPLiteral},
		}},
	}}}

	down := strings.Fields(row(t, render(t, exp), "down.test"))
	if len(down) != 6 {
		t.Fatalf("down.test row = %v, want six cells", down)
	}
	if down[1] != "fail" {
		t.Errorf("down.test status cell = %q, want fail", down[1])
	}
	if down[5] != "fail" {
		t.Errorf("down.test dns cell = %q, want fail", down[5])
	}
}

// qualityExport builds one config with an answered, a failed and an unmeasured
// row in each of the UDP and TCP tables.
func qualityExport() metrics.Export {
	return metrics.Export{Configs: []metrics.ConfigExport{{
		Name: "nl1",
		UDPs: []metrics.UDPExport{
			{
				Target:   "u1",
				Load:     "idle",
				Measured: true,
				Result: probe.UDPResult{
					Up:        true,
					Sent:      10,
					Replies:   9,
					Median:    12 * time.Millisecond,
					Max:       20 * time.Millisecond,
					Jitter:    3 * time.Millisecond,
					Reordered: 2,
				},
			},
			{
				Target:   "u1",
				Load:     "load",
				Measured: true,
				Result:   probe.UDPResult{Sent: 10, Err: errors.New("timeout")},
			},
			{Target: "u2", Load: "idle", Measured: false},
		},
		TCPs: []metrics.TCPExport{
			{Target: "t1", Measured: true, Result: probe.TunnelTCPResult{Up: true, Connect: 20 * time.Millisecond}},
			{Target: "t2", Measured: true, Result: probe.TunnelTCPResult{Err: errors.New("refused")}},
			{Target: "t3", Measured: false},
		},
	}}}
}

func TestRenderUDPTable(t *testing.T) {
	out := render(t, qualityExport())

	// tabwriter pads columns to their widest cell, so the header is compared
	// with its cells joined by single spaces.
	header := strings.Join(rowFields(t, out, "udp", "target"), " ")
	if want := "udp target load up loss rtt rtt max jitter reord"; header != want {
		t.Errorf("udp header = %q, want %q", header, want)
	}

	ok := rowFields(t, out, "u1", "idle")
	if want := []string{"u1", "idle", "up", "10.0%", "12.00ms", "20.00ms", "3.00ms", "2"}; !slices.Equal(ok, want) {
		t.Errorf("u1 idle row = %v, want %v", ok, want)
	}

	failed := rowFields(t, out, "u1", "load")
	if want := []string{"u1", "load", "fail", "-", "-", "-", "-", "-"}; !slices.Equal(failed, want) {
		t.Errorf("u1 load row = %v, want a failure with no fabricated loss or latency", failed)
	}

	unmeasured := rowFields(t, out, "u2", "idle")
	if want := []string{"u2", "idle", "-", "-", "-", "-", "-", "-"}; !slices.Equal(unmeasured, want) {
		t.Errorf("u2 idle row = %v, want every measurement cell absent", unmeasured)
	}
}

func TestRenderTCPTable(t *testing.T) {
	out := render(t, qualityExport())

	header := strings.Join(rowFields(t, out, "tcp", "target"), " ")
	if want := "tcp target up connect"; header != want {
		t.Errorf("tcp header = %q, want %q", header, want)
	}

	ok := rowFields(t, out, "t1")
	if want := []string{"t1", "up", "20.00ms"}; !slices.Equal(ok, want) {
		t.Errorf("t1 row = %v, want %v", ok, want)
	}

	failed := rowFields(t, out, "t2")
	if want := []string{"t2", "fail", "-"}; !slices.Equal(failed, want) {
		t.Errorf("t2 row = %v, want a failure with no fabricated connect time", failed)
	}

	unmeasured := rowFields(t, out, "t3")
	if want := []string{"t3", "-", "-"}; !slices.Equal(unmeasured, want) {
		t.Errorf("t3 row = %v, want every measurement cell absent", unmeasured)
	}
}

func TestRenderSkipsEmptyQualityTables(t *testing.T) {
	out := render(t, export())
	for _, header := range []string{"udp target", "tcp target"} {
		if strings.Contains(out, header) {
			t.Errorf("report without quality targets renders the %q table:\n%s", header, out)
		}
	}

	udpOnly := qualityExport()
	udpOnly.Configs[0].TCPs = nil
	if out := render(t, udpOnly); strings.Contains(out, "tcp target") {
		t.Errorf("report without TCP targets renders the tcp table:\n%s", out)
	}
}

func TestRenderReachability(t *testing.T) {
	exp := export()
	exp.Configs[0].Entry = &metrics.EntryExport{Up: false}
	exp.Reachability = &metrics.ReachabilityExport{
		HasIdentity: true,
		Identity:    egress.Identity{ASName: "PJSC MegaFon", Country: "RU"},
		Controls: []metrics.ControlExport{
			{Kind: "https", Name: "ya", Class: "domestic", Up: true},
			{Kind: "tcp", Name: "cf", Class: "foreign", Up: false},
		},
	}

	var out bytes.Buffer
	if err := Render(&out, exp); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"egress: PJSC MegaFon, RU", "entry tcp: fail"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, out.String())
		}
	}
	for _, row := range []string{"ya", "https", "domestic", "up"} {
		if !strings.Contains(out.String(), row) {
			t.Errorf("report lacks the control cell %q", row)
		}
	}

	exp.Reachability = nil
	exp.Configs[0].Entry = nil
	out.Reset()
	if err := Render(&out, exp); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "reachability") || strings.Contains(out.String(), "entry tcp") {
		t.Errorf("a report without the block mentions it:\n%s", out.String())
	}
}
