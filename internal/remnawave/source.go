// Package remnawave fetches the xray client configs of every host from one
// Remnawave panel subscription.
package remnawave

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
)

const (
	// maxBody caps one panel response; a larger body is an error, not a
	// truncation.
	maxBody = 8 << 20
	// fetchTimeout bounds one Fetch; Fetch itself never retries.
	fetchTimeout = 20 * time.Second
)

// Source reads the per-host configs from one Remnawave subscription URL.
type Source struct {
	url      *url.URL
	host     string
	interval time.Duration
	client   *http.Client
}

var _ xraycfg.Source = (*Source)(nil)

// New validates the subscription URL and builds the client. Errors never echo
// rawURL: it embeds the subscription shortUuid and must not reach logs.
func New(rawURL string, interval time.Duration) (*Source, error) {
	u, err := url.Parse(rawURL)
	// url.Parse's own error repeats the URL, so it is replaced, not wrapped.
	if err != nil || !u.IsAbs() || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("remnawave: url must be an absolute http or https URL with a host")
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/json") {
		u.Path += "/json"
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The panel is reached directly; an environment proxy would change the
	// origin the subscription URL names.
	transport.Proxy = nil

	return &Source{
		url:      u,
		host:     u.Host,
		interval: interval,
		client: &http.Client{
			Transport: transport,
			Timeout:   fetchTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
					return errors.New("remnawave: cross-origin redirect rejected")
				}
				return nil
			},
		},
	}, nil
}

func (*Source) Kind() string { return "remnawave" }

// Refresh returns the fetch interval given to New.
func (s *Source) Refresh() time.Duration { return s.interval }

// Fetch returns every host config of the subscription as a complete set. An
// empty array is a successful empty set; any failure returns an error naming
// only the panel host.
func (s *Source) Fetch(ctx context.Context) ([]xraycfg.Config, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url.String(), nil)
	if err != nil {
		// New validated the URL, and the parse error would repeat it.
		return nil, fmt.Errorf("remnawave: %s: invalid request URL", s.host)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "xray-exporter")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, s.requestError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remnawave: %s: unexpected status %s", s.host, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("remnawave: %s: read response: %w", s.host, err)
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("remnawave: %s: response exceeds %d bytes", s.host, maxBody)
	}

	entries, err := parsePayload(body)
	if err != nil {
		return nil, fmt.Errorf("remnawave: %s: %w", s.host, err)
	}

	labels := labelsFor(entries)
	configs := make([]xraycfg.Config, 0, len(entries))
	for i, entry := range entries {
		raw, err := canonical(entry)
		if err != nil {
			return nil, fmt.Errorf("remnawave: %s: config %d: %w", s.host, i+1, err)
		}
		configs = append(configs, xraycfg.Config{Name: labels[i], Raw: raw})
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })
	return configs, nil
}

// requestError drops the *url.Error's message, which repeats the full URL and
// with it the subscription shortUuid, and keeps only the cause.
func (s *Source) requestError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return fmt.Errorf("remnawave: %s: %w", s.host, uerr.Err)
	}
	return fmt.Errorf("remnawave: %s: %w", s.host, err)
}
