package app

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/OPAL-vpn/xray-exporter/internal/logging"
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
	mirrored := slog.New(logging.Mirror(
		logger.Handler(),
		// The bridge already applied XRAY_LOG_LEVEL, so the file keeps whatever
		// the process emits instead of filtering it a second time.
		slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}),
	))
	return mirrored, f, nil
}
