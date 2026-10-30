package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/egress"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
)

type fakeLookuper struct {
	id    egress.Identity
	err   error
	calls atomic.Int32
}

func (f *fakeLookuper) Lookup(context.Context) (egress.Identity, error) {
	f.calls.Add(1)
	return f.id, f.err
}

// reachabilityApp builds the parts of application the reachability code reads
// and returns the buffer its log lines go to.
func reachabilityApp(rc *config.Reachability) (*application, *bytes.Buffer) {
	cfg := &config.ExporterConfig{Reachability: rc}
	logs := &bytes.Buffer{}
	a := &application{
		env:            Env{Workers: 4, ControlTimeout: 2 * time.Second},
		logger:         slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		snap:           metrics.New(),
		exporterConfig: cfg,
		egressWake:     make(chan struct{}, 1),
	}
	a.snap.SetReachability(reachabilitySpec(cfg))
	return a, logs
}

// closedAddr returns an address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func vlessConfig(addr string) []byte {
	host, port, _ := net.SplitHostPort(addr)
	return []byte(fmt.Sprintf(`{"outbounds":[{"protocol":"vless","settings":{"vnext":[{"address":%q,"port":%s}]}}]}`, host, port))
}

func TestProbeControlsAgainstLocalPeers(t *testing.T) {
	https := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer https.Close()
	open, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()

	a, _ := reachabilityApp(&config.Reachability{Controls: config.ReachabilityControls{
		HTTPS: []config.HostControl{{Name: "local", Host: strings.TrimPrefix(https.URL, "https://"), Class: config.ClassDomestic}},
		TCP: []config.TCPControl{
			{Name: "open", Addr: open.Addr().String(), Class: config.ClassForeign},
			{Name: "closed", Addr: closedAddr(t), Class: config.ClassForeign},
		},
		DNS: []config.HostControl{{Name: "localhost", Host: "localhost", Class: config.ClassDomestic}},
	}})
	a.probeControls(context.Background())

	got := map[string]bool{}
	for _, c := range a.snap.Export().Reachability.Controls {
		got[c.Kind+"/"+c.Name] = c.Up
	}
	want := map[string]bool{"https/local": true, "tcp/open": true, "tcp/closed": false, "dns/localhost": true}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("control %s up = %v, want %v", k, got[k], w)
		}
	}
}

func TestRunControlRejectsUnknownKind(t *testing.T) {
	if _, _, err := runControl(context.Background(), config.Control{Kind: "icmp", Name: "x"}, time.Second); err == nil {
		t.Error("an unknown kind was probed instead of rejected")
	}
}

func TestProbeEntriesUseTheConfigsAddress(t *testing.T) {
	open, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	down := closedAddr(t)

	a, logs := reachabilityApp(&config.Reachability{EntryTCP: true})
	for _, name := range []string{"up", "down", "noentry"} {
		a.snap.Seed(name, nil, nil, time.Now())
	}
	a.recordEntries([]xraycfg.Config{
		{Name: "up", Raw: vlessConfig(open.Addr().String())},
		{Name: "down", Raw: vlessConfig(down)},
		{Name: "noentry", Raw: []byte(`{"outbounds":[{"protocol":"freedom"}]}`)},
	})
	a.probeEntries(context.Background(), []string{"up", "down", "noentry"})

	for _, c := range a.snap.Export().Configs {
		switch c.Name {
		case "up", "down":
			if c.Entry == nil || c.Entry.Up != (c.Name == "up") {
				t.Errorf("entry %s = %+v, want up=%v", c.Name, c.Entry, c.Name == "up")
			}
		case "noentry":
			if c.Entry != nil {
				t.Errorf("a config without an entry was probed: %+v", c.Entry)
			}
		}
	}
	out := logs.String()
	if !strings.Contains(out, "entry address unknown") || !strings.Contains(out, "entry unreachable") {
		t.Errorf("expected the unknown-entry and unreachable warnings, got:\n%s", out)
	}
	if host, _, _ := net.SplitHostPort(down); strings.Contains(out, down) || strings.Contains(out, host+":") {
		t.Errorf("a log line leaks the entry address:\n%s", out)
	}
}

func TestRecordEntriesOnlyWithEntryTCP(t *testing.T) {
	a, _ := reachabilityApp(&config.Reachability{})
	a.recordEntries([]xraycfg.Config{{Name: "x", Raw: vlessConfig("127.0.0.1:443")}})
	if _, ok := a.entryAddr("x"); ok {
		t.Error("entry addresses were read although entry_tcp is off")
	}
}

func TestDialReasonDropsTheName(t *testing.T) {
	_, err := net.DialTimeout("tcp", "no-such-host.invalid:443", 2*time.Second)
	if err == nil {
		t.Skip("the resolver answered for a .invalid name")
	}
	reason := dialReason(err)
	if strings.Contains(reason.Error(), "no-such-host") || reason.Error() == "connect failed" {
		t.Errorf("reason = %q, want the cause without the host name", reason)
	}
}

func TestLookupEgressKeepsLastIdentityOnFailure(t *testing.T) {
	a, _ := reachabilityApp(&config.Reachability{})

	if !a.lookupEgress(context.Background(), &fakeLookuper{id: egress.Identity{ASName: "PJSC MegaFon", Country: "RU"}}) {
		t.Fatal("a good lookup reported failure")
	}
	if a.lookupEgress(context.Background(), &fakeLookuper{err: errors.New("down")}) {
		t.Fatal("a failed lookup reported success")
	}

	got := a.snap.Export().Reachability
	if !got.HasIdentity || got.Identity.ASName != "PJSC MegaFon" {
		t.Errorf("identity = %+v, want the last good one", got)
	}
}

func TestEgressLoopLooksUpOnStartAndOnWake(t *testing.T) {
	a, _ := reachabilityApp(&config.Reachability{})
	fake := &fakeLookuper{id: egress.Identity{ASName: "PJSC MegaFon", Country: "RU"}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.egressLoop(ctx, fake); close(done) }()

	waitFor(t, "the startup lookup", func() bool { return fake.calls.Load() == 1 })
	a.wakeEgress()
	waitFor(t, "the lookup after a wake", func() bool { return fake.calls.Load() == 2 })

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not stop")
	}
}

func TestEgressLoopKeepsLookingWhileTheBlockIsOn(t *testing.T) {
	a, _ := reachabilityApp(&config.Reachability{})
	fake := &fakeLookuper{err: errors.New("down")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.egressLoop(ctx, fake)

	// A failure waits a minute before the next attempt, so a second call means the
	// loop kept the block on rather than dropping into the hourly wait.
	waitFor(t, "the startup lookup", func() bool { return fake.calls.Load() == 1 })
	a.wakeEgress()
	waitFor(t, "a retry after a failure", func() bool { return fake.calls.Load() == 2 })
}

func TestRetryWaitGrowsAndCaps(t *testing.T) {
	for _, tc := range []struct {
		failures int
		want     time.Duration
	}{
		{failures: 1, want: time.Minute},
		{failures: 2, want: 2 * time.Minute},
		{failures: 4, want: 4 * time.Minute},
		{failures: 5, want: 5 * time.Minute},
		{failures: 6, want: 5 * time.Minute},
		{failures: 1440, want: 5 * time.Minute},
	} {
		if got := retryWait(tc.failures); got != tc.want {
			t.Errorf("retryWait(%d) = %s, want %s", tc.failures, got, tc.want)
		}
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !ok() {
		select {
		case <-deadline:
			t.Fatalf("no %s within 5s", what)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
