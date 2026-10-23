// Package xraycfg loads xray client configs and rewrites only the log and
// inbounds sections; every other section keeps its value.
package xraycfg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNoConfigs means the directory was read and is empty. Callers must stop
// every instance rather than keep the previous set.
var ErrNoConfigs = errors.New("no *.json configs")

// Config is one client config; Name is the filename stem and the metric label.
type Config struct {
	Name string
	Raw  []byte
}

// Discover returns sorted *.json configs; a readable empty directory returns
// ErrNoConfigs.
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
