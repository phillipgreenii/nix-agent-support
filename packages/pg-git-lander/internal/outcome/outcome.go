package outcome

import (
	"encoding/json"
	"strings"
)

// Worktree describes one worktree of an operation.
type Worktree struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Missing bool   `json:"missing"`
}

// Holder is the lock holder named by a busy or refused outcome.
type Holder struct {
	OpID      string `json:"op_id"`
	Operation string `json:"operation"`
	Branch    string `json:"branch"`
	// Stage is the stage of the holder's operation, read from its BRANCH
	// record.
	Stage string `json:"stage"`
	// State is "running" or "stopped".
	State string `json:"state"`
	PID   int    `json:"pid"`
	Host  string `json:"host"`
	// AgeS is the seconds the holder has been in its present state: since its
	// stage began (stage_started_at) while running, since it stopped
	// (stop.at) once stopped; the operation's start or the last lease
	// renewal when those are not recorded.
	AgeS int64 `json:"age_s"`
}

// BlockingOp is an operation in the way of a refusal.
type BlockingOp struct {
	OpID      string   `json:"op_id"`
	Operation string   `json:"operation"`
	Branch    string   `json:"branch"`
	Reason    string   `json:"reason"`
	Next      []string `json:"next"`
}

// ConfigErr locates a repo configuration error.
type ConfigErr struct {
	File  string `json:"file"`
	Line  int    `json:"line"`
	Key   string `json:"key"`
	Layer string `json:"layer"`
}

// Fate is a worktree fate with the reason when removal failed.
type Fate struct {
	Fate   string `json:"fate"`
	Reason string `json:"reason"`
}

// Restored names a worktree switched back, with its starting state.
type Restored struct {
	Worktree string `json:"worktree"`
	State    string `json:"state"`
}

// Tips are the ref tips of an operation.
type Tips struct {
	Branch string `json:"branch"`
	Target string `json:"target"`
	Onto   string `json:"onto"`
	Result string `json:"result"`
}

// Stats are the counters of an operation.
type Stats struct {
	DurationMS     int64 `json:"duration_ms"`
	RebasedCommits int   `json:"rebased_commits"`
	AutofixCommits int   `json:"autofix_commits"`
	ConflictRounds int   `json:"conflict_rounds"`
	RaceRetries    int   `json:"race_retries"`
}

// Resolution is one conflict resolution.
type Resolution struct {
	Commit string `json:"commit"`
	Path   string `json:"path"`
	Rule   string `json:"rule"`
	Driver string `json:"driver"`
	Result string `json:"result"`
}

// Autofix is one autofix commit.
type Autofix struct {
	Hook   string   `json:"hook"`
	Commit string   `json:"commit"`
	Files  []string `json:"files"`
}

// SkippedHook is a hook that was skipped.
type SkippedHook struct {
	Point      string `json:"point"`
	Index      int    `json:"index"`
	Exit       int    `json:"exit"`
	StderrTail string `json:"stderr_tail"`
}

// WarningEntry is a warning word with its details.
type WarningEntry struct {
	Warning Warning `json:"warning"`
	Reason  string  `json:"reason"`
	Detail  string  `json:"detail"`
}

// Outcome is the outcome object of design section 14.1. Every field is always
// rendered: pointers render null, slices render [] when nil, strings are
// empty when they do not apply.
type Outcome struct {
	Schema  int    `json:"schema"`
	Outcome Word   `json:"outcome"`
	Exit    int    `json:"exit"`
	OpID    string `json:"op_id"`
	// Operation is "land", "rebase", ... .
	Operation string `json:"operation"`
	Branch    string `json:"branch"`
	Target    string `json:"target"`
	Onto      string `json:"onto"`

	// Reason is a RefusalReason or StopReason (or the cause of an error).
	Reason    string `json:"reason"`
	Detail    string `json:"detail"`
	Retryable *bool  `json:"retryable"`
	Stage     string `json:"stage"`

	Next []string `json:"next"`
	Hint string   `json:"hint"`
	// Message is the one-sentence human summary printed after the branch in
	// the human line (an addition to the section 14.1 fields).
	Message string `json:"message"`

	Holder     *Holder      `json:"holder"`
	BlockingOp []BlockingOp `json:"blocking_op"`
	Paths      []string     `json:"paths"`

	SourceWorktree      *Worktree `json:"source_worktree"`
	DestinationWorktree *Worktree `json:"destination_worktree"`
	WorkWorktree        *Worktree `json:"work_worktree"`

	Role        string     `json:"role"`
	Path        string     `json:"path"`
	ConfigError *ConfigErr `json:"config_error"`

	WorkBranch        string `json:"work_branch"`
	PlaceholderBranch string `json:"placeholder_branch"`
	BranchFate        string `json:"branch_fate"`
	WorktreeFate      *Fate  `json:"worktree_fate"`
	OldBranchTip      string `json:"old_branch_tip"`

	Restored      []Restored     `json:"restored"`
	Tips          *Tips          `json:"tips"`
	LandedCommit  string         `json:"landed_commit"`
	PathTaken     string         `json:"path_taken"`
	Stats         *Stats         `json:"stats"`
	Dropped       []string       `json:"dropped_commits"`
	Resolved      []Resolution   `json:"resolved"`
	Autofix       []Autofix      `json:"autofix"`
	SkippedHooks  []SkippedHook  `json:"skipped_hooks"`
	Warnings      []WarningEntry `json:"warnings"`
	MovedAlready  *bool          `json:"moved_already"`
	ConfigChanged *bool          `json:"config_changed"`

	Log     string   `json:"log"`
	LogTail []string `json:"log_tail"`

	// Repo is the --repo value, used only to template next commands.
	Repo string `json:"-"`
}

// New returns an outcome of the given word with schema and exit filled in.
func New(w Word) *Outcome {
	return &Outcome{Schema: Schema, Outcome: w, Exit: ExitCode(w)}
}

// NextCommands templates the exact commands for resume flags.
func NextCommands(operation, branch, repo string, flags []string) []string {
	cmds := make([]string, 0, len(flags))
	for _, f := range flags {
		parts := []string{"pg-git-lander", operation, branch, f}
		if repo != "" {
			parts = append(parts, "--repo", repo)
		}
		cmds = append(cmds, strings.Join(parts, " "))
	}
	return cmds
}

// NewStopped builds a stopped outcome for a stop reason at a stage, with the
// retryable flag and valid next commands of section 6.3.
func NewStopped(reason StopReason, operation, branch, stage, repo string) *Outcome {
	o := New(Stopped)
	info := Stops[reason]
	r := info.Retryable
	o.Reason, o.Stage, o.Retryable = string(reason), stage, &r
	o.Operation, o.Branch, o.Repo = operation, branch, repo
	o.Next = NextCommands(operation, branch, repo, info.Next)
	return o
}

// NewRefused builds a refused outcome for a refusal reason.
func NewRefused(reason RefusalReason, operation, branch, repo string) *Outcome {
	o := New(Refused)
	info := Refusals[reason]
	o.Reason, o.Hint = string(reason), info.Hint
	o.Operation, o.Branch, o.Repo = operation, branch, repo
	o.Next = NextCommands(operation, branch, repo, info.NextFlags)
	return o
}

// NewBusy builds a busy outcome; busy is always retryable.
func NewBusy(operation, branch, target string, h Holder) *Outcome {
	o := New(Busy)
	t := true
	o.Retryable, o.Operation, o.Branch, o.Target, o.Holder = &t, operation, branch, target, &h
	o.Hint = "Nothing changed; run again later."
	return o
}

// JSON renders the outcome as one JSON object (one line, no trailing
// newline) with every field present.
func (o *Outcome) JSON() ([]byte, error) {
	c := *o
	if c.Schema == 0 {
		c.Schema = Schema
	}
	c.Exit = ExitCode(c.Outcome)
	if c.Next == nil {
		c.Next = []string{}
	}
	if c.BlockingOp == nil {
		c.BlockingOp = []BlockingOp{}
	}
	if c.Paths == nil {
		c.Paths = []string{}
	}
	if c.Restored == nil {
		c.Restored = []Restored{}
	}
	if c.Dropped == nil {
		c.Dropped = []string{}
	}
	if c.Resolved == nil {
		c.Resolved = []Resolution{}
	}
	if c.Autofix == nil {
		c.Autofix = []Autofix{}
	}
	if c.SkippedHooks == nil {
		c.SkippedHooks = []SkippedHook{}
	}
	if c.Warnings == nil {
		c.Warnings = []WarningEntry{}
	}
	if c.LogTail == nil {
		c.LogTail = []string{}
	}
	return json.Marshal(&c)
}
