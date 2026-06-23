package main

import (
	"fmt"
	"os"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/otel"
	"github.com/phillipgreenii/pa-monitor/internal/tui"
)

// bridgeLogger fans bridge output to the right sink: Term() is the
// operator-facing pane line (timestamped, prefix-less, stderr); Detail() is
// low-level diagnostics that must never reach the pane — they go to a local
// log file (always) and OTel logs (when configured).
type bridgeLogger struct {
	now  func() time.Time
	file *tui.ErrorLogger
	emit *otel.ConnEmitter
	out  *os.File // stderr; injectable for tests
}

func newBridgeLogger(cacheDir string, emit *otel.ConnEmitter) *bridgeLogger {
	return &bridgeLogger{
		now:  time.Now,
		file: &tui.ErrorLogger{CacheDir: cacheDir, FileName: "cmux-bridge.log"},
		emit: emit,
		out:  os.Stderr,
	}
}

func (l *bridgeLogger) Term(msg string) {
	fmt.Fprintln(l.out, formatBridgeLine(l.now(), msg))
}

func (l *bridgeLogger) Detail(event string, fields map[string]string) {
	line := event
	for k, v := range fields {
		line += fmt.Sprintf(" %s=%q", k, v)
	}
	l.file.LogString(line)
	l.emit.LogEvent(event, fields)
}

// connAnnouncer turns daemon connect/disconnect events into idempotent
// terminal lines + a connection gauge + low-level detail. Dependencies are
// plain funcs so it is unit-testable without real I/O or an emitter.
type connAnnouncer struct {
	announcedLost bool
	term          func(string)
	detail        func(event string, fields map[string]string)
	gauge         func(connected bool)
}

func (c *connAnnouncer) disconnected(fields map[string]string) {
	if !c.announcedLost {
		c.term("Lost connection to daemon")
		c.announcedLost = true
		c.gauge(false)
	}
	c.detail("daemon.disconnect", fields)
}

func (c *connAnnouncer) connected() {
	if c.announcedLost {
		c.term("Connection to daemon restored")
		c.announcedLost = false
	}
	c.gauge(true)
}
