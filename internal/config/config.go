package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Target is a single probe destination: a hostname, optionally with a path.
type Target struct {
	Host string `yaml:"host"`
}

// Resolver is a DNS resolver reached over DoH, DoT, or both.
type Resolver struct {
	Name    string `yaml:"name"`
	DoHURL  string `yaml:"doh_url"`
	DoTHost string `yaml:"dot_host"`
}

// Targets is the parsed targets.yaml. Nothing is defaulted: a field missing
// from the file is an error, not a built-in value.
type Targets struct {
	EgressCheckURL string     `yaml:"egress_check_url"`
	DNSProbeDomain string     `yaml:"dns_probe_domain"`
	DNSUpstream    string     `yaml:"dns_upstream"`
	Targets        []Target   `yaml:"targets"`
	Resolvers      []Resolver `yaml:"dns_resolvers"`
}

// Load reads and validates a targets file. Errors are prefixed with the file's
// base name so a log line names the file that was rejected.
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
	if strings.TrimSpace(t.EgressCheckURL) == "" {
		return errors.New("egress_check_url is required")
	}
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
	for i, target := range t.Targets {
		if strings.TrimSpace(target.Host) == "" {
			return fmt.Errorf("target %d: host is required", i)
		}
		if _, ok := seenHosts[target.Host]; ok {
			return fmt.Errorf("duplicate target %q", target.Host)
		}
		seenHosts[target.Host] = struct{}{}
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

func validateUpstream(upstream string) error {
	host, port, err := net.SplitHostPort(upstream)
	if err != nil || host == "" {
		return errors.New("dns_upstream must be host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("dns_upstream must be host:port")
	}
	return nil
}
