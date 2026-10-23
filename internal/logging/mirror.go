// Package logging holds exporter-independent slog handlers.
package logging

import (
	"context"
	"log/slog"
)

// Mirror writes every record to both handlers. Each keeps its own level filter,
// so Handle must not pass a record to a handler that refused it.
func Mirror(console, file slog.Handler) slog.Handler {
	return mirrorHandler{console: console, file: file}
}

type mirrorHandler struct {
	console slog.Handler
	file    slog.Handler
}

func (m mirrorHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return m.console.Enabled(ctx, level) || m.file.Enabled(ctx, level)
}

func (m mirrorHandler) Handle(ctx context.Context, record slog.Record) error {
	var err error
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
