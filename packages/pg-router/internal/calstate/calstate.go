// Package calstate persists the per-query slot state of calendar-triggered timer
// queries (file calendar-state.json under the router's log directory) and holds
// the host-sleep sampler the missed-slot rule reads.
//
// The state survives a restart so each slot yields at most one tick: per query
// it remembers the last handled slot, the outcome and the reason. The
// atomic-write code follows internal/metrics/sourcestate.go (copied, not
// refactored).
package calstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// FileName is the state file, under the router's log directory.
const FileName = "calendar-state.json"

// Outcome literals stored in a Record.
const (
	OutcomeFired   = "fired"
	OutcomeCatchUp = "catch_up"
	OutcomeSkipped = "skipped"
)

// Reason strings stored in a Record. An on-time fire more than one poll interval
// after its slot stores "late by <d>" (time.Duration.String form), for
// visibility only; an on-time fire within one poll interval stores no reason.
const (
	ReasonRouterNotRunning = "router not running"
	ReasonHostAsleep       = "host asleep"
	ReasonEnqueueFailed    = "enqueue failed"
	ReasonFirstStart       = "first start"
)

// Record is one query's last handled slot.
type Record struct {
	LastSlot  time.Time `json:"last_slot"`
	Outcome   string    `json:"outcome"`
	Reason    string    `json:"reason,omitempty"`
	DecidedAt time.Time `json:"decided_at"`
}

// file is the on-disk shape: a versioned wrapper around the per-query map.
type file struct {
	Version int               `json:"version"`
	Queries map[string]Record `json:"queries"`
}

const fileVersion = 1

// Read returns the per-query records stored at path. A missing file is no state
// (nil map, nil error). A truncated or unparsable file returns a nil map and an
// error: callers treat it as no state and log the error as a warning. This is
// the read API the status, config check and doctor output use; they never parse
// the JSON themselves.
func Read(path string) (map[string]Record, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read calendar state %s: %w", path, err)
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse calendar state %s: %w", path, err)
	}
	if f.Queries == nil {
		f.Queries = map[string]Record{}
	}
	return f.Queries, nil
}

// Write stores recs atomically at path (temp file in the same directory, then
// rename), creating the directory when needed.
func Write(path string, recs map[string]Record) error {
	b, err := json.MarshalIndent(file{Version: fileVersion, Queries: recs}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".calendar-state-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		if werr != nil {
			return werr
		}
		return cerr
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Prune removes every entry whose query name is not in configured and returns
// the removed names, sorted (the caller logs them).
func Prune(recs map[string]Record, configured map[string]bool) []string {
	var removed []string
	for name := range recs {
		if !configured[name] {
			removed = append(removed, name)
			delete(recs, name)
		}
	}
	sort.Strings(removed)
	return removed
}
