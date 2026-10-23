package probe_test

import (
	"context"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

const testDNSContentType = "application/dns-message"

// readDNSQuery unpacks the request body of a DoH POST.
func readDNSQuery(t *testing.T, r *http.Request) *dns.Msg {
	t.Helper()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("read doh request body: %v", err)
		return nil
	}

	query := new(dns.Msg)
	if err := query.Unpack(body); err != nil {
		t.Errorf("unpack doh request body: %v", err)
		return nil
	}
	if len(query.Question) != 1 {
		t.Errorf("doh request has %d questions, want 1", len(query.Question))
		return nil
	}
	return query
}

func TestProbeDoH(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != testDNSContentType {
			t.Errorf("Content-Type = %q, want %q", got, testDNSContentType)
		}

		query := readDNSQuery(t, r)
		if query == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", testDNSContentType)
		_, _ = w.Write(dnsAnswer(t, query, "192.0.2.10"))
	}))
	defer srv.Close()

	dialer := &redirectDialer{}
	res := probe.ProbeDoH(context.Background(), dialer, srv.URL+"/dns-query", "example.test", 2*time.Second, nil)

	if !res.Up {
		t.Fatalf("Up = false, Err = %v", res.Err)
	}
	if res.Err != nil {
		t.Errorf("Err = %v, want nil", res.Err)
	}
	if res.RTT <= 0 {
		t.Errorf("RTT = %v, want > 0", res.RTT)
	}
	if requests := dialer.Requests(); len(requests) != 1 || requests[0] != srv.Listener.Addr().String() {
		t.Errorf("dialer saw %v, want the DoH host %s", requests, srv.Listener.Addr())
	}
}

// TestProbeDoHOverTLS posts to a TLS resolver named by its address. Go sends no
// SNI for an address literal, so the peer name has to come from the URL: a
// resolver behind such a URL is the case a plain-HTTP DoH test cannot see.
func TestProbeDoHOverTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := readDNSQuery(t, r)
		if query == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", testDNSContentType)
		_, _ = w.Write(dnsAnswer(t, query, "192.0.2.10"))
	}))
	defer srv.Close()

	dohURL := "https://" + srv.Listener.Addr().String() + "/dns-query"
	if host, _, err := net.SplitHostPort(srv.Listener.Addr().String()); err != nil || net.ParseIP(host) == nil {
		t.Fatalf("test server address %q is not an address literal", srv.Listener.Addr())
	}

	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())

	res := probe.ProbeDoH(context.Background(), &redirectDialer{}, dohURL, "example.test", 2*time.Second, roots)

	if !res.Up {
		t.Fatalf("Up = false, Err = %v, want true: the certificate is trusted and names the address in %s", res.Err, dohURL)
	}
	if res.Err != nil {
		t.Errorf("Err = %v, want nil", res.Err)
	}
	if res.RTT <= 0 {
		t.Errorf("RTT = %v, want > 0", res.RTT)
	}
}

func TestProbeDoHRejectsBadResponses(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "server error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				query := readDNSQuery(t, r)
				if query == nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				// A well-formed answer under a 500: only the status keeps this down.
				w.Header().Set("Content-Type", testDNSContentType)
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write(dnsAnswer(t, query, "192.0.2.10"))
			},
		},
		{
			name: "non-dns content type",
			handler: func(w http.ResponseWriter, r *http.Request) {
				query := readDNSQuery(t, r)
				if query == nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				// A well-formed answer under the wrong type: only the type keeps
				// this down.
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write(dnsAnswer(t, query, "192.0.2.10"))
			},
		},
		{
			name: "unparsable body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				readDNSQuery(t, r)
				w.Header().Set("Content-Type", testDNSContentType)
				_, _ = w.Write([]byte("garbage"))
			},
		},
		{
			name: "mismatched id",
			handler: func(w http.ResponseWriter, r *http.Request) {
				query := readDNSQuery(t, r)
				if query == nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				query.Id++
				w.Header().Set("Content-Type", testDNSContentType)
				_, _ = w.Write(dnsAnswer(t, query, "192.0.2.10"))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			res := probe.ProbeDoH(context.Background(), &redirectDialer{}, srv.URL+"/dns-query", "example.test", 2*time.Second, nil)

			if res.Up {
				t.Error("Up = true, want false")
			}
			if res.Err == nil {
				t.Error("Err = nil, want the reason the resolver is down")
			}
		})
	}
}
