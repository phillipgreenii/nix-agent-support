package pggwmworktree

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Observability files, both under <state>/pg-gwm-worktree/:
//
//	exceptions.jsonl  one JSON object per honored exception:
//	                  {"ts","session_id","tool","repo","reason","cwd"}
//	counters.json     {"denies":N,"exceptions":{"<tool>":N}}
//
// A hook is one short process per call, so the counters live in a file (updated
// under an exclusive flock), not in memory.
//
// Review the exceptions with:
//
//	jq -c '[.ts,.tool,.repo,.reason]' "${XDG_STATE_HOME:-$HOME/.local/state}/claude-extended-tool-approver/pg-gwm-worktree/exceptions.jsonl"
const (
	obsDirName   = "pg-gwm-worktree"
	logFileName  = "exceptions.jsonl"
	countersName = "counters.json"
)

// exceptionRecord is one exception log line.
type exceptionRecord struct {
	TS        string `json:"ts"`
	SessionID string `json:"session_id"`
	Tool      string `json:"tool"`
	Repo      string `json:"repo"`
	Reason    string `json:"reason"`
	CWD       string `json:"cwd"`
}

// Counters is the persisted counter file.
type Counters struct {
	Denies     int            `json:"denies"`
	Exceptions map[string]int `json:"exceptions"`
}

// defaultStateDir is the approver's state directory:
// $XDG_STATE_HOME/claude-extended-tool-approver, else ~/.local/state/....
func defaultStateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "claude-extended-tool-approver")
}

func (r *Rule) obsDir() string {
	if r.stateDir == "" {
		return ""
	}
	return filepath.Join(r.stateDir, obsDirName)
}

// recordException appends the log line and bumps the per-tool counter. Failures
// are warned about, never fatal: observability must not turn an allowed
// exception into an error.
func (r *Rule) recordException(rec exceptionRecord) {
	dir := r.obsDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.warn("cannot create %s: %v", dir, err)
		return
	}
	b, _ := json.Marshal(rec)
	f, err := os.OpenFile(filepath.Join(dir, logFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		r.warn("cannot append exception log: %v", err)
	} else {
		_, _ = f.Write(append(b, '\n'))
		_ = f.Close()
	}
	r.bump(func(c *Counters) { c.Exceptions[rec.Tool]++ })
}

func (r *Rule) recordDeny() { r.bump(func(c *Counters) { c.Denies++ }) }

func (r *Rule) bump(mut func(*Counters)) {
	dir := r.obsDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.warn("cannot create %s: %v", dir, err)
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, countersName), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		r.warn("cannot open counters: %v", err)
		return
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		r.warn("cannot lock counters: %v", err)
		return
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	var c Counters
	if raw, err := os.ReadFile(filepath.Join(dir, countersName)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &c)
	}
	if c.Exceptions == nil {
		c.Exceptions = map[string]int{}
	}
	mut(&c)
	out, _ := json.Marshal(c)
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt(out, 0)
	}
}

// ReadCounters loads the persisted counters (zero value when absent).
func ReadCounters(stateDir string) Counters {
	var c Counters
	if raw, err := os.ReadFile(filepath.Join(stateDir, obsDirName, countersName)); err == nil {
		_ = json.Unmarshal(raw, &c)
	}
	if c.Exceptions == nil {
		c.Exceptions = map[string]int{}
	}
	return c
}

func (r *Rule) now() string {
	t := time.Now()
	if r.clock != nil {
		t = r.clock()
	}
	return t.UTC().Format(time.RFC3339)
}
