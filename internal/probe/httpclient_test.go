package probe_test

import (
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

// TestHTTPClientNegotiatesHTTP2 pins ForceAttemptHTTP2: a custom dialer leaves
// the transport on HTTP/1.1 otherwise.
func TestHTTPClientNegotiatesHTTP2(t *testing.T) {
	var got string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Proto
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())

	client := probe.HTTPClient(&redirectDialer{}, 5*time.Second, roots)
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET %s: %v", srv.URL, err)
	}
	defer resp.Body.Close()

	if got != "HTTP/2.0" {
		t.Errorf("server saw %q, want HTTP/2.0", got)
	}
}
