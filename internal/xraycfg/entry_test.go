package xraycfg

import (
	"errors"
	"testing"
)

func TestEntry(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"vless vnext", `{"outbounds":[{"protocol":"vless","settings":{"vnext":[{"address":"edge.example","port":443}]}}]}`, "edge.example:443"},
		{"trojan servers", `{"outbounds":[{"protocol":"trojan","settings":{"servers":[{"address":"192.0.2.7","port":8443}]}}]}`, "192.0.2.7:8443"},
		{"flat", `{"outbounds":[{"protocol":"wireguard","settings":{"address":"198.51.100.1","port":51820}}]}`, "198.51.100.1:51820"},
		{"skips freedom", `{"outbounds":[{"protocol":"freedom","settings":{}},{"protocol":"vless","settings":{"vnext":[{"address":"edge.example","port":443}]}}]}`, "edge.example:443"},
		{"ipv6", `{"outbounds":[{"protocol":"vless","settings":{"vnext":[{"address":"2001:db8::1","port":443}]}}]}`, "[2001:db8::1]:443"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Entry([]byte(tt.raw))
			if err != nil || got != tt.want {
				t.Errorf("Entry = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestEntryNone(t *testing.T) {
	for _, raw := range []string{
		`{"outbounds":[{"protocol":"freedom","settings":{}}]}`,
		`{"outbounds":[{"protocol":"vless","settings":{"vnext":[{"address":"x","port":0}]}}]}`,
		`{}`,
	} {
		if _, err := Entry([]byte(raw)); !errors.Is(err, ErrNoEntry) {
			t.Errorf("Entry(%s) err = %v, want ErrNoEntry", raw, err)
		}
	}
	if _, err := Entry([]byte(`not json`)); err == nil {
		t.Error("invalid JSON accepted")
	}
}
