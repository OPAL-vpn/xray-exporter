package config

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
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

// Vantage turns on the probes that run outside any tunnel: the egress network
// lookup, the controls and, with EntryTCP, a direct connect to each config's
// entry address. The block is optional; without it the exporter sends none of
// that traffic.
type Vantage struct {
	Controls VantageControls `yaml:"controls,omitempty"`
	EntryTCP bool            `yaml:"entry_tcp,omitempty"`
}

// VantageControls are the direct probes run once per cycle.
type VantageControls struct {
	HTTPS []HTTPControl `yaml:"https,omitempty"`
	TCP   []TCPControl  `yaml:"tcp,omitempty"`
	DNS   []DNSControl  `yaml:"dns,omitempty"`
}

type HTTPControl struct {
	Name  string `yaml:"name"`
	Host  string `yaml:"host"`
	Class string `yaml:"class"`
}

type TCPControl struct {
	Name  string `yaml:"name"`
	Addr  string `yaml:"addr"`
	Class string `yaml:"class"`
}

type DNSControl struct {
	Name  string `yaml:"name"`
	Host  string `yaml:"host"`
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
func (c VantageControls) All() []Control {
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

// validate checks the controls. Every error names the block, so the message
// points at the YAML key.
func (v *Vantage) validate() error {
	if err := v.validateControls(); err != nil {
		return fmt.Errorf("vantage: controls: %w", err)
	}
	return nil
}

func (v *Vantage) validateControls() error {
	for i, c := range v.Controls.HTTPS {
		host, err := normalizeHost(c.Host)
		if err != nil {
			return fmt.Errorf("https %d: %w", i, err)
		}
		v.Controls.HTTPS[i].Host = host
	}
	for i, c := range v.Controls.TCP {
		if err := validateAddr(c.Addr); err != nil {
			return fmt.Errorf("tcp %d: %w", i, err)
		}
	}
	for i, c := range v.Controls.DNS {
		host, err := normalizeHost(c.Host)
		if err != nil {
			return fmt.Errorf("dns %d: %w", i, err)
		}
		if net.ParseIP(host) != nil {
			return fmt.Errorf("dns %d: host must be a name, not an address", i)
		}
		v.Controls.DNS[i].Host = host
	}

	seen := make(map[string]struct{})
	for _, c := range v.Controls.All() {
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
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return fmt.Errorf("addr %q must be host:port", addr)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return errors.New("addr " + strconv.Quote(addr) + " has an invalid port")
	}
	return nil
}
