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
//
// Pane output is DEBOUNCED: a drop only reaches the pane ("Lost connection to
// daemon") once the daemon has been unreachable for at least announceAfter.
// Transient drops that recover within that window are recorded to the detail
// log and the gauge, but never printed — so the expected reconnect churn a busy
// workstation produces stays out of the operator-facing pane. The gauge and
// detail log always reflect every transition (they are the machine-readable
// signal); only the human pane line waits for a sustained outage.
type connAnnouncer struct {
	term   func(string)
	detail func(event string, fields map[string]string)
	gauge  func(connected bool)
	// now supplies the clock; nil defaults to time.Now (injected in tests).
	now func() time.Time
	// announceAfter is the sustained-outage threshold before the pane shows the
	// "Lost connection" line. Zero announces on the first drop (no debounce).
	announceAfter time.Duration

	announcedLost     bool
	disconnectedSince time.Time
}

func (c *connAnnouncer) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *connAnnouncer) disconnected(fields map[string]string) {
	now := c.clock()
	if c.disconnectedSince.IsZero() {
		c.disconnectedSince = now
		c.gauge(false)
	}
	c.detail("daemon.disconnect", fields)
	if !c.announcedLost && now.Sub(c.disconnectedSince) >= c.announceAfter {
		c.term("Lost connection to daemon")
		c.announcedLost = true
	}
}

func (c *connAnnouncer) connected() {
	// Only pair a "restored" line with a "Lost" line that actually reached the
	// pane; a debounced transient drop leaves nothing to restore.
	if c.announcedLost {
		c.term("Connection to daemon restored")
	}
	c.announcedLost = false
	c.disconnectedSince = time.Time{}
	c.gauge(true)
}
