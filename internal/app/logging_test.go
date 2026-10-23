package app

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestXrayLoggerForRotatesTheMirroredFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xray.log")
	env := Env{XrayLogFile: path, XrayLogMaxBytes: 1000, XrayLogKeepBytes: 600}

	var console bytes.Buffer
	logger, closer, err := xrayLoggerFor(env, slog.New(slog.NewTextHandler(&console, nil)))
	if err != nil {
		t.Fatalf("xrayLoggerFor: %v", err)
	}
	defer closer.Close()

	for i := range 50 {
		logger.Info(fmt.Sprintf("rotate-%04d %s", i, strings.Repeat("m", 40)))
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if got := int64(len(body)); got > env.XrayLogMaxBytes {
		t.Fatalf("log is %d bytes, want <= %d", got, env.XrayLogMaxBytes)
	}
	if !strings.Contains(string(body), "rotate-0049") {
		t.Errorf("newest record missing from the log:\n%s", body)
	}
	if strings.Contains(string(body), "rotate-0000") {
		t.Errorf("oldest record survived the trim")
	}
	if !strings.HasPrefix(string(body), "time=") {
		t.Errorf("log starts mid-record:\n%s", body[:min(len(body), 80)])
	}
	if !strings.Contains(console.String(), "rotate-0000") {
		t.Errorf("console lost the first record, want it unaffected by the file's trim")
	}
}

func TestXrayLoggerForFailsOnUnopenablePath(t *testing.T) {
	env := Env{XrayLogFile: filepath.Join(t.TempDir(), "missing", "xray.log")}
	if _, _, err := xrayLoggerFor(env, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("xrayLoggerFor = nil error, want one naming XRAY_LOG_FILE")
	} else if !strings.Contains(err.Error(), "XRAY_LOG_FILE") {
		t.Errorf("error = %q, want it to name XRAY_LOG_FILE", err)
	}
}
