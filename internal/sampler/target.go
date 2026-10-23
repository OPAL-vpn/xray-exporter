package sampler

import (
	"net"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

// isIPLiteral reports whether a target needs no DNS lookup.
func isIPLiteral(host string) bool {
	return net.ParseIP(probe.HostOnly(host)) != nil
}
