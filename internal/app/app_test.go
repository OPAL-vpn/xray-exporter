package app

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Anton-beep/xray-exporter/internal/metrics"
)

func TestOverran(t *testing.T) {
	const deadline = 150 * time.Second

	cases := []struct {
		name  string
		cycle metrics.CycleExport
		want  bool
	}{
		{name: "deadline cut the cycle", cycle: metrics.CycleExport{Done: true, Duration: deadline}, want: true},
		{name: "cycle finished early", cycle: metrics.CycleExport{Done: true, Duration: 2 * time.Second}, want: false},
		{name: "cycle never ran", cycle: metrics.CycleExport{}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := overran(tc.cycle, deadline); got != tc.want {
				t.Errorf("overran(%+v, %s) = %v, want %v", tc.cycle, deadline, got, tc.want)
			}
		})
	}

	if overran(metrics.CycleExport{Done: true, Duration: time.Hour}, 0) {
		t.Error("overran with no deadline = true, want false")
	}
}

func TestUnhealthy(t *testing.T) {
	healthy := metrics.ConfigExport{
		Name:    "nl1",
		Running: true,
		Egress:  metrics.EgressExport{Verified: true},
		Cycle:   metrics.CycleExport{Done: true},
	}
	if reasons := unhealthy(metrics.Export{Configs: []metrics.ConfigExport{healthy}}); len(reasons) != 0 {
		t.Errorf("unhealthy(healthy config) = %v, want no reasons", reasons)
	}

	broken := metrics.ConfigExport{
		Name:   "nl1",
		Egress: metrics.EgressExport{},
		Cycle:  metrics.CycleExport{},
	}
	reasons := unhealthy(metrics.Export{Configs: []metrics.ConfigExport{broken}})
	joined := strings.Join(reasons, "; ")
	for _, want := range []string{"not running", "egress not verified", "cycle did not complete"} {
		if !strings.Contains(joined, want) {
			t.Errorf("unhealthy(broken config) = %v, want it to mention %q", reasons, want)
		}
	}

	if reasons := unhealthy(metrics.Export{}); len(reasons) == 0 {
		t.Error("unhealthy(empty export) = no reasons, want one for the absence of configs")
	}
}

func TestMirrorHandlerWritesConsoleAndFile(t *testing.T) {
	var console, file bytes.Buffer
	logger := slog.New(mirrorHandler{
		console: slog.NewTextHandler(&console, &slog.HandlerOptions{Level: slog.LevelInfo}),
		file:    slog.NewTextHandler(&file, &slog.HandlerOptions{Level: slog.LevelDebug}),
	})

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
