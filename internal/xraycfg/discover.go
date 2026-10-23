// Package xraycfg loads xray client configs from a directory and rewrites the
// two top-level sections the exporter owns: log and inbounds. Every other
// section keeps its value so a supplied config's outbound, routing and policy
// are never altered.
package xraycfg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNoConfigs reports a config directory that was read successfully but holds
// no *.json file. It is not a read failure: the directory answered, so the empty
// set is authoritative and the caller must stop every instance instead of
// keeping the previous ones.
var ErrNoConfigs = errors.New("no *.json configs")

// Config is one xray client config found in the config directory. Name is the
// filename stem, which is the Prometheus label.
type Config struct {
	Name string
	Raw  []byte
}

// Discover returns every *.json regular file in dir, sorted by name stem. An
// unreadable directory fails with its own error; a readable directory with no
// configs fails with ErrNoConfigs, which the caller treats as an empty set.
func Discover(dir string) ([]Config, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}

	configs := make([]Config, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		configs = append(configs, Config{
			Name: strings.TrimSuffix(entry.Name(), ".json"),
			Raw:  raw,
		})
	}
	if len(configs) == 0 {
		return nil, fmt.Errorf("%s: %w", dir, ErrNoConfigs)
	}

	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })
	return configs, nil
}
