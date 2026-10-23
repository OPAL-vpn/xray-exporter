package probe_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

const wantUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"

func probeHost(srv *httptest.Server) string {
	return strings.TrimPrefix(srv.URL, "https://")
}

// socksDialerFor returns a dialer through a fake proxy and that proxy.
func socksDialerFor(t *testing.T) (probe.Dialer, *testSocksServer) {
	t.Helper()

	socks := newTestSocksServer(t, "")
	return probe.NewSocksDialer(socks.Addr(), 2*time.Second), socks
}

func TestProbeHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != wantUserAgent {
			t.Errorf("User-Agent = %q, want %q", got, wantUserAgent)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	dialer, socks := socksDialerFor(t)
	res := probe.ProbeHTTPS(context.Background(), dialer, probeHost(srv), 5*time.Second, nil)

	if !res.Up {
		t.Fatalf("Up = false, Err = %v", res.Err)
	}
	if res.Err != nil {
		t.Errorf("Err = %v, want nil", res.Err)
	}
	if res.Status != http.StatusOK {
		t.Errorf("Status = %d, want 200", res.Status)
	}
	if res.TLSVerified {
		t.Error("TLSVerified = true, want false for a self-signed certificate")
	}
	if res.TTFB <= 0 {
		t.Errorf("TTFB = %v, want > 0", res.TTFB)
	}
	if res.Total < res.TTFB {
		t.Errorf("Total = %v, TTFB = %v, want Total >= TTFB", res.Total, res.TTFB)
	}

	requests := socks.Requests()
	if len(requests) != 1 {
		t.Fatalf("proxy saw %d CONNECT requests, want 1", len(requests))
	}
	if requests[0].ATYP != testATYPIPv4 {
		t.Errorf("address type = %#x, want IPv4 (%#x)", requests[0].ATYP, testATYPIPv4)
	}
	if got, want := net.JoinHostPort(requests[0].Host, strconv.Itoa(requests[0].Port)), probeHost(srv); got != want {
		t.Errorf("proxy target = %s, want %s", got, want)
	}
}

// TestProbeHTTPSRejectsScheme pins the host form config.Load also rejects.
func TestProbeHTTPSRejectsScheme(t *testing.T) {
	res := probe.ProbeHTTPS(context.Background(), nil, "https://youtube.com", time.Second, nil)

	if res.Up {
		t.Error("Up = true, want false for a host carrying a scheme")
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "scheme") {
		t.Errorf("Err = %v, want it to reject the scheme", res.Err)
	}
}

func TestProbeHTTPSDoesNotFollowRedirect(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.test/", http.StatusFound)
	}))
	defer srv.Close()

	dialer, _ := socksDialerFor(t)
	res := probe.ProbeHTTPS(context.Background(), dialer, probeHost(srv), 5*time.Second, nil)

	if !res.Up {
		t.Fatalf("Up = false, Err = %v", res.Err)
	}
	if res.Status != http.StatusFound {
		t.Errorf("Status = %d, want 302 (redirect must not be followed)", res.Status)
	}
}

func TestProbeHTTPSContextDeadline(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	dialer, _ := socksDialerFor(t)
	start := time.Now()
	res := probe.ProbeHTTPS(context.Background(), dialer, probeHost(srv), 200*time.Millisecond, nil)

	if res.Up {
		t.Error("Up = true, want false when the deadline expires")
	}
	if res.Err == nil {
		t.Error("Err = nil, want a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("probe took %v, want it bounded by the 200ms timeout", elapsed)
	}
}

func TestProbeHTTPSTLSVerified(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())

	dialer, _ := socksDialerFor(t)
	res := probe.ProbeHTTPS(context.Background(), dialer, probeHost(srv), 5*time.Second, roots)

	if !res.Up {
		t.Fatalf("Up = false, Err = %v", res.Err)
	}
	if !res.TLSVerified {
		t.Errorf("TLSVerified = false, want true with the server certificate in the root pool (Err = %v)", res.Err)
	}
}

func tlsServerFor(t *testing.T, cert tls.Certificate) *httptest.Server {
	t.Helper()

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	return srv
}

func TestProbeHTTPSIPHostChecksPeerName(t *testing.T) {
	cert, roots := testCertificate(t, "example.com")
	srv := tlsServerFor(t, cert)

	dialer, _ := socksDialerFor(t)
	res := probe.ProbeHTTPS(context.Background(), dialer, probeHost(srv), 5*time.Second, roots)

	if !res.Up {
		t.Fatalf("Up = false, Err = %v", res.Err)
	}
	if res.TLSVerified {
		t.Error("TLSVerified = true, want false: the trusted certificate names example.com, not the probed address")
	}
}

func TestProbeHTTPSVerifiesPeerName(t *testing.T) {
	cert, roots := testCertificate(t, "example.com")
	srv := tlsServerFor(t, cert)

	// The request names example.com; the dialer serves it from loopback.
	dialer := &redirectDialer{target: srv.Listener.Addr().String()}
	res := probe.ProbeHTTPS(context.Background(), dialer, "example.com", 5*time.Second, roots)

	if !res.Up {
		t.Fatalf("Up = false, Err = %v", res.Err)
	}
	if !res.TLSVerified {
		t.Errorf("TLSVerified = false, want true for a trusted certificate issued to the probed name (Err = %v)", res.Err)
	}
}
