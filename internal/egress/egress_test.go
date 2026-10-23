package egress

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

// echoServer answers every request with body, like an IP-echo endpoint.
func echoServer(t *testing.T, body string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func dialAddr(srv *httptest.Server) string {
	return strings.TrimPrefix(srv.URL, "http://")
}

// fixedDialer sends every connection to addr, ignoring the requested address.
type fixedDialer struct{ addr string }

func (d *fixedDialer) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, d.addr)
}

func TestSameIPNotVerified(t *testing.T) {
	direct := echoServer(t, "198.51.100.7")
	tunnel := echoServer(t, "198.51.100.7\n")

	v := New(direct.URL, time.Hour, time.Second)

	baseline, err := v.Baseline(context.Background())
	if err != nil {
		t.Fatalf("Baseline: %v", err)
	}
	if baseline != "198.51.100.7" {
		t.Fatalf("Baseline = %q, want 198.51.100.7", baseline)
	}

	res := v.Check(context.Background(), &fixedDialer{addr: dialAddr(tunnel)})
	if res.Err != nil {
		t.Fatalf("Check: %v", res.Err)
	}
	if res.IP != "198.51.100.7" {
		t.Errorf("IP = %q, want 198.51.100.7", res.IP)
	}
	if res.Verified {
		t.Error("Verified = true, want false when the tunnel sees the same address as the direct baseline")
	}
}

func TestDifferentIPVerified(t *testing.T) {
	direct := echoServer(t, "198.51.100.7")

	tunnel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != probe.UserAgent {
			t.Errorf("User-Agent = %q, want the probe user agent %q", got, probe.UserAgent)
		}
		_, _ = io.WriteString(w, "203.0.113.9")
	}))
	t.Cleanup(tunnel.Close)

	v := New(direct.URL, time.Hour, time.Second)
	res := v.Check(context.Background(), &fixedDialer{addr: dialAddr(tunnel)})

	if res.Err != nil {
		t.Fatalf("Check: %v", res.Err)
	}
	if res.IP != "203.0.113.9" {
		t.Errorf("IP = %q, want 203.0.113.9", res.IP)
	}
	if !res.Verified {
		t.Error("Verified = false, want true when the tunnel exits through a different address")
	}
}

func TestBaselineCached(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "198.51.100.7")
	}))
	t.Cleanup(srv.Close)

	v := New(srv.URL, time.Hour, time.Second)
	for range 2 {
		if _, err := v.Baseline(context.Background()); err != nil {
			t.Fatalf("Baseline: %v", err)
		}
	}

	if got := requests.Load(); got != 1 {
		t.Errorf("baseline requests = %d, want 1 within the cache TTL", got)
	}
}

func TestBaselineRefetchedAfterTTL(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "198.51.100.7")
	}))
	t.Cleanup(srv.Close)

	v := New(srv.URL, time.Hour, time.Second)
	now := time.Unix(1_700_000_000, 0)
	v.now = func() time.Time { return now }

	if _, err := v.Baseline(context.Background()); err != nil {
		t.Fatalf("Baseline: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("baseline requests = %d, want 1", got)
	}

	now = now.Add(2 * time.Hour)
	if _, err := v.Baseline(context.Background()); err != nil {
		t.Fatalf("Baseline after TTL: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("baseline requests = %d, want 2 after the TTL elapsed", got)
	}
}

func TestTunnelFailure(t *testing.T) {
	direct := echoServer(t, "198.51.100.7")
	tunnel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(tunnel.Close)

	v := New(direct.URL, time.Hour, time.Second)
	res := v.Check(context.Background(), &fixedDialer{addr: dialAddr(tunnel)})

	if res.Verified {
		t.Error("Verified = true, want false when the tunnel fetch failed")
	}
	if res.Err == nil {
		t.Error("Err = nil, want the tunnel fetch failure")
	}
}

func TestIPParsing(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantIP  string
		wantErr bool
	}{
		{name: "padded address", body: "  203.0.113.9\n", wantIP: "203.0.113.9"},
		{name: "not an address", body: "not an ip", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := New(echoServer(t, tc.body).URL, time.Hour, time.Second)

			ip, err := v.Baseline(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Baseline = %q, want a parse error", ip)
				}
				return
			}
			if err != nil {
				t.Fatalf("Baseline: %v", err)
			}
			if ip != tc.wantIP {
				t.Errorf("IP = %q, want %q", ip, tc.wantIP)
			}
		})
	}
}
