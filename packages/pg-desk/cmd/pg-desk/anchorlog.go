package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/telemetry"
)

// Persistent anchor-write log (bead pg2-kwwn2).
//
// `pg-desk run pr` / `run issue` run as pg-router command roles, whose
// stderr the router discards on success, and `run` (unlike `serve`) has no
// OTLP exporter. The per-write "pg-desk sync: anchor write ... cause=..."
// lines (bead pg2-n6d8y) therefore had no persistent home. `run` now also
// appends them to a bounded file next to the store.
const (
	// anchorWriteLogName is the file, in the same directory as store.db
	// ($XDG_STATE_HOME/pg-desk, default ~/.local/state/pg-desk).
	anchorWriteLogName = "anchor-write.log"

	// anchorWriteLogMaxBytes bounds the live file: an open that finds it at
	// or above this size first rotates it to anchorWriteLogName+".1"
	// (replacing any previous one), so at most two files exist and the total
	// is about 2*max plus whatever one run appends.
	anchorWriteLogMaxBytes int64 = 4 << 20
)

// anchorWriteLogPath returns the persistent anchor-write log path. It is a
// var so tests can redirect it.
var anchorWriteLogPath = func() string {
	return filepath.Join(filepath.Dir(store.DefaultPath()), anchorWriteLogName)
}

// lazyLogFile is an io.Writer that opens (creating the directory, rotating
// when oversized) the log file on the first Write, so a run that logs
// nothing creates nothing. A failed open drops the lines: the log is
// diagnostic and must never fail or slow a run.
type lazyLogFile struct {
	path     string
	maxBytes int64

	mu     sync.Mutex
	opened bool
	f      *os.File
}

func (l *lazyLogFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.opened {
		l.opened = true
		l.f = openBoundedAppend(l.path, l.maxBytes)
	}
	if l.f == nil {
		return len(p), nil
	}
	return l.f.Write(p)
}

// Close closes the file if it was ever opened.
func (l *lazyLogFile) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// openBoundedAppend opens path for appending, first rotating it to
// path+".1" if it already holds maxBytes or more. It returns nil on any
// failure.
func openBoundedAppend(path string, maxBytes int64) *os.File {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() >= maxBytes {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	return f
}

// newAnchorWriteLogger returns the logger `run` hands the sync stage: every
// record goes to stderr (the pre-existing destination, via the process
// default handler's text format) AND to the persistent file. The returned
// close func releases the file.
func newAnchorWriteLogger(stderr io.Writer, path string, maxBytes int64) (*slog.Logger, func() error) {
	lf := &lazyLogFile{path: path, maxBytes: maxBytes}
	h := telemetry.Fanout(slog.NewTextHandler(stderr, nil), slog.NewTextHandler(lf, nil))
	return slog.New(h), lf.Close
}
