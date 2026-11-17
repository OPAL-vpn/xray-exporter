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
	renderReachability(r, exp.Reachability)
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

	if len(cfg.Resolvers) > 0 {
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

	renderUDP(r, cfg.UDPs)
	renderTCP(r, cfg.TCPs)
}

// renderUDP prints one row per target and load pass. A row that was never
// probed has every measurement cell absent: an unmeasured probe is not a
// failure.
func renderUDP(r *renderer, rows []metrics.UDPExport) {
	if len(rows) == 0 {
		return
	}
	r.table(func(t *renderer) {
		t.printf("udp target\tload\tup\tloss\trtt\trtt max\tjitter\treord\n")
		for _, row := range rows {
			t.printf("%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				row.Target, row.Load,
				measuredUpCell(row.Measured, row.Result.Up),
				udpLossCell(row),
				durationCell(udpHasNumbers(row), row.Result.Median),
				durationCell(udpHasNumbers(row), row.Result.Max),
				durationCell(udpHasNumbers(row), row.Result.Jitter),
				udpReorderedCell(row))
		}
	})
}

// renderTCP prints one row per tunnel target.
func renderTCP(r *renderer, rows []metrics.TCPExport) {
	if len(rows) == 0 {
		return
	}
	r.table(func(t *renderer) {
		t.printf("tcp target\tup\tconnect\n")
		for _, row := range rows {
			t.printf("%s\t%s\t%s\n",
				row.Target,
				measuredUpCell(row.Measured, row.Result.Up),
				durationCell(row.Measured && row.Result.Up, row.Result.Connect))
		}
	})
}

// udpHasNumbers reports whether row carries loss and latency numbers: only a
// probe that ran and was answered has them.
func udpHasNumbers(row metrics.UDPExport) bool {
	return row.Measured && row.Result.Up
}

func udpLossCell(row metrics.UDPExport) string {
	if !udpHasNumbers(row) || row.Result.Sent <= 0 {
		return absent
	}
	return fmt.Sprintf("%.1f%%", 100*float64(row.Result.Sent-row.Result.Replies)/float64(row.Result.Sent))
}

func udpReorderedCell(row metrics.UDPExport) string {
	if !udpHasNumbers(row) {
		return absent
	}
	return strconv.Itoa(row.Result.Reordered)
}

// renderReachability prints the egress network and the controls.
func renderReachability(r *renderer, v *metrics.ReachabilityExport) {
	if v == nil {
		return
	}
	r.printf("\n==== reachability ====\n")
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

// measuredUpCell is upCell for a row that may never have been probed: an
// unmeasured row is absent, not a failure.
func measuredUpCell(measured, up bool) string {
	if !measured {
		return absent
	}
	return upCell(up)
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

// durationCell renders d in milliseconds, the unit the quality probes report
// in, or absent when the row has no measurement to show.
func durationCell(measured bool, d time.Duration) string {
	if !measured {
		return absent
	}
	return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
}

func orAbsent(value string) string {
	if value == "" {
		return absent
	}
	return value
}
