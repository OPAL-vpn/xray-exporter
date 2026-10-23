// Package netutil holds addressing helpers shared by the exporter.
package netutil

import (
	"errors"
	"net"
	"strconv"
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
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, errHostPort
	}
	return host, port, nil
}
