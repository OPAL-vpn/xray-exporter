package probe_test

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
)

// socks request address types and reply codes the fake server understands.
const (
	testATYPIPv4   = 0x01
	testATYPDomain = 0x03
	testATYPIPv6   = 0x04

	testRepSucceeded = 0x00
)

// socksRequest is what the fake proxy saw on the wire for one CONNECT. ATYP is
// the raw address-type byte, which is how a test pins that a hostname was sent
// unresolved rather than converted to an IP by the dialer.
type socksRequest struct {
	ATYP byte
	Host string
	Port int
}

// testSocksServer is a minimal SOCKS5 proxy for tests. It accepts only the
// no-auth method, records every CONNECT request, and relays the connection to
// either the requested target or upstream.
type testSocksServer struct {
	listener net.Listener

	mu       sync.Mutex
	upstream string
	failCode byte
	requests []socksRequest
}

// newTestSocksServer starts the proxy on a loopback port. An empty upstream
// means each CONNECT is relayed to the target it asked for.
func newTestSocksServer(t *testing.T, upstream string) *testSocksServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen test socks: %v", err)
	}

	s := &testSocksServer{listener: listener, upstream: upstream}
	t.Cleanup(func() { listener.Close() })
	go s.serve()

	return s
}

// Addr is the proxy address a dialer should be pointed at.
func (s *testSocksServer) Addr() string { return s.listener.Addr().String() }

// setFailCode makes every subsequent CONNECT answered with this reply code
// instead of being relayed.
func (s *testSocksServer) setFailCode(code byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failCode = code
}

// Requests returns the CONNECT requests seen so far, in arrival order.
func (s *testSocksServer) Requests() []socksRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]socksRequest(nil), s.requests...)
}

func (s *testSocksServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *testSocksServer) handle(conn net.Conn) {
	defer conn.Close()
	_ = s.converse(conn)
}

func (s *testSocksServer) converse(conn net.Conn) error {
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return err
	}
	if greeting[0] != 0x05 {
		return fmt.Errorf("greeting version %#x", greeting[0])
	}
	methods := make([]byte, greeting[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return err
	}

	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return err
	}
	if head[0] != 0x05 || head[1] != 0x01 {
		return fmt.Errorf("request version %#x command %#x", head[0], head[1])
	}

	host, err := readTestAddress(conn, head[3])
	if err != nil {
		return err
	}
	var portBuf [2]byte
	if _, err := io.ReadFull(conn, portBuf[:]); err != nil {
		return err
	}
	port := int(binary.BigEndian.Uint16(portBuf[:]))

	s.mu.Lock()
	s.requests = append(s.requests, socksRequest{ATYP: head[3], Host: host, Port: port})
	failCode := s.failCode
	upstream := s.upstream
	s.mu.Unlock()

	if failCode != testRepSucceeded {
		_, err := conn.Write(testSocksReply(failCode))
		return err
	}

	target := net.JoinHostPort(host, strconv.Itoa(port))
	if upstream != "" {
		target = upstream
	}
	upstreamConn, err := net.Dial("tcp", target)
	if err != nil {
		_, werr := conn.Write(testSocksReply(0x04)) // host unreachable
		return werr
	}
	defer upstreamConn.Close()

	if _, err := conn.Write(testSocksReply(testRepSucceeded)); err != nil {
		return err
	}

	go func() {
		_, _ = io.Copy(upstreamConn, conn)
		if tcp, ok := upstreamConn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	_, err = io.Copy(conn, upstreamConn)
	return err
}

func readTestAddress(conn net.Conn, atyp byte) (string, error) {
	switch atyp {
	case testATYPIPv4:
		var ip [4]byte
		if _, err := io.ReadFull(conn, ip[:]); err != nil {
			return "", err
		}
		return net.IP(ip[:]).String(), nil
	case testATYPIPv6:
		var ip [16]byte
		if _, err := io.ReadFull(conn, ip[:]); err != nil {
			return "", err
		}
		return net.IP(ip[:]).String(), nil
	case testATYPDomain:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return "", err
		}
		name := make([]byte, length[0])
		if _, err := io.ReadFull(conn, name); err != nil {
			return "", err
		}
		return string(name), nil
	default:
		return "", fmt.Errorf("address type %#x", atyp)
	}
}

func testSocksReply(code byte) []byte {
	return []byte{0x05, code, 0x00, testATYPIPv4, 0, 0, 0, 0, 0, 0}
}
