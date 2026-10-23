// Package tests holds the end-to-end checks that drive exported entry points
// against real xray-core instances linked into this binary. They are behind the
// integration build tag so the default `go test ./...` runs without the whole
// app cycle and its listeners:
//
//	go test -tags integration ./...
package tests
