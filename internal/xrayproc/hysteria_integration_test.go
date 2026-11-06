//go:build integration

package xrayproc_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	clog "github.com/xtls/xray-core/common/log"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"

	"github.com/OPAL-vpn/xray-exporter/internal/netutil"
	"github.com/OPAL-vpn/xray-exporter/internal/probe"
	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
	"github.com/OPAL-vpn/xray-exporter/internal/xraylog"
	"github.com/OPAL-vpn/xray-exporter/internal/xrayproc"
)

// TestHysteriaClientsAreNotSharedAcrossInstances guards the xray-core fix that
// keys the hysteria client cache by (destination, stream settings). With the
// old address-only key, the second instance reuses the first instance's client
// and therefore authenticates as the first user; the server access log shows
// which user carried each request.
func TestHysteriaClientsAreNotSharedAcrossInstances(t *testing.T) {
	if raceDetectorEnabled {
		// The clients are fine, but the linked hysteria server has an upstream
		// data race in udpSessionManager.clean (conn.go:194 vs :234). The
		// non-race CI step runs this test.
		t.Skip("xray-core hysteria server has an upstream data race; run without -race")
	}

	target := startEchoTarget(t)

	certFile, keyFile, pin := testCertificate(t)
	serverPort := freeUDPPort(t)

	startCore(t, fmt.Sprintf(`{
		"log": {"loglevel": "warning"},
		"inbounds": [{
			"tag": "hy-in",
			"listen": "127.0.0.1",
			"port": %d,
			"protocol": "hysteria",
			"settings": {"version": 2, "clients": [
				{"auth": "auth-a", "email": "a@test"},
				{"auth": "auth-b", "email": "b@test"}
			]},
			"streamSettings": {
				"network": "hysteria",
				"security": "tls",
				"tlsSettings": {
					"alpn": ["h3"],
					"certificates": [{"certificateFile": %q, "keyFile": %q, "oneTimeLoading": true}]
				},
				"hysteriaSettings": {"version": 2}
			}
		}],
		"outbounds": [{
			"tag": "direct",
			"protocol": "freedom",
			"settings": {"finalRules": [{"action": "allow", "ip": ["127.0.0.1/32"]}]}
		}]
	}`, serverPort, certFile, keyFile))

	// xrayproc.Start installs the exporter's process-wide xray log bridge, so
	// the server's access records arrive in this logger, not its own file.
	var captured safeBuffer
	logger := slog.New(slog.NewTextHandler(&captured, nil))
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("xray log:\n%s", captured.String())
		}
	})

	client := func(name, auth string) *xrayproc.Instance {
		raw := fmt.Sprintf(`{
			"outbounds": [{
				"tag": "hy",
				"protocol": "hysteria",
				"settings": {"version": 2, "address": "127.0.0.1", "port": %d},
				"streamSettings": {
					"network": "hysteria",
					"security": "tls",
					"tlsSettings": {
						"serverName": "127.0.0.1",
						"alpn": ["h3"],
						"pinnedPeerCertSha256": %q
					},
					"hysteriaSettings": {"version": 2, "auth": %q}
				}
			}]
		}`, serverPort, pin, auth)
		inst, err := xrayproc.Start(xraycfg.Config{Name: name, Raw: []byte(raw)},
			xraycfg.Upstream{Host: "1.1.1.1", Port: 53}, "info", logger)
		if err != nil {
			t.Fatalf("start %s: %v", name, err)
		}
		// Manager.Apply installs the bridge after its starts; Start alone leaves
		// the server's app/log as the handler, which drops access records.
		xraylog.Install(logger, clog.Severity_Info)
		t.Cleanup(func() {
			if err := inst.Close(); err != nil {
				t.Errorf("close %s: %v", name, err)
			}
		})
		return inst
	}

	// Client A dials first, so with the old cache its client is the shared one.
	dialThroughTunnel(t, client("client-a", "auth-a"), target)
	dialThroughTunnel(t, client("client-b", "auth-b"), target)

	waitForAccessLine(t, &captured, "email: a@test")
	// Fails while the cache is keyed by address only: B's request travels on
	// A's client and the server attributes it to A.
	waitForAccessLine(t, &captured, "email: b@test")
}

// startEchoTarget listens on an ephemeral loopback port and echoes bytes back.
func startEchoTarget(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for target: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	return ln.Addr().String()
}

// freeUDPPort returns a loopback UDP port that was free a moment ago.
func freeUDPPort(t *testing.T) int {
	t.Helper()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve UDP port: %v", err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	if err := pc.Close(); err != nil {
		t.Fatalf("release UDP port: %v", err)
	}
	return port
}

// testCertificate writes a self-signed certificate for 127.0.0.1 and returns
// the SHA-256 pin the client verifies it with.
func testCertificate(t *testing.T) (certFile, keyFile, pin string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	sum := sha256.Sum256(der)
	pin = hex.EncodeToString(sum[:])

	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write certificate: %v", err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certFile, keyFile, pin
}

// startCore builds and starts an xray instance from a raw JSON config.
func startCore(t *testing.T, raw string) *core.Instance {
	t.Helper()

	var parsed conf.Config
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("decode server config: %v", err)
	}
	built, err := parsed.Build()
	if err != nil {
		t.Fatalf("build server config: %v", err)
	}
	inst, err := core.New(built)
	if err != nil {
		t.Fatalf("create server instance: %v", err)
	}
	if err := inst.Start(); err != nil {
		_ = inst.Close()
		t.Fatalf("start server instance: %v", err)
	}
	t.Cleanup(func() {
		if err := inst.Close(); err != nil {
			t.Errorf("close server instance: %v", err)
		}
	})
	return inst
}

// dialThroughTunnel echoes one message through the instance's socks inbound.
func dialThroughTunnel(t *testing.T, inst *xrayproc.Instance, target string) {
	t.Helper()

	dialer := probe.NewSocksDialer(netutil.LoopbackAddr(inst.SocksPort), 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := dialer.DialContext(ctx, "tcp", target)
		if err != nil {
			lastErr = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		lastErr = echoOnce(conn)
		conn.Close()
		if lastErr == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("echo through %s: %v", inst.Name, lastErr)
}

func echoOnce(conn net.Conn) error {
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		return err
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if string(reply) != "ping" {
		return fmt.Errorf("echo = %q, want %q", reply, "ping")
	}
	return nil
}

// waitForAccessLine blocks until needle appears in the captured xray log.
func waitForAccessLine(t *testing.T, logs *safeBuffer, needle string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logs.String(), needle) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%q never appeared in the xray log; log:\n%s", needle, logs.String())
}

// safeBuffer collects slog output from concurrently running instances.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
