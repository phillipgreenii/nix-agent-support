// Package schema is the mergeable on-disk row format of the collector. Every
// row of collector/ticks.jsonl carries a phase tag so the report generator can
// combine phases (phase A seeded, phase B default tiers) from several scratch
// directories.
package schema

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Version is the row schema id.
const Version = "pg-desk-shadow.tick/v1"

// Row kinds.
const (
	KindRunStart  = "run_start"
	KindTickStart = "tick_start"
	KindTick      = "tick"
	KindGap       = "gap"
	KindRunEnd    = "run_end"
)

// Tick statuses.
const (
	StatusOK            = "ok"
	StatusPartial       = "partial"
	StatusFailed        = "failed"
	StatusSkippedBudget = "skipped_budget"
	StatusRecovered     = "recovered"
)

// Gap reasons.
const (
	GapSleep    = "sleep"
	GapRestart  = "restart"
	GapOverrun  = "overrun"
	GapUnfinish = "unfinished-tick"
)

// Item is one change record the shadow reported, one per envelope record.
type Item struct {
	EntityID  string   `json:"entity_id"`
	Kinds     []string `json:"kinds"`
	Seq       int64    `json:"seq"`
	Origin    string   `json:"origin"`
	At        string   `json:"at"`
	Fields    []string `json:"fields,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

// Source is one consulted watched query of the envelope.
type Source struct {
	Query  string `json:"query"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Budget is the reading that decided a tick.
type Budget struct {
	Remaining int    `json:"remaining"`
	ResetAt   string `json:"reset_at,omitempty"`
	Source    string `json:"source,omitempty"`
	Floor     int    `json:"floor"`
	Known     bool   `json:"known"`
}

// Row is one JSON line of ticks.jsonl; Kind discriminates, unused fields are
// omitted.
type Row struct {
	Schema string `json:"schema"`
	Kind   string `json:"kind"`
	Phase  string `json:"phase"`
	// Slot is the aligned slot (UTC, RFC3339) the row belongs to.
	Slot string `json:"slot,omitempty"`
	// At stamps run_start, gap and run_end rows.
	At string `json:"at,omitempty"`

	TickStart  string `json:"tick_start,omitempty"`
	TickEnd    string `json:"tick_end,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	Status     string `json:"status,omitempty"`
	Warmup     bool   `json:"warmup,omitempty"`
	Recovered  bool   `json:"recovered,omitempty"`

	Items          []Item            `json:"items,omitempty"`
	Sources        []Source          `json:"sources,omitempty"`
	Hydrations     int64             `json:"hydrations"`
	GraphQLCostSum int               `json:"graphql_cost_sum"`
	Builds         map[string]string `json:"builds,omitempty"`
	SkippedBudget  bool              `json:"skipped_budget,omitempty"`
	Budget         *Budget           `json:"budget,omitempty"`
	CursorFrom     int64             `json:"cursor_from,omitempty"`
	CursorTo       int64             `json:"cursor_to,omitempty"`
	SandboxDenials int               `json:"sandbox_denials,omitempty"`
	ShimRejects    int               `json:"shim_rejects,omitempty"`
	Error          string            `json:"error,omitempty"`

	// Gap fields.
	Reason  string `json:"reason,omitempty"`
	GapFrom string `json:"gap_from,omitempty"`
	GapTo   string `json:"gap_to,omitempty"`

	// run_start / run_end fields.
	Resumed bool           `json:"resumed,omitempty"`
	Params  map[string]any `json:"params,omitempty"`
}

// ExitPtr is a helper for building rows.
func ExitPtr(c int) *int { return &c }

// Time parses a row timestamp.
func Time(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// Format renders a timestamp the way every row does (UTC, millisecond).
func Format(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// Append writes one row as one line to path (created 0600) and syncs it.
func Append(path string, r Row) error {
	r.Schema = Version
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// ReadAll reads every row of a ticks file; a missing file is an empty list and
// an unparseable line is an error naming its line number (a half-written final
// line, which only a crash can leave, is tolerated and skipped).
func ReadAll(path string) ([]Row, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rows []Row
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	n := 0
	lines := bytes.Count(data, []byte("\n"))
	for sc.Scan() {
		n++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r Row
		if err := json.Unmarshal(line, &r); err != nil {
			if n > lines { // unterminated final line
				continue
			}
			return nil, fmt.Errorf("%s line %d: %w", path, n, err)
		}
		rows = append(rows, r)
	}
	return rows, sc.Err()
}
