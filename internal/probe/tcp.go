package probe

import (
	"context"
	"time"
)

// TunnelTCPResult is the outcome of one TCP CONNECT through the tunnel.
type TunnelTCPResult struct {
	Up bool
	// Connect is the whole dial: the socks handshake plus the remote connect.
	Connect time.Duration
	Err     error
}

// ProbeTunnelTCP CONNECTs to addr through the socks dialer d and closes again.
// It says nothing about what the peer would serve: it measures the non-HTTP TCP
// apps a tunnel carries, unlike the direct reachability controls.
func ProbeTunnelTCP(ctx context.Context, d Dialer, addr string, timeout time.Duration) TunnelTCPResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return TunnelTCPResult{Err: err}
	}
	connect := time.Since(start)
	_ = conn.Close()

	return TunnelTCPResult{Up: true, Connect: connect}
}
