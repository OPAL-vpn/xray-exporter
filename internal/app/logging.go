package app

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/OPAL-vpn/xray-exporter/internal/logging"
)

// xrayLoggerFor routes xray's stream to logger, mirroring it to XRAY_LOG_FILE
// when set. One file holds the whole process; xray carries no instance identity.
func xrayLoggerFor(env Env, logger *slog.Logger) (*slog.Logger, io.Closer, error) {
	if env.XrayLogFile == "" {
		return logger, nil, nil
	}

	f, err := logging.OpenRotating(env.XrayLogFile, env.XrayLogMaxBytes, env.XrayLogKeepBytes, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("XRAY_LOG_FILE: %w", err)
	}
	mirrored := slog.New(logging.Mirror(
		logger.Handler(),
		// XRAY_LOG_LEVEL is already applied; the file must not filter again.
		slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}),
	))
	return mirrored, f, nil
}
