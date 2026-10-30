package app

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func getenvFunc(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestLoadEnvDefaults(t *testing.T) {
	env, err := LoadEnv(func(string) string { return "" })
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}

	want := Env{
		ListenAddr:       ":9787",
		ConfigDir:        "/config/xray",
		ConfigFile:       "/config/xray-exporter-config.yml",
		Interval:         5 * time.Minute,
		Workers:          8,
		ProbeTimeout:     10 * time.Second,
		ResolverTimeout:  5 * time.Second,
		ControlTimeout:   5 * time.Second,
		XrayLogLevel:     "warning",
		XrayLogFile:      "",
		XrayLogMaxBytes:  1 << 30,
		XrayLogKeepBytes: 750 << 20,
		XrayAssetDir:     "/usr/local/share/xray",
		LogLevel:         slog.LevelInfo,
	}
	if env != want {
		t.Errorf("LoadEnv() = %+v, want %+v", env, want)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	env, err := LoadEnv(getenvFunc(map[string]string{
		"LISTEN_ADDR":          "127.0.0.1:9100",
		"XRAY_CONFIG_DIR":      "/etc/xray",
		"EXPORTER_CONFIG_FILE": "/etc/xray-exporter-config.yml",
		"INTERVAL":             "90s",
		"WORKERS":              "3",
		"PROBE_TIMEOUT":        "3s",
		"RESOLVER_TIMEOUT":     "2s",
		"CONTROL_TIMEOUT":      "3s",
		"XRAY_LOG_LEVEL":       "debug",
		"XRAY_LOG_FILE":        "/var/log/xray.log",
		"XRAY_LOG_MAX_BYTES":   "2097152",
		"XRAY_LOG_KEEP_BYTES":  "1048576",
		"XRAY_LOCATION_ASSET":  "/assets",
		"LOG_LEVEL":            "warn",
	}))
	if err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}

	want := Env{
		ListenAddr:       "127.0.0.1:9100",
		ConfigDir:        "/etc/xray",
		ConfigFile:       "/etc/xray-exporter-config.yml",
		Interval:         90 * time.Second,
		Workers:          3,
		ProbeTimeout:     3 * time.Second,
		ResolverTimeout:  2 * time.Second,
		ControlTimeout:   3 * time.Second,
		XrayLogLevel:     "debug",
		XrayLogFile:      "/var/log/xray.log",
		XrayLogMaxBytes:  2097152,
		XrayLogKeepBytes: 1048576,
		XrayAssetDir:     "/assets",
		LogLevel:         slog.LevelWarn,
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
		{name: "CONTROL_TIMEOUT", value: "0s", want: "CONTROL_TIMEOUT"},
		{name: "LOG_LEVEL", value: "loud", want: "LOG_LEVEL"},
		{name: "XRAY_LOG_MAX_BYTES", value: "-1", want: "XRAY_LOG_MAX_BYTES"},
		{name: "XRAY_LOG_MAX_BYTES", value: "1G", want: "XRAY_LOG_MAX_BYTES"},
		{name: "XRAY_LOG_KEEP_BYTES", value: "lots", want: "XRAY_LOG_KEEP_BYTES"},
		{name: "XRAY_LOG_KEEP_BYTES", value: "0", want: "XRAY_LOG_KEEP_BYTES"},
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

func TestLoadEnvRejectsKeepNotBelowMax(t *testing.T) {
	_, err := LoadEnv(getenvFunc(map[string]string{
		"XRAY_LOG_MAX_BYTES":  "1048576",
		"XRAY_LOG_KEEP_BYTES": "1048576",
	}))
	if err == nil {
		t.Fatal("LoadEnv(keep == max) = nil error, want one naming XRAY_LOG_KEEP_BYTES")
	}
	if !strings.Contains(err.Error(), "XRAY_LOG_KEEP_BYTES") {
		t.Errorf("error = %q, want it to name XRAY_LOG_KEEP_BYTES", err)
	}
}

func TestLoadEnvZeroMaxDisablesRotation(t *testing.T) {
	env, err := LoadEnv(getenvFunc(map[string]string{
		"XRAY_LOG_MAX_BYTES":  "0",
		"XRAY_LOG_KEEP_BYTES": "1",
	}))
	if err != nil {
		t.Fatalf("LoadEnv(max=0) = %v, want rotation to be switchable off", err)
	}
	if env.XrayLogMaxBytes != 0 || env.XrayLogKeepBytes != 1 {
		t.Errorf("bytes = (%d, %d), want the values as given", env.XrayLogMaxBytes, env.XrayLogKeepBytes)
	}
}
