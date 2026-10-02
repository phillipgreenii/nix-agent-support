// Package report defines the failure report: the JSON document the wrapper
// writes to report.json before each handler runs, and the one fingerprint
// function that identifies "the same failure again". The wrapper holds a
// Report in memory and is the only author; handlers read it.
package report

import (
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
)

// SchemaVersion is the report's schema_version. It MUST be incremented on any
// breaking change; handlers SHOULD exit 1 on a major version they do not know.
const SchemaVersion = 1

// Mode says how the failure reached the wrapper.
type Mode string

const (
	// ModeArgv: the wrapper ran the command itself.
	ModeArgv Mode = "argv"
	// ModeStdin: the caller already decided something was wrong and piped
	// the evidence in; there is no command.
	ModeStdin Mode = "stdin"
)

// Report is report.json. The JSON Schema in schemas/report.schema.json
// describes exactly this shape.
type Report struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	// ParentRunID is the inherited PG_RESCUE_RUN_ID when this run is nested.
	ParentRunID *string `json:"parent_run_id"`
	// Depth is the nesting depth: 1, or the inherited value plus one.
	Depth int `json:"depth"`
	// Chain is the chain name; null when the run used --handlers, in which
	// case Handlers holds the list.
	Chain    *string  `json:"chain"`
	Handlers []string `json:"handlers"`
	Mode     Mode     `json:"mode"`
	// Command is null in stdin mode.
	Command     *Command `json:"command"`
	Fingerprint string   `json:"fingerprint"`
	OutputFile  string   `json:"output_file"`
	// OutputTail is the last 200 lines, at most 64 KiB, redacted.
	OutputTail string `json:"output_tail"`
	Context    string `json:"context"`
	// Verify is the --verify command; null when the original argv is re-run.
	Verify    *string   `json:"verify"`
	Host      string    `json:"host"`
	StartedAt time.Time `json:"started_at"`
	// Attempts grows by exactly one per handler tried.
	Attempts []Attempt `json:"attempts"`
}

// Command is the wrapped command and what happened to it.
type Command struct {
	Argv []string `json:"argv"`
	Cwd  string   `json:"cwd"`
	// Exit is the command's exit code, or 128+signo if a signal killed it.
	Exit int `json:"exit"`
}

// Attempt is one handler's turn. Its top-level fields are FACTS the wrapper
// recorded; Reported holds the handler's own CLAIMS. Templates label the two
// differently.
type Attempt struct {
	Handler  string           `json:"handler"`
	Position int              `json:"position"`
	Tags     []string         `json:"tags"`
	Outcome  contract.Outcome `json:"outcome"`
	Reason   string           `json:"reason"`
	// Exit is the handler's exit code; -1 when it never exited normally
	// (timeout, signal, failure to spawn).
	Exit       int   `json:"exit"`
	DurationMS int64 `json:"duration_ms"`
	// VerifyMS is set only when verify ran.
	VerifyMS         *int64 `json:"verify_ms,omitempty"`
	StderrFile       string `json:"stderr_file"`
	VerifyOutputFile string `json:"verify_output_file,omitempty"`
	// Reported is the handler's claims, as classified.
	Reported contract.Reported `json:"reported"`
}
