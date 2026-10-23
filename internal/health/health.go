// Package health turns the last measurement export into the reasons a one-shot
// run must exit non-zero.
package health

import (
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
)

// Overran reports whether a config's cycle used its entire deadline, which
// means the deadline cut it short rather than the probes finishing.
func Overran(cycle metrics.CycleExport, deadline time.Duration) bool {
	return cycle.Done && deadline > 0 && cycle.Duration >= deadline
}

// Reasons lists why a one-shot run exits non-zero. A cycle the deadline cut
// short counts as incomplete: the sampler stamps every pass it started as done,
// so only the duration against the deadline tells a truncated pass from a
// finished one — and a pre-deploy gate must not pass on a truncated one.
func Reasons(exp metrics.Export, deadline time.Duration) []string {
	if len(exp.Configs) == 0 {
		return []string{"no configs"}
	}

	var reasons []string
	for _, cfg := range exp.Configs {
		if !cfg.Running {
			reasons = append(reasons, cfg.Name+" not running")
		}
		if !cfg.Egress.Verified {
			reasons = append(reasons, cfg.Name+" egress not verified")
		}
		if !cfg.Cycle.Done || Overran(cfg.Cycle, deadline) {
			reasons = append(reasons, cfg.Name+" cycle did not complete")
		}
	}
	return reasons
}
