package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
)

func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "xray-exporter-config.yml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	return path
}

const validBody = `dns_probe_domain: example.com
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
	path := writeConfigFile(t, validBody)

	targets, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
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
	path := writeConfigFile(t, `dns_probe_domain: example.com
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

func TestLoadRejectsEgressCheckURL(t *testing.T) {
	path := writeConfigFile(t, `egress_check_url: https://api.ipify.org?format=text
dns_probe_domain: example.com
dns_upstream: 1.1.1.1:53
targets:
  - host: youtube.com
`)

	err := loadError(t, path)
	if !strings.Contains(err, "egress_check_url") {
		t.Errorf("error %q does not name egress_check_url", err)
	}
}

func TestLoadRequiresTargets(t *testing.T) {
	path := writeConfigFile(t, `dns_probe_domain: example.com
dns_upstream: 1.1.1.1:53
targets: []
`)

	err := loadError(t, path)
	if !strings.Contains(err, "at least one target is required") {
		t.Errorf("error %q does not require a target", err)
	}
}

func TestLoadRejectsDuplicateTarget(t *testing.T) {
	path := writeConfigFile(t, `dns_probe_domain: example.com
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
	path := writeConfigFile(t, `dns_probe_domain: example.com
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
	path := writeConfigFile(t, `dns_probe_domain: example.com
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
	path := writeConfigFile(t, `dns_probe_domain: example.com
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
	path := filepath.Join(t.TempDir(), "xray-exporter-config.yml")

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

// TestLoadRejectsSchemeInHost covers the form that used to break the DNS probe.
func TestLoadRejectsSchemeInHost(t *testing.T) {
	path := writeConfigFile(t, `dns_probe_domain: example.com
dns_upstream: 1.1.1.1:53
targets:
  - host: youtube.com
  - host: https://youtube.com
`)

	err := loadError(t, path)
	if !strings.Contains(err, "xray-exporter-config.yml: target 1: host must not include a scheme") {
		t.Errorf("error %q does not reject the scheme with a field-named error", err)
	}
}

// TestLoadNormalizesHosts pins the accepted forms and trimming.
func TestLoadNormalizesHosts(t *testing.T) {
	path := writeConfigFile(t, `dns_probe_domain: example.com
dns_upstream: 1.1.1.1:53
targets:
  - host: youtube.com
  - host: example.com/health
  - host: "[2606:4700:4700::1111]"
  - host: "  [2001:db8::1]/status  "
  - host: 1.1.1.1
`)

	targets, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := []string{
		"youtube.com",
		"example.com/health",
		"[2606:4700:4700::1111]",
		"[2001:db8::1]/status",
		"1.1.1.1",
	}
	if len(targets.Targets) != len(want) {
		t.Fatalf("len(Targets) = %d, want %d", len(targets.Targets), len(want))
	}
	for i, host := range want {
		if targets.Targets[i].Host != host {
			t.Errorf("Targets[%d].Host = %q, want %q", i, targets.Targets[i].Host, host)
		}
	}
}

// TestLoadRejectsUnparseableHosts covers broken IPv6 forms.
func TestLoadRejectsUnparseableHosts(t *testing.T) {
	cases := []struct {
		name string
		host string
		want string
	}{
		{name: "unbracketed ipv6", host: "2606:4700:4700::1111", want: "an IPv6 address must be bracketed"},
		{name: "unbracketed ipv6 with path", host: "2606:4700:4700::1111/health", want: "an IPv6 address must be bracketed"},
		{name: "brackets without an address", host: "[not-an-ip]", want: "is not a bracketed address"},
		{name: "unclosed bracket", host: "[2001:db8::1", want: "is not a bracketed address"},
		{name: "port", host: "youtube.com:443", want: "a target takes no port"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, `dns_probe_domain: example.com
dns_upstream: 1.1.1.1:53
targets:
  - host: "`+tc.host+`"
`)

			err := loadError(t, path)
			if !strings.Contains(err, "target 0:") || !strings.Contains(err, tc.want) {
				t.Errorf("error %q does not reject %q with %q", err, tc.host, tc.want)
			}
		})
	}
}

// blockYAML expands an unindented block body into a top-level YAML block.
func blockYAML(key, body string) string {
	var b strings.Builder
	b.WriteString(key + ":\n")
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

// sourceYAML expands an unindented block body into an xray_config_source block.
func sourceYAML(body string) string {
	return blockYAML("xray_config_source", body)
}

func TestLoadSourceRemnawave(t *testing.T) {
	path := writeConfigFile(t, validBody+sourceYAML(`type: remnawave
url: https://panel.test/api/sub/short`))

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	src := cfg.XrayConfigSource
	if src == nil {
		t.Fatal("XrayConfigSource = nil, want the remnawave block")
	}
	if src.Type != "remnawave" {
		t.Errorf("Type = %q, want remnawave", src.Type)
	}
	if src.URL != "https://panel.test/api/sub/short" {
		t.Errorf("URL = %q", src.URL)
	}
	if src.Path != "" {
		t.Errorf("Path = %q, want empty", src.Path)
	}
	if src.Refresh != "" {
		t.Errorf("Refresh = %q, want empty", src.Refresh)
	}
	if src.Interval != 5*time.Minute {
		t.Errorf("Interval = %v, want 5m", src.Interval)
	}
}

// TestLoadSourceRemnawaveRefresh pins the parsed refresh, including the
// unquoted zero that must decode as the string "0".
func TestLoadSourceRemnawaveRefresh(t *testing.T) {
	cases := []struct {
		name    string
		refresh string
		want    time.Duration
	}{
		{name: "explicit", refresh: "90s", want: 90 * time.Second},
		{name: "zero", refresh: "0", want: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, validBody+sourceYAML(`type: remnawave
url: https://panel.test/api/sub/short
refresh: `+tc.refresh))

			cfg, err := config.Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			src := cfg.XrayConfigSource
			if src == nil {
				t.Fatal("XrayConfigSource = nil, want the remnawave block")
			}
			if src.Refresh != tc.refresh {
				t.Errorf("Refresh = %q, want %q", src.Refresh, tc.refresh)
			}
			if src.Interval != tc.want {
				t.Errorf("Interval = %v, want %v", src.Interval, tc.want)
			}
		})
	}
}

func TestLoadSourceDirectory(t *testing.T) {
	path := writeConfigFile(t, validBody+sourceYAML(`type: directory
path: /config/x`))

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	src := cfg.XrayConfigSource
	if src == nil {
		t.Fatal("XrayConfigSource = nil, want the directory block")
	}
	if src.Type != "directory" {
		t.Errorf("Type = %q, want directory", src.Type)
	}
	if src.Path != "/config/x" {
		t.Errorf("Path = %q", src.Path)
	}
	if src.Interval != 0 {
		t.Errorf("Interval = %v, want 0", src.Interval)
	}
}

func TestSourceAbsentIsNil(t *testing.T) {
	cfg, err := config.Load(writeConfigFile(t, validBody))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.XrayConfigSource != nil {
		t.Errorf("XrayConfigSource = %+v, want nil", cfg.XrayConfigSource)
	}
}

func TestRejectSource(t *testing.T) {
	cases := []struct {
		name string
		// source is an unindented xray_config_source body.
		source string
		want   string
		// decoder marks errors that come from KnownFields, not validateSource.
		decoder bool
	}{
		{
			name:   "missing type",
			source: "url: https://panel.test/api/sub/short",
			want:   "xray_config_source: type must be directory or remnawave",
		},
		{
			name:   "other type",
			source: "type: other",
			want:   "xray_config_source: type must be directory or remnawave",
		},
		{
			name:   "missing url",
			source: "type: remnawave",
			want:   "xray_config_source: url is required",
		},
		{
			name:   "relative url",
			source: "type: remnawave\nurl: not-a-url",
			want:   "xray_config_source: url must be an absolute http or https URL with a host",
		},
		{
			name:   "ftp url",
			source: "type: remnawave\nurl: ftp://panel.test/api/sub/short",
			want:   "xray_config_source: url must be an absolute http or https URL with a host",
		},
		{
			name:   "url and path",
			source: "type: remnawave\nurl: https://panel.test/api/sub/short\npath: /config/x",
			want:   "xray_config_source: path and url are mutually exclusive",
		},
		{
			name:   "refresh on directory",
			source: "type: directory\nrefresh: 5m",
			want:   "xray_config_source: refresh is only valid for a remnawave source",
		},
		{
			name:   "url on directory",
			source: "type: directory\nurl: https://panel.test/api/sub/short",
			want:   "xray_config_source: url is only valid for a remnawave source",
		},
		{
			name:   "negative refresh",
			source: "type: remnawave\nurl: https://panel.test/api/sub/short\nrefresh: -1s",
			want:   "xray_config_source: refresh must not be negative",
		},
		{
			name:   "unparseable refresh",
			source: "type: remnawave\nurl: https://panel.test/api/sub/short\nrefresh: 5 minutes",
			want:   "xray_config_source: refresh:",
		},
		{
			name:    "unknown field",
			source:  "type: remnawave\nurl: https://panel.test/api/sub/short\nrefresh_s: 5m",
			want:    "field refresh_s not found",
			decoder: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, validBody+sourceYAML(tc.source))

			err := loadError(t, path)
			if !strings.Contains(err, filepath.Base(path)) {
				t.Errorf("error %q does not name the file %q", err, filepath.Base(path))
			}
			if !strings.Contains(err, tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
			if !tc.decoder && !strings.Contains(err, filepath.Base(path)+": xray_config_source: ") {
				t.Errorf("error %q is not prefixed with %q", err, filepath.Base(path)+": xray_config_source: ")
			}
		})
	}
}

// TestSourceURLErrorDoesNotEchoSecret keeps the shortUuid out of logs: every
// url error names the field, never the value, and url.Parse errors are not
// wrapped because their text includes the URL.
func TestSourceURLErrorDoesNotEchoSecret(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{name: "rejected scheme", url: "ftp://short-secret.panel.test/api/sub/short-secret"},
		{name: "unparseable", url: "https://panel.test/api/sub/short-secret%zz"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, validBody+sourceYAML("type: remnawave\nurl: "+tc.url))

			err := loadError(t, path)
			if !strings.Contains(err, "xray_config_source: url must be an absolute http or https URL with a host") {
				t.Errorf("error %q does not name the url field", err)
			}
			if strings.Contains(err, "short-secret") {
				t.Errorf("error %q echoes the subscription URL", err)
			}
		})
	}
}

func TestLoadUDPTargetsDefaults(t *testing.T) {
	path := writeConfigFile(t, validBody+blockYAML("udp_targets", `- name: tg-stun-1
  kind: stun
  addr: "203.0.113.10:3478"`))

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.UDPTargets) != 1 {
		t.Fatalf("len(UDPTargets) = %d, want 1", len(cfg.UDPTargets))
	}
	target := cfg.UDPTargets[0]
	if target.Name != "tg-stun-1" || target.Kind != "stun" || target.Addr != "203.0.113.10:3478" {
		t.Errorf("target = %+v", target)
	}
	if target.Count != 20 {
		t.Errorf("Count = %d, want the default 20", target.Count)
	}
	if target.SpacingDur != 100*time.Millisecond {
		t.Errorf("SpacingDur = %v, want the default 100ms", target.SpacingDur)
	}
	if target.TimeoutDur != 0 {
		t.Errorf("TimeoutDur = %v, want 0 (the sampler's probe timeout)", target.TimeoutDur)
	}
}

// TestLoadUDPTargetsCountZeroDefaults pins count: 0 to the default. The field
// cannot tell an absent count from a written zero, so both mean 20.
func TestLoadUDPTargetsCountZeroDefaults(t *testing.T) {
	path := writeConfigFile(t, validBody+blockYAML("udp_targets", `- name: tg-stun-1
  kind: stun
  addr: "203.0.113.10:3478"
  count: 0`))

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.UDPTargets[0].Count; got != 20 {
		t.Errorf("Count = %d, want the default 20", got)
	}
}

func TestLoadUDPTargetsRejectsUnknownKind(t *testing.T) {
	path := writeConfigFile(t, validBody+blockYAML("udp_targets", `- name: tg-stun-1
  kind: echo
  addr: "203.0.113.10:3478"`))

	err := loadError(t, path)
	if !strings.Contains(err, "udp_targets[0]: kind must be stun") {
		t.Errorf("error %q does not reject the kind", err)
	}
}

func TestLoadUDPTargetsRejectsMissingName(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "absent", body: `- kind: stun
  addr: "203.0.113.10:3478"`},
		{name: "blank", body: `- name: "   "
  kind: stun
  addr: "203.0.113.10:3478"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, validBody+blockYAML("udp_targets", tc.body))

			err := loadError(t, path)
			if !strings.Contains(err, "udp_targets[0]: name is required") {
				t.Errorf("error %q does not require the name", err)
			}
		})
	}
}

func TestLoadUDPTargetsRejectsBadAddr(t *testing.T) {
	path := writeConfigFile(t, validBody+blockYAML("udp_targets", `- name: tg-stun-1
  kind: stun
  addr: "203.0.113.10"`))

	err := loadError(t, path)
	if !strings.Contains(err, `udp_targets[0]: addr "203.0.113.10" must be host:port`) {
		t.Errorf("error %q does not reject the addr", err)
	}
}

func TestLoadUDPTargetsRejectsDuplicateNames(t *testing.T) {
	path := writeConfigFile(t, validBody+blockYAML("udp_targets", `- name: tg-stun-1
  kind: stun
  addr: "203.0.113.10:3478"
- name: tg-stun-1
  kind: stun
  addr: "203.0.113.11:3478"`))

	err := loadError(t, path)
	if !strings.Contains(err, `udp_targets[1]: duplicate name "tg-stun-1"`) {
		t.Errorf("error %q does not flag the duplicate name", err)
	}
}

func TestLoadRejectsNameSharedAcrossUDPAndTCPBlocks(t *testing.T) {
	path := writeConfigFile(t, validBody+blockYAML("udp_targets", `- name: tg-stun-1
  kind: stun
  addr: "203.0.113.10:3478"`)+blockYAML("tcp_targets", `- name: tg-stun-1
  addr: "149.154.167.51:443"`))

	err := loadError(t, path)
	if !strings.Contains(err, `tcp_targets[0]: duplicate name "tg-stun-1"`) {
		t.Errorf("error %q does not flag the name shared with udp_targets", err)
	}
}

func TestLoadUDPTargetsRejectsCountOutOfRange(t *testing.T) {
	cases := []struct {
		name  string
		count string
	}{
		{name: "negative", count: "-1"},
		{name: "above the cap", count: "1001"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, validBody+blockYAML("udp_targets", `- name: tg-stun-1
  kind: stun
  addr: "203.0.113.10:3478"
  count: `+tc.count))

			err := loadError(t, path)
			if !strings.Contains(err, "udp_targets[0]: count must be in 1-1000") {
				t.Errorf("error %q does not reject the count", err)
			}
		})
	}
}

func TestLoadUDPTargetsRejectsSpacingOutOfRange(t *testing.T) {
	cases := []struct {
		name    string
		spacing string
	}{
		{name: "below the floor", spacing: "500us"},
		{name: "above the ceiling", spacing: "1500ms"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, validBody+blockYAML("udp_targets", `- name: tg-stun-1
  kind: stun
  addr: "203.0.113.10:3478"
  spacing: `+tc.spacing))

			err := loadError(t, path)
			if !strings.Contains(err, "udp_targets[0]: spacing must be between 1ms and 1s") {
				t.Errorf("error %q does not reject the spacing", err)
			}
		})
	}
}

func TestLoadUDPTargetsRejectsUnparseableDurations(t *testing.T) {
	cases := []struct {
		name  string
		field string
		want  string
	}{
		{name: "spacing", field: "spacing: 100 milliseconds", want: "udp_targets[0]: spacing:"},
		{name: "timeout", field: "timeout: 15 seconds", want: "udp_targets[0]: timeout:"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, validBody+blockYAML("udp_targets", `- name: tg-stun-1
  kind: stun
  addr: "203.0.113.10:3478"
  `+tc.field))

			err := loadError(t, path)
			if !strings.Contains(err, tc.want) {
				t.Errorf("error %q does not name the unparseable %s", err, tc.name)
			}
		})
	}
}

func TestLoadRejectsBurstLongerThanTimeout(t *testing.T) {
	cases := []struct {
		name    string
		spacing string
		timeout string
	}{
		{name: "longer", spacing: "1s", timeout: "10s"},
		{name: "equal", spacing: "100ms", timeout: "2s"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, validBody+blockYAML("udp_targets", `- name: tg-stun-1
  kind: stun
  addr: "203.0.113.10:3478"
  count: 20
  spacing: `+tc.spacing+`
  timeout: `+tc.timeout))

			err := loadError(t, path)
			if !strings.Contains(err, "udp_targets[0]: timeout ") || !strings.Contains(err, "must be longer than count x spacing") {
				t.Errorf("error %q does not reject the burst longer than its timeout", err)
			}
		})
	}
}

func TestLoadAcceptsBurstWithinTimeout(t *testing.T) {
	path := writeConfigFile(t, validBody+blockYAML("udp_targets", `- name: tg-stun-1
  kind: stun
  addr: "203.0.113.10:3478"
  count: 20
  spacing: 100ms
  timeout: 5s`))

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	target := cfg.UDPTargets[0]
	if target.TimeoutDur != 5*time.Second {
		t.Errorf("TimeoutDur = %v, want 5s", target.TimeoutDur)
	}
	if target.SpacingDur != 100*time.Millisecond {
		t.Errorf("SpacingDur = %v, want 100ms", target.SpacingDur)
	}
}

func TestLoadUDPUnderLoadURL(t *testing.T) {
	path := writeConfigFile(t, validBody+blockYAML("udp_under_load", "url: https://mirror.example.org/20mb.bin"))

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UDPUnderLoad == nil {
		t.Fatal("UDPUnderLoad = nil, want the block")
	}
	if cfg.UDPUnderLoad.URL != "https://mirror.example.org/20mb.bin" {
		t.Errorf("URL = %q", cfg.UDPUnderLoad.URL)
	}
}

func TestLoadUDPUnderLoadRejectsBadURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{name: "empty", url: `""`, want: "udp_under_load: url is required"},
		{name: "relative", url: "20mb.bin", want: "udp_under_load: url must be an absolute http or https URL with a host"},
		{name: "ftp", url: "ftp://mirror.example.org/20mb.bin", want: "udp_under_load: url must be an absolute http or https URL with a host"},
		{name: "no host", url: "https:///20mb.bin", want: "udp_under_load: url must be an absolute http or https URL with a host"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, validBody+blockYAML("udp_under_load", "url: "+tc.url))

			err := loadError(t, path)
			if !strings.Contains(err, tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// TestUDPUnderLoadURLErrorDoesNotEchoURL keeps the under-load URL out of logs,
// as the source URL's errors do: the message names the field, never the value.
func TestUDPUnderLoadURLErrorDoesNotEchoURL(t *testing.T) {
	urls := []string{
		"ftp://under-load-secret.example/20mb.bin",
		"https://under-load-secret.example/20mb.bin%zz",
	}

	for _, raw := range urls {
		t.Run(raw, func(t *testing.T) {
			path := writeConfigFile(t, validBody+blockYAML("udp_under_load", "url: "+raw))

			err := loadError(t, path)
			if !strings.Contains(err, "udp_under_load: url") {
				t.Errorf("error %q does not name the url field", err)
			}
			if strings.Contains(err, "under-load-secret") {
				t.Errorf("error %q echoes the under-load URL", err)
			}
		})
	}
}

func TestLoadTCPTargets(t *testing.T) {
	path := writeConfigFile(t, validBody+blockYAML("tcp_targets", `- name: mtproto-dc4
  addr: "149.154.167.51:443"`))

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.TCPTargets) != 1 {
		t.Fatalf("len(TCPTargets) = %d, want 1", len(cfg.TCPTargets))
	}
	if target := cfg.TCPTargets[0]; target.Name != "mtproto-dc4" || target.Addr != "149.154.167.51:443" {
		t.Errorf("target = %+v", target)
	}
}

func TestLoadTCPTargetsRejected(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "missing name",
			body: `- addr: "149.154.167.51:443"`,
			want: "tcp_targets[0]: name is required",
		},
		{
			name: "duplicate name",
			body: `- name: mtproto-dc4
  addr: "149.154.167.51:443"
- name: mtproto-dc4
  addr: "149.154.167.52:443"`,
			want: `tcp_targets[1]: duplicate name "mtproto-dc4"`,
		},
		{
			name: "bad addr",
			body: `- name: mtproto-dc4
  addr: "149.154.167.51"`,
			want: `tcp_targets[0]: addr "149.154.167.51" must be host:port`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, validBody+blockYAML("tcp_targets", tc.body))

			err := loadError(t, path)
			if !strings.Contains(err, tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// TestUDPAndTCPBlocksAbsent pins the optionality: without the blocks nothing
// is measured, like the reachability block.
func TestUDPAndTCPBlocksAbsent(t *testing.T) {
	cfg, err := config.Load(writeConfigFile(t, validBody))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.UDPTargets) != 0 || len(cfg.TCPTargets) != 0 || cfg.UDPUnderLoad != nil {
		t.Errorf("UDPTargets = %+v, TCPTargets = %+v, UDPUnderLoad = %+v, want all empty", cfg.UDPTargets, cfg.TCPTargets, cfg.UDPUnderLoad)
	}
}
