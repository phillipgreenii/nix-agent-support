// Package tui implements pr-pool's operator-facing terminal UI. This file
// (Task 4.8) carries ErrorLogger: a verbatim reproduction of pa-monitor's
// own (packages/pa-monitor/internal/tui/errorlog.go), retargeted at
// <LogDir>/tui-errors.log, where LogDir is Options.CacheDir (Task 4.5's
// own field, named "LogDir" in this packet's own Files text -- same
// value, this packet's own name for it).
package tui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// ErrorLogger writes append-mode lines to <CacheDir>/<FileName>, opening
// the file lazily on first use. Safe for concurrent use. When CacheDir is
// empty, LogString silently drops the line.
type ErrorLogger struct {
	CacheDir string
	// FileName is the log file's basename; defaults to "signal-errors.log"
	// when empty -- pa-monitor's own default, preserved verbatim in this
	// reproduction. NewModel (model.go) always sets this explicitly to
	// "tui-errors.log", so the default branch is inherited, never actually
	// exercised, by this package's own production wiring.
	FileName string

	mu   sync.Mutex
	file io.WriteCloser
}

// LogString appends a single newline-terminated line to the log file. The
// caller does not provide a trailing newline. Errors opening the file are
// silently dropped -- best-effort logging. Safe to call on a nil
// *ErrorLogger (Options.CacheDir was empty): a no-op.
func (e *ErrorLogger) LogString(msg string) {
	if e == nil || e.CacheDir == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.file == nil {
		if err := os.MkdirAll(e.CacheDir, 0o755); err != nil {
			return
		}
		name := e.FileName
		if name == "" {
			name = "signal-errors.log"
		}
		f, err := os.OpenFile(filepath.Join(e.CacheDir, name),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		e.file = f
	}
	fmt.Fprintln(e.file, msg)
}

// errorLogPath returns the path an ErrorLogger{CacheDir: cacheDir,
// FileName: "tui-errors.log"} (NewModel's own construction) writes to --
// help.go's footer names this exact path.
func errorLogPath(cacheDir string) string {
	return filepath.Join(cacheDir, "tui-errors.log")
}
