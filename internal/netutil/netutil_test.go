package netutil

import (
	"strings"
	"testing"
)

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

func TestParseTarget(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		host string
		port string
	}{
		{name: "name", in: "ya.ru", host: "ya.ru"},
		{name: "trimmed", in: " ya.ru ", host: "ya.ru"},
		{name: "path", in: "ya.ru/health", host: "ya.ru"},
		{name: "port", in: "example.com:8443", host: "example.com", port: "8443"},
		{name: "port and path", in: "example.com:8443/x", host: "example.com", port: "8443"},
		{name: "port with zero padding", in: "example.com:0443", host: "example.com", port: "443"},
		{name: "ipv4 literal", in: "1.1.1.1", host: "1.1.1.1"},
		{name: "ipv4 literal with port", in: "1.1.1.1:443", host: "1.1.1.1", port: "443"},
		{name: "bracketed ipv6", in: "[2606:4700::1111]", host: "2606:4700::1111"},
		{name: "bracketed ipv6 with port", in: "[2606:4700::1111]:443/x", host: "2606:4700::1111", port: "443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, err := ParseTarget(tc.in)
			if err != nil {
				t.Fatalf("ParseTarget(%q): %v", tc.in, err)
			}
			if target.Host != tc.host || target.Port != tc.port {
				t.Errorf("ParseTarget(%q) = %+v, want host %q port %q", tc.in, target, tc.host, tc.port)
			}
		})
	}
}

func TestParseTargetRejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: " ", want: "host is required"},
		{name: "scheme", in: "https://ya.ru", want: "must not include a scheme"},
		{name: "path only", in: "/health", want: "has no name"},
		{name: "unbracketed ipv6", in: "2606:4700::1111", want: "an IPv6 address must be bracketed"},
		{name: "unbracketed ipv6 with path", in: "2606:4700::1111/h", want: "an IPv6 address must be bracketed"},
		{name: "brackets without an address", in: "[not-an-ip]", want: "is not a bracketed address"},
		{name: "unclosed bracket", in: "[2606:4700::1111", want: "is not a bracketed address"},
		{name: "zero port", in: "ya.ru:0", want: "has an invalid port"},
		{name: "port too large", in: "ya.ru:65536", want: "has an invalid port"},
		{name: "non-numeric port", in: "ya.ru:dns", want: "is not a valid host"},
		{name: "credentials", in: "user:pw@ya.ru", want: "must not carry credentials"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseTarget(tc.in)
			if err == nil {
				t.Fatalf("ParseTarget(%q) accepted", tc.in)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ParseTarget(%q) = %v, want it to contain %q", tc.in, err, tc.want)
			}
		})
	}
}

func TestParseTargetRejectsEmptyPort(t *testing.T) {
	if _, err := ParseTarget("ya.ru:"); err == nil {
		t.Error("ParseTarget accepted a host with an empty port")
	}
}
