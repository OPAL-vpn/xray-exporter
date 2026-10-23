// Package xrayproc runs one in-process xray-core instance per supplied config.
// xray is linked in, never executed as a child process, so a single binary
// carries both the exporter and every tunnel it measures. xray's log stream is
// process-wide; internal/xraylog bridges it into slog.
package xrayproc

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"

	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
	"github.com/OPAL-vpn/xray-exporter/internal/xraylog"
)

// Instance is one running xray-core instance serving one config.
type Instance struct {
	Name      string
	SocksPort int
	DNSPort   int
	Version   string

	core     *core.Instance
	closeOne sync.Once
	closeErr error
}

// Close stops the instance and releases its ports. It is safe to call more
// than once, so a t.Cleanup and a Manager.Close can both own the instance.
func (i *Instance) Close() error {
	i.closeOne.Do(func() { i.closeErr = i.core.Close() })
	return i.closeErr
}

// StartError is one config's failed start. Start has already logged it with the
// config name and xray's own cause, so a caller aggregating failures must not
// log it a second time.
type StartError struct {
	Config string
	Err    error
}

func (e *StartError) Error() string { return e.Config + ": " + e.Err.Error() }

func (e *StartError) Unwrap() error { return e.Err }

// Start builds and starts the xray instance for cfg, with the injected socks
// and tunnel-DNS inbounds listening on freshly allocated loopback ports. It
// returns only once the socks inbound accepts connections; every error names
// the config, and so does the line Start logs for it, because xray's own log
// stream does not.
//
// logger receives that line and the xray output captured while starting.
func Start(cfg xraycfg.Config, up xraycfg.Upstream, logLevel string, logger *slog.Logger) (*Instance, error) {
	inst, err := start(cfg, up, logLevel, logger)
	if err != nil {
		// Same subsystem as the bridged stream, so one filter shows xray's whole
		// story: what it said while starting, then why it failed.
		logger.Error("xray instance failed to start", "subsystem", "xray", "config", cfg.Name, "error", err)
		return nil, &StartError{Config: cfg.Name, Err: err}
	}
	return inst, nil
}

func start(cfg xraycfg.Config, up xraycfg.Upstream, logLevel string, logger *slog.Logger) (*Instance, error) {
	ports, err := allocatePorts()
	if err != nil {
		return nil, err
	}

	injected, err := xraycfg.Inject(cfg.Raw, ports, up, logLevel)
	if err != nil {
		return nil, err
	}

	var jsonConfig conf.Config
	if err := json.Unmarshal(injected, &jsonConfig); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	coreConfig, err := jsonConfig.Build()
	if err != nil {
		return nil, err
	}
	xrayInstance, err := core.New(coreConfig)
	if err != nil {
		return nil, err
	}

	// core.New runs app/log first, which registers xray's own handler for the
	// whole process; the capture has to come after that or it is already gone.
	// From here every line the instance emits while starting is buffered and
	// released with config=<name>.
	release := xraylog.Capture()
	defer func() { release(cfg.Name, logger) }()

	inst := &Instance{
		Name:      cfg.Name,
		SocksPort: ports.Socks,
		DNSPort:   ports.DNS,
		Version:   core.Version(),
		core:      xrayInstance,
	}
	if err := xrayInstance.Start(); err != nil {
		inst.Close()
		return nil, err
	}
	if err := waitForSocks(ports.Socks); err != nil {
		inst.Close()
		return nil, err
	}
	return inst, nil
}
