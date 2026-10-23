// Package logging holds slog handlers that are independent of the exporter's
// wiring.
package logging

import (
	"context"
	"log/slog"
)

// Mirror returns a handler that writes every record to both console and file.
// Each handler keeps its own level filter: Enabled is asked once for the pair,
// so Handle must not hand a record to a handler that refused it.
func Mirror(console, file slog.Handler) slog.Handler {
	return mirrorHandler{console: console, file: file}
}

// mirrorHandler writes every record to two handlers, the console and the
// XRAY_LOG_FILE mirror.
type mirrorHandler struct {
	console slog.Handler
	file    slog.Handler
}

func (m mirrorHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return m.console.Enabled(ctx, level) || m.file.Enabled(ctx, level)
}

func (m mirrorHandler) Handle(ctx context.Context, record slog.Record) error {
	var err error
	// Each handler keeps its own level filter: Enabled is asked once for the
	// pair, so Handle must not hand a record to a handler that refused it.
	if m.console.Enabled(ctx, record.Level) {
		err = m.console.Handle(ctx, record)
	}
	if m.file.Enabled(ctx, record.Level) {
		if fileErr := m.file.Handle(ctx, record); err == nil {
			err = fileErr
		}
	}
	return err
}

func (m mirrorHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return mirrorHandler{console: m.console.WithAttrs(attrs), file: m.file.WithAttrs(attrs)}
}

func (m mirrorHandler) WithGroup(name string) slog.Handler {
	return mirrorHandler{console: m.console.WithGroup(name), file: m.file.WithGroup(name)}
}
