package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

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

// Targets is the parsed targets.yaml. Nothing is defaulted: a missing field is
// an error.
type Targets struct {
	DNSProbeDomain string     `yaml:"dns_probe_domain"`
	DNSUpstream    string     `yaml:"dns_upstream"`
	Targets        []Target   `yaml:"targets"`
	Resolvers      []Resolver `yaml:"dns_resolvers"`
}

func Load(path string) (*Targets, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fileError(path, err)
	}
	defer f.Close()

	var targets Targets
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&targets); err != nil {
		return nil, fileError(path, err)
	}
	if err := targets.validate(); err != nil {
		return nil, fileError(path, err)
	}
	return &targets, nil
}

func fileError(path string, err error) error {
	return fmt.Errorf("%s: %w", filepath.Base(path), err)
}

func (t *Targets) validate() error {
	if strings.TrimSpace(t.DNSProbeDomain) == "" {
		return errors.New("dns_probe_domain is required")
	}
	if strings.TrimSpace(t.DNSUpstream) == "" {
		return errors.New("dns_upstream is required")
	}

	if len(t.Targets) == 0 {
		return errors.New("at least one target is required")
	}
	seenHosts := make(map[string]struct{}, len(t.Targets))
	for i := range t.Targets {
		host, err := normalizeHost(t.Targets[i].Host)
		if err != nil {
			return fmt.Errorf("target %d: %w", i, err)
		}
		if _, ok := seenHosts[host]; ok {
			return fmt.Errorf("duplicate target %q", host)
		}
		seenHosts[host] = struct{}{}
		t.Targets[i].Host = host
	}

	seenNames := make(map[string]struct{}, len(t.Resolvers))
	for i, resolver := range t.Resolvers {
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

	return validateUpstream(t.DNSUpstream)
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
