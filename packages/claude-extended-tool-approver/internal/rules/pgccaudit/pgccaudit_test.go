package pgccaudit

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

// TestPgCCAudit_ApprovedSubcommands pins every read-only-classified pg-ccaudit
// shape prescribed by claude-marketplace/pg-ccaudit's tool-error-waste-review
// skill/command and improvement-retro command (the corpus pg2-amzvw's
// plugin-conformance-check flagged as unrecognized, bead pg2-xu4aq) to
// Approve, plus the rest of main.go's own read-only band (schema/version/help
// and gold/cost, which the current corpus does not happen to exercise in a
// fenced example).
func TestPgCCAudit_ApprovedSubcommands(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
	}{
		{"status", "pg-ccaudit status"},
		{"status with trailing shell comment", "pg-ccaudit status      # coverage + staleness; read it before anything else"},
		{"queries", "pg-ccaudit queries"},
		{"queries --verbose", "pg-ccaudit queries --verbose"},
		{"query with since/until", "pg-ccaudit query error-rate-by-tool --since 2026-07-22 --until 2026-07-30"},
		{"query top-signatures", "pg-ccaudit query top-signatures --since 2026-07-22 --until 2026-07-30"},
		{"query with quoted positional arg", "pg-ccaudit query session-concentration 'some-signature' --since 2026-07-22 --until 2026-07-30"},
		{"candidates", "pg-ccaudit candidates --since 2026-07-22 --until 2026-07-30"},
		{"classify bare status", "pg-ccaudit classify status --since 2026-07-22 --until 2026-07-30"},
		{"classify a real run", "pg-ccaudit classify --classifier cli --since 2026-07-22 --until 2026-07-30 --max 100"},
		{"report", "pg-ccaudit report --classifier cli --since 2026-07-22 --until 2026-07-30 --max 100"},
		{"evaluate", "pg-ccaudit evaluate --classifier cli --since 2026-07-22 --until 2026-07-30"},
		{"gold status", "pg-ccaudit gold status"},
		{"gold seed", "pg-ccaudit gold seed"},
		{"gold sample", "pg-ccaudit gold sample"},
		{"cost", "pg-ccaudit cost"},
		{"schema", "pg-ccaudit schema"},
		{"version", "pg-ccaudit version"},
		{"--version flag", "pg-ccaudit --version"},
		{"help", "pg-ccaudit help"},
		{"--help flag", "pg-ccaudit --help"},
		{"bare invocation", "pg-ccaudit"},
		// A leading compound leaf still approves: LeavesOf's typical production
		// caller (the engine) has already split the expression and hands each
		// leaf to Evaluate separately, but this rule's own for-loop over every
		// parsed leaf must independently reach the pg-ccaudit leaf too.
		{"compound with env prefix", "export FOO=bar && pg-ccaudit status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(tt.command)}
			if got := hookio.Verdict(r.Evaluate(input)).Decision; got != hookio.Approve {
				t.Errorf("%q => %v, want Approve", tt.command, got)
			}
		})
	}
}

// TestPgCCAudit_IngestAsks pins `pg-ccaudit ingest` (in every spelling this
// module recognizes) to Ask, per the tool-error-waste-review skill's explicit
// "do not run pg-ccaudit ingest yourself unless the operator asks" — a
// human-in-the-loop requirement, not a "this is dangerous" one, so it is Ask
// rather than Reject.
func TestPgCCAudit_IngestAsks(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
	}{
		{"bare ingest", "pg-ccaudit ingest"},
		{"ingest with flags", "pg-ccaudit ingest --root /home/user/.claude/projects"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(tt.command)}
			if got := hookio.Verdict(r.Evaluate(input)).Decision; got != hookio.Ask {
				t.Errorf("%q => %v, want Ask", tt.command, got)
			}
		})
	}
}

// TestPgCCAudit_ClassifyCmdEnvVarDefers pins pg2-tqles: pg-ccaudit classify
// execs the program PG_CCAUDIT_CLASSIFY_CMD names, so a pg-ccaudit leaf whose
// VISIBLE environment (own prefix, `env` words, earlier same-expression exports)
// assigns it must not be approved. Each row would be Approve without the guard
// (the same command minus the assignment is in ApprovedSubcommands). Defer is
// NoOpinion, which is stricter than Approve and leaves the verdict to the chain.
func TestPgCCAudit_ClassifyCmdEnvVarDefers(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
	}{
		{"inline prefix, bare classify", "PG_CCAUDIT_CLASSIFY_CMD=x pg-ccaudit classify"},
		{"inline prefix, classify run", "PG_CCAUDIT_CLASSIFY_CMD=x pg-ccaudit classify --classifier cli"},
		{"inline prefix, program with args", "PG_CCAUDIT_CLASSIFY_CMD='rm -rf /tmp/x' pg-ccaudit classify --classifier cli"},
		{"export then classify", "export PG_CCAUDIT_CLASSIFY_CMD=x; pg-ccaudit classify"},
		{"export then classify with &&", "export PG_CCAUDIT_CLASSIFY_CMD=x && pg-ccaudit classify --classifier cli"},
		{"export with unreadable value", "export PG_CCAUDIT_CLASSIFY_CMD=$PROG; pg-ccaudit classify"},
		{"bare export of the name", "export PG_CCAUDIT_CLASSIFY_CMD; pg-ccaudit classify"},
		{"declare -x then classify", "declare -x PG_CCAUDIT_CLASSIFY_CMD=x; pg-ccaudit classify"},
		{"allexport then plain assignment", "set -a; PG_CCAUDIT_CLASSIFY_CMD=x; pg-ccaudit classify"},
		{"env wrapper", "env PG_CCAUDIT_CLASSIFY_CMD=x pg-ccaudit classify"},
		{"other approved subcommand", "PG_CCAUDIT_CLASSIFY_CMD=x pg-ccaudit report --classifier cli"},
		{"bare invocation", "PG_CCAUDIT_CLASSIFY_CMD=x pg-ccaudit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(tt.command)}
			if got := hookio.Verdict(r.Evaluate(input)).Decision; got != hookio.NoOpinion {
				t.Errorf("%q => %v, want NoOpinion (defer)", tt.command, got)
			}
		})
	}
}

// TestPgCCAudit_ClassifyCmdEnvVarGuardIsNarrow pins the other half of pg2-tqles:
// without the variable assigned in the visible environment, nothing changes.
// Includes the shapes a too-broad guard would break: another variable, a
// shell-local (unexported) assignment that never reaches the child, an export in
// a pipeline stage (subshell), and ingest, which keeps its Ask.
func TestPgCCAudit_ClassifyCmdEnvVarGuardIsNarrow(t *testing.T) {
	r := New()
	approved := []struct {
		name    string
		command string
	}{
		{"classify status", "pg-ccaudit classify status"},
		{"classify", "pg-ccaudit classify"},
		{"report", "pg-ccaudit report"},
		{"version", "pg-ccaudit version"},
		{"unrelated variable inline", "FOO=x pg-ccaudit classify"},
		{"unrelated export", "export FOO=x; pg-ccaudit classify"},
		{"similarly-named variable", "PG_CCAUDIT_CLASSIFY_CMD_X=x pg-ccaudit classify"},
		{"unexported shell variable", "PG_CCAUDIT_CLASSIFY_CMD=x; pg-ccaudit classify"},
		{"export in a pipeline stage", "export PG_CCAUDIT_CLASSIFY_CMD=x | cat; pg-ccaudit classify"},
		{"export AFTER the leaf", "pg-ccaudit classify; export PG_CCAUDIT_CLASSIFY_CMD=x"},
	}
	for _, tt := range approved {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(tt.command)}
			if got := hookio.Verdict(r.Evaluate(input)).Decision; got != hookio.Approve {
				t.Errorf("%q => %v, want Approve", tt.command, got)
			}
		})
	}

	for _, command := range []string{
		"pg-ccaudit ingest",
		"PG_CCAUDIT_CLASSIFY_CMD=x pg-ccaudit ingest",
		"export PG_CCAUDIT_CLASSIFY_CMD=x; pg-ccaudit ingest",
	} {
		t.Run("ingest keeps Ask: "+command, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(command)}
			if got := hookio.Verdict(r.Evaluate(input)).Decision; got != hookio.Ask {
				t.Errorf("%q => %v, want Ask", command, got)
			}
		})
	}
}

// TestPgCCAudit_UnrelatedCommandsAbstain covers shapes this rule must NOT
// claim: an unlisted subcommand, a non-Bash tool, and a non-pg-ccaudit
// command entirely — all deferred (NoOpinion/not-applicable), unchanged.
func TestPgCCAudit_UnrelatedCommandsAbstain(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
	}{
		{"unlisted subcommand", "pg-ccaudit frobnicate"},
		{"unrelated command", "kubectl get pods"},
		{"similarly-prefixed but different basename", "pg-ccaudit-helper status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(tt.command)}
			if got := hookio.Verdict(r.Evaluate(input)).Decision; got != hookio.NoOpinion {
				t.Errorf("%q => %v, want NoOpinion", tt.command, got)
			}
		})
	}

	t.Run("non-bash tool", func(t *testing.T) {
		input := &hookio.HookInput{ToolName: "Read", ToolInput: mustJSON("")}
		if got := hookio.Verdict(r.Evaluate(input)).Decision; got != hookio.NoOpinion {
			t.Errorf("non-bash tool => %v, want NoOpinion", got)
		}
	})
}
