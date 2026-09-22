package pgpr

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

// evalPgPR is the shared one-command driver, mirroring gh/ghstack/pnwf's evalGH /
// evalGhStack.
func evalPgPR(t *testing.T, cmd string) hookio.RuleResult {
	t.Helper()
	return hookio.Verdict(New().Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(cmd),
	}))
}

// TestPgPR_ReadOnlyPRVerbs_Approve pins the always-safe read-only band — files and
// commits, with every documented flag combination (--json, --base, both, neither) —
// to Approve. --json is the originally-reported gap (pg2-xf564).
func TestPgPR_ReadOnlyPRVerbs_Approve(t *testing.T) {
	cmds := []string{
		"pg-pr pr files",
		"pg-pr pr files --json",
		"pg-pr pr files --base develop",
		"pg-pr pr files --json --base develop",
		"pg-pr pr files --base develop --json",
		"pg-pr pr commits",
		"pg-pr pr commits --json",
		"pg-pr pr commits --base origin/main",
		"pg-pr pr commits --base origin/main --json",
	}
	for _, cmd := range cmds {
		if got := evalPgPR(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
		}
	}
}

// TestPgPR_WriteVerbs_Unclaimed pins every `pg-pr pr` WRITE verb — create, update,
// close, draft, automerge on/off, merge — to being left UNCLAIMED (NoOpinion via
// hookio.Verdict, the terminal value chain exhaustion produces): this rule must not
// itself manufacture an Approve for any of them, and must not be the one recording a
// NoOpinion VERDICT either (see the package doc — that distinction matters at the
// RuleResult/error level even though Verdict() cannot show it). Each needs its own
// future review, out of scope for this bead.
func TestPgPR_WriteVerbs_Unclaimed(t *testing.T) {
	cmds := []string{
		"pg-pr pr create --title x",
		"pg-pr pr create --title x --no-draft",
		"pg-pr pr create --title x --body y --reviewers a,b --labels c,d",
		"pg-pr pr update 7 --body newbody",
		"pg-pr pr close 7",
		"pg-pr pr draft 7",
		"pg-pr pr automerge on 7",
		"pg-pr pr automerge off 7",
		"pg-pr pr merge 7",
	}
	for _, cmd := range cmds {
		if got := evalPgPR(t, cmd); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want NoOpinion (unclaimed)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestPgPR_ReviewSubmitAndOtherResources_Unclaimed pins `pg-pr review submit` (the
// bead's own explicitly-named out-of-scope write verb) and every OTHER pg-pr
// resource/verb — review draft/post, comment add/resolve, and the non-`pr`
// subcommand families entirely — to being left unclaimed: none of them have
// resource == "pr", so this rule's resource check never reaches them.
func TestPgPR_ReviewSubmitAndOtherResources_Unclaimed(t *testing.T) {
	cmds := []string{
		"pg-pr review submit 7",
		"pg-pr review draft 7",
		"pg-pr review post 7",
		"pg-pr comment add 7 --body hi",
		"pg-pr comment resolve thread-id",
		"pg-pr worktree add 7",
		"pg-pr worktree list",
		"pg-pr branch detect",
		"pg-pr auth status",
		"pg-pr ci runs",
		"pg-pr config show",
		"pg-pr issue show 7",
		"pg-pr migrate feedback",
		"pg-pr version",
	}
	for _, cmd := range cmds {
		if got := evalPgPR(t, cmd); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want NoOpinion (unclaimed)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestPgPR_RetiredListAndView_Unclaimed pins `pg-pr pr list` and `pg-pr pr view` —
// the bead's OWN originally-stated read-only surface — to unclaimed. Both were
// deleted outright in commit 6a7647d9 ("delete retiring CLI command groups"),
// landed 2026-09-21, an ancestor of this branch and a day before this bead was
// filed. Re-reading packages/pg-pr/cmd/pg-pr/pr.go confirms today's `pr` cobra tree
// registers only files/commits (read) and
// create/update/close/draft/automerge/merge (write) — no list, no view. Approving a
// subcommand name pg-pr does not register would classify nothing (cobra refuses an
// unknown command before any side effect), and this rule must not carry a dormant
// Approve for a retired shape forward; see the package doc for the full account.
func TestPgPR_RetiredListAndView_Unclaimed(t *testing.T) {
	cmds := []string{
		"pg-pr pr list",
		"pg-pr pr list --json",
		"pg-pr pr view 7",
		"pg-pr pr view 7 --json",
	}
	for _, cmd := range cmds {
		if got := evalPgPR(t, cmd); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want NoOpinion (unclaimed — list/view are retired, not read-only-but-unimplemented)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestPgPR_UnknownPRSubcommand_Unclaimed pins a future/unrecognized `pr` subcommand
// to unclaimed rather than guessed at.
func TestPgPR_UnknownPRSubcommand_Unclaimed(t *testing.T) {
	cmds := []string{
		"pg-pr pr frobnicate",
		"pg-pr pr",
	}
	for _, cmd := range cmds {
		if got := evalPgPR(t, cmd); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want NoOpinion (unclaimed)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestPgPR_GlobalFlagBeforeOrInsidePath is the pg2-by1ij bypass-regression guard,
// scoped to pg-pr: a flag positioned before the resource word, or between the
// resource and verb words, must not defeat this rule's classification — the
// resolver (pgprCommandPath) must skip it the same way pg-pr's own cobra Find()
// does, rather than reading resource/subcmd off fixed positions. Every case here
// uses a flag that is NOT registered anywhere near `pr` in pg-pr's real cobra tree
// (--repo lives only on the create/update/close/draft/automerge/merge leaves, never
// on files/commits, and never on `pr` itself) — exactly the "unregistered, so
// cobra/this resolver treats it as value-consuming" shape the package doc measures.
func TestPgPR_GlobalFlagBeforeOrInsidePath(t *testing.T) {
	tests := []struct {
		cmd  string
		want hookio.Decision
	}{
		{"pg-pr --repo o/r pr files", hookio.Approve},
		{"pg-pr pr --repo o/r files", hookio.Approve},
		{"pg-pr --repo o/r pr commits --json", hookio.Approve},
		{"pg-pr --repo o/r pr create --title x", hookio.NoOpinion},
		{"pg-pr pr --repo o/r create --title x", hookio.NoOpinion},
	}
	for _, tt := range tests {
		got := evalPgPR(t, tt.cmd)
		if got.Decision != tt.want {
			t.Errorf("cmd %q: got %s (%s), want %s", tt.cmd, got.Decision, got.Reason, tt.want)
		}
	}
}

// TestPgPR_NonPgPrExecutable_Unclaimed pins that a lookalike command (a real `gh`
// invocation, or an unrelated binary) is never claimed by this rule, and that a
// full-path invocation of pg-pr is still recognized by basename.
func TestPgPR_NonPgPrExecutable_Unclaimed(t *testing.T) {
	cmds := []string{
		"gh pr view",
		"gh pr merge --auto",
		"echo pg-pr pr files", // "pg-pr" appears only as an argument to echo
	}
	for _, cmd := range cmds {
		if got := evalPgPR(t, cmd); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want NoOpinion (not this rule's business)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestPgPR_FullPathExecutable_StillMatches pins basename resolution: an absolute
// path to the pg-pr binary is still recognized.
func TestPgPR_FullPathExecutable_StillMatches(t *testing.T) {
	cmd := "/nix/store/abc123-pg-pr/bin/pg-pr pr files --json"
	if got := evalPgPR(t, cmd); got.Decision != hookio.Approve {
		t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
	}
}

// TestPgPR_CompoundLeaf_StillMatches pins that this rule's own for-loop over every
// parsed leaf reaches a pg-pr invocation even when it is not the first leaf in a
// compound command, mirroring gh-stack/pg-ccaudit's identical coverage.
func TestPgPR_CompoundLeaf_StillMatches(t *testing.T) {
	cmd := "export FOO=bar && pg-pr pr files --json"
	if got := evalPgPR(t, cmd); got.Decision != hookio.Approve {
		t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
	}
}

// TestPgPR_NonBashTool_Unclaimed pins that a non-Bash tool call is always unclaimed.
func TestPgPR_NonBashTool_Unclaimed(t *testing.T) {
	input := &hookio.HookInput{ToolName: "Read", ToolInput: mustJSON("")}
	if got := hookio.Verdict(New().Evaluate(input)).Decision; got != hookio.NoOpinion {
		t.Errorf("non-bash tool => %v, want NoOpinion", got)
	}
}

// TestPgPR_Name pins the rule's Name(), which internal/setup's ruleerrors_test.go
// keys its wantGenuineError map on.
func TestPgPR_Name(t *testing.T) {
	if got := New().Name(); got != "pg-pr" {
		t.Errorf("Name() = %q, want %q", got, "pg-pr")
	}
}

// TestPgPR_MalformedBashInput_GenuineError pins the ADR 0043 error-channel split
// directly (setup.ruleerrors_test.go's chain-wide sweep is the OTHER half of this
// coverage; this is the package-local version every sibling rule module also
// carries — see gh_test.go/ghstack_test.go's equivalents).
func TestPgPR_MalformedBashInput_GenuineError(t *testing.T) {
	_, err := New().Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: json.RawMessage(`{`),
	})
	if err == nil {
		t.Fatal("malformed Bash tool_input: got nil error, want a genuine failure")
	}
}
