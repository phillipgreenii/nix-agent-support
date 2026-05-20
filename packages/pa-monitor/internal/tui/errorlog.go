package tui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// ErrorLogger writes append-mode lines to <CacheDir>/signal-errors.log,
// opening the file lazily on first use. Safe for concurrent use. Used by both
// the TUI Model (via Model.signalLog) and the cmuxstatus.Reporter so both
// share one file. When CacheDir is empty, LogString silently drops the line.
type ErrorLogger struct {
	CacheDir string

	mu   sync.Mutex
	file io.WriteCloser
}

// LogString appends a single newline-terminated line to the log file. The
// caller does not provide a trailing newline. Errors opening the file are
// silently dropped — best-effort logging.
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
		f, err := os.OpenFile(filepath.Join(e.CacheDir, "signal-errors.log"),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		e.file = f
	}
	fmt.Fprintln(e.file, msg)
}
