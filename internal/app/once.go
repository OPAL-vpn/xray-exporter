package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/report"
)

func (a *application) once(ctx context.Context) error {
	deadline := a.opts.Deadline
	if deadline <= 0 {
		deadline = 5 * time.Minute
	}

	a.cycle(ctx, deadline)

	if err := a.writeReport(); err != nil {
		return err
	}
	if reasons := unhealthy(a.snap.Export(), deadline); len(reasons) > 0 {
		return fmt.Errorf("one-shot check failed: %s", strings.Join(reasons, "; "))
	}
	return nil
}

// writeReport renders the snapshot to opts.Out, or to stdout when it is empty.
// A file is created mode 0600; an existing file is narrowed to it, because the

func (a *application) writeReport() error {
	exp := a.snap.Export()
	if a.opts.Out == "" {
		return report.Render(os.Stdout, exp)
	}

	f, err := os.OpenFile(a.opts.Out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", a.opts.Out, err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("chmod %s: %w", a.opts.Out, err)
	}
	if err := report.Render(f, exp); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// close releases the process's resources: any running cycle, the xray

func overran(cycle metrics.CycleExport, deadline time.Duration) bool {
	return cycle.Done && deadline > 0 && cycle.Duration >= deadline
}

// unhealthy lists why a one-shot run exits non-zero. A cycle the deadline cut
// short counts as incomplete: the sampler stamps every pass it started as done,
// so only the duration against the deadline tells a truncated pass from a

func unhealthy(exp metrics.Export, deadline time.Duration) []string {
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
		if !cfg.Cycle.Done || overran(cfg.Cycle, deadline) {
			reasons = append(reasons, cfg.Name+" cycle did not complete")
		}
	}
	return reasons
}
