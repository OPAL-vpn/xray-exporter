package xraycfg

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/OPAL-vpn/xray-exporter/internal/netutil"
)

const (
	socksTag = "xray-exporter-socks"
	dnsTag   = "xray-exporter-dns"
)

type Ports struct {
	Socks int
	DNS   int
}

// Inject replaces log and inbounds in raw. Outbounds must already be non-empty.
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
			Listen:   netutil.LoopbackHost,
			Port:     p.Socks,
			Protocol: "socks",
			Tag:      socksTag,
			Settings: socksSettings{Auth: "noauth", UDP: true},
		},
		dnsInbound{
			Listen:   netutil.LoopbackHost,
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
