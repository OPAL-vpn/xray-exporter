package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/OPAL-vpn/xray-exporter/internal/netutil"
)

// Target is a host with an optional /path, or a bracketed IPv6 literal.
type Target struct {
	Host string `yaml:"host"`
}

type Resolver struct {
	Name    string `yaml:"name"`
	DoHURL  string `yaml:"doh_url"`
	DoTHost string `yaml:"dot_host"`
}

// UDP target kinds. A STUN binding burst is the only kind: it is the first
// packet pattern of a Telegram call and answers on plain and TURN servers
// alike.
const udpKindSTUN = "stun"

// The burst defaults and limits. count x spacing must fit the target's timeout
// so a cycle deadline never cuts a burst mid-flight.
const (
	defaultUDPCount   = 20
	maxUDPCount       = 1000
	defaultUDPSpacing = 100 * time.Millisecond
	minUDPSpacing     = time.Millisecond
	maxUDPSpacing     = time.Second
)

// UDPTarget is one UDP quality probe destination: Count datagrams of Kind sent
// Spacing apart. Count, SpacingDur and TimeoutDur are filled by validate.
type UDPTarget struct {
	Name    string `yaml:"name"`
	Kind    string `yaml:"kind"`
	Addr    string `yaml:"addr"`
	Count   int    `yaml:"count,omitempty"`
	Spacing string `yaml:"spacing,omitempty"`
	Timeout string `yaml:"timeout,omitempty"`

	SpacingDur time.Duration `yaml:"-"` // default 100ms
	TimeoutDur time.Duration `yaml:"-"` // 0 means the sampler's probe timeout
}

// TCPTarget is one plain CONNECT through the tunnel.
type TCPTarget struct {
	Name string `yaml:"name"`
	Addr string `yaml:"addr"`
}

// UDPUnderLoad adds the loaded pass: URL is a bulk download run beside every
// burst, through the same tunnel.
type UDPUnderLoad struct {
	URL string `yaml:"url"`
}

// XrayConfigSource selects where full xray client configs come from. Interval is
// the parsed Refresh; it stays zero for a directory source.
type XrayConfigSource struct {
	Type     string        `yaml:"type"`
	Path     string        `yaml:"path,omitempty"`
	URL      string        `yaml:"url,omitempty"`
	Refresh  string        `yaml:"refresh,omitempty"`
	Interval time.Duration `yaml:"-"`
}

// ExporterConfig is the parsed xray-exporter-config.yml. Nothing is defaulted
// except the config source's refresh and a udp target's count and spacing: a
// missing field is an error.
type ExporterConfig struct {
	DNSProbeDomain   string            `yaml:"dns_probe_domain"`
	DNSUpstream      string            `yaml:"dns_upstream"`
	Targets          []Target          `yaml:"targets"`
	Resolvers        []Resolver        `yaml:"dns_resolvers"`
	XrayConfigSource *XrayConfigSource `yaml:"xray_config_source,omitempty"`
	Reachability     *Reachability     `yaml:"reachability,omitempty"`
	UDPTargets       []UDPTarget       `yaml:"udp_targets,omitempty"`
	TCPTargets       []TCPTarget       `yaml:"tcp_targets,omitempty"`
	UDPUnderLoad     *UDPUnderLoad     `yaml:"udp_under_load,omitempty"`
}

func Load(path string) (*ExporterConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fileError(path, err)
	}
	defer f.Close()

	var cfg ExporterConfig
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fileError(path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fileError(path, err)
	}
	return &cfg, nil
}

func fileError(path string, err error) error {
	return fmt.Errorf("%s: %w", filepath.Base(path), err)
}

func (c *ExporterConfig) validate() error {
	if strings.TrimSpace(c.DNSProbeDomain) == "" {
		return errors.New("dns_probe_domain is required")
	}
	if strings.TrimSpace(c.DNSUpstream) == "" {
		return errors.New("dns_upstream is required")
	}

	if len(c.Targets) == 0 {
		return errors.New("at least one target is required")
	}
	seenHosts := make(map[string]struct{}, len(c.Targets))
	for i := range c.Targets {
		host, err := normalizeHost(c.Targets[i].Host)
		if err != nil {
			return fmt.Errorf("target %d: %w", i, err)
		}
		if _, ok := seenHosts[host]; ok {
			return fmt.Errorf("duplicate target %q", host)
		}
		seenHosts[host] = struct{}{}
		c.Targets[i].Host = host
	}

	seenNames := make(map[string]struct{}, len(c.Resolvers))
	for i, resolver := range c.Resolvers {
		if strings.TrimSpace(resolver.Name) == "" {
			return fmt.Errorf("resolver %d: name is required", i)
		}
		if _, ok := seenNames[resolver.Name]; ok {
			return fmt.Errorf("duplicate resolver %q", resolver.Name)
		}
		seenNames[resolver.Name] = struct{}{}
		if resolver.DoHURL == "" && resolver.DoTHost == "" {
			return fmt.Errorf("resolver %q: needs doh_url or dot_host", resolver.Name)
		}
	}

	if err := c.validateUDPAndTCP(); err != nil {
		return err
	}

	if err := validateSource(c.XrayConfigSource); err != nil {
		return err
	}
	if c.Reachability != nil {
		if err := c.Reachability.validate(); err != nil {
			return err
		}
	}

	return validateUpstream(c.DNSUpstream)
}

// validateUDPAndTCP checks the optional UDP and TCP target blocks and parses
// each UDP target's durations. Names are unique across both blocks: both render
// into one target label, so a shared name would collide into one series.
func (c *ExporterConfig) validateUDPAndTCP() error {
	seen := make(map[string]struct{}, len(c.UDPTargets)+len(c.TCPTargets))
	for i := range c.UDPTargets {
		if err := c.UDPTargets[i].validate(); err != nil {
			return fmt.Errorf("udp_targets[%d]: %w", i, err)
		}
		name := c.UDPTargets[i].Name
		if _, ok := seen[name]; ok {
			return fmt.Errorf("udp_targets[%d]: duplicate name %q", i, name)
		}
		seen[name] = struct{}{}
	}
	for i := range c.TCPTargets {
		if err := c.TCPTargets[i].validate(); err != nil {
			return fmt.Errorf("tcp_targets[%d]: %w", i, err)
		}
		name := c.TCPTargets[i].Name
		if _, ok := seen[name]; ok {
			return fmt.Errorf("tcp_targets[%d]: duplicate name %q", i, name)
		}
		seen[name] = struct{}{}
	}
	if c.UDPUnderLoad == nil {
		return nil
	}
	if err := c.UDPUnderLoad.validate(); err != nil {
		return fmt.Errorf("udp_under_load: %w", err)
	}
	return nil
}

// validate checks one udp target and fills its defaults: count 20, spacing
// 100ms. A zero count means the default: YAML cannot tell an absent count from
// a written zero.
func (t *UDPTarget) validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("name is required")
	}
	if t.Kind != udpKindSTUN {
		return fmt.Errorf("kind must be %s", udpKindSTUN)
	}
	if err := validateAddr(t.Addr); err != nil {
		return err
	}
	if t.Count == 0 {
		t.Count = defaultUDPCount
	}
	if t.Count < 1 || t.Count > maxUDPCount {
		return fmt.Errorf("count must be in 1-%d", maxUDPCount)
	}
	if t.Spacing == "" {
		t.SpacingDur = defaultUDPSpacing
	} else {
		spacing, err := time.ParseDuration(t.Spacing)
		if err != nil {
			return fmt.Errorf("spacing: %w", err)
		}
		if spacing < minUDPSpacing || spacing > maxUDPSpacing {
			return fmt.Errorf("spacing must be between %s and %s", minUDPSpacing, maxUDPSpacing)
		}
		t.SpacingDur = spacing
	}
	if t.Timeout == "" {
		t.TimeoutDur = 0
		return nil
	}
	timeout, err := time.ParseDuration(t.Timeout)
	if err != nil {
		return fmt.Errorf("timeout: %w", err)
	}
	if burst := time.Duration(t.Count) * t.SpacingDur; burst >= timeout {
		return fmt.Errorf("timeout %s must be longer than count x spacing (%s)", timeout, burst)
	}
	t.TimeoutDur = timeout
	return nil
}

func (t *TCPTarget) validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("name is required")
	}
	return validateAddr(t.Addr)
}

func (u *UDPUnderLoad) validate() error {
	return validateAbsoluteHTTPURL(u.URL)
}

// validateSource checks the optional source block and parses its refresh. Every
// error names the block, so the message points at the YAML key.
func validateSource(src *XrayConfigSource) error {
	if src == nil {
		return nil
	}
	if err := src.validate(); err != nil {
		return fmt.Errorf("xray_config_source: %w", err)
	}
	return nil
}

func (s *XrayConfigSource) validate() error {
	switch s.Type {
	case "directory":
		if s.URL != "" {
			return errors.New("url is only valid for a remnawave source")
		}
		if s.Refresh != "" {
			return errors.New("refresh is only valid for a remnawave source")
		}
		s.Interval = 0
		return nil
	case "remnawave":
		if s.Path != "" {
			return errors.New("path and url are mutually exclusive")
		}
		if err := validateAbsoluteHTTPURL(s.URL); err != nil {
			return err
		}
		if s.Refresh == "" {
			s.Interval = 5 * time.Minute
			return nil
		}
		interval, err := time.ParseDuration(s.Refresh)
		if err != nil {
			return fmt.Errorf("refresh: %w", err)
		}
		if interval < 0 {
			return errors.New("refresh must not be negative")
		}
		s.Interval = interval
		return nil
	default:
		return errors.New("type must be directory or remnawave")
	}
}

// validateAbsoluteHTTPURL names the url field without echoing the value: the
// source URL embeds the subscription shortUuid and the under-load URL may
// embed credentials, so neither value may reach logs. That is also why
// url.Parse's own error is not wrapped.
func validateAbsoluteHTTPURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("url is required")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("url must be an absolute http or https URL with a host")
	}
	return nil
}

// normalizeHost validates a target host and returns it trimmed. The accepted
// forms are netutil.ParseTarget's; a target adds the rule that it takes no port.
func normalizeHost(host string) (string, error) {
	target, err := netutil.ParseTarget(host)
	if err != nil {
		return "", err
	}
	// A target's DNS probe takes the name, so a port belongs to an https control.
	if target.Port != "" {
		return "", fmt.Errorf("host %q: a target takes no port", host)
	}
	return strings.TrimSpace(host), nil
}

func validateUpstream(upstream string) error {
	if _, _, err := netutil.HostPort(upstream); err != nil {
		return errors.New("dns_upstream must be host:port")
	}
	return nil
}
