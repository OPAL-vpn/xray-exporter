package xraycfg

import (
	"fmt"

	"github.com/OPAL-vpn/xray-exporter/internal/netutil"
)

type Upstream struct {
	Host string
	Port int
}

func UpstreamFrom(s string) (Upstream, error) {
	host, port, err := netutil.HostPort(s)
	if err != nil {
		return Upstream{}, fmt.Errorf("upstream %q is not host:port", s)
	}
	return Upstream{Host: host, Port: port}, nil
}
