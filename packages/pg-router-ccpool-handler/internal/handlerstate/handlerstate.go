// Package handlerstate resolves the handler state directory and opens the
// handler's event log, shared by every adapter binary of this module (bead
// pg2-12gr4.10.3). Moved verbatim out of package main of
// cmd/pg-router-ccpool-handler; no behavior change.
package handlerstate

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/eventlog"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/originprobe"
)

// EventLogName is the JSONL file, inside the handler state directory,
// that the executor's and watchdog's structured events (hard_stop,
// needs_input, reminder, ...) are appended to (bead pg2-ui2gk).
const EventLogName = "events.jsonl"

// NewEventLog opens the handler's event log at
// <stateDir>/events.jsonl (stateDir resolved exactly as the origin prober
// resolves it: cfg.OriginProbe.StateDir, else the XDG/home default). It
// never returns nil: executor.Deps.Log and watchdog.Watchdog.Log treat nil as
// a silent no-op, which is the defect this fixes. If the file cannot be
// opened the handler still must dispatch, so it falls back to a writer on
// os.DevNull (events dropped, warning logged via slog) rather than nil.
func NewEventLog(cfg config.Config) *eventlog.Writer {
	w, err := eventlog.New(filepath.Join(Dir(cfg), EventLogName))
	if err != nil {
		slog.Warn("dispatch: event log unavailable; events dropped", "err", err)
		if w, err = eventlog.New(os.DevNull); err != nil {
			slog.Warn("dispatch: null event log unavailable", "err", err)
		}
	}
	return w
}

// Dir resolves the handler state directory exactly as the origin
// prober does: cfg.OriginProbe.StateDir, else $XDG_STATE_HOME/pg-router-
// ccpool-handler, else ~/.local/state/pg-router-ccpool-handler. It holds
// events.jsonl, the origin state, and the per-session locks.
func Dir(cfg config.Config) string {
	if sd := cfg.OriginProbe.StateDir; sd != "" {
		return sd
	}
	return originprobe.DefaultStateDir()
}
