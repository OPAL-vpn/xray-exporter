package xrayproc

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
)

const (
	// loopbackListen matches the address xraycfg injects into the inbounds.
	loopbackListen = "127.0.0.1"

	// readyTimeout bounds how long Start waits for the socks inbound to accept.
	readyTimeout = 10 * time.Second
	// readyPollInterval is the pause between readiness dials.
	readyPollInterval = 100 * time.Millisecond
	// dialTimeout bounds a single readiness dial, not the whole wait.
	dialTimeout = 100 * time.Millisecond
)

func allocatePorts() (xraycfg.Ports, error) {
	socks, err := xraycfg.FreePort()
	if err != nil {
		return xraycfg.Ports{}, err
	}
	dns, err := xraycfg.FreePort()
	if err != nil {
		return xraycfg.Ports{}, err
	}
	if dns == socks {
		if dns, err = xraycfg.FreePort(); err != nil {
			return xraycfg.Ports{}, err
		}
		if dns == socks {
			return xraycfg.Ports{}, fmt.Errorf("port allocation returned %d twice", socks)
		}
	}
	return xraycfg.Ports{Socks: socks, DNS: dns}, nil
}

// waitForSocks polls the socks port until it accepts, so Start never returns an
// instance whose inbound is still unbound.
func waitForSocks(port int) error {
	addr := net.JoinHostPort(loopbackListen, strconv.Itoa(port))
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
