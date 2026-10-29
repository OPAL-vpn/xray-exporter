// Package report renders the one-shot report table.
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

// absent is the cell for a value that was never measured. A measured failure
// says "fail", so a blank cell never hides an outage.
const absent = "-"

// Render writes exp as a tab-aligned report.
func Render(w io.Writer, exp metrics.Export) error {
	r := &renderer{w: w}
	r.printf("xray-exporter report %s\n", time.Now().UTC().Format(time.RFC3339))
	renderVantage(r, exp.Vantage)
	for _, cfg := range exp.Configs {
		renderConfig(r, cfg)
	}
	return r.err
}

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
	if cfg.Entry != nil {
		r.printf("entry tcp: %s\n", upCell(cfg.Entry.Up))
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

// renderVantage prints the egress network and the controls.
func renderVantage(r *renderer, v *metrics.VantageExport) {
	if v == nil {
		return
	}
	r.printf("\n==== vantage ====\n")
	if v.HasIdentity {
		r.printf("egress: %s, %s\n", orAbsent(v.Identity.ASName), orAbsent(v.Identity.Country))
	} else {
		r.printf("egress: %s\n", absent)
	}
	if len(v.Controls) == 0 {
		return
	}
	r.table(func(t *renderer) {
		t.printf("control\tkind\tclass\tresult\n")
		for _, c := range v.Controls {
			t.printf("%s\t%s\t%s\t%s\n", c.Name, c.Kind, c.Class, upCell(c.Up))
		}
	})
}

func upCell(up bool) string {
	if up {
		return "up"
	}
	return "fail"
}

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

func dnsCell(t metrics.TargetExport) string {
	if !t.HasDNS {
		return absent
	}
	if !t.DNS.Up {
		return "fail"
	}
	return seconds(t.DNS.RTT)
}

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

func seconds(d time.Duration) string {
	return fmt.Sprintf("%.2fs", float64(d.Milliseconds())/1000)
}

func orAbsent(value string) string {
	if value == "" {
		return absent
	}
	return value
}
