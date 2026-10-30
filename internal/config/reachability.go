package config

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/OPAL-vpn/xray-exporter/internal/netutil"
)

// Control classes: where the probed host lives relative to the blocking regime.
// The split between them is the signal: domestic up and foreign down looks like
// a whitelist, everything down is a dead line.
const (
	ClassDomestic = "domestic"
	ClassForeign  = "foreign"
)

// Control kinds, also the value of the kind label.
const (
	ControlHTTPS = "https"
	ControlTCP   = "tcp"
	ControlDNS   = "dns"
)

var controlName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Reachability turns on the probes that run outside any tunnel: the egress
// network lookup, the controls and, with EntryTCP, a direct connect to each
// config's entry address. The block is optional; without it the exporter sends
// none of that traffic.
type Reachability struct {
	Controls ReachabilityControls `yaml:"controls,omitempty"`
	EntryTCP bool                 `yaml:"entry_tcp,omitempty"`
}

// ReachabilityControls are the direct probes run once per cycle.
type ReachabilityControls struct {
	HTTPS []HostControl `yaml:"https,omitempty"`
	TCP   []TCPControl  `yaml:"tcp,omitempty"`
	DNS   []HostControl `yaml:"dns,omitempty"`
}

// HostControl is an https or dns control: both name a host.
type HostControl struct {
	Name  string `yaml:"name"`
	Host  string `yaml:"host"`
	Class string `yaml:"class"`
}

type TCPControl struct {
	Name  string `yaml:"name"`
	Addr  string `yaml:"addr"`
	Class string `yaml:"class"`
}

// Control is one direct probe in a form the app and the snapshot share.
type Control struct {
	Kind  string
	Name  string
	Class string
	// Target is the host (https, dns) or host:port (tcp).
	Target string
}

// All lists every control in a stable order: https, tcp, dns, each as written.
func (c ReachabilityControls) All() []Control {
	out := make([]Control, 0, len(c.HTTPS)+len(c.TCP)+len(c.DNS))
	for _, h := range c.HTTPS {
		out = append(out, Control{Kind: ControlHTTPS, Name: h.Name, Class: h.Class, Target: h.Host})
	}
	for _, t := range c.TCP {
		out = append(out, Control{Kind: ControlTCP, Name: t.Name, Class: t.Class, Target: t.Addr})
	}
	for _, d := range c.DNS {
		out = append(out, Control{Kind: ControlDNS, Name: d.Name, Class: d.Class, Target: d.Host})
	}
	return out
}

// validate normalises the control targets in place and checks every control.
// Each error names the block, so the message points at the YAML key.
func (r *Reachability) validate() error {
	if err := r.normalizeTargets(); err != nil {
		return fmt.Errorf("reachability: controls: %w", err)
	}
	if err := r.checkControls(); err != nil {
		return fmt.Errorf("reachability: controls: %w", err)
	}
	return nil
}

func (r *Reachability) normalizeTargets() error {
	for i, c := range r.Controls.HTTPS {
		host, err := normalizeControlHost(c.Host)
		if err != nil {
			return fmt.Errorf("https %d: %w", i, err)
		}
		r.Controls.HTTPS[i].Host = host
	}
	for i, c := range r.Controls.TCP {
		if err := validateAddr(c.Addr); err != nil {
			return fmt.Errorf("tcp %d: %w", i, err)
		}
	}
	for i, c := range r.Controls.DNS {
		host, err := normalizeDNSHost(c.Host)
		if err != nil {
			return fmt.Errorf("dns %d: %w", i, err)
		}
		r.Controls.DNS[i].Host = host
	}
	return nil
}

// normalizeControlHost accepts host[:port][/path]: a target's grammar plus the
// port an https control adds over one.
func normalizeControlHost(host string) (string, error) {
	if _, err := netutil.ParseTarget(host); err != nil {
		return "", err
	}
	return strings.TrimSpace(host), nil
}

// normalizeDNSHost accepts the name a dns control resolves, so it takes no port.
func normalizeDNSHost(host string) (string, error) {
	target, err := netutil.ParseTarget(host)
	if err != nil {
		return "", err
	}
	if target.Port != "" {
		return "", fmt.Errorf("host %q: a dns control takes no port", host)
	}
	if net.ParseIP(target.Host) != nil {
		return "", errors.New("host must be a name, not an address")
	}
	return strings.TrimSpace(host), nil
}

// checkControls checks names, classes and duplicates.
func (r *Reachability) checkControls() error {
	seen := make(map[string]struct{})
	for _, c := range r.Controls.All() {
		if !controlName.MatchString(c.Name) {
			return fmt.Errorf("%s control %q: name must be lowercase letters, digits, - or _", c.Kind, c.Name)
		}
		if c.Class != ClassDomestic && c.Class != ClassForeign {
			return fmt.Errorf("%s control %q: class must be %s or %s", c.Kind, c.Name, ClassDomestic, ClassForeign)
		}
		key := c.Kind + "/" + c.Name
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate %s control %q", c.Kind, c.Name)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// validateAddr accepts host:port with a numeric port.
func validateAddr(addr string) error {
	if _, _, err := netutil.HostPort(addr); err != nil {
		return fmt.Errorf("addr %q must be host:port with a port in 1-65535", addr)
	}
	return nil
}
