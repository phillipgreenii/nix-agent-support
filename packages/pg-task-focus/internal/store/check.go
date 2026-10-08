package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

// CheckReport is what an offline check found in a log (INV-LOG-30).
type CheckReport struct {
	// Path is the log file that was read.
	Path string
	// Size is its length in bytes.
	Size int64
	// Lines is the number of committed lines: the count a service would start
	// from after recovery. Batches is the number of committed batches.
	Lines, Batches int
	// Recovery is what the next start would do to the end of the log; its
	// Sidecar is empty.
	Recovery Recovery
	// Events are the committed events, so a caller can replay them.
	Events []event.Event
	// Problem is why a service would refuse to start: a *CorruptError, with
	// the 1-based line, or an *UnknownVersionError. It is nil for a log that
	// starts, recovery or not. When it is set Lines, Batches, Recovery and
	// Events are zero.
	Problem error
}

// Check reads a log without taking the directory lock and without changing
// anything, so it works while a service has the log open (INV-LOG-20,
// INV-LOG-30). path is the data directory or the log file itself. A problem
// with the log's content is reported in the CheckReport; the error is for a
// log that cannot be read at all.
func Check(path string) (CheckReport, error) {
	if info, err := os.Stat(path); err != nil {
		return CheckReport{}, fmt.Errorf("checking the event log: %w", err)
	} else if info.IsDir() {
		path = filepath.Join(path, logName)
	}
	f, err := os.Open(path)
	if err != nil {
		return CheckReport{}, fmt.Errorf("checking the event log: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return CheckReport{}, fmt.Errorf("checking the event log: %w", err)
	}

	events, end, rep, err := scan(f)
	if err != nil {
		var corrupt *CorruptError
		var version *UnknownVersionError
		if errors.As(err, &corrupt) || errors.As(err, &version) {
			return CheckReport{Path: path, Size: info.Size(), Problem: err}, nil
		}
		return CheckReport{}, fmt.Errorf("checking the event log: %w", err)
	}
	return CheckReport{
		Path:     path,
		Size:     info.Size(),
		Lines:    len(events),
		Batches:  rep.Batches,
		Recovery: wouldRecover(rep, end),
		Events:   events,
	}, nil
}
