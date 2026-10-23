package sampler

import (
	"net"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

// isIPLiteral reports whether a target needs no DNS lookup. A configured target
// is a host with an optional path, and an IPv6 literal arrives bracketed, so
// probe.HostOnly settles the form before the address is parsed.
func isIPLiteral(host string) bool {
	return net.ParseIP(probe.HostOnly(host)) != nil
}
