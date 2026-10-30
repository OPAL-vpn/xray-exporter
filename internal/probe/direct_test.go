package probe_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/probe"
)

func TestProbeTCP(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()

	res := probe.ProbeTCP(context.Background(), addr, 2*time.Second)
	if !res.Up || res.Err != nil || res.RTT <= 0 {
		t.Errorf("open port: %+v", res)
	}

	l.Close()
	res = probe.ProbeTCP(context.Background(), addr, 2*time.Second)
	if res.Up || res.Err == nil {
		t.Errorf("closed port: %+v, want a failure with an error", res)
	}
}

func TestProbeTCPHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A documentation address never answers; a cancelled context must return
	// at once instead of waiting for the timeout.
	start := time.Now()
	res := probe.ProbeTCP(ctx, "192.0.2.1:443", 5*time.Second)
	if res.Up || time.Since(start) > time.Second {
		t.Errorf("cancelled probe: %+v after %v", res, time.Since(start))
	}
}

func TestProbeSystemDNS(t *testing.T) {
	res := probe.ProbeSystemDNS(context.Background(), "localhost", 2*time.Second)
	if !res.Up || res.Err != nil {
		t.Errorf("localhost: %+v", res)
	}

	res = probe.ProbeSystemDNS(context.Background(), "192.0.2.1", time.Second)
	if res.Up || !errors.Is(res.Err, probe.ErrIPLiteral) {
		t.Errorf("address literal: %+v, want ErrIPLiteral", res)
	}

	res = probe.ProbeSystemDNS(context.Background(), "no-such-host.invalid", 2*time.Second)
	if res.Up || res.Err == nil {
		t.Errorf("unresolvable name: %+v", res)
	}
}
