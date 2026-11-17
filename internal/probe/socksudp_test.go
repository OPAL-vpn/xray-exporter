package probe_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

// socksUDPDatagram is one datagram the fake relay received: the destination
// its RFC 1928 header named and the payload after it.
type socksUDPDatagram struct {
	DST     string
	Payload []byte
}

// testSocksUDPServer is a no-auth SOCKS5 proxy that answers UDP ASSOCIATE with
// a real UDP socket, echoes datagrams back, and records their destinations.
type testSocksUDPServer struct {
	listener net.Listener
	udp      *net.UDPConn

	mu            sync.Mutex
	failAssociate bool
	dropAfter     int
	fragmentReply bool
	datagrams     []socksUDPDatagram
}

// newTestSocksUDPServer starts the proxy and its echo socket.
func newTestSocksUDPServer(t *testing.T) *testSocksUDPServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen test socks tcp: %v", err)
	}
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		listener.Close()
		t.Fatalf("listen test socks udp: %v", err)
	}

	s := &testSocksUDPServer{listener: listener, udp: udp}
	t.Cleanup(func() {
		listener.Close()
		udp.Close()
	})
	go s.serve()
	go s.echo()

	return s
}

func (s *testSocksUDPServer) Addr() string { return s.listener.Addr().String() }

func (s *testSocksUDPServer) failAssociations() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failAssociate = true
}

// dropEchoAfter stops echoing once n datagrams have arrived.
func (s *testSocksUDPServer) dropEchoAfter(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropAfter = n
}

// fragmentReplies sets FRAG=1 on every echoed datagram.
func (s *testSocksUDPServer) fragmentReplies() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fragmentReply = true
}

func (s *testSocksUDPServer) Datagrams() []socksUDPDatagram {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]socksUDPDatagram(nil), s.datagrams...)
}

func (s *testSocksUDPServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *testSocksUDPServer) handle(conn net.Conn) {
	defer conn.Close()

	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return
	}
	methods := make([]byte, greeting[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return
	}

	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return
	}
	if head[0] != 0x05 || head[1] != 0x03 {
		return
	}
	if _, err := readTestAddress(conn, head[3]); err != nil {
		return
	}
	var portBuf [2]byte
	if _, err := io.ReadFull(conn, portBuf[:]); err != nil {
		return
	}

	s.mu.Lock()
	fail := s.failAssociate
	s.mu.Unlock()

	if fail {
		_ = writeTestSocksUDPReply(conn, 0x01, "0.0.0.0:0")
		return
	}
	if err := writeTestSocksUDPReply(conn, testRepSucceeded, s.udp.LocalAddr().String()); err != nil {
		return
	}

	// The association lives as long as the TCP connection.
	_, _ = io.Copy(io.Discard, conn)
}

func (s *testSocksUDPServer) echo() {
	buf := make([]byte, 65535)
	for {
		n, client, err := s.udp.ReadFromUDP(buf)
		if err != nil {
			return
		}
		dst, payload, err := decodeTestSocksUDPDatagram(buf[:n])
		if err != nil {
			return
		}

		s.mu.Lock()
		s.datagrams = append(s.datagrams, socksUDPDatagram{DST: dst, Payload: append([]byte(nil), payload...)})
		count := len(s.datagrams)
		dropAfter := s.dropAfter
		fragment := s.fragmentReply
		s.mu.Unlock()

		if dropAfter > 0 && count > dropAfter {
			continue
		}

		reply, err := encodeTestSocksUDPDatagram(dst, payload)
		if err != nil {
			return
		}
		if fragment {
			reply[2] = 0x01
		}
		if _, err := s.udp.WriteToUDP(reply, client); err != nil {
			return
		}
	}
}

func TestSocksUDPHeaderRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		dst  string
	}{
		{"domain", "example.com:3478"},
		{"ipv4", "1.2.3.4:53"},
		{"ipv6", "[2001:db8::1]:443"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestSocksUDPServer(t)
			relay := newTestSocksUDPRelay(t, server)
			payload := []byte("binding request over " + tt.name)

			if err := relay.WriteTo(tt.dst, payload); err != nil {
				t.Fatalf("WriteTo: %v", err)
			}
			src, got := readSocksUDP(t, relay)
			if src != tt.dst {
				t.Errorf("reply source = %s, want %s", src, tt.dst)
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("reply payload = %q, want %q", got, payload)
			}

			datagrams := server.Datagrams()
			if len(datagrams) != 1 {
				t.Fatalf("proxy received %d datagrams, want 1", len(datagrams))
			}
			if datagrams[0].DST != tt.dst {
				t.Errorf("header destination = %s, want %s", datagrams[0].DST, tt.dst)
			}
			if !bytes.Equal(datagrams[0].Payload, payload) {
				t.Errorf("header payload = %q, want %q", datagrams[0].Payload, payload)
			}
		})
	}
}

func TestSocksUDPRelayAssociateAndEcho(t *testing.T) {
	server := newTestSocksUDPServer(t)
	relay := newTestSocksUDPRelay(t, server)

	first := "203.0.113.10:3478"
	if err := relay.WriteTo(first, []byte("first")); err != nil {
		t.Fatalf("WriteTo first: %v", err)
	}
	src, payload := readSocksUDP(t, relay)
	if src != first {
		t.Errorf("first reply source = %s, want %s", src, first)
	}
	if string(payload) != "first" {
		t.Errorf("first reply payload = %q, want %q", payload, "first")
	}

	second := "example.org:53"
	if err := relay.WriteTo(second, []byte("second")); err != nil {
		t.Fatalf("WriteTo second: %v", err)
	}
	src, payload = readSocksUDP(t, relay)
	if src != second {
		t.Errorf("second reply source = %s, want %s", src, second)
	}
	if string(payload) != "second" {
		t.Errorf("second reply payload = %q, want %q", payload, "second")
	}

	datagrams := server.Datagrams()
	if len(datagrams) != 2 {
		t.Fatalf("proxy received %d datagrams, want 2; the association was not reused", len(datagrams))
	}
	if datagrams[0].DST != first || datagrams[1].DST != second {
		t.Errorf("destinations = %s, %s; want %s, %s", datagrams[0].DST, datagrams[1].DST, first, second)
	}
}

func TestSocksUDPRelayAssociateFailure(t *testing.T) {
	server := newTestSocksUDPServer(t)
	server.failAssociations()

	relay, err := probe.NewSocksUDPRelay(server.Addr(), 2*time.Second)
	if err == nil {
		relay.Close()
		t.Fatal("NewSocksUDPRelay succeeded, want an associate failure")
	}
	if !strings.Contains(err.Error(), "reply 1") {
		t.Errorf("error = %v, want it to name reply code 1", err)
	}
}

func TestSocksUDPRelayRejectsFragment(t *testing.T) {
	server := newTestSocksUDPServer(t)
	server.fragmentReplies()
	relay := newTestSocksUDPRelay(t, server)

	if err := relay.WriteTo("1.2.3.4:53", []byte("bind")); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if err := relay.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	_, _, err := relay.ReadFrom()
	if err == nil || !strings.Contains(err.Error(), "fragment") {
		t.Fatalf("ReadFrom error = %v, want a fragment rejection", err)
	}
}

func TestSocksUDPRelayReadDeadline(t *testing.T) {
	server := newTestSocksUDPServer(t)
	relay := newTestSocksUDPRelay(t, server)

	if err := relay.WriteTo("1.2.3.4:53", []byte("answered")); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	readSocksUDP(t, relay)

	server.dropEchoAfter(1)
	if err := relay.WriteTo("1.2.3.4:53", []byte("dropped")); err != nil {
		t.Fatalf("WriteTo dropped: %v", err)
	}
	if err := relay.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	_, _, err := relay.ReadFrom()
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("ReadFrom error = %v, want a timeout from the read deadline", err)
	}
}

// newTestSocksUDPRelay associates with the fake proxy and closes in cleanup.
func newTestSocksUDPRelay(t *testing.T, server *testSocksUDPServer) *probe.SocksUDPRelay {
	t.Helper()

	relay, err := probe.NewSocksUDPRelay(server.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("NewSocksUDPRelay: %v", err)
	}
	t.Cleanup(func() { relay.Close() })

	return relay
}

// readSocksUDP reads one reply with a deadline so a dead fake fails the test
// instead of hanging it.
func readSocksUDP(t *testing.T, relay *probe.SocksUDPRelay) (string, []byte) {
	t.Helper()

	if err := relay.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	src, payload, err := relay.ReadFrom()
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}

	return src, payload
}

// decodeTestSocksUDPDatagram is the fake's own RFC 1928 UDP header parser; the
// relay's encoder is checked against it, not against the code under test.
func decodeTestSocksUDPDatagram(b []byte) (string, []byte, error) {
	if len(b) < 4 {
		return "", nil, errors.New("short datagram")
	}
	if b[0] != 0 || b[1] != 0 {
		return "", nil, errors.New("nonzero reserved bytes")
	}
	if b[2] != 0 {
		return "", nil, fmt.Errorf("fragment %d unsupported", b[2])
	}

	rest := b[4:]
	var host string
	var addrLen int
	switch b[3] {
	case testATYPIPv4:
		addrLen = 4
		if len(rest) < addrLen+2 {
			return "", nil, errors.New("short ipv4 address")
		}
		host = net.IP(rest[:addrLen]).String()
	case testATYPIPv6:
		addrLen = 16
		if len(rest) < addrLen+2 {
			return "", nil, errors.New("short ipv6 address")
		}
		host = net.IP(rest[:addrLen]).String()
	case testATYPDomain:
		if len(rest) < 1 {
			return "", nil, errors.New("short domain length")
		}
		addrLen = 1 + int(rest[0])
		if len(rest) < addrLen+2 {
			return "", nil, errors.New("short domain address")
		}
		host = string(rest[1:addrLen])
	default:
		return "", nil, fmt.Errorf("address type %#x", b[3])
	}

	port := binary.BigEndian.Uint16(rest[addrLen : addrLen+2])

	return net.JoinHostPort(host, strconv.Itoa(int(port))), rest[addrLen+2:], nil
}

// encodeTestSocksUDPDatagram builds a header naming dst followed by payload.
func encodeTestSocksUDPDatagram(dst string, payload []byte) ([]byte, error) {
	host, portStr, err := net.SplitHostPort(dst)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}

	datagram := []byte{0, 0, 0}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			datagram = append(datagram, testATYPIPv4)
			datagram = append(datagram, v4...)
		} else {
			datagram = append(datagram, testATYPIPv6)
			datagram = append(datagram, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return nil, fmt.Errorf("hostname %q too long", host)
		}
		datagram = append(datagram, testATYPDomain, byte(len(host)))
		datagram = append(datagram, host...)
	}

	var portBuf [2]byte
	binary.BigEndian.PutUint16(portBuf[:], uint16(port))
	datagram = append(datagram, portBuf[:]...)

	return append(datagram, payload...), nil
}

// writeTestSocksUDPReply writes a SOCKS5 reply whose BND.ADDR/BND.PORT is bound.
func writeTestSocksUDPReply(conn net.Conn, rep byte, bound string) error {
	addr, err := encodeTestSocksUDPDatagram(bound, nil)
	if err != nil {
		return err
	}
	// The UDP header's RSV/FRAG bytes are the reply's RSV byte plus the ATYP.
	frame := append([]byte{0x05, rep}, addr[2:]...)
	_, err = conn.Write(frame)

	return err
}
