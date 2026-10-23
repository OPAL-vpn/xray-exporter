package xrayproc_test

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	clog "github.com/xtls/xray-core/common/log"

	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
	"github.com/OPAL-vpn/xray-exporter/internal/xraylog"
	"github.com/OPAL-vpn/xray-exporter/internal/xrayproc"
)

const (
	freedomOutbound = `{"protocol":"freedom","settings":{}}`
	// realityPublicKey is 32 zero bytes in base64url without padding, the only
	// shape xray accepts for a REALITY public key.
	realityPublicKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

var upstream = xraycfg.Upstream{Host: "9.9.9.9", Port: 53}

func config(name, outbound string) xraycfg.Config {
	return xraycfg.Config{Name: name, Raw: []byte(`{"outbounds":[` + outbound + `]}`)}
}

func socksAddr(port int) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

// testLogger discards what xray logs: the handler is process-global, so a test
// that kept one would see the next test's instances too.
func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// syncBuffer is a log sink xray's own goroutines can write to while the test
// reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitDial polls until a TCP connection to port succeeds or timeout passes.
func waitDial(t *testing.T, port int, timeout time.Duration) {
	t.Helper()
	addr := socksAddr(port)
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial %s: %v", addr, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// socksConnect performs a socks5 CONNECT to an IPv4 host, finishes the reply,
// and returns its REP byte.
func socksConnect(t *testing.T, socksPort, targetPort int, host string) byte {
	t.Helper()
	ip := net.ParseIP(host).To4()
	if ip == nil {
		t.Fatalf("test target %q is not an IPv4 literal", host)
	}

	conn, err := net.DialTimeout("tcp", socksAddr(socksPort), 2*time.Second)
	if err != nil {
		t.Fatalf("dial socks port %d: %v", socksPort, err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write greeting: %v", err)
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		t.Fatalf("read greeting: %v", err)
	}
	if greeting[0] != 0x05 || greeting[1] != 0x00 {
		t.Fatalf("greeting = % x, want 05 00", greeting)
	}

	request := append([]byte{0x05, 0x01, 0x00, 0x01}, ip...)
	request = append(request, byte(targetPort>>8), byte(targetPort))
	if _, err := conn.Write(request); err != nil {
		t.Fatalf("write connect: %v", err)
	}

	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		t.Fatalf("read connect reply: %v", err)
	}
	if header[0] != 0x05 {
		t.Fatalf("reply version = %#x, want 0x05", header[0])
	}
	switch header[3] {
	case 0x01:
		_, err = io.ReadFull(conn, make([]byte, 4+2))
	case 0x03:
		length := make([]byte, 1)
		if _, err = io.ReadFull(conn, length); err == nil {
			_, err = io.ReadFull(conn, make([]byte, int(length[0])+2))
		}
	case 0x04:
		_, err = io.ReadFull(conn, make([]byte, 16+2))
	default:
		t.Fatalf("reply address type = %#x, want 1, 3 or 4", header[3])
	}
	if err != nil {
		t.Fatalf("read connect reply body: %v", err)
	}
	return header[1]
}

func findInstance(t *testing.T, m *xrayproc.Manager, name string) *xrayproc.Instance {
	t.Helper()
	for _, inst := range m.Instances() {
		if inst.Name == name {
			return inst
		}
	}
	t.Fatalf("instance %q not found in %v", name, instanceNames(m))
	return nil
}

func instanceNames(m *xrayproc.Manager) []string {
	instances := m.Instances()
	names := make([]string, len(instances))
	for i, inst := range instances {
		names[i] = inst.Name
	}
	return names
}

func TestStartFreedomConfig(t *testing.T) {
	inst, err := xrayproc.Start(config("freedom", freedomOutbound), upstream, "warning", testLogger(t))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := inst.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	if inst.Name != "freedom" {
		t.Errorf("Name = %q, want %q", inst.Name, "freedom")
	}
	if inst.Version == "" {
		t.Error("Version is empty, want xray's version")
	}
	if inst.SocksPort == 0 || inst.DNSPort == 0 {
		t.Fatalf("ports = socks %d, dns %d, want both allocated", inst.SocksPort, inst.DNSPort)
	}
	if inst.SocksPort == inst.DNSPort {
		t.Fatalf("socks port and dns port are both %d", inst.SocksPort)
	}

	waitDial(t, inst.SocksPort, 2*time.Second)

	// The CONNECT target is a loopback listener, not a public address: xray replies
	// after the upstream dial, so a target it cannot reach would fail the reply and
	// make the test depend on the runner's routes.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Errorf("close loopback listener: %v", err)
		}
	})

	if got := socksConnect(t, inst.SocksPort, listener.Addr().(*net.TCPAddr).Port, "127.0.0.1"); got != 0x00 {
		t.Fatalf("socks CONNECT reply = %#x, want 0x00", got)
	}
}

// TestStartRegistersModernTransports pins that vless, REALITY and ws are
// registered in the process by the distro/all blank import: without it,
// core.New panics on the unregistered protobuf type instead of returning.
func TestStartRegistersModernTransports(t *testing.T) {
	tests := []struct {
		name     string
		outbound string
	}{
		{
			name: "vless reality raw",
			outbound: `{"protocol":"vless","settings":{"vnext":[{"address":"203.0.113.1","port":443,` +
				`"users":[{"id":"11111111-1111-1111-1111-111111111111","encryption":"none"}]}]},` +
				`"streamSettings":{"network":"raw","security":"reality",` +
				`"realitySettings":{"serverName":"example.com","publicKey":"` + realityPublicKey + `","shortId":"ab"}}}`,
		},
		{
			name: "vless websocket tls",
			outbound: `{"protocol":"vless","settings":{"vnext":[{"address":"203.0.113.1","port":443,` +
				`"users":[{"id":"11111111-1111-1111-1111-111111111111","encryption":"none"}]}]},` +
				`"streamSettings":{"network":"ws","security":"tls","wsSettings":{"path":"/x"},` +
				`"tlsSettings":{"serverName":"example.com"}}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inst, err := xrayproc.Start(config("modern", tc.outbound), upstream, "warning", testLogger(t))
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(func() {
				if err := inst.Close(); err != nil {
					t.Errorf("Close: %v", err)
				}
			})
			if inst.Version == "" {
				t.Error("Version is empty, want xray's version")
			}
		})
	}
}

func TestStartErrorIsReturned(t *testing.T) {
	inst, err := xrayproc.Start(config("broken", `{"protocol":"nope"}`), upstream, "warning", testLogger(t))
	if err == nil {
		inst.Close()
		t.Fatal("Start: want error, got nil")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("error = %q, want it to name the config %q", err, "broken")
	}
}

// TestStartFailureIsAttributed pins that a config that cannot start leaves a
// line naming it: xray's own stream carries no instance identity.
func TestStartFailureIsAttributed(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	inst, err := xrayproc.Start(config("bad", `{"protocol":"nope"}`), upstream, "warning", logger)
	if err == nil {
		inst.Close()
		t.Fatal("Start: want error, got nil")
	}

	out := buf.String()
	for _, want := range []string{"config=bad", "subsystem=xray", "nope"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q does not contain %q", out, want)
		}
	}
}

// TestManagerApplyRoutesXrayLogs pins that steady-state xray lines reach slog
// once Apply returns: every instance start registers xray's own process-global
// handler, so only the install at the end of Apply puts the exporter's back.
func TestManagerApplyRoutesXrayLogs(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	t.Cleanup(func() { xraylog.Install(testLogger(t), clog.Severity_Unknown) })

	m := xrayproc.NewManager(upstream, "info", logger)
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Errorf("Manager.Close: %v", err)
		}
	})
	if _, _, errs := m.Apply([]xraycfg.Config{config("a", freedomOutbound)}); len(errs) != 0 {
		t.Fatalf("Apply errors: %v", errs)
	}

	clog.Record(&clog.GeneralMessage{Severity: clog.Severity_Error, Content: "steady boom"})

	out := buf.String()
	for _, want := range []string{"subsystem=xray", "steady boom"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q does not contain %q", out, want)
		}
	}
}

func TestManagerApplyDiff(t *testing.T) {
	m := xrayproc.NewManager(upstream, "warning", testLogger(t))
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Errorf("Manager.Close: %v", err)
		}
	})

	a := config("a", freedomOutbound)
	b := config("b", freedomOutbound)
	c := config("c", freedomOutbound)

	started, stopped, errs := m.Apply([]xraycfg.Config{a, b})
	if len(errs) != 0 {
		t.Fatalf("Apply([a b]) errors: %v", errs)
	}
	if got := strings.Join(started, ","); got != "a,b" {
		t.Errorf("started = %v, want [a b]", started)
	}
	if len(stopped) != 0 {
		t.Errorf("stopped = %v, want none", stopped)
	}

	bPort := findInstance(t, m, "b").SocksPort
	waitDial(t, bPort, 2*time.Second)

	started, stopped, errs = m.Apply([]xraycfg.Config{a, c})
	if len(errs) != 0 {
		t.Fatalf("Apply([a c]) errors: %v", errs)
	}
	if got := strings.Join(started, ","); got != "c" {
		t.Errorf("started = %v, want [c]", started)
	}
	if got := strings.Join(stopped, ","); got != "b" {
		t.Errorf("stopped = %v, want [b]", stopped)
	}
	if got := strings.Join(instanceNames(m), ","); got != "a,c" {
		t.Errorf("running instances = %v, want [a c]", got)
	}

	waitDial(t, findInstance(t, m, "a").SocksPort, 2*time.Second)
	waitDial(t, findInstance(t, m, "c").SocksPort, 2*time.Second)

	if conn, err := net.DialTimeout("tcp", socksAddr(bPort), 200*time.Millisecond); err == nil {
		conn.Close()
		t.Errorf("removed config b still accepts on port %d", bPort)
	}
}

func TestManagerApplyKeepsPortsForUnchanged(t *testing.T) {
	m := xrayproc.NewManager(upstream, "warning", testLogger(t))
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Errorf("Manager.Close: %v", err)
		}
	})

	a := config("a", freedomOutbound)
	if _, _, errs := m.Apply([]xraycfg.Config{a, config("b", freedomOutbound)}); len(errs) != 0 {
		t.Fatalf("Apply([a b]) errors: %v", errs)
	}
	before := findInstance(t, m, "a")

	if _, _, errs := m.Apply([]xraycfg.Config{a}); len(errs) != 0 {
		t.Fatalf("Apply([a]) errors: %v", errs)
	}
	after := findInstance(t, m, "a")

	if after != before {
		t.Error("unchanged config a was restarted, want the same instance")
	}
	if after.SocksPort != before.SocksPort || after.DNSPort != before.DNSPort {
		t.Errorf("ports = socks %d, dns %d, want socks %d, dns %d",
			after.SocksPort, after.DNSPort, before.SocksPort, before.DNSPort)
	}
	waitDial(t, after.SocksPort, 2*time.Second)
}
