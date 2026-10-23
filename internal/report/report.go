// Package report renders one measurement result as the human-readable table
// --once prints: a header per config, the egress verdict, one row per target
// and one row per resolver.
package report

import (
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

// absent is the cell for a value that was never measured. A measured failure is
// not absent: it says "fail", so a blank cell never hides an outage.
const absent = "-"

// Render writes exp as a report. Cells are tab-separated and aligned by
// tabwriter; a value that was never measured is "-".
func Render(w io.Writer, exp metrics.Export) error {
	r := &renderer{w: w}
	r.printf("xray-exporter report %s\n", time.Now().UTC().Format(time.RFC3339))
	for _, cfg := range exp.Configs {
		renderConfig(r, cfg)
	}
	return r.err
}

// renderer accumulates the first write error, so Render can return it without
// checking every line.
type renderer struct {
	w   io.Writer
	err error
}

func (r *renderer) printf(format string, args ...any) {
	if r.err != nil {
		return
	}
	_, r.err = fmt.Fprintf(r.w, format, args...)
}

// table runs row against a tabwriter and flushes it.
func (r *renderer) table(row func(*renderer)) {
	if r.err != nil {
		return
	}
	tw := tabwriter.NewWriter(r.w, 0, 0, 2, ' ', 0)
	inner := &renderer{w: tw}
	row(inner)
	if inner.err != nil {
		r.err = inner.err
		return
	}
	r.err = tw.Flush()
}

func renderConfig(r *renderer, cfg metrics.ConfigExport) {
	r.printf("\n==== config: %s ====\n", cfg.Name)
	r.printf("xray: %s\n", orAbsent(cfg.XrayVersion))

	verdict := "verified"
	if !cfg.Egress.Verified {
		verdict = "NOT VERIFIED"
	}
	r.printf("egress: %s %s\n", orAbsent(cfg.Egress.IP), verdict)
	if !cfg.Egress.Verified {
		r.printf("warning: egress not verified (traffic may leave directly)\n")
	}

	r.table(func(t *renderer) {
		t.printf("target\tstatus\ttls\tttfb\ttotal\tdns\n")
		for _, target := range cfg.Targets {
			t.printf("%s\t%s\t%s\t%s\t%s\t%s\n",
				target.Target, statusCell(target), tlsCell(target),
				ttfbCell(target), totalCell(target), dnsCell(target))
		}
	})

	if len(cfg.Resolvers) == 0 {
		return
	}
	r.table(func(t *renderer) {
		t.printf("resolver\tdoh\tdot\n")
		for _, resolver := range cfg.Resolvers {
			transports := make(map[string]string, len(resolver.Transports))
			for _, transport := range resolver.Transports {
				transports[transport.Transport] = resultCell(transport.Result)
			}
			t.printf("%s\t%s\t%s\n",
				resolver.Name, orAbsent(transports[metrics.TransportDoH]), orAbsent(transports[metrics.TransportDoT]))
		}
	})
}

// statusCell is the HTTP status, "fail" when the request produced no response,
// and "-" when the target was never measured.
func statusCell(t metrics.TargetExport) string {
	switch {
	case !t.HasHTTP:
		return absent
	case t.HTTP.Status == 0:
		return "fail"
	default:
		return strconv.Itoa(t.HTTP.Status)
	}
}

// tlsCell is the certificate verdict; it needs a response to mean anything.
func tlsCell(t metrics.TargetExport) string {
	if !t.HasHTTP || !t.HTTP.Up {
		return absent
	}
	if t.HTTP.TLSVerified {
		return "ok"
	}
	return "fail"
}

func ttfbCell(t metrics.TargetExport) string {
	if !t.HasHTTP || !t.HTTP.Up {
		return absent
	}
	return seconds(t.HTTP.TTFB)
}

func totalCell(t metrics.TargetExport) string {
	if !t.HasHTTP || !t.HTTP.Up {
		return absent
	}
	return seconds(t.HTTP.Total)
}

// dnsCell is "-" for an address literal, which has nothing to resolve.
func dnsCell(t metrics.TargetExport) string {
	if !t.HasDNS {
		return absent
	}
	if !t.DNS.Up {
		return "fail"
	}
	return seconds(t.DNS.RTT)
}

// resultCell is a resolver transport's round trip, "fail" when it answered
// nothing, and "-" when the transport was never probed.
func resultCell(r probe.ResolverResult) string {
	switch {
	case r.Up:
		return seconds(r.RTT)
	case r.Err != nil:
		return "fail"
	default:
		return absent
	}
}

// seconds renders a duration as milliseconds-derived seconds with two
// decimals, the resolution a human report needs.
func seconds(d time.Duration) string {
	return fmt.Sprintf("%.2fs", float64(d.Milliseconds())/1000)
}

// orAbsent returns value, or "-" when it is empty.
func orAbsent(value string) string {
	if value == "" {
		return absent
	}
	return value
}
