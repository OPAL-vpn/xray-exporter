package probe

import (
	"strings"

	"github.com/miekg/dns"

	"github.com/OPAL-vpn/xray-exporter/internal/netutil"
)

// newQuery assigns a random ID, which DoH checks against the reply.
func newQuery(name string) *dns.Msg {
	query := new(dns.Msg)
	query.SetQuestion(dns.Fqdn(name), dns.TypeA)
	return query
}

// HostOnly strips a path, a port and IPv6 brackets from a target host, so a DNS
// question names the host and nothing else.
func HostOnly(host string) string {
	target, err := netutil.ParseTarget(host)
	if err != nil {
		return strings.TrimSpace(host)
	}
	return target.Host
}
