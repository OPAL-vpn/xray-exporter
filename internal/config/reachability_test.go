package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const baseConfig = `dns_probe_domain: example.com
dns_upstream: 1.1.1.1:53
targets:
  - host: ya.ru
`

func load(t *testing.T, extra string) (*ExporterConfig, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "xray-exporter-config.yml")
	if err := os.WriteFile(path, []byte(baseConfig+extra), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestReachabilityOptional(t *testing.T) {
	cfg, err := load(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Reachability != nil {
		t.Error("Reachability set without the block")
	}
}

func TestReachabilityEmptyBlockEnables(t *testing.T) {
	cfg, err := load(t, "reachability: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Reachability == nil {
		t.Error("an empty block must still enable the lookup")
	}
}

func TestReachabilityValid(t *testing.T) {
	cfg, err := load(t, `reachability:
  controls:
    https:
      - { name: ya, host: ya.ru, class: domestic }
      - { name: alt, host: ya.ru:8443/health, class: domestic }
    tcp:
      - { name: cf, addr: "1.1.1.1:443", class: foreign }
    dns:
      - { name: google, host: google.com, class: foreign }
  entry_tcp: true
`)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Reachability.EntryTCP || len(cfg.Reachability.Controls.All()) != 4 {
		t.Errorf("entry_tcp = %v, controls = %d", cfg.Reachability.EntryTCP, len(cfg.Reachability.Controls.All()))
	}
	if got := cfg.Reachability.Controls.HTTPS[1].Host; got != "ya.ru:8443/health" {
		t.Errorf("an https control dropped its port: host = %q", got)
	}
}

func TestReachabilityRejected(t *testing.T) {
	ctl := func(body string) string { return "reachability:\n  controls:\n" + body }
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"unknown key", "reachability:\n  region: ru-moscow\n", "region"},
		{"bad class", ctl("    tcp:\n      - { name: a, addr: \"1.1.1.1:443\", class: abroad }\n"), "class must be"},
		{"bad addr", ctl("    tcp:\n      - { name: a, addr: \"1.1.1.1\", class: foreign }\n"), "host:port"},
		{"scheme in host", ctl("    https:\n      - { name: a, host: \"https://ya.ru\", class: domestic }\n"), "scheme"},
		{"dns address", ctl("    dns:\n      - { name: a, host: 1.1.1.1, class: foreign }\n"), "not an address"},
		{"duplicate", ctl("    tcp:\n      - { name: a, addr: \"1.1.1.1:443\", class: foreign }\n      - { name: a, addr: \"8.8.8.8:443\", class: foreign }\n"), "duplicate"},
		{"bad host", ctl("    https:\n      - { name: a, host: \"\", class: domestic }\n"), "host is required"},
		{"bad name", ctl("    tcp:\n      - { name: A b, addr: \"1.1.1.1:443\", class: foreign }\n"), "name must be"},
		{"https bad port", ctl("    https:\n      - { name: a, host: \"ya.ru:99999\", class: domestic }\n"), "invalid port"},
		{"dns port", ctl("    dns:\n      - { name: a, host: \"google.com:53\", class: foreign }\n"), "a dns control takes no port"},
		{"tcp zero port", ctl("    tcp:\n      - { name: a, addr: \"1.1.1.1:0\", class: foreign }\n"), "host:port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, tt.yaml)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
