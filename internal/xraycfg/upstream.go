package xraycfg

import (
	"fmt"
	"net"
	"strconv"
)

// Upstream is the resolver the tunnel DNS inbound forwards to.
type Upstream struct {
	Host string
	Port int
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
