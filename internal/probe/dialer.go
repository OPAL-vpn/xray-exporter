// Package probe measures targets through a config's tunnel: a SOCKS5 dialer,
// HTTPS probes, and DNS probes over UDP, DoH and DoT.
package probe

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// Dialer is the subset of net.Dialer the probes need.
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// SOCKS5 protocol constants, RFC 1928.
const (
	socksVersion = 0x05
	socksNoAuth  = 0x00

	socksCmdConnect = 0x01

	socksATYPIPv4   = 0x01
	socksATYPDomain = 0x03
	socksATYPIPv6   = 0x04

	socksRepSucceeded = 0x00
)

type socksDialer struct {
	addr    string
	timeout time.Duration
	dialer  net.Dialer
}

// NewSocksDialer reaches targets through the SOCKS5 proxy at addr. The hostname
// is sent unresolved (socks5h), so DNS happens beyond the tunnel.
func NewSocksDialer(addr string, timeout time.Duration) Dialer {
	return &socksDialer{
		addr:    addr,
		timeout: timeout,
		dialer:  net.Dialer{Timeout: timeout},
	}
}

// DialContext always opens TCP; the probes carry only HTTP and TLS.
func (d *socksDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("socks dial %s: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return nil, fmt.Errorf("socks dial %s: invalid port %q", addr, portStr)
	}

	conn, err := d.dialer.DialContext(ctx, "tcp", d.addr)
	if err != nil {
		return nil, fmt.Errorf("socks dial proxy %s: %w", d.addr, err)
	}

	if deadline, ok := d.handshakeDeadline(ctx); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			conn.Close()
			return nil, fmt.Errorf("socks handshake: %w", err)
		}
	}

	if err := socksConnect(conn, host, uint16(port)); err != nil {
		conn.Close()
		return nil, err
	}

	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("socks connect: %w", err)
	}

	return conn, nil
}

// handshakeDeadline bounds the greeting and CONNECT exchange by whichever of
// the timeout and the context expires first.
func (d *socksDialer) handshakeDeadline(ctx context.Context) (time.Time, bool) {
	var deadline time.Time
	if d.timeout > 0 {
		deadline = time.Now().Add(d.timeout)
	}
	if ctxDeadline, ok := ctx.Deadline(); ok && (deadline.IsZero() || ctxDeadline.Before(deadline)) {
		deadline = ctxDeadline
	}
	return deadline, !deadline.IsZero()
}

// socksConnect performs the no-auth greeting and one CONNECT. Host is passed
// verbatim: a name goes out as ATYP=3 and is never resolved here.
func socksConnect(conn net.Conn, host string, port uint16) error {
	if _, err := conn.Write([]byte{socksVersion, 1, socksNoAuth}); err != nil {
		return fmt.Errorf("socks handshake: %w", err)
	}

	var method [2]byte
	if _, err := io.ReadFull(conn, method[:]); err != nil {
		return fmt.Errorf("socks handshake: %w", err)
	}
	if method[0] != socksVersion {
		return fmt.Errorf("socks handshake: version %#x", method[0])
	}
	if method[1] != socksNoAuth {
		return fmt.Errorf("socks handshake: proxy requires method %#x", method[1])
	}

	request, err := connectRequest(host, port)
	if err != nil {
		return err
	}
	if _, err := conn.Write(request); err != nil {
		return fmt.Errorf("socks connect: %w", err)
	}

	var reply [4]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return fmt.Errorf("socks connect: %w", err)
	}
	if reply[0] != socksVersion {
		return fmt.Errorf("socks connect: version %#x", reply[0])
	}
	if reply[1] != socksRepSucceeded {
		return fmt.Errorf("socks connect: reply %d", reply[1])
	}

	// Skip the bound address so the caller sees only the relayed payload.
	switch reply[3] {
	case socksATYPIPv4:
		err = discard(conn, 4)
	case socksATYPIPv6:
		err = discard(conn, 16)
	case socksATYPDomain:
		var length [1]byte
		if _, err = io.ReadFull(conn, length[:]); err == nil {
			err = discard(conn, int(length[0]))
		}
	default:
		return fmt.Errorf("socks connect: address type %#x", reply[3])
	}
	if err != nil {
		return fmt.Errorf("socks connect: %w", err)
	}
	if err := discard(conn, 2); err != nil {
		return fmt.Errorf("socks connect: %w", err)
	}

	return nil
}

func connectRequest(host string, port uint16) ([]byte, error) {
	request := []byte{socksVersion, socksCmdConnect, 0x00}

	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			request = append(request, socksATYPIPv4)
			request = append(request, v4...)
		} else {
			request = append(request, socksATYPIPv6)
			request = append(request, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, fmt.Errorf("socks connect: hostname %q too long", host)
		}
		request = append(request, socksATYPDomain, byte(len(host)))
		request = append(request, host...)
	}

	var portBuf [2]byte
	binary.BigEndian.PutUint16(portBuf[:], port)

	return append(request, portBuf[:]...), nil
}

func discard(r io.Reader, n int) error {
	_, err := io.CopyN(io.Discard, r, int64(n))
	return err
}
