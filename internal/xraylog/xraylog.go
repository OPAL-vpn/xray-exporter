// Package xraylog routes xray-core's process-global log stream into slog.
// xray registers one handler for the whole process and every instance start
// replaces it, so Install runs after starts and Capture wraps a single start.
package xraylog

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	clog "github.com/xtls/xray-core/common/log"
)

// none is below every severity; Severity_Unknown already means "no minimum".
const none clog.Severity = -1

// ParseSeverity maps an XRAY_LOG_LEVEL spelling onto xray's severity ladder.
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
	// handlerMu serializes the global handler swaps.
	handlerMu sync.Mutex
	// current is the handler installed last; xray cannot return the previous one.
	current clog.Handler = discard{}
)

type discard struct{}

func (discard) Handle(clog.Message) {}

// Install makes logger the destination of xray's log stream. Severity_Unknown
// means no minimum. Every instance start replaces the global handler, so
// Install must run after the instances are running.
func Install(logger *slog.Logger, min clog.Severity) {
	swap(&bridge{logger: logger.With("subsystem", "xray"), min: min})
}

// Capture buffers xray's global stream until release is called. release
// restores the previous handler and writes the buffered lines with the config
// that produced them. It must be called once and not overlap an Install.
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

func swap(h clog.Handler) clog.Handler {
	handlerMu.Lock()
	defer handlerMu.Unlock()

	previous := current
	current = h
	clog.RegisterHandler(h)
	return previous
}

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

// accepts follows xray's ladder: more verbose severities have lower values.
func (b *bridge) accepts(severity clog.Severity) bool {
	if b.min == clog.Severity_Unknown {
		return true
	}
	return severity <= b.min
}

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

func write(logger *slog.Logger, level slog.Level, msg clog.Message) {
	logger.Log(context.Background(), level, msg.String())
}

// describe returns the severity and slog level. Access and DNS messages carry
// no severity of their own and count as informational.
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
