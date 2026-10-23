package probe

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"time"
)

// UserAgent is the fixed user agent every request the exporter sends, so a
// probe looks the same from every vantage point.
const UserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"

// HTTPClient returns the HTTP client the package shares: connections go
// through d, keep-alives are off, redirects come back as responses, and the
// leaf is verified against the host of the requested URL, whether that is a
// DNS name or an address literal (VerifyHostname matches IP SANs). roots
// overrides the system roots when non-nil.
//
// The transport derives that name from the URL it is dialing, so verification
// does not depend on the connection state: Go sends no SNI for an address
// literal, and the state carries no other copy of the name. ProbeHTTPS
// replaces the callback to keep the handshake and record a verdict instead of
// failing the request.
func HTTPClient(d Dialer, timeout time.Duration, roots *x509.CertPool) *http.Client {
	transport := &http.Transport{
		DialContext:       d.DialContext,
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{RootCAs: roots},
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
