package prchange

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// RunRecordContract identifies the shape and version of one run record line.
const RunRecordContract = "pg-pr-change.run-record/v1"

// RunRecordFileName is the file name of the run record. It differs on purpose
// from pg-desk's `run-record.log` (the legacy per-run record that `pg-desk run`
// writes and rotates, deleted with the legacy path): a registered log source
// whose path does not match the path written leaves the stream empty without
// any error, so the two must never be confused.
const RunRecordFileName = "pr-change-runs.jsonl"

// RunRecord is one line of the run record: what one run of the command did. It
// is for people and for the plan summary script; no alert reads it, so a dead
// log pipeline cannot silence an alert (the outcome alerts read the store).
// Members beyond the design's list are additive; none will be removed or
// change meaning.
type RunRecord struct {
	Contract string `json:"contract"`
	RunID    string `json:"run_id"`
	// At is the start of the run, FinishedAt its end (RFC 3339, UTC).
	At         string `json:"at"`
	FinishedAt string `json:"finished_at"`

	// Entity is the normalized PR id; EventID is the item id as the router gave
	// it (with its @<12 hex> suffix when it had one).
	Entity  string `json:"entity"`
	EventID string `json:"event_id"`
	// Seq is the delivery key (the change sequence, or the id suffix's value),
	// 0 when the run carried none; SeqSource says which.
	Seq       int64  `json:"seq"`
	SeqSource string `json:"seq_source"`
	Mode      string `json:"mode"`

	Hydrate HydrateRecord `json:"hydrate"`
	// ExitCode is the process exit code; ErrorClass and FailingStage are empty
	// for a run that succeeded.
	ExitCode     int    `json:"exit_code"`
	ErrorClass   string `json:"error_class,omitempty"`
	FailingStage string `json:"failing_stage,omitempty"`

	// Decide is absent when the decider never ran (the run stopped at the lock or
	// at hydrate).
	Decide *DecideRecord `json:"decide,omitempty"`
	// Adoption and Residual: the adoption outcome and candidate anchors of the
	// PR, and (apply only) the number of actions a read-only plan still lists
	// after the apply.
	Adoption *AdoptionRecord `json:"adoption,omitempty"`
	Residual *int            `json:"residual,omitempty"`
	// ResidualError is set when the read-only plan after an apply could not be
	// read, so the residual is unknown.
	ResidualError string `json:"residual_error,omitempty"`

	// Lane says whether the run was recorded through pg-desk's lane write path.
	Lane LaneRecord `json:"lane_record"`
}

// HydrateRecord is the hydrate stage of a run. Result is ok, failed or not_run
// (the run ended at the lock).
type HydrateRecord struct {
	Result   string `json:"result"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Class    string `json:"class,omitempty"`
	// Line is the refresh line pg-desk printed (for example "no change").
	Line string `json:"line,omitempty"`
}

// DecideRecord is the decide stage of a run.
type DecideRecord struct {
	ExitCode int `json:"exit_code"`
	// Rules is the per-rule outcome and skip reason.
	Rules []RuleOutcome `json:"rules"`
	// Actions are the planned actions (kind and operation) and ByKind the count
	// per kind. Both are the plan the decider computed before it acted.
	Actions []PlannedAction `json:"actions"`
	ByKind  map[string]int  `json:"by_kind"`
	// PlanError is set when the read-only plan the record is built from could not
	// be read; the lists above are then empty and no decisions are recorded.
	PlanError string `json:"plan_error,omitempty"`
}

// RuleOutcome is one rule's result of the run. Outcome is planned (plan mode,
// the rule has actions), skipped (the rule produced no action; Reason says why),
// or, in apply mode, failed, applied, deduped or skipped (every action of the
// rule was skipped on a dependency). Counts are the decider's apply counters.
type RuleOutcome struct {
	Rule    string         `json:"rule"`
	Outcome string         `json:"outcome"`
	Reason  string         `json:"reason,omitempty"`
	Counts  map[string]int `json:"counts,omitempty"`
	// Facts are the facts the rule reported: why it skipped, or (planned) the
	// facts of its first action. Absent when the rule reported none.
	Facts map[string]any `json:"facts,omitempty"`
}

// PlannedAction is one action of the plan.
type PlannedAction struct {
	Rule string `json:"rule"`
	Kind string `json:"kind"`
	Op   string `json:"op"`
}

// AdoptionRecord is the adoption outcome and the candidate anchors of the PR.
// Outcome is none (nothing to adopt), planned (plan mode), adopted, failed or
// unadoptable (an item matched but lacks what its key needs).
type AdoptionRecord struct {
	Outcome    string            `json:"outcome"`
	Candidates []AnchorCandidate `json:"candidates"`
}

// AnchorCandidate is one anchor the view links: its id and state, and whether
// it is already keyed or still to be adopted (and by what it matched).
type AnchorCandidate struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Keyed     bool   `json:"keyed"`
	MatchedBy string `json:"matched_by,omitempty"`
}

// LaneRecord is the outcome of the lane-record call.
type LaneRecord struct {
	// Result is recorded, failed or skipped (a run that ended at the lock holds no
	// lock to write the PR's state under, so it records nothing there).
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
	// DroppedDecisions is true when the write path refused the document with the
	// decisions in it (a rule or reason outside its closed list) and the run was
	// recorded without them.
	DroppedDecisions bool `json:"dropped_decisions,omitempty"`
}

// newRunID is a random 16-hex-digit id, unique enough to join a record to the
// log lines of one run.
func newRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}

// AppendRunRecord appends rec as ONE line to the file at path, in ONE write
// call under an exclusive lock on the file, so concurrent runs (a manual run,
// the paced backfill, live dispatches, in other processes) cannot interleave
// lines. The directory is created when needed.
func AppendRunRecord(path string, rec RunRecord) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode run record: %w", err)
	}
	line = append(line, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create run record directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open run record %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock run record %s: %w", path, err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	n, err := f.Write(line)
	if err != nil {
		return fmt.Errorf("append run record %s: %w", path, err)
	}
	if n != len(line) {
		return fmt.Errorf("append run record %s: short write (%d of %d bytes)", path, n, len(line))
	}
	return nil
}
