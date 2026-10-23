package probe

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"time"
)

// UserAgent is fixed so every probe looks the same from every vantage point.
const UserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"

// HTTPClient returns the shared client: connections go through d, keep-alives
// and redirects are off, and roots override the system pool. The certificate is
// verified against the URL host, because an address literal sends no SNI.
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
