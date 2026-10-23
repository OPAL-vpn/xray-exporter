package xrayproc

import (
	"fmt"
	"net"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/netutil"
	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
)

const (
	readyTimeout      = 10 * time.Second
	readyPollInterval = 100 * time.Millisecond
	dialTimeout       = 100 * time.Millisecond
)

// FreePort reserves and releases a loopback port for xray to bind.
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

func allocatePorts() (xraycfg.Ports, error) {
	socks, err := FreePort()
	if err != nil {
		return xraycfg.Ports{}, err
	}
	dns, err := FreePort()
	if err != nil {
		return xraycfg.Ports{}, err
	}
	if dns == socks {
		if dns, err = FreePort(); err != nil {
			return xraycfg.Ports{}, err
		}
		if dns == socks {
			return xraycfg.Ports{}, fmt.Errorf("port allocation returned %d twice", socks)
		}
	}
	return xraycfg.Ports{Socks: socks, DNS: dns}, nil
}

func waitForSocks(port int) error {
	addr := netutil.LoopbackAddr(port)
	deadline := time.Now().Add(readyTimeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, dialTimeout)
		if err == nil {
			conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("socks inbound on %s not accepting after %s: %w", addr, readyTimeout, err)
		}
		time.Sleep(readyPollInterval)
	}
}
