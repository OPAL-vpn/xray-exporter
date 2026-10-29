package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
	"github.com/OPAL-vpn/xray-exporter/internal/metrics"
	"github.com/OPAL-vpn/xray-exporter/internal/vantage"
	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
)

type fakeLookuper struct {
	id  vantage.Identity
	err error
}

func (f *fakeLookuper) Lookup(context.Context) (vantage.Identity, error) { return f.id, f.err }

// vantageApp builds the parts of application the vantage code reads.
func vantageApp(v *config.Vantage) *application {
	cfg := &config.ExporterConfig{Vantage: v}
	a := &application{
		env:            Env{Workers: 4, ControlTimeout: 2 * time.Second},
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		snap:           metrics.New(),
		exporterConfig: cfg,
		vantageWake:    make(chan struct{}, 1),
	}
	a.snap.SetVantage(vantageSpec(cfg))
	return a
}

func TestControlsAgainstLocalPeers(t *testing.T) {
	https := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer https.Close()
	open, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	closedAddr := closed.Addr().String()
	closed.Close()

	a := vantageApp(&config.Vantage{Controls: config.VantageControls{
		HTTPS: []config.HTTPControl{{Name: "local", Host: strings.TrimPrefix(https.URL, "https://"), Class: config.ClassDomestic}},
		TCP: []config.TCPControl{
			{Name: "open", Addr: open.Addr().String(), Class: config.ClassForeign},
			{Name: "closed", Addr: closedAddr, Class: config.ClassForeign},
		},
		DNS: []config.DNSControl{{Name: "localhost", Host: "localhost", Class: config.ClassDomestic}},
	}})
	a.controls(context.Background())

	got := map[string]bool{}
	for _, c := range a.snap.Export().Vantage.Controls {
		got[c.Kind+"/"+c.Name] = c.Up
	}
	want := map[string]bool{"https/local": true, "tcp/open": true, "tcp/closed": false, "dns/localhost": true}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("control %s up = %v, want %v", k, got[k], w)
		}
	}
}

func TestProbesOffWithoutBlock(t *testing.T) {
	a := vantageApp(nil)
	a.controls(context.Background())
	a.entries(context.Background(), []string{"nl1"})
	if a.snap.Export().Vantage != nil {
		t.Error("vantage exported without a block")
	}
}

func TestEntriesProbeTheConfigsAddress(t *testing.T) {
	open, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	host, port, _ := net.SplitHostPort(open.Addr().String())

	a := vantageApp(&config.Vantage{EntryTCP: true})
	for _, name := range []string{"up", "down", "noentry"} {
		a.snap.Seed(name, nil, nil, time.Now())
	}
	a.recordEntries([]xraycfg.Config{
		{Name: "up", Raw: []byte(`{"outbounds":[{"protocol":"vless","settings":{"vnext":[{"address":"` + host + `","port":` + port + `}]}}]}`)},
		{Name: "down", Raw: []byte(`{"outbounds":[{"protocol":"vless","settings":{"vnext":[{"address":"127.0.0.1","port":1}]}}]}`)},
		{Name: "noentry", Raw: []byte(`{"outbounds":[{"protocol":"freedom"}]}`)},
	})
	a.entries(context.Background(), []string{"up", "down", "noentry"})

	for _, c := range a.snap.Export().Configs {
		wantUp := c.Name == "up"
		if c.Entry == nil || c.Entry.Up != wantUp {
			t.Errorf("entry %s = %+v, want up=%v", c.Name, c.Entry, wantUp)
		}
		if c.Entry != nil && c.Entry.Err != nil && strings.Contains(c.Entry.Err.Error(), "127.0.0.1") {
			t.Errorf("entry %s error leaks the address: %v", c.Name, c.Entry.Err)
		}
	}
}

func TestLookupKeepsLastIdentityOnFailure(t *testing.T) {
	a := vantageApp(&config.Vantage{})

	a.lookupVantage(context.Background(), &fakeLookuper{id: vantage.Identity{ASName: "PJSC MegaFon", Country: "RU"}})
	a.lookupVantage(context.Background(), &fakeLookuper{err: errors.New("down")})

	got := a.snap.Export().Vantage
	if !got.HasIdentity || got.Identity.ASName != "PJSC MegaFon" {
		t.Errorf("identity = %+v, want the last good one", got)
	}
}

func TestVantageLoopLooksUpAndStops(t *testing.T) {
	orig := newLookuper
	defer func() { newLookuper = orig }()
	newLookuper = func() lookuper {
		return &fakeLookuper{id: vantage.Identity{ASName: "PJSC MegaFon", Country: "RU"}}
	}

	a := vantageApp(&config.Vantage{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.vantageLoop(ctx); close(done) }()

	deadline := time.After(5 * time.Second)
	for !a.snap.Export().Vantage.HasIdentity {
		select {
		case <-deadline:
			t.Fatal("no lookup within 5s")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not stop")
	}
}
