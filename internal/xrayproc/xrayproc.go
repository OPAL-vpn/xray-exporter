// Package xrayproc runs one in-process xray-core instance per supplied config.
// xray is linked in, never executed as a child process, so a single binary
// carries both the exporter and every tunnel it measures. xray's log stream is
// process-wide; internal/xraylog bridges it into slog.
package xrayproc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"

	"github.com/Anton-beep/xray-exporter/internal/xraycfg"
	"github.com/Anton-beep/xray-exporter/internal/xraylog"
)

const (
	// loopbackListen matches the address xraycfg injects into the inbounds.
	loopbackListen = "127.0.0.1"

	// readyTimeout bounds how long Start waits for the socks inbound to accept.
	readyTimeout = 10 * time.Second
	// readyPollInterval is the pause between readiness dials.
	readyPollInterval = 100 * time.Millisecond
	// dialTimeout bounds a single readiness dial, not the whole wait.
	dialTimeout = 100 * time.Millisecond
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
		return nil, fmt.Errorf("%s: %w", cfg.Name, err)
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

// allocatePorts reserves two loopback ports, retrying once if the OS hands out
// the same number twice.
func allocatePorts() (xraycfg.Ports, error) {
	socks, err := xraycfg.FreePort()
	if err != nil {
		return xraycfg.Ports{}, err
	}
	dns, err := xraycfg.FreePort()
	if err != nil {
		return xraycfg.Ports{}, err
	}
	if dns == socks {
		if dns, err = xraycfg.FreePort(); err != nil {
			return xraycfg.Ports{}, err
		}
		if dns == socks {
			return xraycfg.Ports{}, fmt.Errorf("port allocation returned %d twice", socks)
		}
	}
	return xraycfg.Ports{Socks: socks, DNS: dns}, nil
}

// waitForSocks polls the socks port until it accepts, so Start never returns an
// instance whose inbound is still unbound.
func waitForSocks(port int) error {
	addr := net.JoinHostPort(loopbackListen, strconv.Itoa(port))
	deadline := time.Now().Add(readyTimeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, dialTimeout)
		if err == nil {
			conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("socks inbound on %s not accepting after %s: %w", addr, readyTimeout, err)
		}
		time.Sleep(readyPollInterval)
	}
}

// managed pairs a running instance with the raw bytes it was started from, so
// Apply can tell an unchanged config from an edited one.
type managed struct {
	inst *Instance
	raw  []byte
}

// Manager owns the process's xray instances and reconciles them against the
// configs found on disk. It serializes Apply, so a reload and a scrape-driven
// Instances read never race.
type Manager struct {
	mu       sync.Mutex
	up       xraycfg.Upstream
	logLevel string
	logger   *slog.Logger
	running  map[string]managed
}

// NewManager returns a manager that starts every instance against up and
// logLevel and routes xray's log stream to logger.
func NewManager(up xraycfg.Upstream, logLevel string, logger *slog.Logger) *Manager {
	return &Manager{
		up:       up,
		logLevel: logLevel,
		logger:   logger,
		running:  make(map[string]managed),
	}
}

// Apply reconciles the running instances with cfgs: configs that disappeared or
// whose raw bytes changed are stopped, missing ones are started, and unchanged
// ones keep their instance and ports. A config that fails to start is reported
// in errs and does not abort the rest of the batch. started and stopped are
// sorted by config name. An unparseable logLevel is reported in errs too and
// leaves the previously installed handler in place.
//
// Apply installs the log handler after the last start, because every instance
// start replaces it.
func (m *Manager) Apply(cfgs []xraycfg.Config) (started, stopped []string, errs []error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	wanted := make(map[string]xraycfg.Config, len(cfgs))
	for _, cfg := range cfgs {
		wanted[cfg.Name] = cfg
	}

	for name, current := range m.running {
		next, keeping := wanted[name]
		if keeping && bytes.Equal(current.raw, next.Raw) {
			continue
		}
		if err := current.inst.Close(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
		delete(m.running, name)
		stopped = append(stopped, name)
	}

	for _, cfg := range cfgs {
		if _, running := m.running[cfg.Name]; running {
			continue
		}
		inst, err := Start(cfg, m.up, m.logLevel, m.logger)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		m.running[cfg.Name] = managed{inst: inst, raw: cfg.Raw}
		started = append(started, cfg.Name)
	}

	if min, err := xraylog.ParseSeverity(m.logLevel); err != nil {
		errs = append(errs, err)
	} else {
		xraylog.Install(m.logger, min)
	}

	sort.Strings(started)
	sort.Strings(stopped)
	return started, stopped, errs
}

// Instances returns the running instances sorted by name. The slice is a
// snapshot; the instances themselves live until a later Apply or Close.
func (m *Manager) Instances() []*Instance {
	m.mu.Lock()
	defer m.mu.Unlock()

	instances := make([]*Instance, 0, len(m.running))
	for _, current := range m.running {
		instances = append(instances, current.inst)
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].Name < instances[j].Name })
	return instances
}

// Close stops every running instance.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for name, current := range m.running {
		if err := current.inst.Close(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
		delete(m.running, name)
	}
	return errors.Join(errs...)
}
