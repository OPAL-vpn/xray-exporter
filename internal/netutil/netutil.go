// Package netutil holds addressing helpers shared by the exporter.
package netutil

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// LoopbackHost keeps tunnel-facing listeners and dialers off external interfaces.
const LoopbackHost = "127.0.0.1"

func LoopbackAddr(port int) string {
	return net.JoinHostPort(LoopbackHost, strconv.Itoa(port))
}

var errHostPort = errors.New("not host:port")

// HostPort parses host:port with a non-empty host and a port in 1..65535.
func HostPort(s string) (string, int, error) {
	host, rawPort, err := net.SplitHostPort(s)
	if err != nil || host == "" {
		return "", 0, errHostPort
	}
	port, err := parsePort(rawPort)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}

// parsePort reads a numeric port in 1..65535.
func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, errHostPort
	}
	return n, nil
}

// Target is the host part of a probe target: host[:port], with any path dropped.
type Target struct {
	Host string // name or address literal, IPv6 brackets stripped
	Port string // "" when no port is written
}

// ParseTarget reads host[:port][/path], the grammar a target and an https
// control share. url.Parse does the splitting, so brackets, escapes and paths
// follow the same rules the probes apply when they build a request. The port is
// optional here and required to be in 1..65535.
func ParseTarget(raw string) (Target, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Target{}, errors.New("host is required")
	}
	// A scheme would break the DNS probe of the same target.
	if strings.Contains(s, "://") {
		return Target{}, errors.New("host must not include a scheme")
	}

	u, err := url.Parse("https://" + s)
	if err != nil {
		return Target{}, targetError(raw, s)
	}
	if u.User != nil {
		return Target{}, fmt.Errorf("host %q must not carry credentials", raw)
	}

	target := Target{Host: u.Hostname()}
	if target.Host == "" {
		return Target{}, fmt.Errorf("host %q has no name", raw)
	}
	if strings.HasSuffix(u.Host, ":") {
		return Target{}, fmt.Errorf("host %q has an invalid port", raw)
	}
	if port := u.Port(); port != "" {
		n, err := parsePort(port)
		if err != nil {
			return Target{}, fmt.Errorf("host %q has an invalid port", raw)
		}
		target.Port = strconv.Itoa(n)
	}
	return target, nil
}

// targetError names the bracketing rule, which url's own message does not: it
// reports an unbracketed IPv6 literal as an invalid port.
func targetError(raw, hostPort string) error {
	switch {
	case strings.Contains(hostPort, "["):
		return fmt.Errorf("host %q is not a bracketed address", raw)
	case strings.Count(hostPort, ":") > 1:
		return fmt.Errorf("host %q: an IPv6 address must be bracketed", raw)
	}
	return fmt.Errorf("host %q is not a valid host", raw)
}
