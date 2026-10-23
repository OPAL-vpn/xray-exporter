package app

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Env is the exporter's configuration surface: one field per environment
// variable, with the default applied when the variable is unset. LoadEnv is the
// only place those defaults exist.
type Env struct {
	ListenAddr      string
	ConfigDir       string
	TargetsFile     string
	Interval        time.Duration
	Workers         int
	ProbeTimeout    time.Duration
	ResolverTimeout time.Duration
	EgressTTL       time.Duration
	XrayLogLevel    string
	XrayLogFile     string
	XrayAssetDir    string
	LogLevel        slog.Level
}

// LoadEnv reads the exporter's environment. getenv is os.Getenv in main and a
// map lookup in tests. Every error names the variable it came from, so a bad
// container environment is diagnosable from the first log line.
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
	if env.EgressTTL, err = duration(getenv, "EGRESS_CACHE_TTL", time.Hour); err != nil {
		return Env{}, err
	}
	if env.Workers, err = workers(getenv); err != nil {
		return Env{}, err
	}
	if env.LogLevel, err = logLevel(getenv); err != nil {
		return Env{}, err
	}

	return env, nil
}

// text returns the variable's value, or def when it is unset.
func text(getenv func(string) string, name, def string) string {
	if v := getenv(name); v != "" {
		return v
	}
	return def
}

// duration parses a positive duration variable.
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

// workers parses WORKERS, the probe concurrency within one config.
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

// logLevel parses LOG_LEVEL. Only the four slog names are accepted: an empty
// value is handled before this call, so "" is an error here, not "info".
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
