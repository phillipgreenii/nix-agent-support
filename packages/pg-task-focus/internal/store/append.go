package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// AppendStage names the step of an append that failed.
type AppendStage string

// The stages of a failed append.
const (
	// StageWrite: the write of the lines failed.
	StageWrite AppendStage = "write"
	// StageSync: the write succeeded and its fsync failed.
	StageSync AppendStage = "sync"
)

// AppendError is the failure of an Append: nothing was acknowledged, and the
// outcome is unknown to the caller until it retries (INV-LOG-21). Whether the
// store stayed writable is Health: a write whose rollback worked leaves it
// writable; every other failure makes it read-only.
type AppendError struct {
	Stage AppendStage
	// Err is the cause, joined with the cause of a failed rollback.
	Err error
}

// Error implements error.
func (e *AppendError) Error() string {
	return fmt.Sprintf("appending to the event log failed at the %s step: %v", e.Stage, e.Err)
}

// Unwrap returns the cause.
func (e *AppendError) Unwrap() error { return e.Err }

// AppendStats describes a successful Append.
type AppendStats struct {
	// Events is the number of events written, Bytes their size with their
	// newlines, Duration the time the write and the fsync took, and
	// SyncDuration the part of it the fsync took.
	Events       int
	Bytes        int64
	Duration     time.Duration
	SyncDuration time.Duration
}

// Append writes the events as newline-terminated lines with ONE Write, then
// makes them durable with ONE Sync, and returns only after that (INV-LOG-29).
// The caller makes the events a batch: Append does not add its commit marker.
//
// A failed Write is rolled back: the log is truncated to its size before the
// append and that is made durable (INV-LOG-21), so no partial bytes remain.
// If that works the store stays writable and the error is an *AppendError
// with StageWrite. If the rollback fails, or the Sync of the append failed
// (the file's state is then unknown), the store enters read-only mode, and
// every later Append returns ErrStoreUnavailable.
//
// An event that cannot be encoded is returned as an error before anything is
// written and does not affect the store.
func (s *Store) Append(evs []event.Event) (AppendStats, error) {
	if len(evs) == 0 {
		return AppendStats{}, nil
	}
	var buf []byte
	for _, e := range evs {
		line, err := event.Encode(e)
		if err != nil {
			return AppendStats{}, fmt.Errorf("encoding event %s: %w", e.ID, err)
		}
		buf = append(append(buf, line...), '\n')
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refuse(); err != nil {
		return AppendStats{}, err
	}

	start := time.Now()
	n, err := s.log.Write(buf)
	if err == nil && n < len(buf) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return AppendStats{}, s.failedWrite(err)
	}
	syncStart := time.Now()
	if err := s.log.Sync(); err != nil {
		return AppendStats{}, s.failedSync(err)
	}
	syncDone := time.Now()
	s.size.Add(int64(len(buf)))
	return AppendStats{
		Events: len(evs), Bytes: int64(len(buf)),
		Duration: syncDone.Sub(start), SyncDuration: syncDone.Sub(syncStart),
	}, nil
}

// refuse reports why no append can be attempted, or nil.
func (s *Store) refuse() error {
	s.state.Lock()
	defer s.state.Unlock()
	switch {
	case s.closed:
		return fmt.Errorf("%w: the store is closed", ErrStoreUnavailable)
	case s.health.ReadOnly:
		return ErrStoreUnavailable
	}
	return nil
}

// failedWrite rolls back a write that failed. The caller holds mu.
func (s *Store) failedWrite(werr error) error {
	if reason, rerr := s.rollback(); rerr != nil {
		s.MarkReadOnly(reason)
		werr = errors.Join(werr, rerr)
	}
	return &AppendError{Stage: StageWrite, Err: werr}
}

// failedSync handles an fsync that failed: the file's state is unknown, so
// the store goes read-only first, which keeps that as the first cause, and
// then tries to take the append back out of the file. The caller holds mu.
func (s *Store) failedSync(serr error) error {
	s.MarkReadOnly(ReasonAppendSync)
	if _, rerr := s.rollback(); rerr != nil {
		serr = errors.Join(serr, rerr)
	}
	return &AppendError{Stage: StageSync, Err: serr}
}

// rollback truncates the log to the size it had before the failed append and
// makes that durable. When it fails it returns the reason read-only mode
// names. The caller holds mu.
func (s *Store) rollback() (ReadOnlyReason, error) {
	if err := s.log.Truncate(s.size.Load()); err != nil {
		return ReasonWriteRollbackTruncate, fmt.Errorf("rolling back the append: truncating the log: %w", err)
	}
	if err := s.log.Sync(); err != nil {
		return ReasonWriteRollbackSync, fmt.Errorf("rolling back the append: syncing the log: %w", err)
	}
	return "", nil
}

// Probe checks that the data directory can still take a write: it creates and
// removes a temporary file in it and opens the log for append, without
// writing a byte. It backs the store_writable gauge. It does not consult the
// read-only state, and it is safe to call while an Append runs.
func (s *Store) Probe() error {
	f, name, err := s.fs.CreateTemp(s.dir, ".probe-*")
	if err != nil {
		return fmt.Errorf("probing the data directory: creating a temporary file: %w", err)
	}
	err = errors.Join(f.Close(), s.fs.Remove(name))
	if err != nil {
		return fmt.Errorf("probing the data directory: removing the temporary file: %w", err)
	}
	log, err := s.fs.OpenFile(s.path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("probing the data directory: opening the log for append: %w", err)
	}
	if err := log.Close(); err != nil {
		return fmt.Errorf("probing the data directory: closing the log: %w", err)
	}
	return nil
}
