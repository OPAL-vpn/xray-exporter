// Package xraycfg loads xray client configs from a directory and rewrites the
// two top-level sections the exporter owns: log and inbounds. Every other
// section keeps its value so a supplied config's outbound, routing and policy
// are never altered.
package xraycfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	// loopbackListen keeps every injected inbound off the machine's interfaces.
	loopbackListen = "127.0.0.1"
	socksTag       = "xray-exporter-socks"
	dnsTag         = "xray-exporter-dns"
)

// Ports are the loopback ports the injected inbounds listen on.
type Ports struct {
	Socks int
	DNS   int
}

// Upstream is the resolver the tunnel DNS inbound forwards to.
type Upstream struct {
	Host string
	Port int
}

// ErrNoConfigs reports a config directory that was read successfully but holds
// no *.json file. It is not a read failure: the directory answered, so the empty
// set is authoritative and the caller must stop every instance instead of
// keeping the previous ones.
var ErrNoConfigs = errors.New("no *.json configs")

// Config is one xray client config found in the config directory. Name is the
// filename stem, which is the Prometheus label. Ports is left zero by Discover;
// the caller fills it with the ports it allocated.
type Config struct {
	Name  string
	Path  string
	Raw   []byte
	Ports Ports
}

// Discover returns every *.json regular file in dir, sorted by name stem. An
// unreadable directory fails with its own error; a readable directory with no
// configs fails with ErrNoConfigs, which the caller treats as an empty set.
func Discover(dir string) ([]Config, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}

	configs := make([]Config, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		configs = append(configs, Config{
			Name: strings.TrimSuffix(entry.Name(), ".json"),
			Path: path,
			Raw:  raw,
		})
	}
	if len(configs) == 0 {
		return nil, fmt.Errorf("%s: %w", dir, ErrNoConfigs)
	}

	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })
	return configs, nil
}

// UpstreamFrom parses a host:port resolver address.
func UpstreamFrom(s string) (Upstream, error) {
	host, port, err := net.SplitHostPort(s)
	if err != nil || host == "" {
		return Upstream{}, fmt.Errorf("upstream %q is not host:port", s)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return Upstream{}, fmt.Errorf("upstream %q is not host:port", s)
	}
	return Upstream{Host: host, Port: n}, nil
}

// Inject replaces the top-level log and inbounds sections of raw and returns the
// re-encoded config. Outbounds must already be non-empty: replacing inbounds is
// only safe for a complete client config.
func Inject(raw []byte, p Ports, up Upstream, logLevel string) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if err := requireOutbounds(doc); err != nil {
		return nil, err
	}

	logSection, err := marshal(logSection{LogLevel: logLevel, Access: "none"})
	if err != nil {
		return nil, err
	}
	inbounds, err := marshal([]any{
		socksInbound{
			Listen:   loopbackListen,
			Port:     p.Socks,
			Protocol: "socks",
			Tag:      socksTag,
			Settings: socksSettings{Auth: "noauth", UDP: true},
		},
		dnsInbound{
			Listen:   loopbackListen,
			Port:     p.DNS,
			Protocol: "dokodemo-door",
			Tag:      dnsTag,
			Settings: dnsSettings{Address: up.Host, Port: up.Port, Network: "udp"},
		},
	})
	if err != nil {
		return nil, err
	}
	doc["log"] = logSection
	doc["inbounds"] = inbounds

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return out, nil
}

func requireOutbounds(doc map[string]json.RawMessage) error {
	raw, ok := doc["outbounds"]
	if !ok {
		return errors.New("outbounds is empty")
	}
	var outbounds []json.RawMessage
	if err := json.Unmarshal(raw, &outbounds); err != nil {
		return fmt.Errorf("outbounds is not an array: %w", err)
	}
	if len(outbounds) == 0 {
		return errors.New("outbounds is empty")
	}
	return nil
}

// FreePort reserves and releases a loopback port, so the number handed out is
// neither fixed nor documented.
func FreePort() (int, error) {
	ln, err := net.Listen("tcp", loopbackListen+":0")
	if err != nil {
		return 0, fmt.Errorf("reserve loopback port: %w", err)
	}
	defer ln.Close()

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("reserve loopback port: got %T", ln.Addr())
	}
	return addr.Port, nil
}

func marshal(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode section: %w", err)
	}
	return b, nil
}

type logSection struct {
	LogLevel string `json:"loglevel"`
	Access   string `json:"access"`
}

type socksInbound struct {
	Listen   string        `json:"listen"`
	Port     int           `json:"port"`
	Protocol string        `json:"protocol"`
	Tag      string        `json:"tag"`
	Settings socksSettings `json:"settings"`
}

type socksSettings struct {
	Auth string `json:"auth"`
	UDP  bool   `json:"udp"`
}

type dnsInbound struct {
	Listen   string      `json:"listen"`
	Port     int         `json:"port"`
	Protocol string      `json:"protocol"`
	Tag      string      `json:"tag"`
	Settings dnsSettings `json:"settings"`
}

type dnsSettings struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
	Network string `json:"network"`
}
