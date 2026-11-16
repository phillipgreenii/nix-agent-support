package state

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"time"
)

// LeasePhaseBuckets are the upper bounds, in seconds, of the lease-phase
// histograms; the exporter renders them unchanged. The largest is 1800.
var LeasePhaseBuckets = []float64{0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600, 1800}

const countersWait = time.Second

// Histogram is a Prometheus-style histogram: Buckets[i] counts observations
// <= LeasePhaseBuckets[i] (cumulative); Count is the +Inf bucket.
type Histogram struct {
	Buckets []int64 `json:"buckets"`
	Sum     float64 `json:"sum"`
	Count   int64   `json:"count"`
}

// RepoCounters are one repository's counters.
type RepoCounters struct {
	Refusals    map[string]int64      `json:"refusals"`
	ReapRemoved int64                 `json:"reap_removed"`
	ReapKept    int64                 `json:"reap_kept"`
	LeasePhases map[string]*Histogram `json:"lease_phases"` // wait, create, switch
}

// Snapshot is the content of counters.json, keyed by repo label.
type Snapshot struct {
	SchemaVersion int                      `json:"schema_version"`
	Repos         map[string]*RepoCounters `json:"repos"`
}

// Counters is the one store of every counter and histogram the exporter
// serves ($XDG_STATE_HOME/pg-gwm/counters.json). It has its own flock and is
// best effort: a failure to count is swallowed and never changes a command's
// exit code.
type Counters struct{ Path string }

func (c Counters) read() (Snapshot, error) {
	b, err := os.ReadFile(c.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{SchemaVersion: SchemaVersion, Repos: map[string]*RepoCounters{}}, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return Snapshot{}, err
	}
	if s.Repos == nil {
		s.Repos = map[string]*RepoCounters{}
	}
	return s, nil
}

// Read returns the counters without the lock (writes are rename-atomic); a
// missing file reads as empty.
func (c Counters) Read() (Snapshot, error) { return c.read() }

func (c Counters) update(label string, fn func(*RepoCounters)) {
	unlock, err := flockFile(context.Background(), c.Path+".lock", countersWait)
	if err != nil {
		return
	}
	defer unlock()
	s, err := c.read()
	if err != nil {
		s = Snapshot{Repos: map[string]*RepoCounters{}}
	}
	rc := s.Repos[label]
	if rc == nil {
		rc = &RepoCounters{}
		s.Repos[label] = rc
	}
	if rc.Refusals == nil {
		rc.Refusals = map[string]int64{}
	}
	if rc.LeasePhases == nil {
		rc.LeasePhases = map[string]*Histogram{}
	}
	fn(rc)
	s.SchemaVersion = SchemaVersion
	b, err := json.MarshalIndent(&s, "", "  ")
	if err != nil {
		return
	}
	_ = writeAtomic(c.Path, append(b, '\n'), nil)
}

// Refusal counts one refusal (every error exit, including busy and internal)
// by error name. Call it outside the repo lock.
func (c Counters) Refusal(label, errorName string) {
	c.update(label, func(rc *RepoCounters) { rc.Refusals[errorName]++ })
}

// Reaped adds one reap's removed and kept worktree counts.
func (c Counters) Reaped(label string, removed, kept int) {
	c.update(label, func(rc *RepoCounters) {
		rc.ReapRemoved += int64(removed)
		rc.ReapKept += int64(kept)
	})
}

// LeasePhase records one observation of phase ("wait", "create" or "switch").
func (c Counters) LeasePhase(label, phase string, seconds float64) {
	c.update(label, func(rc *RepoCounters) {
		h := rc.LeasePhases[phase]
		if h == nil {
			h = &Histogram{}
			rc.LeasePhases[phase] = h
		}
		if len(h.Buckets) != len(LeasePhaseBuckets) {
			h.Buckets = make([]int64, len(LeasePhaseBuckets))
		}
		for i, ub := range LeasePhaseBuckets {
			if seconds <= ub {
				h.Buckets[i]++
			}
		}
		h.Sum += seconds
		h.Count++
	})
}

// RepoReap is one repository's entry in the reaper record.
type RepoReap struct {
	Label     string    `json:"label"`
	At        time.Time `json:"at"`
	Outcome   string    `json:"outcome"`
	Removed   int       `json:"removed"`
	Kept      int       `json:"kept"`
	Preserved int       `json:"preserved"`
	Broken    int       `json:"broken"`
	Created   int       `json:"created"`
}

// ReaperRecord is the one record of reap runs (reaper.json).
type ReaperRecord struct {
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended"`
	Outcome string    `json:"outcome"`
	// LastSuccess is the end of the last successful run. A write that leaves
	// it nil keeps the stored value, so later failed runs do not erase it.
	LastSuccess *time.Time          `json:"last_success"`
	Repos       map[string]RepoReap `json:"repos"`
}

// ReadReaperRecord reads path; a missing file reads as an empty record.
func ReadReaperRecord(path string) (ReaperRecord, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ReaperRecord{Repos: map[string]RepoReap{}}, nil
	}
	if err != nil {
		return ReaperRecord{}, err
	}
	var r ReaperRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return ReaperRecord{}, err
	}
	if r.Repos == nil {
		r.Repos = map[string]RepoReap{}
	}
	return r, nil
}

// WriteReaperRecord merges r into the record at path atomically: r's run
// fields replace the stored ones, its Repos entries replace the stored entries
// of the same repo label and never drop other repositories' entries, and a nil
// LastSuccess keeps the stored one.
func WriteReaperRecord(path string, r ReaperRecord) error {
	unlock, err := flockFile(context.Background(), path+".lock", userLockWait)
	if err != nil {
		return err
	}
	defer unlock()
	old, err := ReadReaperRecord(path)
	if err != nil {
		old = ReaperRecord{Repos: map[string]RepoReap{}}
	}
	for k, v := range r.Repos {
		old.Repos[k] = v
	}
	r.Repos = old.Repos
	if r.LastSuccess == nil {
		r.LastSuccess = old.LastSuccess
	}
	b, err := json.MarshalIndent(&r, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(b, '\n'), nil)
}
