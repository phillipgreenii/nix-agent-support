package main

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Persistent per-run record (bead pg2-dpml1).
//
// pg-router discards the stderr of a successful command-role run, and the
// router's own success events carry no change field, so how many sweep runs
// were true no-ops, and how many caught a change the changes feed had not
// reported, could not be measured. `run` therefore also appends one JSON line
// per run (the pipeline's run record, see pipeline.WriteRunRecord) to a
// bounded file next to the store.
const (
	// runRecordLogName is the file, in the same directory as store.db.
	runRecordLogName = "run-record.log"

	// runRecordLogMaxBytes bounds the live file the way a log file
	// is bounded: an open that finds it at or above this size rotates it to
	// runRecordLogName+".1" first. A run record is about 250 bytes, so this
	// holds well over a day of records at the current event rate.
	runRecordLogMaxBytes int64 = 8 << 20
)

// runRecordLogPath returns the persistent run-record path. It is a var so
// tests can redirect it.
var runRecordLogPath = func() string {
	return filepath.Join(filepath.Dir(store.DefaultPath()), runRecordLogName)
}

// newRunRecordFile returns the lazily opened record file: a run that writes
// no record creates nothing, and a failed open drops the records rather than
// failing the run.
func newRunRecordFile() *lazyLogFile {
	return &lazyLogFile{path: runRecordLogPath(), maxBytes: runRecordLogMaxBytes}
}

// recordEarlyFailure writes the run record for a failure that happened before
// any pipeline ran (args, config, store open, bead resolution) or that no
// pipeline stage logged (a `run issue` ticket-key path). A failure the
// pipeline already logged carries a pipeline stage and is not written again,
// so every invocation leaves exactly one record. `run thread` is outside the
// record's scope.
func recordEarlyFailure(w io.Writer, entityType, entityID string, change gather.ChangeKind, start time.Time, err error) {
	if entityType == "thread" {
		return
	}
	stage, _ := pipeline.StageOf(err)
	switch stage {
	case "", runStageArgs, runStageConfig, runStageStoreOpen, runStageResolveBead:
	default:
		return
	}
	pipeline.WriteRunRecord(w, time.Now(), time.Since(start), "", pipeline.PathEarly, entityType, entityID, change, "error", "", err)
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
