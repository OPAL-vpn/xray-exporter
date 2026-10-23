package logging_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/OPAL-vpn/xray-exporter/internal/logging"
)

func TestMirrorHandlerWritesConsoleAndFile(t *testing.T) {
	var console, file bytes.Buffer
	logger := slog.New(logging.Mirror(
		slog.NewTextHandler(&console, &slog.HandlerOptions{Level: slog.LevelInfo}),
		slog.NewTextHandler(&file, &slog.HandlerOptions{Level: slog.LevelDebug}),
	))

	logger.Info("instance started", "config", "nl1")
	logger.Debug("xray heartbeat")

	for name, buf := range map[string]*bytes.Buffer{"console": &console, "file": &file} {
		out := buf.String()
		if !strings.Contains(out, "instance started") || !strings.Contains(out, "config=nl1") {
			t.Errorf("%s got %q, want the message and its attributes", name, out)
		}
	}

	// The console keeps its own level: only the file mirrors the debug stream.
	if strings.Contains(console.String(), "xray heartbeat") {
		t.Errorf("console got the debug line, want its info filter to hold:\n%s", console.String())
	}
	if !strings.Contains(file.String(), "xray heartbeat") {
		t.Errorf("file missed the debug line, want it to keep the mirrored stream:\n%s", file.String())
	}
}
