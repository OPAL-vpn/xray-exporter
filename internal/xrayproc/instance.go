// Package xrayproc runs one in-process xray-core instance per config. xray is
// linked in, and internal/xraylog bridges its process-wide log stream.
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

type Instance struct {
	Name      string
	SocksPort int
	DNSPort   int
	Version   string

	core     *core.Instance
	closeOne sync.Once
	closeErr error
}

// Close is safe to call more than once.
func (i *Instance) Close() error {
	i.closeOne.Do(func() { i.closeErr = i.core.Close() })
	return i.closeErr
}

// StartError is one config's failed start. Start already logged it, so callers
// must not log it again.
type StartError struct {
	Config string
	Err    error
}

func (e *StartError) Error() string { return e.Config + ": " + e.Err.Error() }

func (e *StartError) Unwrap() error { return e.Err }

// Start starts cfg with injected socks and tunnel-DNS inbounds and returns once
// the socks inbound accepts.
func Start(cfg xraycfg.Config, up xraycfg.Upstream, logLevel string, logger *slog.Logger) (*Instance, error) {
	inst, err := start(cfg, up, logLevel, logger)
	if err != nil {
		// Match the bridged stream's subsystem so one filter shows the whole story.
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

	// core.New registers xray's own process-wide handler, so capture after it.
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
