// Package locks is the lock layer of pg-git-lander: branch, worktree and
// admin locks are git refs under refs/pg-git-lander/locks/ that point at a
// blob holding one JSON record (design section 8.2). It acquires them with a
// create-if-absent git update-ref, renews them by compare-and-swap, waits for
// held ones and takes an operation's whole lock set in one global order.
package locks

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// SchemaVersion is the newest record schema this tool reads and writes.
const SchemaVersion = 1

// Role is the role of a lock record.
type Role string

// Roles.
const (
	RoleBranch   Role = "branch"
	RoleTarget   Role = "target"
	RoleWorktree Role = "worktree"
	RoleAdmin    Role = "admin"
)

// State is the state of a lock record.
type State string

// States.
const (
	StateRunning State = "running"
	StateStopped State = "stopped"
)

// Lock ref namespaces.
const (
	RefPrefix       = "refs/pg-git-lander/locks/"
	branchPrefix    = RefPrefix + "heads/"
	worktreePrefix  = RefPrefix + "worktrees/"
	adminPrefix     = RefPrefix + "admin/"
	MainWorktreeAdm = "_main"
	AdminEvents     = "events"
)

// BranchRef is the lock ref of a branch, for the BRANCH and TARGET roles.
func BranchRef(branch string) string { return branchPrefix + branch }

// WorktreeRef is the lock ref of a worktree, by git admin name.
func WorktreeRef(admin string) string { return worktreePrefix + admin }

// AdminRef is the lock ref of an admin lock (events is the events file).
func AdminRef(name string) string { return adminPrefix + name }

// AdminName derives a worktree's lock name from its private git dir
// (git rev-parse --git-dir): the basename of <common>/worktrees/<name>, or
// _main for the main worktree. git's own name is canonical, so a typed path's
// case or symlinks cannot give one worktree two locks.
func AdminName(gitDir string) string {
	gitDir = filepath.Clean(gitDir)
	if filepath.Base(filepath.Dir(gitDir)) == "worktrees" {
		return filepath.Base(gitDir)
	}
	return MainWorktreeAdm
}

// Owner identifies the process holding a lock.
type Owner struct {
	Host        string `json:"host"`
	PID         int    `json:"pid"`
	PIDStart    int64  `json:"pid_start"`
	Token       string `json:"token"`
	ToolVersion string `json:"tool_version"`
}

// Lease is the holder's lease; renewed_at is informational only.
type Lease struct {
	RenewedAt time.Time `json:"renewed_at"`
	TTLSec    int       `json:"ttl_s"`
}

// Start is a worktree's starting state: a branch name or a detached oid.
type Start struct {
	Branch   string `json:"branch,omitempty"`
	Detached string `json:"detached,omitempty"`
}

// WorktreeEntry is one worktree an operation uses.
type WorktreeEntry struct {
	Admin string   `json:"admin"`
	Path  string   `json:"path"`
	Roles []string `json:"roles"`
	Start Start    `json:"start"`
}

// Keep holds the keep flags and the removal consent of section 11.3.
type Keep struct {
	SourceWorktree bool `json:"source_worktree"`
	SourceBranch   bool `json:"source_branch"`
	RemovalConsent bool `json:"removal_consent"`
}

// Move is the write-ahead intent of the move (step 11).
type Move struct {
	Intent         *string `json:"intent"`
	ExpectedResult *string `json:"expected_result"`
}

// CurrentCommand is the step the operation is running; its deadline gates
// renewal.
type CurrentCommand struct {
	Argv0     string    `json:"argv0"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Deadline  time.Time `json:"deadline"`
}

// Tips are the recorded branch tips (null until known).
type Tips struct {
	Branch *string `json:"branch"`
	Target *string `json:"target"`
	Onto   *string `json:"onto"`
	Result *string `json:"result"`
}

// Attempts counts retries.
type Attempts struct {
	ConflictRounds int            `json:"conflict_rounds"`
	RaceRetries    int            `json:"race_retries"`
	Autofix        map[string]int `json:"autofix"`
}

// Stop is the stop object of a stopped record. Hook output is never stored.
type Stop struct {
	Reason       string    `json:"reason"`
	Retryable    bool      `json:"retryable"`
	At           time.Time `json:"at"`
	Stage        string    `json:"stage"`
	Paths        []string  `json:"paths"`
	Message      string    `json:"message"`
	MovedAlready bool      `json:"moved_already"`
}

// Record is one lock record. A BRANCH record carries the operation's whole
// state; TARGET and worktree records carry only schema, role, state, op_id,
// branch, owner and lease; an admin record only schema, role, state, owner and
// lease (see MarshalJSON).
type Record struct {
	Schema int    `json:"schema"`
	Role   Role   `json:"role"`
	State  State  `json:"state"`
	OpID   string `json:"op_id"`

	Operation string `json:"operation"`
	Branch    string `json:"branch"`
	Target    string `json:"target"`

	Owner Owner  `json:"owner"`
	Lease *Lease `json:"lease"`

	Worktrees         []WorktreeEntry `json:"worktrees"`
	WorkBranch        string          `json:"work_branch"`
	PlaceholderBranch string          `json:"placeholder_branch"`
	Keep              Keep            `json:"keep"`
	Move              Move            `json:"move"`
	StartedAt         time.Time       `json:"started_at"`
	Stage             string          `json:"stage"`
	StageStartedAt    time.Time       `json:"stage_started_at"`
	CurrentCommand    *CurrentCommand `json:"current_command"`
	Tips              Tips            `json:"tips"`
	Attempts          Attempts        `json:"attempts"`
	ConfigDigest      string          `json:"config_digest"`
	Context           map[string]any  `json:"context"`
	Stop              *Stop           `json:"stop"`
}

type fullRecord Record

type shortRecord struct {
	Schema int    `json:"schema"`
	Role   Role   `json:"role"`
	State  State  `json:"state"`
	OpID   string `json:"op_id"`
	Branch string `json:"branch"`
	Owner  Owner  `json:"owner"`
	Lease  *Lease `json:"lease"`
}

type adminRecord struct {
	Schema int    `json:"schema"`
	Role   Role   `json:"role"`
	State  State  `json:"state"`
	Owner  Owner  `json:"owner"`
	Lease  *Lease `json:"lease"`
}

// MarshalJSON writes the form of the record's role.
func (r Record) MarshalJSON() ([]byte, error) {
	switch r.Role {
	case RoleBranch:
		return json.Marshal(fullRecord(r))
	case RoleTarget, RoleWorktree:
		return json.Marshal(shortRecord{r.Schema, r.Role, r.State, r.OpID, r.Branch, r.Owner, r.Lease})
	case RoleAdmin:
		return json.Marshal(adminRecord{r.Schema, r.Role, r.State, r.Owner, r.Lease})
	}
	return nil, fmt.Errorf("locks: unknown role %q", r.Role)
}

// ErrNewerSchema marks a record whose schema this tool does not support.
var ErrNewerSchema = errors.New("lock record has a newer schema than this tool supports")

// Parse decodes a record blob. Any error means the record is unreadable and
// the lock MUST be treated as held by a live holder.
func Parse(blob []byte) (*Record, error) {
	var r Record
	if err := json.Unmarshal(blob, &r); err != nil {
		return nil, fmt.Errorf("parse lock record: %w", err)
	}
	if r.Schema > SchemaVersion {
		return nil, fmt.Errorf("schema %d: %w", r.Schema, ErrNewerSchema)
	}
	if r.Schema < 1 {
		return nil, errors.New("lock record has no schema")
	}
	switch r.Role {
	case RoleBranch, RoleTarget, RoleWorktree, RoleAdmin:
	default:
		return nil, fmt.Errorf("lock record has unknown role %q", r.Role)
	}
	if r.State != StateRunning && r.State != StateStopped {
		return nil, fmt.Errorf("lock record has unknown state %q", r.State)
	}
	return &r, nil
}

// Clone returns a deep copy.
func (r Record) Clone() Record {
	b, err := json.Marshal(r)
	if err != nil {
		return r
	}
	var c Record
	if json.Unmarshal(b, &c) != nil {
		return r
	}
	return c
}

// Short returns the TARGET or worktree record derived from a BRANCH record.
func (r Record) Short(role Role) Record {
	return Record{
		Schema: r.Schema, Role: role, State: r.State, OpID: r.OpID, Branch: r.Branch,
		Owner: r.Owner, Lease: r.Lease,
	}
}
