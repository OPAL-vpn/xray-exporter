// Package health decides whether a one-shot run may exit zero.
package health

import (
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
)

// Overran reports whether the deadline cut the cycle short.
func Overran(cycle metrics.CycleExport, deadline time.Duration) bool {
	return cycle.Done && deadline > 0 && cycle.Duration >= deadline
}

// Reasons lists why a one-shot run must fail. A cycle that used its whole
// deadline counts as truncated, because the sampler stamps truncated passes done.
func Reasons(exp metrics.Export, deadline time.Duration) []string {
	if len(exp.Configs) == 0 {
		return []string{"no configs"}
	}

	var reasons []string
	for _, cfg := range exp.Configs {
		if !cfg.Running {
			reasons = append(reasons, cfg.Name+" not running")
		}
		if !cfg.Cycle.Done || Overran(cfg.Cycle, deadline) {
			reasons = append(reasons, cfg.Name+" cycle did not complete")
		}
	}
	return reasons
}
