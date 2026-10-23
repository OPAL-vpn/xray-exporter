package netutil

import "testing"

func TestLoopbackAddr(t *testing.T) {
	for _, tc := range []struct {
		port int
		want string
	}{
		{port: 0, want: "127.0.0.1:0"},
		{port: 9787, want: "127.0.0.1:9787"},
	} {
		if got := LoopbackAddr(tc.port); got != tc.want {
			t.Errorf("LoopbackAddr(%d) = %q, want %q", tc.port, got, tc.want)
		}
	}
}

func TestHostPort(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		host string
		port int
		ok   bool
	}{
		{name: "ipv4", in: "9.9.9.9:53", host: "9.9.9.9", port: 53, ok: true},
		{name: "ipv6", in: "[::1]:53", host: "::1", port: 53, ok: true},
		{name: "name", in: "dns.example:853", host: "dns.example", port: 853, ok: true},
		{name: "empty", in: ""},
		{name: "no port", in: "9.9.9.9"},
		{name: "no host", in: ":53"},
		{name: "zero port", in: "9.9.9.9:0"},
		{name: "port too large", in: "9.9.9.9:65536"},
		{name: "non-numeric port", in: "9.9.9.9:dns"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, port, err := HostPort(tc.in)
			if tc.ok {
				if err != nil {
					t.Fatalf("HostPort(%q): %v", tc.in, err)
				}
				if host != tc.host || port != tc.port {
					t.Errorf("HostPort(%q) = %q, %d, want %q, %d", tc.in, host, port, tc.host, tc.port)
				}
				return
			}
			if err == nil {
				t.Errorf("HostPort(%q) = %q, %d, want an error", tc.in, host, port)
			}
		})
	}
}
