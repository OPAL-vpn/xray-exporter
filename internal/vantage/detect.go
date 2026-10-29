// Package vantage finds out which network the host egresses from.
package vantage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

const (
	ipinfoURL = "https://ipinfo.io/json"
	// maxBody caps the response; a larger body is an error.
	maxBody       = 1 << 20
	lookupTimeout = 10 * time.Second
)

// Identity is the egress network as ipinfo reports it. It is a best guess made
// by a third party, so it is only ever shown, never acted on.
type Identity struct {
	ASName  string
	Country string
}

// orgPattern is ipinfo's "AS12714 PJSC MegaFon".
var orgPattern = regexp.MustCompile(`^AS\d+\s+(.+)$`)

// Detector asks ipinfo.io which network the request came from.
type Detector struct {
	client *http.Client
	url    string
}

// New returns a detector for ipinfo.io.
func New() *Detector { return newDetector(ipinfoURL) }

// newDetector takes the URL so tests can point it at an httptest server.
func newDetector(url string) *Detector {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The lookup describes this host's own route; an environment proxy would
	// report the proxy's network.
	transport.Proxy = nil
	return &Detector{
		url: url,
		client: &http.Client{
			Transport:     transport,
			Timeout:       lookupTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Lookup returns the egress network.
func (d *Detector) Lookup(ctx context.Context) (Identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "xray-exporter")

	resp, err := d.client.Do(req)
	if err != nil {
		return Identity{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Identity{}, fmt.Errorf("unexpected status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return Identity{}, fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxBody {
		return Identity{}, fmt.Errorf("response exceeds %d bytes", maxBody)
	}

	var doc struct {
		Org     string `json:"org"`
		Country string `json:"country"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return Identity{}, fmt.Errorf("invalid JSON: %w", err)
	}
	m := orgPattern.FindStringSubmatch(doc.Org)
	if m == nil {
		return Identity{}, errors.New("no AS name in org")
	}
	return Identity{ASName: m[1], Country: doc.Country}, nil
}
