package probe_test

import (
	"context"
	"errors"
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
	errorReply bool
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

// replyError answers with a binding error response instead of a success, as a
// TURN server does for an unauthenticated binding request.
func (s *testSTUNServer) replyError() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errorReply = true
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
		drop, delay, delayFirst := s.drop, s.delay, s.delayFirst
		foreign, errorReply := s.foreign, s.errorReply
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
		messageType := stun.BindingSuccess
		if errorReply {
			messageType = stun.BindingError
		}
		raw := stun.MustBuild(messageType, stun.NewTransactionIDSetter(id)).Raw

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

	mu         sync.Mutex
	replies    int
	breakAfter int // send a truncated datagram once this many replies were relayed; 0 never
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

// breakAfterReplies makes the relay go bad once n replies were relayed: it
// sends a truncated datagram, so the client's next read fails instead of timing
// out. Closing the relay socket cannot stand in for this: the kernel reports the
// dead port on the client's next write, and on the next read ahead of any reply
// still queued, so it never reaches the collection loop.
func (f *testSocksUDPForwarder) breakAfterReplies(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.breakAfter = n
}

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

	f.mu.Lock()
	f.replies++
	broken := f.breakAfter > 0 && f.replies >= f.breakAfter
	f.mu.Unlock()
	if broken {
		_, _ = f.udp.WriteToUDP([]byte{0x00, 0x00}, client)
	}
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
	server.delayFirstReply(150 * time.Millisecond)
	socksAddr, target := stunProbePath(t, server)

	result := probe.ProbeSTUN(context.Background(), socksAddr, target, 3, 50*time.Millisecond, 2*time.Second)

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

func TestProbeSTUNAcceptsBindingError(t *testing.T) {
	server := newTestSTUNServer(t)
	server.replyError()
	socksAddr, target := stunProbePath(t, server)

	result := probe.ProbeSTUN(context.Background(), socksAddr, target, 5, 5*time.Millisecond, time.Second)

	if result.Err != nil {
		t.Fatalf("ProbeSTUN: %v", result.Err)
	}
	if !result.Up {
		t.Error("Up = false, want true; a binding error response is a matching reply")
	}
	if result.Replies != 5 {
		t.Errorf("Replies = %d, want 5", result.Replies)
	}
}

func TestProbeSTUNStopsOnceAllAnswered(t *testing.T) {
	server := newTestSTUNServer(t)
	socksAddr, target := stunProbePath(t, server)

	start := time.Now()
	result := probe.ProbeSTUN(context.Background(), socksAddr, target, 10, 5*time.Millisecond, 5*time.Second)
	elapsed := time.Since(start)

	if result.Err != nil {
		t.Fatalf("ProbeSTUN: %v", result.Err)
	}
	if result.Replies != 10 {
		t.Fatalf("Replies = %d, want 10", result.Replies)
	}
	if elapsed >= 2*time.Second {
		t.Errorf("elapsed = %v, want well under the 5s timeout once every request is answered", elapsed)
	}
}

// TestProbeSTUNTimestampsRepliesWhileSending pins that a reply's RTT is taken
// when the reply arrives, not when the whole paced burst has been sent. The
// 250ms spacing dwarfs a loopback RTT, so a drain-after-send implementation
// reports the pacing wait of the first requests as latency: its max would be
// the full 1.75s send window and its median about 875ms.
func TestProbeSTUNTimestampsRepliesWhileSending(t *testing.T) {
	server := newTestSTUNServer(t)
	socksAddr, target := stunProbePath(t, server)

	const (
		count   = 8
		spacing = 250 * time.Millisecond
	)
	result := probe.ProbeSTUN(context.Background(), socksAddr, target, count, spacing, 6*time.Second)

	if result.Err != nil {
		t.Fatalf("ProbeSTUN: %v", result.Err)
	}
	if !result.Up {
		t.Fatal("Up = false, want true")
	}
	if result.Replies != count {
		t.Fatalf("Replies = %d, want %d", result.Replies, count)
	}
	if result.Max > 600*time.Millisecond {
		t.Errorf("Max = %v, want < 600ms: the send spacing leaked into the RTT", result.Max)
	}
	if result.Median > 400*time.Millisecond {
		t.Errorf("Median = %v, want < 400ms: the send spacing leaked into the RTT", result.Median)
	}
}

// TestProbeSTUNCancellationStopsBurst pins that a cancelled context ends the
// paced burst promptly with the requests sent so far, and joins the receive
// goroutine instead of leaving it blocked on the relay.
func TestProbeSTUNCancellationStopsBurst(t *testing.T) {
	server := newTestSTUNServer(t)
	socksAddr, target := stunProbePath(t, server)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	result := probe.ProbeSTUN(ctx, socksAddr, target, 10, 500*time.Millisecond, 10*time.Second)
	elapsed := time.Since(start)

	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("Err = %v, want context.Canceled", result.Err)
	}
	if result.Sent != 1 {
		t.Errorf("Sent = %d, want 1: the cancel landed in the first pacing wait", result.Sent)
	}
	if elapsed > 2*time.Second {
		t.Errorf("elapsed = %v, want prompt return after the cancel", elapsed)
	}
}

// TestProbeSTUNRelayReadErrorDiscardsBurst pins that a relay read that fails
// mid-collection does not publish the truncated burst: a partial reply set
// would read as a success with inflated loss and a median over the replies that
// happened to arrive before the relay broke. The relay breaks after the fourth
// reply, so the failure lands in the collection loop with four replies already
// counted.
func TestProbeSTUNRelayReadErrorDiscardsBurst(t *testing.T) {
	server := newTestSTUNServer(t)
	proxy := newTestSocksUDPForwarder(t)
	proxy.breakAfterReplies(4)

	result := probe.ProbeSTUN(context.Background(), proxy.Addr(), server.Addr(), 5, 50*time.Millisecond, 2*time.Second)

	if result.Err == nil {
		t.Fatal("Err = nil, want the relay read failure")
	}
	if result.Up {
		t.Error("Up = true, want false: a truncated burst is a failure")
	}
	if result.Sent != 5 {
		t.Errorf("Sent = %d, want 5: the failure must land in collection, not in the send loop", result.Sent)
	}
	if result.Replies != 4 {
		t.Errorf("Replies = %d, want 4: the replies relayed before the socket died", result.Replies)
	}
	if result.Median != 0 || result.Max != 0 || result.Jitter != 0 {
		t.Errorf("latencies = %v/%v/%v, want zero: nothing is published from a truncated burst", result.Median, result.Max, result.Jitter)
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
