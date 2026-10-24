package backfill

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// The two modes of the composed command and of the driver.
const (
	ModePlan  = "plan"
	ModeApply = "apply"
)

// The contract strings of the lines of the run record file. The first is the
// composed command's own (prchange.RunRecordContract; a test in internal/prchange
// pins the equality). The other two are rows of a DISTINCT kind this driver
// appends to the same file: every reader of the run record (the plan summary, the
// by-timestamp listing, the log source) takes the latest RUN record per PR and mode
// and ignores them.
const (
	RunRecordContract  = "pg-pr-change.run-record/v1"
	FlipMarkerContract = "pg-pr-backfill.flip-marker/v1"
	PassStartContract  = "pg-pr-backfill.pass-start/v1"
)

// RunRecordFileName is the run record's file name (prchange.RunRecordFileName).
const RunRecordFileName = "pr-change-runs.jsonl"

// Record is the part of one run record line the driver reads. The composed
// command writes more members; they are additive and ignored here.
type Record struct {
	Contract   string `json:"contract"`
	RunID      string `json:"run_id"`
	At         string `json:"at"`
	FinishedAt string `json:"finished_at"`
	Entity     string `json:"entity"`
	EventID    string `json:"event_id"`
	Mode       string `json:"mode"`

	Hydrate struct {
		Result string `json:"result"`
	} `json:"hydrate"`
	ExitCode     int    `json:"exit_code"`
	ErrorClass   string `json:"error_class"`
	FailingStage string `json:"failing_stage"`

	Decide   *DecideRecord   `json:"decide"`
	Adoption *AdoptionRecord `json:"adoption"`
	Residual *int            `json:"residual"`
	// ResidualError is set when the residual of an apply run could not be read.
	ResidualError string `json:"residual_error"`

	// Time is At parsed; the zero time when At did not parse. Seq is the line's
	// position in the file: the order of events, which a second-resolution At
	// cannot give.
	Time time.Time `json:"-"`
	Seq  int       `json:"-"`
}

// DecideRecord is the decide stage of a run record.
type DecideRecord struct {
	ExitCode  int            `json:"exit_code"`
	Rules     []RuleOutcome  `json:"rules"`
	Actions   []Planned      `json:"actions"`
	ByKind    map[string]int `json:"by_kind"`
	PlanError string         `json:"plan_error"`
}

// RuleOutcome is one rule's result in a run.
type RuleOutcome struct {
	Rule    string         `json:"rule"`
	Outcome string         `json:"outcome"`
	Reason  string         `json:"reason"`
	Counts  map[string]int `json:"counts"`
}

// Planned is one action of the plan.
type Planned struct {
	Rule string `json:"rule"`
	Kind string `json:"kind"`
	Op   string `json:"op"`
}

// AdoptionRecord is the adoption outcome and the candidate anchors of the PR.
type AdoptionRecord struct {
	Outcome    string            `json:"outcome"`
	Candidates []AnchorCandidate `json:"candidates"`
}

// AnchorCandidate is one anchor the PR's view links.
type AnchorCandidate struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Keyed     bool   `json:"keyed"`
	MatchedBy string `json:"matched_by"`
}

// Marker is one row of a distinct kind the driver appends to the run record: a
// flip marker (Time is the flip's effective time, which MAY be earlier than At,
// the moment the row was written, so the runbook can write it just before the
// deployment apply that changes the role's mode) or the start of a new pass of
// one mode.
type Marker struct {
	Contract string `json:"contract"`
	Kind     string `json:"kind"`
	At       string `json:"at"`
	Time     string `json:"time,omitempty"`
	Mode     string `json:"mode"`
	Note     string `json:"note,omitempty"`

	// T is the effective time: Time when set, else At. Seq is the line's position.
	T   time.Time `json:"-"`
	Seq int       `json:"-"`
}

// Log is a read run record file.
type Log struct {
	Records []Record
	// FlipMarkers and PassStarts are in file order.
	FlipMarkers []Marker
	PassStarts  []Marker
	// Ignored counts lines that are neither a run record nor a marker (another
	// contract, or not JSON); a count, never an error, so a newer writer's rows
	// cannot break a reader.
	Ignored int
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// ReadLog reads the run record at path. A missing file is an empty log.
func ReadLog(path string) (*Log, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Log{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read run record: %w", err)
	}
	defer func() { _ = f.Close() }()
	lg := &Log{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	seq := 0
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		seq++
		var head struct {
			Contract string `json:"contract"`
		}
		if json.Unmarshal(line, &head) != nil {
			lg.Ignored++
			continue
		}
		switch head.Contract {
		case RunRecordContract:
			var r Record
			if json.Unmarshal(line, &r) != nil {
				lg.Ignored++
				continue
			}
			r.Time, r.Seq = parseTime(r.At), seq
			lg.Records = append(lg.Records, r)
		case FlipMarkerContract, PassStartContract:
			var m Marker
			if json.Unmarshal(line, &m) != nil {
				lg.Ignored++
				continue
			}
			m.T, m.Seq = parseTime(m.Time), seq
			if m.T.IsZero() {
				m.T = parseTime(m.At)
			}
			if head.Contract == FlipMarkerContract {
				lg.FlipMarkers = append(lg.FlipMarkers, m)
			} else {
				lg.PassStarts = append(lg.PassStarts, m)
			}
		default:
			lg.Ignored++
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read run record %s: %w", path, err)
	}
	return lg, nil
}

// Latest returns, per PR, the latest record of mode among records for which keep
// is true (nil keeps all). Ties on time go to the later line.
func (lg *Log) Latest(mode string, keep func(Record) bool) map[string]Record {
	out := map[string]Record{}
	for _, r := range lg.Records {
		if r.Mode != mode || (keep != nil && !keep(r)) {
			continue
		}
		if cur, ok := out[r.Entity]; ok && r.Time.Before(cur.Time) {
			continue
		}
		out[r.Entity] = r
	}
	return out
}

// PassStart is the file position of the latest pass-start marker of mode, and
// false when there is none. A pass-start marker begins a new pass: only the
// records after it satisfy a resume.
func (lg *Log) PassStart(mode string) (int, bool) {
	best, found := 0, false
	for _, m := range lg.PassStarts {
		if m.Mode == mode && (!found || m.Seq > best) {
			best, found = m.Seq, true
		}
	}
	return best, found
}

// AppendLine appends v as ONE JSON line to the file at path, in one write call
// under an exclusive lock on the file (the lock the composed command takes for its
// own records), so a marker cannot interleave with a live run's line.
func AppendLine(path string, v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode run record row: %w", err)
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

// sortedKeys returns the keys of m in order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
