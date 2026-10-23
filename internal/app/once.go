package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/OPAL-vpn/xray-exporter/internal/health"
	"github.com/OPAL-vpn/xray-exporter/internal/report"
)

// once runs a single cycle, writes the report and fails unless every config is
// running, every egress is verified and every cycle completed.
func (a *application) once(ctx context.Context) error {
	deadline := a.opts.Deadline
	if deadline <= 0 {
		deadline = DefaultDeadline
	}

	a.cycle(ctx, deadline)

	if err := a.writeReport(); err != nil {
		return err
	}
	if reasons := health.Reasons(a.snap.Export(), deadline); len(reasons) > 0 {
		return fmt.Errorf("one-shot check failed: %s", strings.Join(reasons, "; "))
	}
	return nil
}

// writeReport renders the snapshot to opts.Out, or to stdout when it is empty.
// A file is created mode 0600; an existing file is narrowed to it, because the
// flag promises the mode, not just the creation mode.
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
