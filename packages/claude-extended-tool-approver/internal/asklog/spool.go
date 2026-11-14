package asklog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// The PreToolUse hook has a hard timeout (5 s in the machine's settings) and
// a verdict that misses it is lost. The ask-log write is observability, so it
// MUST NOT be able to cost a verdict: the hook bounds its open and its write
// (OpenWithin, RunWithin) and, when the database is too slow or locked, appends
// the record to a spool file next to the database instead of dropping it. The
// next hook that gets through drains the spool into the database, keeping the
// original timestamp. Trade-off: a spooled row appears in the database late
// (on the next successful hook run), never lost; a record whose spool append
// also fails is reported on stderr (the one case that can still lose a row).

// ErrBudgetExceeded is returned by OpenWithin and RunWithin when the work did
// not finish within its budget. The work is abandoned, not cancelled: it may
// still complete in the background until the process exits.
var ErrBudgetExceeded = errors.New("asklog: time budget exceeded")

// SpoolPath returns the spool file that belongs to the database at dbPath.
func SpoolPath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "asks-spool.jsonl")
}

// OpenWithin is NewStore bounded by budget. On timeout it returns
// ErrBudgetExceeded; a store the abandoned open eventually produces is closed.
func OpenWithin(dbPath string, budget time.Duration) (*Store, error) {
	type res struct {
		s   *Store
		err error
	}
	ch := make(chan res, 1)
	go func() {
		s, err := NewStore(dbPath)
		ch <- res{s, err}
	}()
	t := time.NewTimer(budget)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.s, r.err
	case <-t.C:
		go func() {
			if r := <-ch; r.s != nil {
				_ = r.s.Close()
			}
		}()
		return nil, ErrBudgetExceeded
	}
}

// RunWithin runs fn and returns its error, or ErrBudgetExceeded if it has not
// returned within budget.
func RunWithin(budget time.Duration, fn func() error) error {
	ch := make(chan error, 1)
	go func() { ch <- fn() }()
	t := time.NewTimer(budget)
	defer t.Stop()
	select {
	case err := <-ch:
		return err
	case <-t.C:
		return ErrBudgetExceeded
	}
}

// ctxGrace bounds how long RunWithinCtx waits, after cancelling, for fn to stop.
// A variable so tests can make the wait effectively unbounded.
var ctxGrace = 1 * time.Second

// RunWithinCtx is RunWithin for work that honours a context. On a budget miss
// it cancels the context and waits up to ctxGrace for fn to stop, so a write
// still waiting on a database lock is abandoned instead of committing later
// (which would duplicate a row the caller then spools).
func RunWithinCtx(budget time.Duration, fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan error, 1)
	go func() { ch <- fn(ctx) }()
	t := time.NewTimer(budget)
	defer t.Stop()
	select {
	case err := <-ch:
		return err
	case <-t.C:
		cancel()
		g := time.NewTimer(ctxGrace)
		defer g.Stop()
		select {
		case <-ch:
		case <-g.C:
		}
		return ErrBudgetExceeded
	}
}

type spoolEntry struct {
	At             string       `json:"at"`
	Cause          string       `json:"cause"`
	Input          spoolInput   `json:"input"`
	Decision       int          `json:"decision"`
	Reason         string       `json:"reason"`
	Trace          []spoolTrace `json:"trace,omitempty"`
	SandboxEnabled *bool        `json:"sandbox_enabled,omitempty"`
}

type spoolTrace struct {
	Rule     string `json:"rule"`
	Decision int    `json:"decision"`
	Reason   string `json:"reason"`
}

// spoolInput is the persisted subset of hookio.HookInput the pre-tool row uses.
type spoolInput struct {
	SessionID      string          `json:"session_id"`
	CWD            string          `json:"cwd"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	PermissionMode string          `json:"permission_mode,omitempty"`
	ToolUseID      string          `json:"tool_use_id,omitempty"`
	AgentID        string          `json:"agent_id,omitempty"`
	AgentType      string          `json:"agent_type,omitempty"`
	PromptID       string          `json:"prompt_id,omitempty"`
	TranscriptPath string          `json:"transcript_path,omitempty"`
}

// AppendToSpool records one PreToolUse decision in the spool beside dbPath.
// cause is a short human string (why the database write did not happen).
// Each record is one O_APPEND write, so concurrent hooks do not interleave.
func AppendToSpool(dbPath string, input *hookio.HookInput, result hookio.RuleResult, sandbox *bool, cause string) error {
	e := spoolEntry{
		At:    nowISO(),
		Cause: cause,
		Input: spoolInput{
			SessionID: input.SessionID, CWD: input.CWD, ToolName: input.ToolName,
			ToolInput: input.ToolInput, PermissionMode: input.PermissionMode,
			ToolUseID: input.ToolUseID, AgentID: input.AgentID, AgentType: input.AgentType,
			PromptID: input.PromptID, TranscriptPath: input.TranscriptPath,
		},
		Decision:       int(result.Decision),
		Reason:         result.Reason,
		SandboxEnabled: sandbox,
	}
	for _, t := range result.Trace {
		e.Trace = append(e.Trace, spoolTrace{t.RuleName, int(t.Decision), t.Reason})
	}
	if len(e.Input.ToolInput) == 0 {
		e.Input.ToolInput = json.RawMessage("null")
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(SpoolPath(dbPath), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// SpoolLen returns the number of records waiting in the spool (0 if none).
// This is the "misses" count: it is what an operator or a metric reads.
func SpoolLen(dbPath string) int {
	f, err := os.Open(SpoolPath(dbPath))
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		n++
	}
	return n
}

// DrainSpool replays up to max spooled records into the database. The spool is
// claimed with an atomic rename so two hooks never replay the same file; records
// that fail to insert, or exceed max, are appended back. A record whose
// (session_id, tool_use_id) is already in the table (a write that was abandoned
// by the hook but still committed) is skipped, not duplicated. Returns the
// number of records inserted.
func (s *Store) DrainSpool(dbPath string, max int) (int, error) {
	sp := SpoolPath(dbPath)
	if _, err := os.Stat(sp); err != nil {
		return 0, nil
	}
	claimed := fmt.Sprintf("%s.draining.%d", sp, os.Getpid())
	if err := os.Rename(sp, claimed); err != nil {
		return 0, nil // another hook claimed it
	}
	data, err := os.ReadFile(claimed)
	if err != nil {
		return 0, err
	}
	var keep [][]byte
	inserted := 0
	var firstErr error
	start := 0
	for i := 0; i <= len(data); i++ {
		if i < len(data) && data[i] != '\n' {
			continue
		}
		raw := data[start:i]
		start = i + 1
		if len(raw) == 0 {
			continue
		}
		if inserted >= max || firstErr != nil {
			keep = append(keep, append([]byte(nil), raw...))
			continue
		}
		var e spoolEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			continue // unreadable record: nothing to retry
		}
		ok, err := s.replaySpooled(&e)
		if err != nil {
			firstErr = err
			keep = append(keep, append([]byte(nil), raw...))
			continue
		}
		if ok {
			inserted++
		}
	}
	if len(keep) > 0 {
		f, err := os.OpenFile(sp, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return inserted, err // leave the claimed file in place for a later look
		}
		for _, k := range keep {
			_, _ = f.Write(append(k, '\n'))
		}
		_ = f.Close()
	}
	_ = os.Remove(claimed)
	return inserted, firstErr
}

func (s *Store) replaySpooled(e *spoolEntry) (bool, error) {
	if e.Input.ToolUseID != "" {
		var n int
		if err := s.db.QueryRowContext(
			context.Background(),
			`SELECT COUNT(*) FROM tool_decisions WHERE session_id = ? AND tool_use_id = ?`,
			e.Input.SessionID, e.Input.ToolUseID,
		).Scan(&n); err != nil {
			return false, err
		}
		if n > 0 {
			return false, nil
		}
	}
	in := &hookio.HookInput{
		SessionID: e.Input.SessionID, CWD: e.Input.CWD, ToolName: e.Input.ToolName,
		ToolInput: e.Input.ToolInput, PermissionMode: e.Input.PermissionMode,
		ToolUseID: e.Input.ToolUseID, AgentID: e.Input.AgentID, AgentType: e.Input.AgentType,
		PromptID: e.Input.PromptID, TranscriptPath: e.Input.TranscriptPath,
	}
	res := hookio.RuleResult{Decision: hookio.Decision(e.Decision), Reason: e.Reason}
	if e.Trace != nil {
		res.Trace = []hookio.TraceEntry{}
		for _, t := range e.Trace {
			res.Trace = append(res.Trace, hookio.TraceEntry{RuleName: t.Rule, Decision: hookio.Decision(t.Decision), Reason: t.Reason})
		}
	}
	prev := s.sandboxEnabled
	s.sandboxEnabled = e.SandboxEnabled
	defer func() { s.sandboxEnabled = prev }()
	if err := recordPreToolDecisionAt(context.Background(), s, in, res, e.At); err != nil {
		return false, err
	}
	return true, nil
}
