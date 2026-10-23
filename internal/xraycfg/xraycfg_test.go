package xraycfg_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
)

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestDiscover(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "b.json", "{\"outbounds\":[{\"protocol\":\"b\"}]}")
	writeFile(t, dir, "a.json", "{\"outbounds\":[{\"protocol\":\"a\"}]}")
	writeFile(t, dir, "notes.txt", "not a config")

	configs, err := xraycfg.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("len(configs) = %d, want 2", len(configs))
	}

	wantNames := []string{"a", "b"}
	for i, want := range wantNames {
		if configs[i].Name != want {
			t.Errorf("configs[%d].Name = %q, want %q", i, configs[i].Name, want)
		}
		wantPath := filepath.Join(dir, want+".json")
		wantRaw, err := os.ReadFile(wantPath)
		if err != nil {
			t.Fatalf("read %s: %v", wantPath, err)
		}
		if !reflect.DeepEqual(configs[i].Raw, wantRaw) {
			t.Errorf("configs[%d].Raw = %q, want %q", i, configs[i].Raw, wantRaw)
		}
	}
}

func TestDiscoverEmpty(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "notes.txt", "not a config")

	_, err := xraycfg.Discover(dir)
	if err == nil {
		t.Fatal("Discover: want error, got nil")
	}
	if !errors.Is(err, xraycfg.ErrNoConfigs) {
		t.Errorf("error = %v, want ErrNoConfigs", err)
	}
	if !strings.Contains(err.Error(), "no *.json configs") {
		t.Errorf("error = %q, want it to contain %q", err, "no *.json configs")
	}
}

func TestDiscoverMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")

	_, err := xraycfg.Discover(dir)
	if err == nil {
		t.Fatal("Discover: want error, got nil")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error = %q, want it to contain the directory path %q", err, dir)
	}
	// An unreadable directory is not the authoritative empty set: the caller
	// keeps the previous configs rather than stopping every instance.
	if errors.Is(err, xraycfg.ErrNoConfigs) {
		t.Errorf("error = %v, want a read error, not ErrNoConfigs", err)
	}
}

func TestUpstreamFrom(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    xraycfg.Upstream
		wantErr string
	}{
		{name: "ipv4", input: "1.1.1.1:53", want: xraycfg.Upstream{Host: "1.1.1.1", Port: 53}},
		{name: "hostname", input: "dns.example:53", want: xraycfg.Upstream{Host: "dns.example", Port: 53}},
		{name: "missing port", input: "1.1.1.1", wantErr: "host:port"},
		{name: "zero port", input: "1.1.1.1:0", wantErr: "host:port"},
		{name: "port out of range", input: "1.1.1.1:70000", wantErr: "host:port"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := xraycfg.UpstreamFrom(tc.input)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("UpstreamFrom(%q): want error, got %+v", tc.input, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("UpstreamFrom(%q): %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("UpstreamFrom(%q) = %+v, want %+v", tc.input, got, tc.want)
			}
		})
	}
}

// injectFixture carries every section the injector must leave untouched.
const injectFixture = `{
  "log": {"loglevel": "debug", "access": "/tmp/a.log"},
  "inbounds": [
    {
      "listen": "127.0.0.1",
      "port": 10808,
      "protocol": "socks",
      "tag": "user-socks",
      "settings": {"auth": "noauth", "udp": true}
    }
  ],
  "outbounds": [
    {
      "protocol": "vless",
      "tag": "proxy",
      "settings": {"vnext": [{"address": "example.com", "port": 443, "users": [{"id": "uuid", "encryption": "none"}]}]}
    },
    {"protocol": "freedom", "tag": "direct"}
  ],
  "routing": {
    "domainStrategy": "IPIfNonMatch",
    "rules": [{"type": "field", "domain": ["geosite:category-ads"], "outboundTag": "proxy"}]
  },
  "policy": {"levels": {"0": {"handshake": 4, "connIdle": 300}}},
  "observatory": {
    "subjectSelector": ["proxy"],
    "probeUrl": "https://www.google.com/generate_204",
    "probeInterval": "10s"
  }
}`

type inbound struct {
	Listen   string          `json:"listen"`
	Port     int             `json:"port"`
	Protocol string          `json:"protocol"`
	Tag      string          `json:"tag"`
	Settings json.RawMessage `json:"settings"`
}

func decodeInbounds(t *testing.T, doc map[string]json.RawMessage) []inbound {
	t.Helper()
	var inbounds []inbound
	if err := json.Unmarshal(doc["inbounds"], &inbounds); err != nil {
		t.Fatalf("decode inbounds: %v", err)
	}
	return inbounds
}

func decodeJSON(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

func TestInjectReplacesInboundsAndLog(t *testing.T) {
	raw := []byte(injectFixture)
	ports := xraycfg.Ports{Socks: 1111, DNS: 2222}

	out, err := xraycfg.Inject(raw, ports, xraycfg.Upstream{Host: "9.9.9.9", Port: 53}, "warning")
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("decode injected config: %v", err)
	}

	inbounds := decodeInbounds(t, doc)
	if len(inbounds) != 2 {
		t.Fatalf("len(inbounds) = %d, want 2", len(inbounds))
	}

	socks := inbounds[0]
	if socks.Listen != "127.0.0.1" || socks.Port != ports.Socks || socks.Protocol != "socks" || socks.Tag != "xray-exporter-socks" {
		t.Errorf("socks inbound = %+v", socks)
	}
	wantSocksSettings := map[string]any{"auth": "noauth", "udp": true}
	if got := decodeJSON(t, socks.Settings); !reflect.DeepEqual(got, wantSocksSettings) {
		t.Errorf("socks settings = %v, want %v", got, wantSocksSettings)
	}

	dns := inbounds[1]
	if dns.Listen != "127.0.0.1" || dns.Port != ports.DNS || dns.Protocol != "dokodemo-door" || dns.Tag != "xray-exporter-dns" {
		t.Errorf("dns inbound = %+v", dns)
	}
	wantDNSSettings := map[string]any{"address": "9.9.9.9", "port": float64(53), "network": "udp"}
	if got := decodeJSON(t, dns.Settings); !reflect.DeepEqual(got, wantDNSSettings) {
		t.Errorf("dns settings = %v, want %v", got, wantDNSSettings)
	}

	wantLog := map[string]any{"loglevel": "warning", "access": "none"}
	if got := decodeJSON(t, doc["log"]); !reflect.DeepEqual(got, wantLog) {
		t.Errorf("log = %v, want %v", got, wantLog)
	}

	var original map[string]json.RawMessage
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatalf("decode original config: %v", err)
	}
	if len(doc) != len(original) {
		t.Errorf("len(doc) = %d, want %d: no top-level key may be added or removed", len(doc), len(original))
	}
	for key, rawBefore := range original {
		if key == "log" || key == "inbounds" {
			continue
		}
		rawAfter, ok := doc[key]
		if !ok {
			t.Fatalf("top-level key %q disappeared", key)
		}
		before, after := decodeJSON(t, rawBefore), decodeJSON(t, rawAfter)
		if !reflect.DeepEqual(before, after) {
			t.Errorf("top-level key %q changed:\nbefore: %v\nafter:  %v", key, before, after)
		}
	}
}

func TestInjectLogLevelFromParameter(t *testing.T) {
	out, err := xraycfg.Inject([]byte(injectFixture), xraycfg.Ports{Socks: 1111, DNS: 2222}, xraycfg.Upstream{Host: "9.9.9.9", Port: 53}, "error")
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("decode injected config: %v", err)
	}
	wantLog := map[string]any{"loglevel": "error", "access": "none"}
	if got := decodeJSON(t, doc["log"]); !reflect.DeepEqual(got, wantLog) {
		t.Errorf("log = %v, want %v", got, wantLog)
	}
}

func TestInjectErrors(t *testing.T) {
	ports := xraycfg.Ports{Socks: 1111, DNS: 2222}
	upstream := xraycfg.Upstream{Host: "9.9.9.9", Port: 53}

	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "invalid json", raw: `{"outbounds":`, wantErr: "invalid JSON"},
		{name: "empty outbounds", raw: `{"outbounds":[]}`, wantErr: "outbounds is empty"},
		{name: "missing outbounds", raw: `{"log":{"loglevel":"debug"}}`, wantErr: "outbounds is empty"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := xraycfg.Inject([]byte(tc.raw), ports, upstream, "warning")
			if err == nil {
				t.Fatal("Inject: want error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
