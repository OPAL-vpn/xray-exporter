package probe

import (
	"strings"

	"github.com/miekg/dns"
)

// newQuery assigns a random ID, which DoH checks against the reply.
func newQuery(name string) *dns.Msg {
	query := new(dns.Msg)
	query.SetQuestion(dns.Fqdn(name), dns.TypeA)
	return query
}

// HostOnly strips a path and IPv6 brackets from a target host.
func HostOnly(host string) string {
	host = strings.TrimSpace(host)
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	return host
}
