package probe

import (
	"strings"

	"github.com/miekg/dns"
)

// newQuery builds a recursive A query for name. SetQuestion assigns a random
// message ID, which the DoH check then compares the answer against.
func newQuery(name string) *dns.Msg {
	query := new(dns.Msg)
	query.SetQuestion(dns.Fqdn(name), dns.TypeA)
	return query
}

// HostOnly returns the name a DNS query asks for: the host without its /path
// and without the brackets an IPv6 literal carries in a URL, so
// "example.com/health" is queried as "example.com" and "[::1]/x" as "::1".
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
