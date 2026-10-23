// Package netutil holds the addressing helpers shared by the exporter's
// packages.
package netutil

import (
	"errors"
	"net"
	"strconv"
)

// LoopbackHost is the interface every tunnel-facing listener and dialer uses,
// so probe traffic never leaves the machine's external interfaces.
const LoopbackHost = "127.0.0.1"

// LoopbackAddr renders port as an address on LoopbackHost.
func LoopbackAddr(port int) string {
	return net.JoinHostPort(LoopbackHost, strconv.Itoa(port))
}

// errHostPort is the single rejection reason for HostPort; callers name the
// field they were parsing.
var errHostPort = errors.New("not host:port")

// HostPort parses a host:port address with a non-empty host and a port in
// 1..65535.
func HostPort(s string) (string, int, error) {
	host, rawPort, err := net.SplitHostPort(s)
	if err != nil || host == "" {
		return "", 0, errHostPort
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, errHostPort
	}
	return host, port, nil
}
