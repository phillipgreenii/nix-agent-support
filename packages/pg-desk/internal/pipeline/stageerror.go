package pipeline

import (
	"context"
	"errors"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/sync"
)

// Stage names identify which step of a `pg-desk run` failed. They appear in
// the structured stderr log (`stage`) so a failure that pg-router records
// only as "exit status 1" can still be attributed to a stage (bead
// pg2-gp50o). The set is closed; the cmd layer adds its own pre-pipeline
// stages (config, store_open, resolve_bead) through TagStage.
const (
	StageGather            = "gather"
	StageKnownCheck        = "known_check"
	StageInterpret         = "interpret"
	StageStore             = "store"
	StageRecordSyncError   = "record_sync_error"
	StageSync              = "sync"
	StageSyncRetryState    = "sync_retry_state"
	StageLoadFacts         = "load_facts"
	StageInterpretOnlyArgs = "validate_change"
)

// Error classes are a coarse, stable vocabulary for WHY a stage failed, so an
// alert can be grouped without parsing free text. They are diagnostic only:
// no code branches on a class, and the exit-code contract is unchanged.
const (
	// ClassCanceled: the context was canceled (SIGTERM from launchd or the
	// router, or an upstream deadline propagating).
	ClassCanceled = "canceled"
	// ClassDeadline: a context deadline expired.
	ClassDeadline = "deadline"
	// ClassKilled: a pg-connector subprocess was killed by a signal
	// (exec reports exit code -1, text "signal: killed").
	ClassKilled = "killed"
	// ClassStoreBusy: the SQLite store was locked by another process.
	ClassStoreBusy = "store_busy"
	// ClassConnector: pg-connector answered with a failure wire code.
	ClassConnector = "connector"
	// ClassError: anything else.
	ClassError = "error"
)

// StageError tags an error with the pipeline stage that produced it and a
// coarse failure class. Error() returns the wrapped error's text unchanged,
// so every existing message (and every test matching on one) is untouched;
// the stage and class travel in the error chain and are read back with
// StageOf.
type StageError struct {
	Stage string
	Class string
	Err   error
}

func (e *StageError) Error() string { return e.Err.Error() }
func (e *StageError) Unwrap() error { return e.Err }

// TagStage wraps err as a *StageError for stage, classifying it with
// ClassifyError. A nil err stays nil; an err that already carries a stage
// keeps its innermost (first-assigned) stage and is returned as-is.
func TagStage(stage string, err error) error {
	if err == nil {
		return nil
	}
	var se *StageError
	if errors.As(err, &se) {
		return err
	}
	return &StageError{Stage: stage, Class: ClassifyError(err), Err: err}
}

// StageOf returns the stage and class recorded in err's chain, or empty
// strings when err carries none.
func StageOf(err error) (stage, class string) {
	var se *StageError
	if errors.As(err, &se) {
		return se.Stage, se.Class
	}
	return "", ""
}

// ClassifyError maps err onto the failure-class vocabulary above. Context
// errors are matched structurally; the killed and store-busy cases have no
// typed error in the chain (the gather layer flattens a killed child into
// "exit -1" text, and the SQLite driver reports a lock as text), so those two
// match the message.
func ClassifyError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.Canceled):
		return ClassCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return ClassDeadline
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "signal: killed"), strings.Contains(msg, ": exit -1:"):
		return ClassKilled
	case strings.Contains(msg, "database is locked"), strings.Contains(msg, "SQLITE_BUSY"):
		return ClassStoreBusy
	}
	var ce *sync.ConnectorError
	if errors.As(err, &ce) {
		return ClassConnector
	}
	return ClassError
}
