//go:build race

package xrayproc_test

// raceDetectorEnabled reports whether the binary was built with -race. xray-
// core's hysteria server reads udpSessionManager.closed without its lock in
// clean() (transport/internet/hysteria/conn.go:194) while run() writes it
// (:234), so the in-process hysteria server test cannot run under -race.
const raceDetectorEnabled = true
