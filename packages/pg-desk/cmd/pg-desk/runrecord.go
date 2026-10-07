package main

import (
	"io"
	"path/filepath"
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

	// runRecordLogMaxBytes bounds the live file the way anchorWriteLogMaxBytes
	// does: an open that finds it at or above this size rotates it to
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
