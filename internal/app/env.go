package app

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Env is the exporter's configuration; LoadEnv applies the defaults.
type Env struct {
	ListenAddr       string
	ConfigDir        string
	TargetsFile      string
	Interval         time.Duration
	Workers          int
	ProbeTimeout     time.Duration
	ResolverTimeout  time.Duration
	XrayLogLevel     string
	XrayLogFile      string
	XrayLogMaxBytes  int64
	XrayLogKeepBytes int64
	XrayAssetDir     string
	LogLevel         slog.Level
}

// LoadEnv reads the environment; every error names the variable it came from.
func LoadEnv(getenv func(string) string) (Env, error) {
	env := Env{
		ListenAddr:   text(getenv, "LISTEN_ADDR", ":9787"),
		ConfigDir:    text(getenv, "XRAY_CONFIG_DIR", "/config/xray"),
		TargetsFile:  text(getenv, "TARGETS_FILE", "/config/targets.yaml"),
		XrayLogLevel: text(getenv, "XRAY_LOG_LEVEL", "warning"),
		XrayLogFile:  getenv("XRAY_LOG_FILE"),
		XrayAssetDir: text(getenv, "XRAY_LOCATION_ASSET", "/usr/local/share/xray"),
	}

	var err error
	if env.Interval, err = duration(getenv, "INTERVAL", 5*time.Minute); err != nil {
		return Env{}, err
	}
	if env.ProbeTimeout, err = duration(getenv, "PROBE_TIMEOUT", 10*time.Second); err != nil {
		return Env{}, err
	}
	if env.ResolverTimeout, err = duration(getenv, "RESOLVER_TIMEOUT", 5*time.Second); err != nil {
		return Env{}, err
	}
	if env.XrayLogMaxBytes, err = byteCount(getenv, "XRAY_LOG_MAX_BYTES", 1<<30); err != nil {
		return Env{}, err
	}
	if env.XrayLogKeepBytes, err = byteCount(getenv, "XRAY_LOG_KEEP_BYTES", 750<<20); err != nil {
		return Env{}, err
	}
	if env.XrayLogMaxBytes > 0 && (env.XrayLogKeepBytes <= 0 || env.XrayLogKeepBytes >= env.XrayLogMaxBytes) {
		return Env{}, fmt.Errorf("XRAY_LOG_KEEP_BYTES must be > 0 and below XRAY_LOG_MAX_BYTES")
	}
	if env.Workers, err = workers(getenv); err != nil {
		return Env{}, err
	}
	if env.LogLevel, err = logLevel(getenv); err != nil {
		return Env{}, err
	}

	return env, nil
}

func text(getenv func(string) string, name, def string) string {
	if v := getenv(name); v != "" {
		return v
	}
	return def
}

func duration(getenv func(string) string, name string, def time.Duration) (time.Duration, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration: %w", name, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be > 0", name)
	}
	return d, nil
}

func byteCount(getenv func(string) string, name string, def int64) (int64, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a byte count: %w", name, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("%s must be >= 0", name)
	}
	return n, nil
}

func workers(getenv func(string) string) (int, error) {
	raw := getenv("WORKERS")
	if raw == "" {
		return 8, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("WORKERS must be an integer: %w", err)
	}
	if n < 1 {
		return 0, fmt.Errorf("WORKERS must be >= 1")
	}
	return n, nil
}

func logLevel(getenv func(string) string) (slog.Level, error) {
	raw := getenv("LOG_LEVEL")
	if raw == "" {
		return slog.LevelInfo, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("LOG_LEVEL: unknown level %q, want debug, info, warn or error", raw)
}
