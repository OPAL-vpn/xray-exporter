package xraylog_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	clog "github.com/xtls/xray-core/common/log"

	"github.com/Anton-beep/xray-exporter/internal/xraylog"
)

// quiet restores a no-op handler after the test: the xray log handler is
// process-global, so a test's handler would otherwise outlive it.
func quiet(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { clog.RegisterHandler(discard{}) })
}

type discard struct{}

func (discard) Handle(clog.Message) {}

// newLogger returns a text logger at debug and the buffer it writes to.
func newLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

func TestParseSeverity(t *testing.T) {
	tests := []struct {
		in   string
		want clog.Severity
	}{
		{"debug", clog.Severity_Debug},
		{"info", clog.Severity_Info},
		{"warning", clog.Severity_Warning},
		{"error", clog.Severity_Error},
		// xray lowercases its own loglevel, so a differently cased value is the
		// same spelling.
		{"Warning", clog.Severity_Warning},
	}

	for _, tc := range tests {
		got, err := xraylog.ParseSeverity(tc.in)
		if err != nil {
			t.Errorf("ParseSeverity(%q) error = %v, want nil", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseSeverity(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}

	if _, err := xraylog.ParseSeverity("none"); err != nil {
		t.Errorf("ParseSeverity(none) error = %v, want nil", err)
	}
	if _, err := xraylog.ParseSeverity("trace"); err == nil {
		t.Error("ParseSeverity(trace) = nil error, want an unknown-level error")
	}
}

func TestInstallMapsSeverity(t *testing.T) {
	quiet(t)
	logger, buf := newLogger()

	xraylog.Install(logger, clog.Severity_Debug)
	clog.Record(&clog.GeneralMessage{Severity: clog.Severity_Error, Content: "boom"})

	out := buf.String()
	for _, want := range []string{"level=ERROR", "subsystem=xray", "boom"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q does not contain %q", out, want)
		}
	}
}

func TestInstallFiltersBelowMin(t *testing.T) {
	quiet(t)
	logger, buf := newLogger()

	xraylog.Install(logger, clog.Severity_Warning)
	clog.Record(&clog.GeneralMessage{Severity: clog.Severity_Info, Content: "chatty"})
	if out := buf.String(); strings.Contains(out, "chatty") {
		t.Errorf("info message passed min=warning: %q", out)
	}

	clog.Record(&clog.GeneralMessage{Severity: clog.Severity_Warning, Content: "careful"})
	out := buf.String()
	for _, want := range []string{"level=WARN", "careful"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q does not contain %q", out, want)
		}
	}
}

// TestInstallUnknownMinPassesEverything pins that an unset minimum is not a
// filter: Severity_Unknown means "no minimum", not "drop everything".
func TestInstallUnknownMinPassesEverything(t *testing.T) {
	quiet(t)
	logger, buf := newLogger()

	xraylog.Install(logger, clog.Severity_Unknown)
	clog.Record(&clog.GeneralMessage{Severity: clog.Severity_Debug, Content: "verbose"})

	if out := buf.String(); !strings.Contains(out, "verbose") {
		t.Errorf("output %q does not contain the debug message, want min=unknown to pass everything", out)
	}
}

func TestInstallNoneDropsEverything(t *testing.T) {
	quiet(t)
	logger, buf := newLogger()

	min, err := xraylog.ParseSeverity("none")
	if err != nil {
		t.Fatalf("ParseSeverity(none): %v", err)
	}
	xraylog.Install(logger, min)
	clog.Record(&clog.GeneralMessage{Severity: clog.Severity_Error, Content: "boom"})

	if out := buf.String(); out != "" {
		t.Errorf("output = %q, want nothing at log level none", out)
	}
}

func TestCaptureAttributesToConfig(t *testing.T) {
	quiet(t)
	logger, buf := newLogger()

	release := xraylog.Capture()
	clog.Record(&clog.GeneralMessage{Severity: clog.Severity_Error, Content: "startup boom"})
	release("nl1", logger)

	out := buf.String()
	for _, want := range []string{"config=nl1", "subsystem=xray", "level=ERROR", "startup boom"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q does not contain %q", out, want)
		}
	}
}

func TestCaptureRestoresPreviousHandler(t *testing.T) {
	quiet(t)
	logger, buf := newLogger()

	xraylog.Install(logger, clog.Severity_Debug)
	release := xraylog.Capture()
	clog.Record(&clog.GeneralMessage{Severity: clog.Severity_Info, Content: "buffered"})
	release("nl1", logger)
	clog.Record(&clog.GeneralMessage{Severity: clog.Severity_Info, Content: "steady"})

	out := buf.String()
	for _, want := range []string{"buffered", "steady"} {
		if got := strings.Count(out, want); got != 1 {
			t.Errorf("%q appears %d times in %q, want exactly 1", want, got, out)
		}
	}
}
