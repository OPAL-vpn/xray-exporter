package probe

import (
	"errors"
	"net/url"
	"strings"
)

// probeURL builds the https URL for a target and rejects a supplied scheme.
func probeURL(host string) (*url.URL, error) {
	raw := strings.TrimSpace(host)
	if strings.Contains(raw, "://") {
		return nil, errors.New("probe: host must not include a scheme")
	}

	target, err := url.Parse("https://" + raw)
	if err != nil {
		return nil, err
	}
	if target.Host == "" {
		return nil, errors.New("probe: empty host")
	}
	if target.Path == "" {
		target.Path = "/"
	}

	return target, nil
}
