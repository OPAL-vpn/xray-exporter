package xraycfg

import (
	"context"
	"time"
)

// Source supplies the whole config set. Fetch returns a complete set or an
// error; ErrNoConfigs means the source is readable and deliberately empty.
type Source interface {
	Kind() string
	Refresh() time.Duration
	Fetch(ctx context.Context) ([]Config, error)
}

// Directory reads the configs from a local directory.
type Directory struct {
	Path string
}

func (Directory) Kind() string { return "directory" }

// Refresh returns 0: a directory never needs to be polled for changes.
func (Directory) Refresh() time.Duration { return 0 }

// Fetch reads every *.json file in Path; it ignores ctx because the read is
// local. It returns ErrNoConfigs for a readable empty directory.
func (d Directory) Fetch(context.Context) ([]Config, error) {
	return Discover(d.Path)
}
