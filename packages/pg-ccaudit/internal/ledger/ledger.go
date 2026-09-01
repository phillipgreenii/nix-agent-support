// Package ledger persists classifier spend BY RUN, written as the run
// progresses rather than only once it finishes (bead pg2-ohvpk).
//
// # The problem this closes
//
// `pg-ccaudit report --classifier cli` reports its cost line on stderr only
// after the whole classification pass returns. A run killed — timed out,
// SIGTERM'd, or the terminal closed — before that point left no record
// anywhere of what it had already spent: real paid model calls with no
// trace. This package is the fix: a caller appends a snapshot of the
// cumulative cost so far after every batch, so the ledger always holds an
// answer to "what did that run spend" even for a run that never finished.
//
// # Why entries are cumulative snapshots, not deltas
//
// Each Entry already carries the RUN'S TOTAL cost as of when it was
// written, not just that batch's increment. That means the caller never
// needs to read the ledger back to know what to write next — it always has
// the authoritative running total in memory (classify.Cost accumulates the
// same way) — and a reader only ever needs the LAST entry for a RunID, never
// a sum across entries for that run.
package ledger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnvPath overrides the ledger location.
const EnvPath = "PG_CCAUDIT_COST_LEDGER"

// Entry is one snapshot of one run's cumulative classifier spend.
type Entry struct {
	RunID      string `json:"run_id"`
	Command    string `json:"command"` // "classify" | "report"
	Classifier string `json:"classifier"`
	// Since and Until are the CENSUS window the run classified, not this
	// entry's own timestamp — carried through so `pg-ccaudit cost` can show
	// which corpus window a run's spend paid to classify.
	Since               string    `json:"since,omitempty"`
	Until               string    `json:"until,omitempty"`
	StartedAt           time.Time `json:"started_at"`
	UpdatedAt           time.Time `json:"updated_at"`
	CandidatesIn        int       `json:"candidates_in"`
	Calls               int       `json:"calls"`
	Batches             int       `json:"batches"`
	USD                 float64   `json:"usd"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	CacheReadTokens     int64     `json:"cache_read_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	// Done is true once the run reached its own natural end (successfully or
	// with a reported classifier error) rather than being killed mid-pass.
	// A run's LAST ledger entry with Done=false is the signature of a run
	// that never got to finish — killed, not merely unlucky.
	Done bool `json:"done"`
}

// DefaultPath resolves the ledger location, beside the index by default —
// the same convention gold.DefaultPath and cache.DefaultPath use.
func DefaultPath() (string, error) {
	if p := os.Getenv(EnvPath); p != "" {
		return p, nil
	}
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "pg-ccaudit", "cost-ledger.jsonl"), nil
}

// Append writes one snapshot durably: opened, written and closed within this
// call, so a nil return means the entry has already reached the OS and
// survives the process dying immediately afterward.
func Append(path string, e Entry) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create cost ledger directory %s: %w", dir, err)
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open cost ledger %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode ledger entry for run %s: %w", e.RunID, err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write cost ledger %s: %w", path, err)
	}
	return f.Sync()
}

// Load reads every snapshot ever appended. As with cache.Load, a malformed
// LAST line is treated as an interrupted write-in-progress and dropped
// rather than failing the whole load; a malformed line anywhere else is a
// genuine corruption and fails loudly.
//
// The caller MUST check os.IsNotExist on the returned error: no ledger yet
// is the normal state before any CLI classification pass has ever run.
func Load(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open cost ledger %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read cost ledger %s: %w", path, err)
	}
	var out []Entry
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			if i == len(lines)-1 {
				break
			}
			return nil, fmt.Errorf("cost ledger %s line %d: %w", path, i+1, err)
		}
		out = append(out, e)
	}
	return out, nil
}

// Latest collapses the append log to one entry per RunID — the most recently
// written snapshot for each — in the order each run FIRST appears, so two
// loads of the same ledger produce the same order.
func Latest(entries []Entry) []Entry {
	byRun := map[string]Entry{}
	var order []string
	for _, e := range entries {
		if _, ok := byRun[e.RunID]; !ok {
			order = append(order, e.RunID)
		}
		byRun[e.RunID] = e
	}
	out := make([]Entry, 0, len(order))
	for _, id := range order {
		out = append(out, byRun[id])
	}
	return out
}

// AverageCostPerCall reports the measured $-per-call average for a
// classifier, across every run ever recorded for it (collapsed to each run's
// latest snapshot first, so a run with several appended snapshots is not
// counted several times). This is what seeds `classify status`'s projected
// cost: the bead's own problem statement is that this arithmetic "cannot be
// seeded while spend is unmeasurable" — this ledger is the measurement.
//
// ok is false when no call has ever been recorded for this classifier, so a
// caller can report "unknown" rather than a fabricated $0.00.
func AverageCostPerCall(entries []Entry, classifier string) (avg float64, calls int, ok bool) {
	var usd float64
	for _, e := range Latest(entries) {
		if e.Classifier != classifier {
			continue
		}
		usd += e.USD
		calls += e.Calls
	}
	if calls == 0 {
		return 0, 0, false
	}
	return usd / float64(calls), calls, true
}
