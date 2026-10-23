package xraycfg

import (
	"fmt"
	"net"

	"github.com/OPAL-vpn/xray-exporter/internal/netutil"
)

// FreePort reserves and releases a loopback port, so the number handed out is
// neither fixed nor documented.
func FreePort() (int, error) {
	ln, err := net.Listen("tcp", netutil.LoopbackHost+":0")
	if err != nil {
		return 0, fmt.Errorf("reserve loopback port: %w", err)
	}
	defer ln.Close()

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("reserve loopback port: got %T", ln.Addr())
	}
	return addr.Port, nil
}
