package deniedroots

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func newTestEvaluator(t *testing.T, deniedRoots string) *patheval.PathEvaluator {
	t.Helper()
	if deniedRoots != "" {
		t.Setenv("CETA_DENIED_ROOTS", deniedRoots)
	}
	return patheval.NewWithCWD("/Users/testuser/project", "/Users/testuser/project")
}

// TestRead_DeniedRoot_Reject pins the Read-tool half of pg2-fxu7k: a Read whose
// file_path names a machine-configured denied root is REJECTED, with a reason
// that names the offending root, the path, and the session cwd to resolve
// against instead — the "redirect message" the bead asks for.
func TestRead_DeniedRoot_Reject(t *testing.T) {
	pe := newTestEvaluator(t, "/home:/mnt:/repo")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/home/user/repo/main.go"}),
		CWD:       "/Users/testuser/project",
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.Reject {
		t.Fatalf("Read of a denied-root path: got %s, want reject (reason=%q)", got.Decision, got.Reason)
	}
	for _, want := range []string{"/home", "/Users/testuser/project", "/home/user/repo/main.go"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("reject reason %q does not mention %q", got.Reason, want)
		}
	}
}

// TestRead_NotDeniedRoot_NotApplicable is the false-positive guarantee: a Read
// whose path is NOT under any configured denied root abstains (NotApplicable),
// so path-safety and the rest of the chain still decide it normally.
func TestRead_NotDeniedRoot_NotApplicable(t *testing.T) {
	pe := newTestEvaluator(t, "/home:/mnt:/repo")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/Users/testuser/project/foo.go"}),
		CWD:       "/Users/testuser/project",
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion || got.Module != "" {
		t.Errorf("Read of a non-denied path: got %+v, want the exhaustion NoOpinion (this rule not-applicable)", got)
	}
}

// TestRead_Unconfigured_NeverRejects is the zero-false-positives-elsewhere
// guarantee: with NO CETA_DENIED_ROOTS configured at all (every machine that
// has not opted in), a path shaped exactly like the measured defect
// ("/home/...") is never rejected by this rule.
func TestRead_Unconfigured_NeverRejects(t *testing.T) {
	pe := newTestEvaluator(t, "")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/home/user/repo/main.go"}),
		CWD:       "/Users/testuser/project",
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision == hookio.Reject {
		t.Errorf("Read with no denied roots configured: got reject (reason=%q), want not-applicable", got.Reason)
	}
}

// TestBash_DeniedRoot_Reject covers the bead's stated larger marginal win: a
// Bash command whose argument names an absolute path under a denied root is
// REJECTED before safe-commands/path-safety ever run, across the argument
// shapes the corpus evidence and this rule's own scan cover.
func TestBash_DeniedRoot_Reject(t *testing.T) {
	pe := newTestEvaluator(t, "/home:/mnt:/repo")
	r := New(pe)
	tests := []struct {
		name    string
		command string
	}{
		{"bare positional", "cat /home/user/repo/file.go"},
		{"second denied root", "ls /repo/sub/dir"},
		{"third denied root", "grep -r foo /mnt/data"},
		{"glued flag value", "cat --file=/home/user/.bashrc"},
		{"redirection target", "cat file.go < /home/user/repo/file.go"},
		{"leaf inside a compound", "git status && cat /home/user/x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				ToolInput: mustJSON(hookio.BashToolInput{Command: tt.command}),
				CWD:       "/Users/testuser/project",
			}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != hookio.Reject {
				t.Errorf("%q: got %s, want reject (reason=%q)", tt.command, got.Decision, got.Reason)
			}
		})
	}
}

// TestBash_NotDeniedRoot_NotApplicable pins the abstain side: an ordinary Bash
// command with no denied-root reference must not be touched by this rule, so
// safe-commands still decides it.
func TestBash_NotDeniedRoot_NotApplicable(t *testing.T) {
	pe := newTestEvaluator(t, "/home:/mnt:/repo")
	r := New(pe)
	tests := []struct {
		name    string
		command string
	}{
		{"cwd-relative path", "cat ./foo.go"},
		{"absolute path outside every denied root", "cat /Users/testuser/project/foo.go"},
		{"bare command, no path arg", "ls -la"},
		// A look-alike sibling directory must not be swept in.
		{"look-alike sibling", "cat /homefoo/bar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				ToolInput: mustJSON(hookio.BashToolInput{Command: tt.command}),
				CWD:       "/Users/testuser/project",
			}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != hookio.NoOpinion || got.Module != "" {
				t.Errorf("%q: got %+v, want the exhaustion NoOpinion (this rule not-applicable)", tt.command, got)
			}
		})
	}
}

// TestBash_MessageArgCarveOut proves the cmdparse.SkipMessageArgs reuse: a
// commit message that merely MENTIONS a denied-root-shaped path in prose is not
// itself a path reference and must not be rejected — mirroring
// internal/rules/secrets' identical carve-out for the same class of Bash scan.
func TestBash_MessageArgCarveOut(t *testing.T) {
	pe := newTestEvaluator(t, "/home:/mnt:/repo")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(hookio.BashToolInput{Command: `git commit -m "the old code lived at /home/user/x, moved it"`}),
		CWD:       "/Users/testuser/project",
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision == hookio.Reject {
		t.Errorf("commit message mentioning a denied-root-shaped path: got reject (reason=%q), want not-applicable", got.Reason)
	}
}

// TestBash_PgCcauditQueryArgCarveOut pins pg2-21tke: pg-ccaudit query's
// positional parameter is a SQL LIKE-pattern query argument over an indexed
// column, not a filesystem path, so a denied-root-shaped value there must not
// be rejected — the exact false positive the bead reports
// ("pg-ccaudit query root-first-last-seen /home" denied for naming a
// fabricated /home root, even though the command never touches /home on
// disk).
func TestBash_PgCcauditQueryArgCarveOut(t *testing.T) {
	pe := newTestEvaluator(t, "/home:/mnt:/repo")
	r := New(pe)
	tests := []struct {
		name    string
		command string
	}{
		{"the bead's literal false positive", "pg-ccaudit query root-first-last-seen /home"},
		{"a different denied root as the LIKE pattern", "pg-ccaudit query root-first-last-seen /repo"},
		{"a sig-parameter query", `pg-ccaudit query session-concentration "/home%"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				ToolInput: mustJSON(hookio.BashToolInput{Command: tt.command}),
				CWD:       "/Users/testuser/project",
			}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision == hookio.Reject {
				t.Errorf("%q: got reject (reason=%q), want not-applicable", tt.command, got.Reason)
			}
		})
	}
}

// TestBash_PgCcauditOtherArgs_StillRejected proves the carve-out is narrow:
// pg-ccaudit's --db flag names a REAL database path pg-ccaudit opens, and a
// fabricated-root value there must still be rejected exactly like any other
// command's path argument — the carve-out removes only the one positional
// pgCcauditQueryArgIndex identifies, never the whole command line.
func TestBash_PgCcauditOtherArgs_StillRejected(t *testing.T) {
	pe := newTestEvaluator(t, "/home:/mnt:/repo")
	r := New(pe)
	tests := []struct {
		name    string
		command string
	}{
		{"--db value is a real path, still checked", "pg-ccaudit query root-first-last-seen /home --db /home/index.db"},
		{"an unrecognized subcommand keeps the old (safe) scan", "pg-ccaudit ingest /home/transcripts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{
				ToolName:  "Bash",
				ToolInput: mustJSON(hookio.BashToolInput{Command: tt.command}),
				CWD:       "/Users/testuser/project",
			}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != hookio.Reject {
				t.Errorf("%q: got %s, want reject (reason=%q)", tt.command, got.Decision, got.Reason)
			}
		})
	}
}

// TestBash_Unconfigured_NeverRejects mirrors TestRead_Unconfigured_NeverRejects
// for Bash: no CETA_DENIED_ROOTS configured means this rule is a no-op on every
// Bash call, including one shaped exactly like the measured defect.
func TestBash_Unconfigured_NeverRejects(t *testing.T) {
	pe := newTestEvaluator(t, "")
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(hookio.BashToolInput{Command: "cat /home/user/repo/file.go"}),
		CWD:       "/Users/testuser/project",
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision == hookio.Reject {
		t.Errorf("Bash with no denied roots configured: got reject (reason=%q), want not-applicable", got.Reason)
	}
}

// TestOtherTools_NotApplicable: this rule is scoped to Read and Bash only (per
// the bead) — a Write/Edit/Glob call, even one naming a denied-root path, is not
// this rule's jurisdiction.
func TestOtherTools_NotApplicable(t *testing.T) {
	pe := newTestEvaluator(t, "/home:/mnt:/repo")
	r := New(pe)
	tests := []struct {
		tool      string
		toolInput json.RawMessage
	}{
		{"Write", mustJSON(map[string]string{"file_path": "/home/user/x", "content": "y"})},
		{"Edit", mustJSON(map[string]string{"file_path": "/home/user/x", "old_string": "a", "new_string": "b"})},
		{"Glob", mustJSON(map[string]string{"pattern": "*.go", "path": "/home/user"})},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: tt.tool, ToolInput: tt.toolInput, CWD: "/Users/testuser/project"}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != hookio.NoOpinion || got.Module != "" {
				t.Errorf("%s: got %+v, want the exhaustion NoOpinion (this rule not-applicable)", tt.tool, got)
			}
		})
	}
}
