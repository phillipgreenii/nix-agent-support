// Package tui implements pg-router's operator-facing terminal UI. This file
// (Task 4.8) carries ErrorLogger: a verbatim reproduction of pa-monitor's
// own (packages/pa-monitor/internal/tui/errorlog.go), retargeted at
// <LogDir>/tui-errors.log, where LogDir is Options.CacheDir (Task 4.5's
// own field, named "LogDir" in this packet's own Files text -- same
// value, this packet's own name for it).
package tui

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// DefaultErrorLogMaxBytes bounds the live log file (bead pg2-yqbht): a write
// that would push it past this size first rotates it to <name>.1 (replacing
// any previous .1), so at most two files exist and the total stays near
// 2*max. The TUI is an interactive process, not a launchd agent, so no
// shared rotator covers its log.
const DefaultErrorLogMaxBytes int64 = 4 << 20

// ErrorLogger writes append-mode lines to <CacheDir>/<FileName>, opening
// the file lazily on first use and size-capping it (see
// DefaultErrorLogMaxBytes). Safe for concurrent use. When CacheDir is
// empty, LogString silently drops the line.
type ErrorLogger struct {
	CacheDir string
	// FileName is the log file's basename; defaults to "signal-errors.log"
	// when empty -- pa-monitor's own default, preserved verbatim in this
	// reproduction. NewModel (model.go) always sets this explicitly to
	// "tui-errors.log", so the default branch is inherited, never actually
	// exercised, by this package's own production wiring.
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
// silently dropped -- best-effort logging. Safe to call on a nil
// *ErrorLogger (Options.CacheDir was empty): a no-op.
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

// errorLogPath returns the path an ErrorLogger{CacheDir: cacheDir,
// FileName: "tui-errors.log"} (NewModel's own construction) writes to --
// help.go's footer names this exact path.
func errorLogPath(cacheDir string) string {
	return filepath.Join(cacheDir, "tui-errors.log")
}

// tailErrorLogMaxReadBytes bounds how much of tui-errors.log tailErrorLog
// reads from the END of the file before splitting it into lines (bead
// pg2-5l2he's Problems modal), so an unbounded/huge log file can never make
// that modal itself slow or memory-hungry. Generous enough that any
// reasonable tail request comfortably fits inside it.
const tailErrorLogMaxReadBytes = 64 * 1024

// tailErrorLog returns up to n of the most recent non-empty lines from
// <cacheDir>/tui-errors.log (errorLogPath), oldest-first -- the same
// ordering convention reply.Activity already uses (model.go's
// advanceSinceCursor). An empty cacheDir, a log file that does not exist
// yet, or a read error all report nil: there is nothing to show, not a
// failure the caller (problems.go's renderProblemsModal) needs to render
// specially -- it falls back to its own "(no errors logged yet)" text.
func tailErrorLog(cacheDir string, n int) []string {
	if cacheDir == "" || n <= 0 {
		return nil
	}
	f, err := os.Open(errorLogPath(cacheDir))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil
	}
	size := info.Size()
	if size <= 0 {
		return nil
	}
	readSize := size
	if readSize > tailErrorLogMaxReadBytes {
		readSize = tailErrorLogMaxReadBytes
	}

	buf := make([]byte, readSize)
	if _, err := f.ReadAt(buf, size-readSize); err != nil && err != io.EOF {
		return nil
	}

	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if readSize < size && len(lines) > 0 {
		// The read started at an arbitrary mid-file offset, not the file's
		// own line boundary -- the first "line" is possibly a truncated
		// fragment of a real line, so it is dropped rather than shown as
		// if it were whole.
		lines = lines[1:]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
