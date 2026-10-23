package probe

import (
	"errors"
	"net/url"
	"strings"
)

// probeURL turns a target host into the request URL. A host is a name or a
// bracketed address with an optional path, so the scheme and the root path are
// the probe's own; a scheme in the host is the config error Load rejects.
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
