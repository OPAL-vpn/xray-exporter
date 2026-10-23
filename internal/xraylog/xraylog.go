// Package xraylog routes xray-core's log stream into slog.
//
// xray-core has no per-instance logging: app/log.New registers one handler for
// the whole process, and every instance start replaces it. The exporter
// therefore owns the global registration: Install once the instances are
// running, and Capture around a start so that output produced while starting
// can be attributed to the config that produced it.
package xraylog

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	clog "github.com/xtls/xray-core/common/log"
)

// none is below every severity xray emits, so installing it drops the whole
// stream. Severity_Unknown cannot mean that: Install reads it as "no minimum".
const none clog.Severity = -1

// ParseSeverity maps an XRAY_LOG_LEVEL spelling onto xray's severity ladder.
// "none" yields a minimum below every severity, so nothing passes it. Case and
// surrounding space are tolerated, as in xray's own parser.
func ParseSeverity(s string) (clog.Severity, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return clog.Severity_Debug, nil
	case "info":
		return clog.Severity_Info, nil
	case "warning":
		return clog.Severity_Warning, nil
	case "error":
		return clog.Severity_Error, nil
	case "none":
		return none, nil
	}
	return clog.Severity_Unknown, fmt.Errorf("unknown xray log level %q, want debug, info, warning, error or none", s)
}

var (
	// handlerMu serializes Install and Capture, which both swap the global
	// handler and the record of what they installed.
	handlerMu sync.Mutex
	// current is the handler this package installed last. xray offers no way to
	// read back the global handler, and RegisterHandler discards it, so Capture
	// can only restore a handler remembered here.
	current clog.Handler = discard{}
)

// discard is the handler in place before the first Install: it drops messages,
// which is what xray's own nil handler does.
type discard struct{}

func (discard) Handle(clog.Message) {}

// Install makes logger the destination of xray's process-global log stream,
// with subsystem=xray on every line, and drops messages less severe than min.
// A min of Severity_Unknown means no minimum. logger must not be nil.
//
// Every xray instance start re-registers its own handler, so Install belongs
// after the instances are running.
func Install(logger *slog.Logger, min clog.Severity) {
	swap(&bridge{logger: logger.With("subsystem", "xray"), min: min})
}

// Capture replaces xray's global handler with one that buffers messages until
// the returned release is called. release restores the handler in place before
// the capture and writes the buffered lines to logger with subsystem=xray and
// the given config, so a start's output is attributed to the config being
// started. The capture itself does not filter severities; the release logger's
// handler decides what it keeps.
//
// release must be called once, after the start it wraps, and must not overlap
// an Install: the manager sequences both.
func Capture() (release func(config string, logger *slog.Logger)) {
	captured := &capture{}
	previous := swap(captured)
	return func(config string, logger *slog.Logger) {
		swap(previous)
		target := logger.With("subsystem", "xray", "config", config)
		for _, msg := range captured.drain() {
			_, level := describe(msg)
			write(target, level, msg)
		}
	}
}

// swap installs h as the process's xray handler and returns the handler it
// replaced.
func swap(h clog.Handler) clog.Handler {
	handlerMu.Lock()
	defer handlerMu.Unlock()

	previous := current
	current = h
	clog.RegisterHandler(h)
	return previous
}

// bridge is what Install registers.
type bridge struct {
	logger *slog.Logger
	min    clog.Severity
}

func (b *bridge) Handle(msg clog.Message) {
	severity, level := describe(msg)
	if !b.accepts(severity) {
		return
	}
	write(b.logger, level, msg)
}

// accepts follows xray's ladder: severities count down from Debug, so a message
// passes when it is no more verbose than min.
func (b *bridge) accepts(severity clog.Severity) bool {
	if b.min == clog.Severity_Unknown {
		return true
	}
	return severity <= b.min
}

// capture buffers messages until release drains them.
type capture struct {
	mu       sync.Mutex
	messages []clog.Message
}

func (c *capture) Handle(msg clog.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = append(c.messages, msg)
}

func (c *capture) drain() []clog.Message {
	c.mu.Lock()
	defer c.mu.Unlock()

	messages := c.messages
	c.messages = nil
	return messages
}

// write logs one xray message at level.
func write(logger *slog.Logger, level slog.Level, msg clog.Message) {
	logger.Log(context.Background(), level, msg.String())
}

// describe returns the xray severity and the slog level for msg. Access and DNS
// messages carry no severity of their own, so they count as informational.
func describe(msg clog.Message) (clog.Severity, slog.Level) {
	general, ok := msg.(*clog.GeneralMessage)
	if !ok {
		return clog.Severity_Info, slog.LevelInfo
	}
	return general.Severity, levelOf(general.Severity)
}

func levelOf(severity clog.Severity) slog.Level {
	switch severity {
	case clog.Severity_Error:
		return slog.LevelError
	case clog.Severity_Warning:
		return slog.LevelWarn
	case clog.Severity_Debug:
		return slog.LevelDebug
	}
	return slog.LevelInfo
}
