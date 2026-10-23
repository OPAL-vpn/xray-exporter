package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
)

// xrayLoggerFor returns the logger xray's stream is routed through, mirroring
// the same lines into XRAY_LOG_FILE when it is set. One file holds the whole
// process's stream because xray's log carries no instance identity.
func xrayLoggerFor(env Env, logger *slog.Logger) (*slog.Logger, io.Closer, error) {
	if env.XrayLogFile == "" {
		return logger, nil, nil
	}

	f, err := os.OpenFile(env.XrayLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, nil, fmt.Errorf("XRAY_LOG_FILE: %w", err)
	}
	mirrored := slog.New(mirrorHandler{
		console: logger.Handler(),
		// The bridge already applied XRAY_LOG_LEVEL, so the file keeps whatever
		// the process emits instead of filtering it a second time.
		file: slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}),
	})
	return mirrored, f, nil
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
