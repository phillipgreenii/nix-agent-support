package supervisor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants/internal/control"
)

// SchemaVersion is the status file's schema_version. ReadStatus accepts only
// this version.
const SchemaVersion = 1

// LastExit is how an assistant's last session ended.
type LastExit struct {
	Status  int     `json:"status"`           // #{pane_dead_status}
	Signal  int     `json:"signal,omitempty"` // #{pane_dead_signal}; 0 for none
	UptimeS float64 `json:"uptime_s"`
}

// AssistantStatus is one assistant's entry in the status file.
type AssistantStatus struct {
	ID              string    `json:"id"`
	Enabled         bool      `json:"enabled"`
	Running         bool      `json:"running"`
	PID             int       `json:"pid,omitempty"`
	SessionID       string    `json:"session_id,omitempty"`
	StartedAt       time.Time `json:"started_at,omitzero"`
	ContextTokens   int64     `json:"context_tokens,omitempty"`
	ContextFraction float64   `json:"context_fraction,omitempty"`
	ContextAt       time.Time `json:"context_at,omitzero"` // when the context figure was read
	Ladder          string    `json:"ladder,omitempty"`    // meter | disabled | exit
	Rung            string    `json:"rung,omitempty"`      // nudge | interrupt | kill | force-kill
	Reason          string    `json:"reason,omitempty"`
	WaitUntil       time.Time `json:"wait_until,omitzero"`
	WaitReason      string    `json:"wait_reason,omitempty"` // backoff | block_hold | missing_cwd
	Hint            string    `json:"hint,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	ConfigError     string    `json:"config_error,omitempty"`
	LastExit        *LastExit `json:"last_exit,omitempty"`
	LastTick        time.Time `json:"last_tick,omitzero"`
}

// DaemonStatus is the daemon's entry in the status file. ControlStateDir and
// ClaudeConfigDir are the daemon's effective PG_CLAUDE_CONTROL_STATE_DIR and
// CLAUDE_CONFIG_DIR, which a session's interactive startup files could set
// differently.
type DaemonStatus struct {
	PID             int       `json:"pid"`
	Version         string    `json:"version"`
	StartedAt       time.Time `json:"started_at"`
	ConfigPath      string    `json:"config_path"`
	ConfigHash      string    `json:"config_hash"`
	ControlStateDir string    `json:"pg_claude_control_state_dir,omitempty"`
	ClaudeConfigDir string    `json:"claude_config_dir,omitempty"`
	ConfigProblems  []string  `json:"config_problems,omitempty"`
}

// Status is the whole status file, <state dir>/status.json. Assistants are
// ordered by id; Blocks are the current block readings, each with the time it
// was observed, which a restarted daemon seeds its start hold from.
type Status struct {
	SchemaVersion int               `json:"schema_version"`
	WrittenAt     time.Time         `json:"written_at"`
	Daemon        DaemonStatus      `json:"daemon"`
	Assistants    []AssistantStatus `json:"assistants"`
	Orphans       []string          `json:"orphans,omitempty"`
	Blocks        []control.Limit   `json:"blocks"`
}

// StatusBoard is the in-memory status every loop updates and the file it is
// flushed to. Its mutex covers only memory: Set and the other setters copy
// their argument in and never wait on file I/O, and Flush copies the state
// out under the lock and writes the file outside it.
//
// Flush writes the file, so it MUST be called only from the daemon's
// heartbeat goroutine, never from an assistant's loop: a loop that wants the
// file written at the end of its tick signals the heartbeat, without blocking,
// instead.
//
// One Flush writes at a time. A Flush that finds a write in progress returns
// nil at once, handing its state to the writer in progress, which writes it
// next and reports any error of that write itself. So the file always ends up
// holding the latest state flushed, and a Flush caller may perform other
// callers' handed-off writes as well as its own before it returns.
type StatusBoard struct {
	path  string
	write func(path string, s Status) error // writeStatus; a test seam

	mu         sync.Mutex // guards every field below
	daemon     DaemonStatus
	assistants map[string]AssistantStatus
	orphans    []string
	blocks     []control.Limit
	flushAt    time.Time // the latest time passed to Flush
	pending    bool      // a Flush's state is not written yet
	writing    bool      // a Flush is writing the file
}

// NewStatusBoard returns a board that flushes to path, with the daemon's
// entry d and no assistants, orphans or block readings.
func NewStatusBoard(path string, d DaemonStatus) *StatusBoard {
	d.ConfigProblems = slices.Clone(d.ConfigProblems)
	return &StatusBoard{
		path:       path,
		write:      writeStatus,
		daemon:     d,
		assistants: map[string]AssistantStatus{},
	}
}

// Set records s as the entry for s.ID, replacing any earlier one.
func (b *StatusBoard) Set(s AssistantStatus) {
	if s.LastExit != nil {
		le := *s.LastExit
		s.LastExit = &le
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.assistants[s.ID] = s
}

// SetOrphans records the sessions no valid assistant supervises.
func (b *StatusBoard) SetOrphans(names []string) {
	names = slices.Clone(names)
	slices.Sort(names)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.orphans = names
}

// SetBlocks records the current block readings.
func (b *StatusBoard) SetBlocks(l []control.Limit) {
	l = slices.Clone(l)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.blocks = l
}

// Flush writes the state to the file atomically (a temporary file in the same
// directory, then a rename), mode 0600, with written_at set to now (the
// latest now any Flush has passed). A missing directory is created 0700; a
// path that is not absolute is an error. Call it only from the heartbeat
// goroutine (see StatusBoard).
func (b *StatusBoard) Flush(now time.Time) error {
	b.mu.Lock()
	if now.After(b.flushAt) {
		b.flushAt = now
	}
	b.pending = true
	if b.writing {
		b.mu.Unlock()
		return nil // the writer in progress writes this state next
	}
	b.writing = true
	var err error
	for {
		// b.mu is held here. writing is cleared under the same lock as the
		// last pending check, so no Flush can hand its state to a writer
		// that has already decided to stop.
		if !b.pending {
			b.writing = false
			b.mu.Unlock()
			return err
		}
		b.pending = false
		s := b.snapshot()
		b.mu.Unlock()
		err = b.writeUnlocked(s)
		b.mu.Lock()
	}
}

// writeUnlocked writes s with b.mu not held. If the write does not return (a
// panic), it clears writing, so a later Flush still writes.
func (b *StatusBoard) writeUnlocked(s Status) error {
	returned := false
	defer func() {
		if !returned {
			b.mu.Lock()
			b.writing = false
			b.mu.Unlock()
		}
	}()
	err := b.write(b.path, s)
	returned = true
	return err
}

// snapshot copies the state out. The caller holds b.mu. Every value the
// result shares with the board (slices, LastExit pointers) is never changed
// in place: the setters replace them.
func (b *StatusBoard) snapshot() Status {
	s := Status{
		SchemaVersion: SchemaVersion,
		WrittenAt:     b.flushAt,
		Daemon:        b.daemon,
		Assistants:    make([]AssistantStatus, 0, len(b.assistants)),
		Orphans:       b.orphans,
		Blocks:        b.blocks,
	}
	for _, a := range b.assistants {
		s.Assistants = append(s.Assistants, a)
	}
	slices.SortFunc(s.Assistants, func(x, y AssistantStatus) int { return strings.Compare(x.ID, y.ID) })
	if s.Blocks == nil {
		s.Blocks = []control.Limit{}
	}
	return s
}

func writeStatus(path string, s Status) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("status %s: %w", path, err)
	}
	if err := writeFileAtomic(path, append(data, '\n')); err != nil {
		return fmt.Errorf("status %s: %w", path, err)
	}
	return nil
}

// ReadStatus reads a status file. A missing file is an error wrapping
// fs.ErrNotExist; a file that does not parse, or whose schema_version is not
// SchemaVersion, is an error naming the file.
func ReadStatus(path string) (Status, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Status{}, fmt.Errorf("status: %w", err)
	}
	return ParseStatus(path, data)
}

// ParseStatus parses data, the content of the status file at path (named in
// the error), as ReadStatus does. A caller that also prints the file uses it
// on the bytes it printed.
func ParseStatus(path string, data []byte) (Status, error) {
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		return Status{}, fmt.Errorf("status %s: %w", path, err)
	}
	if s.SchemaVersion != SchemaVersion {
		return Status{}, fmt.Errorf("status %s: schema_version %d, want %d", path, s.SchemaVersion, SchemaVersion)
	}
	return s, nil
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory and a rename, so a reader sees the old file or the new one, never
// part of one. The file is mode 0600; a missing directory is created 0700. A
// path that is not absolute is refused, so nothing is ever written under the
// process's working directory.
func writeFileAtomic(path string, data []byte) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%q is not an absolute path", path)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*") // mode 0600
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
