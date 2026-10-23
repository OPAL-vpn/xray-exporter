// Package egress verifies that a config's traffic leaves through its tunnel by
// comparing the address an echo endpoint sees with and without the tunnel.
package egress

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

// maxEchoBody bounds the echo response; an address needs far less.
const maxEchoBody = 128

// tunnelTimeout bounds the tunneled fetch; a slower echo endpoint cannot say
// anything about routing.
const tunnelTimeout = 5 * time.Second

// Result is the outcome of one egress check.
type Result struct {
	IP string
	// Verified is false when the tunnel address equals the direct baseline: the
	// config is routing traffic out directly.
	Verified bool
	Err      error
}

// Verifier caches the direct baseline and compares it with a tunneled fetch.
type Verifier struct {
	url     string
	ttl     time.Duration
	timeout time.Duration

	now func() time.Time

	mu        sync.Mutex
	ip        string
	fetchedAt time.Time
}

// New returns a Verifier for the IP-echo endpoint at url.
func New(url string, ttl, timeout time.Duration) *Verifier {
	return &Verifier{
		url:     url,
		ttl:     ttl,
		timeout: timeout,
		now:     time.Now,
	}
}

// Baseline fetches the direct address and caches it for the TTL. A failed fetch
// is not cached, and the lock is held across the fetch so callers share it.
func (v *Verifier) Baseline(ctx context.Context) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if !v.fetchedAt.IsZero() && v.now().Sub(v.fetchedAt) < v.ttl {
		return v.ip, nil
	}

	client := &http.Client{
		Timeout: v.timeout,
		// A nil Transport would be http.DefaultTransport, which honours
		// HTTP_PROXY; the baseline must leave the process directly.
		Transport: &http.Transport{},
	}
	defer client.CloseIdleConnections()

	ip, err := fetchIP(ctx, client, v.url)
	if err != nil {
		return "", fmt.Errorf("egress baseline: %w", err)
	}

	v.ip = ip
	v.fetchedAt = v.now()

	return ip, nil
}

func (v *Verifier) Check(ctx context.Context, d probe.Dialer) Result {
	baseline, err := v.Baseline(ctx)
	if err != nil {
		return Result{Err: err}
	}

	client := probe.HTTPClient(d, tunnelTimeout, nil)
	defer client.CloseIdleConnections()

	ip, err := fetchIP(ctx, client, v.url)
	if err != nil {
		return Result{Err: fmt.Errorf("egress check: %w", err)}
	}

	return Result{IP: ip, Verified: ip != baseline}
}

func fetchIP(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", probe.UserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: status %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxEchoBody))
	if err != nil {
		return "", fmt.Errorf("%s: read: %w", url, err)
	}

	raw := strings.TrimSpace(string(body))
	ip := net.ParseIP(raw)
	if ip == nil {
		return "", fmt.Errorf("%s: %q is not an IP address", url, raw)
	}

	return ip.String(), nil
}
