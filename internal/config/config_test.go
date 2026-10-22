package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Anton-beep/xray-exporter/internal/config"
)

func writeTargetsFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "targets.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write targets file: %v", err)
	}
	return path
}

const validBody = `egress_check_url: https://api.ipify.org?format=text
dns_probe_domain: example.com
dns_upstream: 1.1.1.1:53
targets:
  - host: youtube.com
  - host: netflix.com
  - host: gosuslugi.ru
dns_resolvers:
  - name: cloudflare
    doh_url: https://cloudflare-dns.com/dns-query
    dot_host: one.one.one.one
  - name: quad9
    doh_url: https://dns.quad9.net/dns-query
`

func TestLoadValidTargets(t *testing.T) {
	path := writeTargetsFile(t, validBody)

	targets, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if targets.EgressCheckURL != "https://api.ipify.org?format=text" {
		t.Errorf("EgressCheckURL = %q", targets.EgressCheckURL)
	}
	if targets.DNSProbeDomain != "example.com" {
		t.Errorf("DNSProbeDomain = %q", targets.DNSProbeDomain)
	}
	if targets.DNSUpstream != "1.1.1.1:53" {
		t.Errorf("DNSUpstream = %q", targets.DNSUpstream)
	}

	wantHosts := []string{"youtube.com", "netflix.com", "gosuslugi.ru"}
	if len(targets.Targets) != len(wantHosts) {
		t.Fatalf("len(Targets) = %d, want %d", len(targets.Targets), len(wantHosts))
	}
	for i, want := range wantHosts {
		if targets.Targets[i].Host != want {
			t.Errorf("Targets[%d].Host = %q, want %q", i, targets.Targets[i].Host, want)
		}
	}

	if len(targets.Resolvers) != 2 {
		t.Fatalf("len(Resolvers) = %d, want 2", len(targets.Resolvers))
	}
	cloudflare := targets.Resolvers[0]
	if cloudflare.Name != "cloudflare" {
		t.Errorf("Resolvers[0].Name = %q", cloudflare.Name)
	}
	if cloudflare.DoHURL != "https://cloudflare-dns.com/dns-query" {
		t.Errorf("Resolvers[0].DoHURL = %q", cloudflare.DoHURL)
	}
	if cloudflare.DoTHost != "one.one.one.one" {
		t.Errorf("Resolvers[0].DoTHost = %q", cloudflare.DoTHost)
	}
	quad9 := targets.Resolvers[1]
	if quad9.Name != "quad9" {
		t.Errorf("Resolvers[1].Name = %q", quad9.Name)
	}
	if quad9.DoHURL != "https://dns.quad9.net/dns-query" {
		t.Errorf("Resolvers[1].DoHURL = %q", quad9.DoHURL)
	}
	if quad9.DoTHost != "" {
		t.Errorf("Resolvers[1].DoTHost = %q, want empty", quad9.DoTHost)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	path := writeTargetsFile(t, `egress_check_url: https://api.ipify.org?format=text
dns_probe_domain: example.com
dns_upstream: 1.1.1.1:53
targets:
  - host: youtube.com
    tags: [video]
dns_resolvers:
  - name: cloudflare
    doh_url: https://cloudflare-dns.com/dns-query
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load accepted an unknown field")
	}
	msg := err.Error()
	if !strings.Contains(msg, filepath.Base(path)) {
		t.Errorf("error %q does not name the file %q", msg, filepath.Base(path))
	}
	if !strings.Contains(msg, "field tags not found") {
		t.Errorf("error %q does not mention the unknown field", msg)
	}
}

func TestLoadRequiresEgressCheckURL(t *testing.T) {
	path := writeTargetsFile(t, `dns_probe_domain: example.com
dns_upstream: 1.1.1.1:53
targets:
  - host: youtube.com
dns_resolvers:
  - name: cloudflare
    doh_url: https://cloudflare-dns.com/dns-query
`)

	err := loadError(t, path)
	if !strings.Contains(err, "targets.yaml: egress_check_url is required") {
		t.Errorf("error %q does not require egress_check_url", err)
	}
}

func TestLoadRequiresTargets(t *testing.T) {
	path := writeTargetsFile(t, `egress_check_url: https://api.ipify.org?format=text
dns_probe_domain: example.com
dns_upstream: 1.1.1.1:53
targets: []
`)

	err := loadError(t, path)
	if !strings.Contains(err, "at least one target is required") {
		t.Errorf("error %q does not require a target", err)
	}
}

func TestLoadRejectsDuplicateTarget(t *testing.T) {
	path := writeTargetsFile(t, `egress_check_url: https://api.ipify.org?format=text
dns_probe_domain: example.com
dns_upstream: 1.1.1.1:53
targets:
  - host: youtube.com
  - host: youtube.com
`)

	err := loadError(t, path)
	if !strings.Contains(err, `duplicate target "youtube.com"`) {
		t.Errorf("error %q does not flag the duplicate target", err)
	}
}

func TestLoadRequiresResolverTransport(t *testing.T) {
	path := writeTargetsFile(t, `egress_check_url: https://api.ipify.org?format=text
dns_probe_domain: example.com
dns_upstream: 1.1.1.1:53
targets:
  - host: youtube.com
dns_resolvers:
  - name: cloudflare
`)

	err := loadError(t, path)
	if !strings.Contains(err, `resolver "cloudflare": needs doh_url or dot_host`) {
		t.Errorf("error %q does not require a resolver transport", err)
	}
}

func TestLoadRejectsDuplicateResolver(t *testing.T) {
	path := writeTargetsFile(t, `egress_check_url: https://api.ipify.org?format=text
dns_probe_domain: example.com
dns_upstream: 1.1.1.1:53
targets:
  - host: youtube.com
dns_resolvers:
  - name: cloudflare
    doh_url: https://cloudflare-dns.com/dns-query
  - name: cloudflare
    dot_host: one.one.one.one
`)

	err := loadError(t, path)
	if !strings.Contains(err, `duplicate resolver "cloudflare"`) {
		t.Errorf("error %q does not flag the duplicate resolver", err)
	}
}

func TestLoadRejectsDNSUpstreamWithoutPort(t *testing.T) {
	path := writeTargetsFile(t, `egress_check_url: https://api.ipify.org?format=text
dns_probe_domain: example.com
dns_upstream: 1.1.1.1
targets:
  - host: youtube.com
`)

	err := loadError(t, path)
	if !strings.Contains(err, "dns_upstream must be host:port") {
		t.Errorf("error %q does not flag dns_upstream", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "targets.yaml")

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load accepted a missing file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error %q does not wrap os.ErrNotExist", err)
	}
}

func loadError(t *testing.T, path string) string {
	t.Helper()
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load accepted an invalid file")
	}
	return err.Error()
}
