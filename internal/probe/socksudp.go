package probe

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

const (
	socksCmdAssociate = 0x03

	// socksUDPMaxDatagram is the largest UDP payload one read can carry.
	socksUDPMaxDatagram = 65535
)

// SocksUDPRelay is one SOCKS5 UDP ASSOCIATE session through a socks inbound.
// The TCP connection keeps the association alive; all datagrams travel over a
// single UDP socket to the relay address the proxy advertised.
type SocksUDPRelay struct {
	tcp net.Conn
	udp net.Conn
}

// NewSocksUDPRelay associates a UDP relay through the SOCKS5 proxy at socksAddr.
// The TCP dial and the handshake are bounded by timeout; timeout <= 0 means no
// bound.
func NewSocksUDPRelay(socksAddr string, timeout time.Duration) (*SocksUDPRelay, error) {
	tcp, err := net.DialTimeout("tcp", socksAddr, timeout)
	if err != nil {
		return nil, fmt.Errorf("socks udp dial proxy %s: %w", socksAddr, err)
	}

	if timeout > 0 {
		if err := tcp.SetDeadline(time.Now().Add(timeout)); err != nil {
			tcp.Close()
			return nil, fmt.Errorf("socks udp handshake: %w", err)
		}
	}

	relayAddr, err := socksUDPAssociate(tcp)
	if err != nil {
		tcp.Close()
		return nil, err
	}

	if err := tcp.SetDeadline(time.Time{}); err != nil {
		tcp.Close()
		return nil, fmt.Errorf("socks udp handshake: %w", err)
	}

	udp, err := net.Dial("udp", relayAddr)
	if err != nil {
		tcp.Close()
		return nil, fmt.Errorf("socks udp dial relay %s: %w", relayAddr, err)
	}

	return &SocksUDPRelay{tcp: tcp, udp: udp}, nil
}

// WriteTo sends payload to dst ("host:port" or "[v6]:port") through the relay.
func (r *SocksUDPRelay) WriteTo(dst string, payload []byte) error {
	header, err := encodeSocksUDPHeader(dst)
	if err != nil {
		return fmt.Errorf("socks udp write: %w", err)
	}

	datagram := make([]byte, 0, len(header)+len(payload))
	datagram = append(datagram, header...)
	datagram = append(datagram, payload...)

	if _, err := r.udp.Write(datagram); err != nil {
		return fmt.Errorf("socks udp write to %s: %w", dst, err)
	}

	return nil
}

// SetReadDeadline bounds the next ReadFrom.
func (r *SocksUDPRelay) SetReadDeadline(t time.Time) error {
	if err := r.udp.SetReadDeadline(t); err != nil {
		return fmt.Errorf("socks udp deadline: %w", err)
	}

	return nil
}

// ReadFrom returns the next reply's source address and payload.
func (r *SocksUDPRelay) ReadFrom() (src string, payload []byte, err error) {
	buf := make([]byte, socksUDPMaxDatagram)

	n, err := r.udp.Read(buf)
	if err != nil {
		return "", nil, fmt.Errorf("socks udp read: %w", err)
	}

	src, payload, err = splitSocksUDPHeader(buf[:n])
	if err != nil {
		return "", nil, fmt.Errorf("socks udp read: %w", err)
	}

	return src, payload, nil
}

// Close ends the association by closing the UDP socket and the TCP connection.
func (r *SocksUDPRelay) Close() error {
	return errors.Join(r.udp.Close(), r.tcp.Close())
}

// socksUDPAssociate runs the no-auth greeting and one UDP ASSOCIATE, returning
// the relay address from BND.ADDR/BND.PORT.
func socksUDPAssociate(conn net.Conn) (string, error) {
	if _, err := conn.Write([]byte{socksVersion, 1, socksNoAuth}); err != nil {
		return "", fmt.Errorf("socks udp handshake: %w", err)
	}

	var method [2]byte
	if _, err := io.ReadFull(conn, method[:]); err != nil {
		return "", fmt.Errorf("socks udp handshake: %w", err)
	}
	if method[0] != socksVersion {
		return "", fmt.Errorf("socks udp handshake: version %#x", method[0])
	}
	if method[1] != socksNoAuth {
		return "", fmt.Errorf("socks udp handshake: proxy requires method %#x", method[1])
	}

	// The client's UDP socket is not open yet, so the request names
	// 0.0.0.0:0 and the proxy learns the client address from this TCP peer.
	request := []byte{socksVersion, socksCmdAssociate, 0x00, socksATYPIPv4, 0, 0, 0, 0, 0, 0}
	if _, err := conn.Write(request); err != nil {
		return "", fmt.Errorf("socks udp associate: %w", err)
	}

	var reply [4]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return "", fmt.Errorf("socks udp associate: %w", err)
	}
	if reply[0] != socksVersion {
		return "", fmt.Errorf("socks udp associate: version %#x", reply[0])
	}
	if reply[1] != socksRepSucceeded {
		return "", fmt.Errorf("socks udp associate: reply %d", reply[1])
	}

	relayAddr, err := readSocksUDPAddr(conn, reply[3])
	if err != nil {
		return "", fmt.Errorf("socks udp associate: %w", err)
	}

	return relayAddr, nil
}

// readSocksUDPAddr reads a DST.ADDR/DST.PORT pair of any RFC 1928 address type.
func readSocksUDPAddr(r io.Reader, atyp byte) (string, error) {
	var host string

	switch atyp {
	case socksATYPIPv4:
		var ip [4]byte
		if _, err := io.ReadFull(r, ip[:]); err != nil {
			return "", err
		}
		host = net.IP(ip[:]).String()
	case socksATYPIPv6:
		var ip [16]byte
		if _, err := io.ReadFull(r, ip[:]); err != nil {
			return "", err
		}
		host = net.IP(ip[:]).String()
	case socksATYPDomain:
		var length [1]byte
		if _, err := io.ReadFull(r, length[:]); err != nil {
			return "", err
		}
		name := make([]byte, length[0])
		if _, err := io.ReadFull(r, name); err != nil {
			return "", err
		}
		host = string(name)
	default:
		return "", fmt.Errorf("address type %#x", atyp)
	}

	var portBuf [2]byte
	if _, err := io.ReadFull(r, portBuf[:]); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(portBuf[:])

	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

// encodeSocksUDPHeader builds the RFC 1928 UDP header naming dst.
func encodeSocksUDPHeader(dst string) ([]byte, error) {
	host, portStr, err := net.SplitHostPort(dst)
	if err != nil {
		return nil, fmt.Errorf("invalid destination %q: %w", dst, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return nil, fmt.Errorf("invalid destination %q: port %q", dst, portStr)
	}

	header := []byte{0x00, 0x00, 0x00} // RSV, RSV, FRAG
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			header = append(header, socksATYPIPv4)
			header = append(header, v4...)
		} else {
			header = append(header, socksATYPIPv6)
			header = append(header, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, fmt.Errorf("invalid destination %q: hostname too long", dst)
		}
		header = append(header, socksATYPDomain, byte(len(host)))
		header = append(header, host...)
	}

	var portBuf [2]byte
	binary.BigEndian.PutUint16(portBuf[:], uint16(port))
	header = append(header, portBuf[:]...)

	return header, nil
}

// splitSocksUDPHeader strips the RFC 1928 UDP header, returning the source
// address and the payload after it. Fragmented datagrams are not supported.
func splitSocksUDPHeader(b []byte) (src string, payload []byte, err error) {
	if len(b) < 4 {
		return "", nil, errors.New("short header")
	}
	if b[2] != 0 {
		return "", nil, fmt.Errorf("fragment %d unsupported", b[2])
	}

	rest := b[4:]
	var host string
	var addrLen int

	switch b[3] {
	case socksATYPIPv4:
		addrLen = 4
		if len(rest) < addrLen+2 {
			return "", nil, errors.New("short header")
		}
		host = net.IP(rest[:addrLen]).String()
	case socksATYPIPv6:
		addrLen = 16
		if len(rest) < addrLen+2 {
			return "", nil, errors.New("short header")
		}
		host = net.IP(rest[:addrLen]).String()
	case socksATYPDomain:
		if len(rest) < 1 {
			return "", nil, errors.New("short header")
		}
		addrLen = 1 + int(rest[0])
		if len(rest) < addrLen+2 {
			return "", nil, errors.New("short header")
		}
		host = string(rest[1:addrLen])
	default:
		return "", nil, fmt.Errorf("address type %#x", b[3])
	}

	port := binary.BigEndian.Uint16(rest[addrLen : addrLen+2])

	return net.JoinHostPort(host, strconv.Itoa(int(port))), rest[addrLen+2:], nil
}
