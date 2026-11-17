package probe_test

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/stun/v3"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

// testSTUNServer is a loopback STUN server: it answers every binding request
// with a success response echoing the request's transaction id.
type testSTUNServer struct {
	conn *net.UDPConn

	mu         sync.Mutex
	drop       int
	delay      time.Duration
	delayFirst time.Duration
	foreign    bool
	requests   int
}

// newTestSTUNServer starts the server on a loopback port.
func newTestSTUNServer(t *testing.T) *testSTUNServer {
	t.Helper()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen test stun: %v", err)
	}

	s := &testSTUNServer{conn: conn}
	t.Cleanup(func() { conn.Close() })
	go s.serve()

	return s
}

func (s *testSTUNServer) Addr() string { return s.conn.LocalAddr().String() }

// dropFirst ignores the first n binding requests.
func (s *testSTUNServer) dropFirst(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drop = n
}

// delayReplies holds every reply back by d.
func (s *testSTUNServer) delayReplies(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delay = d
}

// delayFirstReply holds the reply to the first request back by an extra d, so
// the replies to later requests overtake it.
func (s *testSTUNServer) delayFirstReply(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delayFirst = d
}

// replyForeign answers with a random transaction id instead of the request's.
func (s *testSTUNServer) replyForeign() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.foreign = true
}

func (s *testSTUNServer) serve() {
	buf := make([]byte, 1500)
	for {
		n, client, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}

		var request stun.Message
		if err := stun.Decode(buf[:n], &request); err != nil {
			continue
		}
		if request.Type != stun.BindingRequest {
			continue
		}

		s.mu.Lock()
		index := s.requests
		s.requests++
		drop, delay, delayFirst, foreign := s.drop, s.delay, s.delayFirst, s.foreign
		s.mu.Unlock()

		if index < drop {
			continue
		}
		if index == 0 {
			delay += delayFirst
		}

		id := request.TransactionID
		if foreign {
			id = stun.NewTransactionID()
		}
		raw := stun.MustBuild(stun.BindingSuccess, stun.NewTransactionIDSetter(id)).Raw

		go func() {
			if delay > 0 {
				time.Sleep(delay)
			}
			_, _ = s.conn.WriteToUDP(raw, client)
		}()
	}
}

// testSocksUDPForwarder is a no-auth SOCKS5 proxy whose UDP ASSOCIATE socket
// forwards each datagram to the destination its RFC 1928 header names and
// relays the answer back, so a probe reaches a real loopback server.
type testSocksUDPForwarder struct {
	listener net.Listener
	udp      *net.UDPConn
}

// newTestSocksUDPForwarder starts the proxy and its forwarding socket.
func newTestSocksUDPForwarder(t *testing.T) *testSocksUDPForwarder {
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

	f := &testSocksUDPForwarder{listener: listener, udp: udp}
	t.Cleanup(func() {
		listener.Close()
		udp.Close()
	})
	go f.serve()
	go f.forward()

	return f
}

func (f *testSocksUDPForwarder) Addr() string { return f.listener.Addr().String() }

func (f *testSocksUDPForwarder) serve() {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *testSocksUDPForwarder) handle(conn net.Conn) {
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

	if err := writeTestSocksUDPReply(conn, testRepSucceeded, f.udp.LocalAddr().String()); err != nil {
		return
	}

	// The association lives as long as the TCP connection.
	_, _ = io.Copy(io.Discard, conn)
}

func (f *testSocksUDPForwarder) forward() {
	buf := make([]byte, 65535)
	for {
		n, client, err := f.udp.ReadFromUDP(buf)
		if err != nil {
			return
		}
		dst, payload, err := decodeTestSocksUDPDatagram(buf[:n])
		if err != nil {
			return
		}
		go f.relay(dst, payload, client)
	}
}

// relay sends one payload to dst and writes the answer back to client.
func (f *testSocksUDPForwarder) relay(dst string, payload []byte, client *net.UDPAddr) {
	upstream, err := net.Dial("udp", dst)
	if err != nil {
		return
	}
	defer upstream.Close()

	if err := upstream.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return
	}
	if _, err := upstream.Write(payload); err != nil {
		return
	}

	reply := make([]byte, 65535)
	n, err := upstream.Read(reply)
	if err != nil {
		return
	}

	frame, err := encodeTestSocksUDPDatagram(dst, reply[:n])
	if err != nil {
		return
	}
	_, _ = f.udp.WriteToUDP(frame, client)
}

// stunProbePath starts the forwarding proxy in front of server and returns the
// proxy address and the STUN target to probe.
func stunProbePath(t *testing.T, server *testSTUNServer) (socksAddr, target string) {
	t.Helper()

	proxy := newTestSocksUDPForwarder(t)

	return proxy.Addr(), server.Addr()
}

func TestProbeSTUNAllReplies(t *testing.T) {
	server := newTestSTUNServer(t)
	socksAddr, target := stunProbePath(t, server)

	result := probe.ProbeSTUN(context.Background(), socksAddr, target, 10, 5*time.Millisecond, time.Second)

	if result.Err != nil {
		t.Fatalf("ProbeSTUN: %v", result.Err)
	}
	if !result.Up {
		t.Error("Up = false, want true")
	}
	if result.Sent != 10 {
		t.Errorf("Sent = %d, want 10", result.Sent)
	}
	if result.Replies != 10 {
		t.Errorf("Replies = %d, want 10", result.Replies)
	}
	if result.Median <= 0 {
		t.Errorf("Median = %v, want > 0", result.Median)
	}
	if result.Max < result.Median {
		t.Errorf("Max = %v, want >= Median %v", result.Max, result.Median)
	}
	if result.Jitter < 0 {
		t.Errorf("Jitter = %v, want >= 0", result.Jitter)
	}
}

func TestProbeSTUNLoss(t *testing.T) {
	server := newTestSTUNServer(t)
	server.dropFirst(5)
	socksAddr, target := stunProbePath(t, server)

	result := probe.ProbeSTUN(context.Background(), socksAddr, target, 10, 5*time.Millisecond, 500*time.Millisecond)

	if result.Err != nil {
		t.Fatalf("ProbeSTUN: %v", result.Err)
	}
	if !result.Up {
		t.Error("Up = false, want true")
	}
	if result.Sent != 10 {
		t.Errorf("Sent = %d, want 10", result.Sent)
	}
	if result.Replies != 5 {
		t.Errorf("Replies = %d, want 5", result.Replies)
	}
}

func TestProbeSTUNAllLost(t *testing.T) {
	server := newTestSTUNServer(t)
	server.dropFirst(10)
	socksAddr, target := stunProbePath(t, server)

	result := probe.ProbeSTUN(context.Background(), socksAddr, target, 10, 5*time.Millisecond, 300*time.Millisecond)

	if result.Err != nil {
		t.Fatalf("ProbeSTUN: %v, want no error when every reply is lost", result.Err)
	}
	if result.Up {
		t.Error("Up = true, want false")
	}
	if result.Sent != 10 {
		t.Errorf("Sent = %d, want 10", result.Sent)
	}
	if result.Replies != 0 {
		t.Errorf("Replies = %d, want 0", result.Replies)
	}
	if result.Median != 0 || result.Max != 0 || result.Jitter != 0 {
		t.Errorf("latencies = %v/%v/%v, want zero", result.Median, result.Max, result.Jitter)
	}
}

func TestProbeSTUNIgnoresForeignTransactionID(t *testing.T) {
	server := newTestSTUNServer(t)
	server.replyForeign()
	socksAddr, target := stunProbePath(t, server)

	result := probe.ProbeSTUN(context.Background(), socksAddr, target, 3, 5*time.Millisecond, 300*time.Millisecond)

	if result.Err != nil {
		t.Fatalf("ProbeSTUN: %v", result.Err)
	}
	if result.Replies != 0 {
		t.Errorf("Replies = %d, want 0; a reply with an unsent transaction id was counted", result.Replies)
	}
	if result.Up {
		t.Error("Up = true, want false")
	}
}

func TestProbeSTUNCountsReordering(t *testing.T) {
	server := newTestSTUNServer(t)
	server.delayReplies(10 * time.Millisecond)
	server.delayFirstReply(40 * time.Millisecond)
	socksAddr, target := stunProbePath(t, server)

	result := probe.ProbeSTUN(context.Background(), socksAddr, target, 3, 20*time.Millisecond, time.Second)

	if result.Err != nil {
		t.Fatalf("ProbeSTUN: %v", result.Err)
	}
	if result.Replies != 3 {
		t.Fatalf("Replies = %d, want 3", result.Replies)
	}
	if result.Reordered < 1 {
		t.Errorf("Reordered = %d, want >= 1; the delayed first reply was not counted", result.Reordered)
	}
}

func TestProbeSTUNUnreachableRelay(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	socksAddr := listener.Addr().String()
	listener.Close()

	result := probe.ProbeSTUN(context.Background(), socksAddr, "127.0.0.1:3478", 3, 5*time.Millisecond, 200*time.Millisecond)

	if result.Err == nil {
		t.Fatal("Err = nil, want the dial failure")
	}
	if result.Up {
		t.Error("Up = true, want false")
	}
}
