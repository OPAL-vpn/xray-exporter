package app

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

// getenvFunc turns a map into the getenv function LoadEnv takes.
func getenvFunc(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestLoadEnvDefaults(t *testing.T) {
	env, err := LoadEnv(func(string) string { return "" })
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}

	want := Env{
		ListenAddr:      ":9787",
		ConfigDir:       "/config/xray",
		TargetsFile:     "/config/targets.yaml",
		Interval:        5 * time.Minute,
		Workers:         8,
		ProbeTimeout:    10 * time.Second,
		ResolverTimeout: 5 * time.Second,
		EgressTTL:       time.Hour,
		XrayLogLevel:    "warning",
		XrayLogFile:     "",
		XrayAssetDir:    "/usr/local/share/xray",
		LogLevel:        slog.LevelInfo,
	}
	if env != want {
		t.Errorf("LoadEnv() = %+v, want %+v", env, want)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	env, err := LoadEnv(getenvFunc(map[string]string{
		"LISTEN_ADDR":         "127.0.0.1:9100",
		"XRAY_CONFIG_DIR":     "/etc/xray",
		"TARGETS_FILE":        "/etc/targets.yaml",
		"INTERVAL":            "90s",
		"WORKERS":             "3",
		"PROBE_TIMEOUT":       "3s",
		"RESOLVER_TIMEOUT":    "2s",
		"EGRESS_CACHE_TTL":    "10m",
		"XRAY_LOG_LEVEL":      "debug",
		"XRAY_LOG_FILE":       "/var/log/xray.log",
		"XRAY_LOCATION_ASSET": "/assets",
		"LOG_LEVEL":           "warn",
	}))
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}

	want := Env{
		ListenAddr:      "127.0.0.1:9100",
		ConfigDir:       "/etc/xray",
		TargetsFile:     "/etc/targets.yaml",
		Interval:        90 * time.Second,
		Workers:         3,
		ProbeTimeout:    3 * time.Second,
		ResolverTimeout: 2 * time.Second,
		EgressTTL:       10 * time.Minute,
		XrayLogLevel:    "debug",
		XrayLogFile:     "/var/log/xray.log",
		XrayAssetDir:    "/assets",
		LogLevel:        slog.LevelWarn,
	}
	if env != want {
		t.Errorf("LoadEnv() = %+v, want %+v", env, want)
	}
}

func TestLoadEnvInvalid(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{name: "WORKERS", value: "0", want: "WORKERS must be >= 1"},
		{name: "WORKERS", value: "-2", want: "WORKERS must be >= 1"},
		{name: "WORKERS", value: "eight", want: "WORKERS"},
		{name: "INTERVAL", value: "nope", want: "INTERVAL"},
		{name: "PROBE_TIMEOUT", value: "fast", want: "PROBE_TIMEOUT"},
		{name: "RESOLVER_TIMEOUT", value: "0s", want: "RESOLVER_TIMEOUT"},
		{name: "EGRESS_CACHE_TTL", value: "-1m", want: "EGRESS_CACHE_TTL"},
		{name: "LOG_LEVEL", value: "loud", want: "LOG_LEVEL"},
	}

	for _, tc := range cases {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			_, err := LoadEnv(getenvFunc(map[string]string{tc.name: tc.value}))
			if err == nil {
				t.Fatalf("LoadEnv(%s=%s) = nil error, want one naming %s", tc.name, tc.value, tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("LoadEnv(%s=%s) error = %q, want it to contain %q", tc.name, tc.value, err, tc.want)
			}
		})
	}
}
