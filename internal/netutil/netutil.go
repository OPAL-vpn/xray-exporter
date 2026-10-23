// Package netutil holds the addressing helpers shared by the exporter's
// packages.
package netutil

import (
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
