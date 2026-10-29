package xraycfg

import (
	"encoding/json"
	"errors"
	"net"
	"strconv"
)

// ErrNoEntry means no outbound names a server to connect to.
var ErrNoEntry = errors.New("no outbound with a server address")

// entryOutbound covers the places xray puts a server address: vless and vmess
// under vnext, trojan, shadowsocks and socks under servers, and the flat form.
type entryOutbound struct {
	Protocol string `json:"protocol"`
	Settings struct {
		Address string `json:"address"`
		Port    int    `json:"port"`
		Vnext   []struct {
			Address string `json:"address"`
			Port    int    `json:"port"`
		} `json:"vnext"`
		Servers []struct {
			Address string `json:"address"`
			Port    int    `json:"port"`
		} `json:"servers"`
	} `json:"settings"`
}

// Entry returns host:port of the first outbound that names a server. Local
// protocols (freedom, blackhole, dns) have none and are skipped.
func Entry(raw []byte) (string, error) {
	var doc struct {
		Outbounds []entryOutbound `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", err
	}
	for _, out := range doc.Outbounds {
		host, port := out.Settings.Address, out.Settings.Port
		if len(out.Settings.Vnext) > 0 {
			host, port = out.Settings.Vnext[0].Address, out.Settings.Vnext[0].Port
		} else if len(out.Settings.Servers) > 0 {
			host, port = out.Settings.Servers[0].Address, out.Settings.Servers[0].Port
		}
		if host == "" || port < 1 || port > 65535 {
			continue
		}
		return net.JoinHostPort(host, strconv.Itoa(port)), nil
	}
	return "", ErrNoEntry
}
