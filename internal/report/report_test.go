package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Anton-beep/xray-exporter/internal/metrics"
	"github.com/Anton-beep/xray-exporter/internal/probe"
)

// export builds one config with a hostname target, an address-literal target
// and a single DoH resolver.
func export(verified bool) metrics.Export {
	return metrics.Export{Configs: []metrics.ConfigExport{{
		Name:        "nl1",
		Running:     true,
		XrayVersion: "v26.10.21",
		Egress:      metrics.EgressExport{IP: "203.0.113.7", Verified: verified},
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

// row returns the first line whose first field is name.
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

func TestRender(t *testing.T) {
	out := render(t, export(true))

	if !strings.HasPrefix(out, "xray-exporter report ") {
		t.Errorf("report does not start with its header line:\n%s", out)
	}
	if !strings.Contains(out, "==== config: nl1 ====") {
		t.Errorf("report lacks the config header:\n%s", out)
	}
	if !strings.Contains(out, "xray: v26.10.21") {
		t.Errorf("report lacks the xray version:\n%s", out)
	}
	if !strings.Contains(out, "egress: 203.0.113.7 verified") {
		t.Errorf("report lacks the verified egress verdict:\n%s", out)
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

func TestRenderEgressWarning(t *testing.T) {
	out := render(t, export(false))

	if !strings.Contains(out, "egress not verified") {
		t.Errorf("report for unverified egress lacks the warning:\n%s", out)
	}
	if !strings.Contains(out, "NOT VERIFIED") {
		t.Errorf("report for unverified egress lacks the verdict:\n%s", out)
	}
}

func TestRenderFailedTarget(t *testing.T) {
	exp := metrics.Export{Configs: []metrics.ConfigExport{{
		Name:   "nl1",
		Egress: metrics.EgressExport{Verified: true},
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
