package tui

import (
	"os"
	"path/filepath"
	"sync"
)

// DefaultErrorLogMaxBytes bounds the live log file (bead pg2-yqbht): a write
// that would push it past this size first rotates it to <name>.1 (replacing
// any previous .1), so at most two files exist per log and the total stays
// near 2*max. Plain-text TUI logs are written by interactive processes, not
// launchd agents, so no shared rotator covers them.
const DefaultErrorLogMaxBytes int64 = 4 << 20

// ErrorLogger writes append-mode lines to <CacheDir>/signal-errors.log,
// opening the file lazily on first use and size-capping it (see
// DefaultErrorLogMaxBytes). Safe for concurrent use. Used by both
// the TUI Model (via Model.signalLog) and the cmuxstatus.Reporter so both
// share one file. When CacheDir is empty, LogString silently drops the line.
type ErrorLogger struct {
	CacheDir string
	// FileName is the log file's basename; defaults to "signal-errors.log"
	// when empty (preserving existing callers).
	FileName string
	// MaxBytes caps the live file before it is rotated to <name>.1; zero
	// means DefaultErrorLogMaxBytes.
	MaxBytes int64

	mu   sync.Mutex
	file *os.File
	size int64
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
	name := e.FileName
	if name == "" {
		name = "signal-errors.log"
	}
	path := filepath.Join(e.CacheDir, name)
	limit := e.MaxBytes
	if limit <= 0 {
		limit = DefaultErrorLogMaxBytes
	}
	line := msg + "\n"
	if e.file == nil {
		if err := os.MkdirAll(e.CacheDir, 0o755); err != nil {
			return
		}
		if !e.open(path) {
			return
		}
	}
	if e.size > 0 && e.size+int64(len(line)) > limit {
		_ = e.file.Close()
		e.file = nil
		_ = os.Rename(path, path+".1")
		if !e.open(path) {
			return
		}
	}
	n, _ := e.file.WriteString(line)
	e.size += int64(n)
}

// open opens path for appending and records its current size. It reports
// false (leaving e.file nil) on failure.
func (e *ErrorLogger) open(path string) bool {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false
	}
	e.size = 0
	if fi, err := f.Stat(); err == nil {
		e.size = fi.Size()
	}
	e.file = f
	return true
}
