package xraycfg_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/OPAL-vpn/xray-exporter/internal/xraycfg"
)

func TestDirectoryFetchMatchesDiscover(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "b.json", "{\"outbounds\":[{\"protocol\":\"b\"}]}")
	writeFile(t, dir, "a.json", "{\"outbounds\":[{\"protocol\":\"a\"}]}")
	writeFile(t, dir, "notes.txt", "not a config")

	want, err := xraycfg.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got, err := xraycfg.Directory{Path: dir}.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(configs) = %d, want 2", len(got))
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Fetch() = %+v, want Discover() = %+v", got, want)
	}
	if got[0].Name != "a" || got[1].Name != "b" {
		t.Errorf("names = %q, %q, want sorted a, b", got[0].Name, got[1].Name)
	}
	if string(got[0].Raw) != "{\"outbounds\":[{\"protocol\":\"a\"}]}" {
		t.Errorf("Raw = %q, want the raw file bytes", got[0].Raw)
	}
}

func TestDirectoryFetchEmptyIsErrNoConfigs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "notes.txt", "not a config")

	configs, err := xraycfg.Directory{Path: dir}.Fetch(context.Background())
	if err == nil {
		t.Fatalf("Fetch: want error, got configs %+v", configs)
	}
	if !errors.Is(err, xraycfg.ErrNoConfigs) {
		t.Errorf("error = %v, want ErrNoConfigs", err)
	}
}

func TestDirectoryFetchMissingDirErrors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")

	configs, err := xraycfg.Directory{Path: dir}.Fetch(context.Background())
	if err == nil {
		t.Fatalf("Fetch: want error, got configs %+v", configs)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error = %q, want it to contain the directory path %q", err, dir)
	}
	if errors.Is(err, xraycfg.ErrNoConfigs) {
		t.Errorf("error = %v, want a read error, not ErrNoConfigs", err)
	}
}

func TestDirectoryKindAndRefresh(t *testing.T) {
	src := xraycfg.Directory{Path: t.TempDir()}

	if got := src.Kind(); got != "directory" {
		t.Errorf("Kind() = %q, want %q", got, "directory")
	}
	if got := src.Refresh(); got != 0 {
		t.Errorf("Refresh() = %v, want 0", got)
	}
}
