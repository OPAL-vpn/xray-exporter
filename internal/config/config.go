package config

import (
	"errors"
	"fmt"
	"net"
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
// except the config source's refresh: a missing field is an error.
type ExporterConfig struct {
	DNSProbeDomain   string            `yaml:"dns_probe_domain"`
	DNSUpstream      string            `yaml:"dns_upstream"`
	Targets          []Target          `yaml:"targets"`
	Resolvers        []Resolver        `yaml:"dns_resolvers"`
	XrayConfigSource *XrayConfigSource `yaml:"xray_config_source,omitempty"`
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

	if err := validateSource(c.XrayConfigSource); err != nil {
		return err
	}

	return validateUpstream(c.DNSUpstream)
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
		if err := validateSourceURL(s.URL); err != nil {
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

// validateSourceURL names the url field without echoing the value: the URL
// embeds the subscription shortUuid and must not reach logs.
func validateSourceURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("url is required")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("url must be an absolute http or https URL with a host")
	}
	return nil
}

// normalizeHost validates a target host and returns it trimmed. This is the
// single owner of the accepted forms.
func normalizeHost(host string) (string, error) {
	h := strings.TrimSpace(host)
	if h == "" {
		return "", errors.New("host is required")
	}
	// A scheme would break the DNS probe of the same target.
	if strings.Contains(h, "://") {
		return "", errors.New("host must not include a scheme")
	}

	name := h
	if i := strings.IndexByte(name, '/'); i >= 0 {
		name = name[:i]
	}
	if name == "" {
		return "", fmt.Errorf("host %q has no name", h)
	}

	if strings.HasPrefix(name, "[") {
		if !strings.HasSuffix(name, "]") || net.ParseIP(name[1:len(name)-1]) == nil {
			return "", fmt.Errorf("host %q is not a bracketed address", h)
		}
		return h, nil
	}
	if ip := net.ParseIP(name); ip != nil {
		if ip.To4() == nil {
			return "", fmt.Errorf("host %q: an IPv6 address must be bracketed", h)
		}
		return h, nil
	}
	if strings.Contains(name, ":") {
		return "", fmt.Errorf("host %q must be a hostname or a bracketed IPv6 address", h)
	}
	return h, nil
}

func validateUpstream(upstream string) error {
	if _, _, err := netutil.HostPort(upstream); err != nil {
		return errors.New("dns_upstream must be host:port")
	}
	return nil
}
