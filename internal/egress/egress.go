// Package egress verifies that a config's own routing sends traffic through
// its tunnel: the address an echo endpoint sees through the tunnel must differ
// from the address it sees when the exporter asks directly.
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

	"github.com/Anton-beep/xray-exporter/internal/probe"
)

// maxEchoBody bounds the body read from the echo endpoint. An address is at
// most 45 bytes, so anything longer is not one.
const maxEchoBody = 128

// tunnelTimeout bounds the request that leaves through the tunnel. An echo
// endpoint that needs longer than this cannot say anything about routing.
const tunnelTimeout = 5 * time.Second

// Result is the outcome of one egress check.
type Result struct {
	// IP is the address the echo endpoint saw through the tunnel, empty when
	// the fetch or parse failed.
	IP string
	// Verified is true when the tunnel's address differs from the direct
	// baseline: their equality means the config is routing traffic out
	// directly.
	Verified bool
	// Err is why the check produced no verdict.
	Err error
}

// Verifier compares the egress address through a tunnel with the one seen by a
// direct request. The direct request is the only one the process ever makes,
// and its address is cached for ttl.
type Verifier struct {
	url     string
	ttl     time.Duration
	timeout time.Duration

	// now is the clock, replaceable so a test can age the cache.
	now func() time.Time

	mu        sync.Mutex
	ip        string
	fetchedAt time.Time
}

// New returns a Verifier for the IP-echo endpoint at url. The direct baseline
// fetch is bounded by timeout; the request through the tunnel is capped at 5 s.
func New(url string, ttl, timeout time.Duration) *Verifier {
	return &Verifier{
		url:     url,
		ttl:     ttl,
		timeout: timeout,
		now:     time.Now,
	}
}

// Baseline fetches the egress address directly and caches it for the
// verifier's TTL. A failed fetch is not cached: the next call asks again. The
// cache lock is held across the fetch so concurrent callers share the one
// direct request.
func (v *Verifier) Baseline(ctx context.Context) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if !v.fetchedAt.IsZero() && v.now().Sub(v.fetchedAt) < v.ttl {
		return v.ip, nil
	}

	client := &http.Client{
		Timeout: v.timeout,
		// A nil Transport would be http.DefaultTransport, which honours
		// HTTP_PROXY from the environment; the baseline must leave the
		// process directly.
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

// Check fetches the egress address through d and compares it with the direct
// baseline. The result is unverified unless both fetches produced an address
// and the addresses differ.
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

// fetchIP GETs url and returns the address the endpoint reports.
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
