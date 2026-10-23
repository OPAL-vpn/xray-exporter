package xrayproc

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
	"github.com/OPAL-vpn/xray-exporter/internal/xraylog"
)

// managed pairs an instance with the raw bytes it was started from.
type managed struct {
	inst *Instance
	raw  []byte
}

// Manager owns the xray instances and serializes Apply against Instances.
type Manager struct {
	mu       sync.Mutex
	up       xraycfg.Upstream
	logLevel string
	logger   *slog.Logger
	running  map[string]managed
}

// NewManager returns a manager for up at logLevel.
func NewManager(up xraycfg.Upstream, logLevel string, logger *slog.Logger) *Manager {
	return &Manager{
		up:       up,
		logLevel: logLevel,
		logger:   logger,
		running:  make(map[string]managed),
	}
}

// Apply stops configs that changed or disappeared, starts missing ones, and
// keeps unchanged instances. Failed starts are reported without aborting the
// batch. The log handler is installed after the starts because each start
// replaces it.
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

// Instances returns a snapshot sorted by name.
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
