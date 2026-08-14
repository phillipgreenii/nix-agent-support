package gh

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

type stubResolver struct {
	currentBranch string
	runBranch     string
	currentErr    error
	runErr        error
}

func (s *stubResolver) CurrentBranch(cwd string) (string, error) {
	return s.currentBranch, s.currentErr
}

func (s *stubResolver) RunBranch(runID string) (string, error) {
	return s.runBranch, s.runErr
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestGH_ReadOnly_Approve(t *testing.T) {
	readOnly := []string{
		"gh pr view", "gh pr list", "gh pr status", "gh pr diff", "gh pr checks",
		"gh issue view", "gh issue list", "gh issue status",
		"gh repo view", "gh repo list",
		"gh run view", "gh run list",
		"gh release view", "gh release list",
		"gh search issues",
		"gh status",
		"gh auth status",
		"gh api /repos",
	}
	r := New(nil)
	for _, cmd := range readOnly {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestGH_Modifying_Ask(t *testing.T) {
	// `gh pr create` used to belong here. It has a draft-aware verdict of its own since
	// pg2-25oru (Approve with --draft, Reject without), pinned by the draft-first
	// fixtures below; `gh issue create` is deliberately untouched by that ruling.
	modifying := []string{
		"gh issue create",
	}
	r := New(nil)
	for _, cmd := range modifying {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s, want ask", cmd, got.Decision)
		}
	}
}

func TestGH_PrMerge_Reject(t *testing.T) {
	r := New(nil)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "gh pr merge"}),
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.Reject {
		t.Errorf("gh pr merge: got %s, want reject", got.Decision)
	}
}

func TestGH_PrMergeAuto_Abstain(t *testing.T) {
	r := New(nil)
	commands := []string{
		"gh pr merge --auto",
		"gh pr merge --squash --auto 1234",
		"gh pr merge --auto --delete-branch",
	}
	for _, cmd := range commands {
		input := &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
		}
		got := hookio.Verdict(r.Evaluate(input))
		if got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s, want abstain", cmd, got.Decision)
		}
	}
}

func TestGH_PrMergeAutoMerge_Reject(t *testing.T) {
	// --auto-merge is not a real gh flag, but guard against substring matching
	r := New(nil)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "gh pr merge --auto-merge"}),
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.Reject {
		t.Errorf("gh pr merge --auto-merge: got %s, want reject", got.Decision)
	}
}

// TestGH_DraftFirstRuledTable pins EVERY row of the operator ruling VERBATIM as the
// ruling table wrote it (2026-07-30, pg2-4yy4r item 2; implemented as pg2-25oru). The
// last two rows are the UNCHANGED ones and belong here precisely because they are
// unchanged: the `--auto` Abstain is only defensible while the `gh pr ready` Ask above it
// holds, so a change to either must be read against the whole table.
func TestGH_DraftFirstRuledTable(t *testing.T) {
	tests := []struct {
		cmd  string
		want hookio.Decision
	}{
		{"gh pr create --draft", hookio.Approve},
		{"gh pr create", hookio.Reject},
		{"gh pr create --web", hookio.Approve},
		{"gh pr ready", hookio.Ask},
		{"gh pr ready --undo", hookio.Approve},
		{"gh pr merge --auto", hookio.NoOpinion},
		{"gh pr merge", hookio.Reject},
	}
	for _, tt := range tests {
		got := evalGH(t, tt.cmd)
		if got.Decision != tt.want {
			t.Errorf("cmd %q: got %s (%s), want %s", tt.cmd, got.Decision, got.Reason, tt.want)
		}
	}
}

// TestGH_PrCreateDraft_Approve pins the draft spellings that MUST reach Approve. Every
// row was measured accepted by gh 2.97.0 on 2026-08-12 (see pr.go's spelling table): the
// short `-d` exists, so an exact-token `--draft` test would REJECT the blessed path;
// clusters are one token; and `-dtx` proves the arity truncation keeps a draft letter
// that sits BEFORE a value-taking one.
func TestGH_PrCreateDraft_Approve(t *testing.T) {
	cmds := []string{
		"gh pr create --draft",
		"gh pr create -d",
		"gh pr create -dw",  // clustered draft + web
		"gh pr create -wd",  // ... in the other order
		"gh pr create -df",  // clustered draft + fill
		"gh pr create -dtx", // cluster: -d boolean, then -t with value "x"
		"gh pr create --draft=true",
		"gh pr create --draft=1",
		"gh pr create --fill --draft",                        // flag AFTER another flag
		"gh pr create --title x --body y --draft",            // draft LAST, after values
		"gh pr create --draft --title x --body y",            // draft FIRST
		"gh pr create -t 'add d' --draft",                    // a `d` in a separated value
		"gh pr create --base main --head feat -d",            // short LAST
		"gh pr new --draft",                                  // the `new` alias
		"gh pr new -d",                                       //
		"gh pr create --draft --reviewer me --label bug -dw", // repeated, mixed forms
	}
	for _, cmd := range cmds {
		got := evalGH(t, cmd)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve — a draft create is the blessed landing step", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGH_PrCreateNonDraft_Reject pins the non-draft spellings that MUST reach Reject.
// The `-tdocs` / `-Rme/draft-tool` rows are the ARITY trap: an arity-blind letter scan
// finds a `d` inside a VALUE and would approve a mergeable PR (measured: `-tdocs` sets
// title "docs" and is not a draft). `--draft=false` / `--draft=0` are the pflag negation,
// and `--draft --draft=false` is pflag's last-one-wins, which cmdparse.HasLongFlag alone
// reads the other way round.
func TestGH_PrCreateNonDraft_Reject(t *testing.T) {
	cmds := []string{
		"gh pr create",
		"gh pr create --fill",
		"gh pr create --title x --body y",
		"gh pr create --draft=false",
		"gh pr create --draft=0",
		"gh pr create --draft=f",
		"gh pr create --title x --draft=false", // negation AFTER a value flag
		"gh pr create --draft --draft=false",   // pflag last-one-wins: NON-draft
		"gh pr create -tdocs --fill",           // `d` inside the -t VALUE
		"gh pr create -tdraft-support",         // ... spelling the whole word
		"gh pr create -Rme/draft-tool --fill",  // `d` inside the inherited -R VALUE
		"gh pr create -bfixed --title x",       // `d` inside the -b VALUE
		"gh pr create -t 'add d thing' --fill", // ... in a SEPARATED value
		"gh pr create --head feat --base main", //
		"gh pr create -- --draft",              // after end-of-options it is not a flag
		"gh pr new",                            // the `new` alias
		"gh pr new --fill",                     //
	}
	for _, cmd := range cmds {
		got := evalGH(t, cmd)
		if got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s (%s), want reject — a non-draft PR is immediately mergeable", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGH_PrCreateNonDraft_ReasonNamesRemedy pins the acceptance criterion on the REASON
// text, not just the level: a Reject is not user-overridable in-session, so the message
// is the only thing that tells the caller what to run instead. It must name the
// draft-first flow and BOTH halves of the two-step remedy.
func TestGH_PrCreateNonDraft_ReasonNamesRemedy(t *testing.T) {
	reason := evalGH(t, "gh pr create --fill").Reason
	for _, want := range []string{"DRAFT FIRST", "gh pr create --draft", "gh pr ready"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q does not name %q", reason, want)
		}
	}
}

// TestGH_PrCreateWeb_Approve pins the `--web` row. The CLI does not create the PR — the
// browser opens and the human picks draft-or-not — so this is human-in-the-loop by
// construction. This row was NOT explicitly ruled; see pr.go's WEB paragraph.
func TestGH_PrCreateWeb_Approve(t *testing.T) {
	cmds := []string{
		"gh pr create --web",
		"gh pr create -w",
		"gh pr create --fill --web",        // flag AFTER another flag
		"gh pr create --title x --web",     //
		"gh pr create --web --draft=false", // explicit non-draft, but still the browser
		"gh pr new -w",                     // the `new` alias
	}
	for _, cmd := range cmds {
		got := evalGH(t, cmd)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve — --web defers the draft choice to a human in the browser", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGH_PrReady_Ask is the row the whole design rests on: before pg2-25oru `gh pr ready`
// matched no branch and emitted `{}`, so create -> ready -> `merge --auto` ran end to end
// with no person in it. `--undo=false` is a mark-ready in pflag terms and must NOT reach
// the --undo Approve.
func TestGH_PrReady_Ask(t *testing.T) {
	cmds := []string{
		"gh pr ready",
		"gh pr ready 123",
		"gh pr ready feature-branch",
		"gh pr ready https://github.com/o/r/pull/123",
		"gh pr ready --repo o/r",
		"gh pr ready --undo=false",
		"gh pr ready --undo=0",
		"gh pr ready 123 --undo=false", // flag AFTER the positional
		"gh pr ready --undo --undo=false",
		"gh pr ready -- --undo", // after end-of-options it is not a flag
	}
	for _, cmd := range cmds {
		got := evalGH(t, cmd)
		if got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s (%s), want ask — marking ready is the single point at which a PR becomes mergeable", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGH_PrReadyUndo_Approve pins the reverse direction: `--undo` moves the PR AWAY from
// mergeable, which is where the draft-first flow wants it, and it is the documented
// repair when a PR came back non-draft.
func TestGH_PrReadyUndo_Approve(t *testing.T) {
	cmds := []string{
		"gh pr ready --undo",
		"gh pr ready --undo 123",
		"gh pr ready 123 --undo", // flag AFTER the positional
		"gh pr ready --undo=true",
		"gh pr ready --repo o/r --undo",
		"gh pr ready --undo=false --undo", // pflag last-one-wins: back to draft
	}
	for _, cmd := range cmds {
		got := evalGH(t, cmd)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve — --undo converts back to draft", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGH_PrMergeAutoNegated_Reject pins the spelling the deleted exact-token hasFlag got
// right only by accident and the new matcher must get right on purpose:
// `gh pr merge --auto=false` is an IMMEDIATE merge (pflag bool), so it must reach the
// Reject and not the --auto Abstain. `gh pr merge --auto=true` is the converse — a real
// --auto that an exact-token test would have Rejected.
func TestGH_PrMergeAutoNegated_Reject(t *testing.T) {
	for _, cmd := range []string{"gh pr merge --auto=false", "gh pr merge --auto=0", "gh pr merge --auto --auto=false"} {
		if got := evalGH(t, cmd); got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s (%s), want reject — an explicitly false --auto merges NOW", cmd, got.Decision, got.Reason)
		}
	}
	for _, cmd := range []string{"gh pr merge --auto=true", "gh pr merge --auto=1", "gh pr merge 123 --auto"} {
		if got := evalGH(t, cmd); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want abstain — this IS --auto", cmd, got.Decision, got.Reason)
		}
	}
}

// SEPARATED-VALUE FIXTURES (pg2-ylrda)
//
// A value passed as its OWN argv token can spell any flag, and pg2-25oru's scan modeled only a
// value GLUED to a short. So `gh pr create --title -d --body y` — which gh titles `-d` and
// creates a NON-DRAFT, immediately mergeable PR — read as a draft and was APPROVED: a false
// Approve on precisely the command that rule set exists to Reject, and worse than the Abstain
// fall-through pg2-by1ij closed, because Abstain at least lands on a lower floor.
//
// EVERY binding below is MEASURED on gh 2.97.0 (nixpkgs), 2026-08-12, via a mutual-exclusion
// message gh emits only while both tokens are still flags; pr.go's pg2-ylrda block records each
// probe and its decisive output. These fixtures assert the VERDICT; TestGH_FlagTokens asserts
// the walk that produces it, so a regression is reported as a parse fact too.

// TestGH_SeparatedValue_PrCreate is the bead's headline case plus the converse it must not
// break: a GENUINE draft/web flag standing AFTER a consumed value has to survive.
func TestGH_SeparatedValue_PrCreate(t *testing.T) {
	tests := []struct {
		cmd  string
		want hookio.Decision
		why  string
	}{
		// The defect: gh binds the `-d` as a VALUE and creates a MERGEABLE PR.
		{"gh pr create --title -d --body y", hookio.Reject, "`-d` is the --title VALUE, so this is a NON-draft create"},
		{"gh pr create -t -d", hookio.Reject, "the same defect through the SHORT spelling"},
		{"gh pr create -t -d --body y", hookio.Reject, ""},
		{"gh pr create --body -d --title x", hookio.Reject, "any value-taking flag, not only --title"},
		{"gh pr create --template -d --fill", hookio.Reject, ""},
		{"gh pr create --recover -d --fill", hookio.Reject, "a value-taking long with NO short form"},
		{"gh pr create -R -d --fill", hookio.Reject, "including the INHERITED -R/--repo"},
		{"gh pr create --repo -d --fill", hookio.Reject, ""},
		{"gh pr create --title -w --body y", hookio.Reject, "`-w` is the TITLE, so no browser ever opens"},
		{"gh pr create -t -w", hookio.Reject, ""},
		{"gh pr create --title --draft=false", hookio.Reject, "the title is the literal string `--draft=false`"},
		// A REAL flag after a consumed value must still be seen, or the fix would have bought
		// the Reject by making the blessed path unreachable.
		{"gh pr create --title x -d", hookio.Approve, "a real -d AFTER a consumed value"},
		{"gh pr create -t -d --draft", hookio.Approve, "`-t` ate `-d`; the `--draft` is real"},
		{"gh pr create --title -d --draft", hookio.Approve, ""},
		{"gh pr create --title=-d --draft", hookio.Approve, "'='-glued: the value binds in ONE token"},
		{"gh pr create -dt -d", hookio.Approve, "cluster: -d boolean, then -t eats the next token"},
		{"gh pr create --title x -w", hookio.Approve, "a real -w after a consumed value"},
		// Each no-value flag consumes NOTHING, which is what the complement table buys.
		{"gh pr create --fill -d", hookio.Approve, ""},
		{"gh pr create --dry-run -d", hookio.Approve, ""},
		{"gh pr create --fill-first -d", hookio.Approve, ""},
		{"gh pr create --fill-verbose -d", hookio.Approve, ""},
		{"gh pr create --no-maintainer-edit -d", hookio.Approve, ""},
		{"gh pr create --editor -d", hookio.Approve, ""},
		{"gh pr create --draft -d", hookio.Approve, ""},
		// `--` is NOT an end-of-options terminator while a value-taking flag is expecting a
		// value: measured, `gh pr create --title -- --draft --web` and `-t -- -d --web` BOTH
		// still report the draft+web conflict, so the `--draft`/`-d` after it really is a flag.
		{"gh pr create --title -- --draft", hookio.Approve, "the `--` was eaten as the title"},
		{"gh pr create -t -- --draft", hookio.Approve, ""},
		{"gh pr create -t -- -d", hookio.Approve, ""},
		// THE ONE ROW THIS CHANGE MAKES MORE PERMISSIVE, and it is a corrected FALSE REJECT,
		// not a weakened gate: measured, `gh pr create -d --title --draft=false --web` still
		// reports the draft+web conflict, so `--draft=false` is the TITLE and `-d` really does
		// make it a draft. pg2-25oru read the swallowed token as pflag's last-one-wins negation
		// and Rejected the blessed path. Every other direction of this change is strictly
		// stricter; see pr.go's arity block for why an unknown flag defaults to value-taking.
		{"gh pr create -d --title --draft=false", hookio.Approve, "the negation is the TITLE; `-d` stands"},
	}
	for _, tt := range tests {
		got := evalGH(t, tt.cmd)
		if got.Decision != tt.want {
			t.Errorf("cmd %q: got %s (%s), want %s — %s", tt.cmd, got.Decision, got.Reason, tt.want, tt.why)
		}
	}
	// The Reject must still NAME the draft-first flow, per the acceptance criterion: a Reject
	// is not user-overridable in-session, so the reason is the only remedy the caller gets.
	reason := evalGH(t, "gh pr create --title -d --body y").Reason
	for _, want := range []string{"DRAFT FIRST", "gh pr create --draft", "gh pr ready"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q does not name %q", reason, want)
		}
	}
}

// TestGH_SeparatedValue_PrReady pins the same class on the branch that matters most: a `--undo`
// swallowed as the value of -R/--repo is a MARK-READY, the single act the draft-first flow puts
// a person in front of, and it was taking the `--undo` Approve.
func TestGH_SeparatedValue_PrReady(t *testing.T) {
	for _, cmd := range []string{
		"gh pr ready -R --undo",
		"gh pr ready --repo --undo",
		"gh pr ready -R --undo 123",
	} {
		if got := evalGH(t, cmd); got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s (%s), want ask — the `--undo` is the REPO value, so this MARKS THE PR READY",
				cmd, got.Decision, got.Reason)
		}
	}
	for _, cmd := range []string{
		"gh pr ready -R o/r --undo", // a real --undo AFTER a consumed value
		"gh pr ready --repo o/r --undo",
		"gh pr ready -R -- --undo", // measured: `-R` eats the `--`, so `--undo` IS a flag
	} {
		if got := evalGH(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve — this `--undo` is a real flag", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGH_SeparatedValue_PrMerge pins the class on the `--auto` branch. Its Abstain is
// deliberate and defensible ONLY for a real `--auto`, which cannot merge a draft; an `--auto`
// swallowed as the merge body/subject/repo is an IMMEDIATE merge and must reach the Reject.
func TestGH_SeparatedValue_PrMerge(t *testing.T) {
	for _, cmd := range []string{
		"gh pr merge -b --auto",
		"gh pr merge --body --auto",
		"gh pr merge -t --auto",
		"gh pr merge --subject --auto",
		"gh pr merge -A --auto",
		"gh pr merge --author-email --auto",
		"gh pr merge -F --auto",
		"gh pr merge --body-file --auto",
		"gh pr merge --match-head-commit --auto",
		"gh pr merge -R --auto",
		"gh pr merge --repo --auto",
	} {
		if got := evalGH(t, cmd); got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s (%s), want reject — the `--auto` is a flag VALUE, so this merges NOW",
				cmd, got.Decision, got.Reason)
		}
	}
	for _, cmd := range []string{
		"gh pr merge -b x --auto", // a real --auto AFTER a consumed value
		"gh pr merge -R o/r --auto",
		"gh pr merge -d --auto", // -d/--delete-branch is BOOLEAN here, unlike create's -d
		"gh pr merge -m --auto", // -m is --merge here, but --milestone (value-taking) on create
		"gh pr merge -r --auto", // -r is --rebase here, but --reviewer (value-taking) on create
		"gh pr merge -s --auto",
		"gh pr merge --delete-branch --auto",
	} {
		if got := evalGH(t, cmd); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want abstain — this IS --auto", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGH_SeparatedValue_IssueCreate pins the ONE gated branch this class cannot reach, so the
// gap is recorded rather than discovered later. `gh issue create` is a flat Ask that reads no
// flag at all, so no value — however it is spelled — can move its verdict; measured,
// `gh issue create --title --web` answers "must provide `--title` and `--body`", i.e. gh really
// does bind `--web` as the title, and the verdict is Ask either way. What would make it
// reachable: a ruling that makes this branch flag-aware, at which point it needs an
// `issueCreateArity` table measured the same way (`gh issue create --help`: the no-value flags
// are -e/--editor, -w/--web and --help; every other flag there takes a value).
func TestGH_SeparatedValue_IssueCreate(t *testing.T) {
	for _, cmd := range []string{
		"gh issue create --title --web",
		"gh issue create -t --web",
		"gh issue create --title x --body y",
		"gh issue create -R --title",
	} {
		if got := evalGH(t, cmd); got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s (%s), want ask — this branch reads no flag, so a separated value cannot move it",
				cmd, got.Decision, got.Reason)
		}
	}
}

// TestGH_FlagTokens pins the arity walk itself, so a regression is reported as a PARSE fact and
// not only as a changed verdict — the same reason TestGH_CommandPath and TestGH_ParseGhAPICall
// exist. Each expectation follows from a MEASURED binding recorded in pr.go's pg2-ylrda block.
func TestGH_FlagTokens(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		arity ghFlagArity
		want  []string
	}{
		{
			"separated long value is dropped",
			[]string{"--title", "-d", "--body", "y"},
			prCreateArity,
			[]string{"--title", "--body"},
		},
		{
			"'='-glued long keeps its value",
			[]string{"--title=-d", "--draft"},
			prCreateArity,
			[]string{"--title=-d", "--draft"},
		},
		{"bare short takes the next token", []string{"-t", "-d"}, prCreateArity, []string{"-"}},
		{"a cluster's LAST letter takes it too", []string{"-dt", "-d"}, prCreateArity, []string{"-d"}},
		{"glued short value consumes nothing", []string{"-tdocs"}, prCreateArity, []string{"-"}},
		{"a boolean letter BEFORE the value survives", []string{"-dtx"}, prCreateArity, []string{"-d"}},
		{"an all-boolean cluster is untouched", []string{"-dw"}, prCreateArity, []string{"-dw"}},
		{
			"a no-value long consumes nothing",
			[]string{"--draft", "-d"},
			prCreateArity,
			[]string{"--draft", "-d"},
		},
		{
			"pflag hands `--` to a waiting flag as its VALUE",
			[]string{"--title", "--", "--draft"},
			prCreateArity,
			[]string{"--title", "--draft"},
		},
		{"... and to a waiting short", []string{"-t", "--", "-d"}, prCreateArity, []string{"-", "-d"}},
		{"a REAL `--` stops the walk", []string{"--", "-d"}, prCreateArity, []string{"--"}},
		{"a lone `-` is an operand", []string{"-", "-d"}, prCreateArity, []string{"-", "-d"}},
		{
			"a trailing value-taking long has nothing to eat",
			[]string{"--title"},
			prCreateArity,
			[]string{"--title"},
		},
		{"a trailing value-taking short likewise", []string{"-t"}, prCreateArity, []string{"-"}},
		{
			"an UNKNOWN long defaults to value-taking",
			[]string{"--new-thing", "-d"},
			prCreateArity,
			[]string{"--new-thing"},
		},
		// An unknown letter is value-taking, so it ends the cluster AND eats a separated value.
		// Both rows lose a signal rather than invent one — the fail-closed direction: here the
		// second `-d` is dropped, which can only make the verdict stricter.
		{
			"an UNKNOWN short letter ends the cluster",
			[]string{"-dZx", "-d"},
			prCreateArity,
			[]string{"-d", "-d"},
		},
		{
			"... and eats a separated value when last",
			[]string{"-dZ", "-d"},
			prCreateArity,
			[]string{"-d"},
		},
		{
			"operands are kept in place",
			[]string{"--repo", "o/r", "123", "--draft"},
			prCreateArity,
			[]string{"--repo", "123", "--draft"},
		},
		// The other two tables, whose whole point is that they differ from create's.
		{"ready: -R eats the --undo", []string{"-R", "--undo"}, prReadyArity, []string{"-"}},
		{"ready: --undo eats nothing", []string{"--undo", "123"}, prReadyArity, []string{"--undo", "123"}},
		{"merge: -b eats the --auto", []string{"-b", "--auto"}, prMergeArity, []string{"-"}},
		{"merge: -d is BOOLEAN here", []string{"-d", "--auto"}, prMergeArity, []string{"-d", "--auto"}},
		{"merge: -m/-r are BOOLEAN here", []string{"-mr", "--auto"}, prMergeArity, []string{"-mr", "--auto"}},
		{
			"merge: booleans plus an operand",
			[]string{"--squash", "--auto", "1234"},
			prMergeArity,
			[]string{"--squash", "--auto", "1234"},
		},
		{"no args at all", nil, prCreateArity, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ghFlagTokens(tt.args, tt.arity); !slices.Equal(got, tt.want) {
				t.Errorf("ghFlagTokens(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

// evalGH is the shared one-command driver for the pg2-cl0v2 `gh api` fixtures.
func evalGH(t *testing.T, cmd string) hookio.RuleResult {
	t.Helper()
	return hookio.Verdict(New(nil).Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": cmd}),
		CWD:       "/tmp/test-repo",
	}))
}

// TestGH_ApiMutatingMethod_NotApproved is the pg2-cl0v2 core guard: an explicit
// mutating -X/--method, in EVERY spelling gh accepts, must not be auto-approved.
// The four `-X`/`--method` rows marked "probed" measured `allow` on 2026-07-30 with
// the blanket "read-only gh api" approval this replaces.
func TestGH_ApiMutatingMethod_NotApproved(t *testing.T) {
	cmds := []string{
		"gh api -X PATCH repos/o/r/pulls/5 -f draft=false", // probed VERBATIM
		"gh api -X POST repos/o/r/pulls -f title=x",        // probed VERBATIM
		"gh api --method=PATCH repos/o/r/pulls/5",          // =-glued long
		"gh api -XPATCH repos/o/r/pulls/5",                 // value glued to the short
		"gh api -X=PATCH repos/o/r/pulls/5",                // pflag's =-glued short
		"gh api -iXPATCH repos/o/r/pulls/5",                // bool short clustered ahead
		"gh api -iX PATCH repos/o/r/pulls/5",               // cluster + separated value
		"gh api -X DELETE repos/o/r/issues/1/labels/bug",
		"gh api --method DELETE repos/o/r/git/refs/heads/feat",
		"gh api -X post repos/o/r/pulls", // gh upper-cases; so must the rule
		"gh api -X PUT repos/o/r/branches/main/protection",
		"gh api -X TEAPOT repos/o/r", // unknown verb: fail closed, not approved
		"gh api -X",                  // no value at all: fail closed
	}
	for _, cmd := range cmds {
		got := evalGH(t, cmd)
		if got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got APPROVE (%s) — a mutating gh api must never be auto-approved", cmd, got.Reason)
		}
	}
}

// TestGH_ApiFlagPositionIndependent pins that the method flag is found WHEREVER it
// appears in the argv — after the endpoint as readily as before it.
func TestGH_ApiFlagPositionIndependent(t *testing.T) {
	cmds := []string{
		"gh api repos/o/r/pulls/5 --method PATCH",
		"gh api repos/o/r/pulls/5 -X PATCH",
		"gh api repos/o/r/pulls/5 -XPATCH",
		"gh api --paginate repos/o/r/pulls/5 -H 'Accept: application/json' -X PATCH",
		"gh api -H 'Accept: application/json' repos/o/r/pulls/5 --method=PATCH",
	}
	for _, cmd := range cmds {
		got := evalGH(t, cmd)
		if got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got APPROVE (%s) — the method flag must be found in any argv position", cmd, got.Reason)
		}
	}
}

// TestGH_ApiBodyParamIsPost pins gh's DOCUMENTED default: with no explicit -X, any
// of -f/-F/--field/--raw-field/--input makes the request a POST. Each row was
// MEASURED sending `> POST` on gh 2.96.0, 2026-07-30 (see api.go's doc block), so
// none of these may be approved as read-only.
func TestGH_ApiBodyParamIsPost(t *testing.T) {
	cmds := []string{
		"gh api repos/o/r/pulls -f title=x",
		"gh api repos/o/r/issues -F body=x",
		"gh api repos/o/r/issues --field body=x",
		"gh api repos/o/r/issues --raw-field body=x",
		"gh api repos/o/r/pulls --input payload.json",
		"gh api repos/o/r/pulls --input=payload.json",
		"gh api -f title=x repos/o/r/pulls",  // body param BEFORE the endpoint
		"gh api -ftitle=x repos/o/r/pulls",   // value glued to the short
		"gh api graphql -f query=mutation{}", // GraphQL always POSTs
	}
	for _, cmd := range cmds {
		got := evalGH(t, cmd)
		if got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got APPROVE (%s) — a body parameter makes gh api POST by default", cmd, got.Reason)
		}
	}
}

// TestGH_ApiReadOnly_Approve is the pg2-cl0v2 REGRESSION GUARD: gh api is the normal
// way to READ the API and must keep its Approve. The `-X GET` rows are the measured
// precedence case — an explicit GET BEATS the -f POST default, sending the params as
// a query string. The `-H`/`-q`/`-p`/`-t` rows are the FLAG-ARITY guard: those shorts
// take a value, and a value containing an `X` (or an `f`) must not be scanned as more
// flag letters and manufacture a false gate.
func TestGH_ApiReadOnly_Approve(t *testing.T) {
	cmds := []string{
		"gh api repos/o/r/pulls/5", // probed VERBATIM
		"gh api /repos",
		"gh api -X GET repos/o/r/pulls",
		"gh api --method GET repos/o/r/pulls",
		"gh api --method=GET repos/o/r/pulls",
		"gh api -XGET repos/o/r/pulls",
		"gh api -X get repos/o/r/pulls",                    // gh upper-cases to GET
		"gh api -X GET search/issues -f q=repo:cli/cli",    // explicit GET beats -f
		"gh api -X HEAD repos/o/r",                         //
		"gh api 'repos/o/r/pulls?state=open&per_page=100'", // read-only query params
		"gh api repos/o/r/pulls --jq '.[].number'",         //
		"gh api --paginate repos/o/r/pulls",                //
		"gh api -H 'X-Foo: PUT' repos/o/r",                 // header VALUE holds X and PUT
		"gh api -HX-Foo:PUT repos/o/r",                     // ... glued to the short
		"gh api -q '.forks' repos/o/r",                     //
		"gh api -p corsair repos/o/r",                      //
		"gh api -t '{{.name}}' repos/o/r",                  //
		"gh api --cache 60m repos/o/r",                     // separated long value
		"gh api --hostname github.com repos/o/r",           //
		"gh api -i repos/o/r",                              // the one boolean short
		"gh api repos/{owner}/{repo}/pulls/5/merge",        // merge endpoint, but a GET
	}
	for _, cmd := range cmds {
		got := evalGH(t, cmd)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve (read-only gh api)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGH_ApiPullRequestMerge_Reject pins the PATH-AWARE half of the pg2-cl0v2
// verdict. `gh api --method PUT repos/o/r/pulls/5/merge` measured `allow` on
// 2026-07-30 while the sibling `gh pr merge` branch REJECTS the identical
// operation. The verdict must not be weaker than the control it would bypass, so
// this is a Reject and not the generic Ask — see apiVerdict's doc comment.
func TestGH_ApiPullRequestMerge_Reject(t *testing.T) {
	cmds := []string{
		"gh api --method PUT repos/o/r/pulls/5/merge", // probed VERBATIM
		"gh api -XPUT repos/o/r/pulls/5/merge",
		"gh api --method=PUT repos/o/r/pulls/5/merge",
		"gh api -X PUT repos/o/r/pulls/5/merge",
		"gh api -X PUT /repos/o/r/pulls/5/merge",           // leading slash
		"gh api -X PUT repos/{owner}/{repo}/pulls/5/merge", // gh placeholders
		"gh api -X PUT 'repos/o/r/pulls/5/merge?foo=bar'",  // trailing query string
		"gh api repos/o/r/pulls/5/merge -X PUT",            // flag after the endpoint
		"gh api -X PUT repos/o/r/pulls/5/merge -f merge_method=squash",
	}
	for _, cmd := range cmds {
		got := evalGH(t, cmd)
		if got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s (%s), want reject — it is the operation `gh pr merge` is rejected for", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGH_ApiOtherMutation_Ask pins the PATH-BLIND floor: every mutation that is not one
// of the two operations with a control of its own gets the conservative Ask, matching
// the verdict the equivalent porcelain already carries (`gh issue create` is Ask).
//
// `POST .../pulls` LEFT THIS TABLE IN pg2-h8h3f and is now Rejected without
// `draft=true`, so it lives in TestGH_ApiCreate_MirrorsPrCreateVerdict and
// TestGH_ApiPullRequestCreate_DraftAware. It was here because pg2-cl0v2 could not read
// the draft VALUE and so could not follow the porcelain; that was a capability gap, never
// a ruling, and the row moved the moment the gap closed. `PATCH .../pulls/5 -f draft=false`
// STAYS: GitHub's REST PATCH cannot convert a PR to draft, so it is not the create
// operation and has no draft-first control to mirror.
//
// `gh api -X POST repos/o/r/merges` also stays, deliberately — see IsPullRequestMerge for
// why the merge Reject is not widened to it without an operator ruling.
func TestGH_ApiOtherMutation_Ask(t *testing.T) {
	cmds := []string{
		"gh api -X PATCH repos/o/r/pulls/5 -f draft=false",
		"gh api repos/o/r/issues -f title=x",
		"gh api graphql -f query=mutation{}",
		"gh api -X DELETE repos/o/r/git/refs/heads/feat",
		"gh api -X POST repos/o/r/merges -f base=main", // deliberately NOT the merge Reject
	}
	for _, cmd := range cmds {
		got := evalGH(t, cmd)
		if got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s (%s), want ask (generic gh api mutation)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGH_ApiMutation_TextIsNotAnOperation pins that the verdict keys on PARSED
// tokens and never on command TEXT. pg2-5b901 is the live precedent for the failure
// mode: primarycommit hard-denied a `bd update` whose ARGUMENT TEXT documented a
// commit. A mutating gh api spelling QUOTED in a commit message, a bd body, or a gh
// pr comment is text, and MUST NOT be gated by this rule.
func TestGH_ApiMutation_TextIsNotAnOperation(t *testing.T) {
	cmds := []string{
		`bd comment pg2-cl0v2 -m "gh api --method PUT repos/o/r/pulls/5/merge is prohibited"`,
		`bd update pg2-cl0v2 --notes "never gh api -X POST repos/o/r/pulls -f title=x"`,
		`git commit -m "gate gh api -X PUT .../merge"`,
		`gh pr comment 5 --body "do not run gh api --method PUT repos/o/r/pulls/5/merge"`,
		`gh pr view --json title --jq '.title'`,
	}
	for _, cmd := range cmds {
		got := evalGH(t, cmd)
		if got.Decision == hookio.Reject || got.Decision == hookio.Ask {
			t.Errorf("cmd %q: got %s (%s) — a mutating spelling appearing as TEXT must not be gated", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGH_ApiMerge_MirrorsPrMergeVerdict is the CONSISTENCY guard the acceptance
// criterion asks for: the gh api merge verdict must never be WEAKER than the
// `gh pr merge` verdict it would otherwise bypass. Written as a comparison so that
// relaxing either branch alone fails here, not just in the branch's own test.
func TestGH_ApiMerge_MirrorsPrMergeVerdict(t *testing.T) {
	porcelain := evalGH(t, "gh pr merge")
	viaAPI := evalGH(t, "gh api --method PUT repos/o/r/pulls/5/merge")
	if viaAPI.Decision < porcelain.Decision {
		t.Errorf("gh api merge is WEAKER than gh pr merge: api=%s (%s) vs porcelain=%s (%s)",
			viaAPI.Decision, viaAPI.Reason, porcelain.Decision, porcelain.Reason)
	}
}

// TestGH_ApiCreate_MirrorsPrCreateVerdict is the same consistency guard for the OTHER
// control the blanket approval bypassed, and it is an EXACT MIRROR again (pg2-h8h3f).
//
// ITS HISTORY IS THE POINT, so it is recorded rather than lost to a rename. pg2-cl0v2
// landed it as an exact mirror of `gh pr create`, which was then one undifferentiated Ask.
// pg2-25oru gave the porcelain TWO verdicts (Approve with --draft, Reject without) but
// could not follow it here, because parseGhAPICall read GitHub's `draft` only as a PRESENCE
// boolean; so the test was WEAKENED to TestGH_ApiCreate_NotWeakerThanDraftCreate, which
// asserted only "strictly more restrictive than the blessed draft create, and at least the
// Ask floor". pg2-h8h3f added the body-parameter VALUE reader, which is the capability the
// weakening was waiting on, so the exact mirror — and the name — are restored.
//
// EVERY ROW IS A RELATION, NOT A HARDCODED VERDICT: each asserts that the api spelling and
// the porcelain spelling of ONE operation reach the SAME decision. Written that way,
// retuning the draft-first ruling moves both together and this test keeps passing, while
// relaxing EITHER path alone fails here — which is the property a `want` column cannot
// express. The absolute levels are pinned by prCreateVerdict's own fixtures and by
// TestGH_ApiPullRequestCreate_DraftAware.
func TestGH_ApiCreate_MirrorsPrCreateVerdict(t *testing.T) {
	mirrors := []struct {
		what      string
		porcelain string
		viaAPI    string
	}{
		{
			what:      "the blessed DRAFT create",
			porcelain: "gh pr create --draft",
			viaAPI:    "gh api -X POST repos/o/r/pulls -f draft=true -f title=x",
		},
		{
			what:      "draft ABSENT",
			porcelain: "gh pr create --title x",
			viaAPI:    "gh api -X POST repos/o/r/pulls -f title=x",
		},
		{
			what:      "draft explicitly FALSE",
			porcelain: "gh pr create --draft=false --title x",
			viaAPI:    "gh api -X POST repos/o/r/pulls -f draft=false -f title=x",
		},
	}
	for _, m := range mirrors {
		gotAPI, gotPorcelain := evalGH(t, m.viaAPI), evalGH(t, m.porcelain)
		if gotAPI.Decision != gotPorcelain.Decision {
			t.Errorf("%s: the two spellings of one operation DISAGREE — api %q got %s (%s), porcelain %q got %s (%s)",
				m.what, m.viaAPI, gotAPI.Decision, gotAPI.Reason, m.porcelain, gotPorcelain.Decision, gotPorcelain.Reason)
		}
	}
}

// TestGH_ApiCreate_UnreadableBodyHoldsTheAskFloor pins the ONE case pg2-h8h3f leaves short
// of the mirror above, so the residual is asserted rather than merely described.
//
// `--input payload.json` and `-F draft=@file` put the draft value OUTSIDE argv — measured,
// and for `--input` measured twice over, since it also DEMOTES an argv `-f draft=true` to a
// query-string parameter while the body still comes wholly from the file. With no readable
// value the choice is between Reject (which would refuse a legitimate draft create with no
// in-session override — the objection that created this gap) and the pg2-cl0v2 Ask floor.
// It is the floor.
//
// So the assertion is a RANGE, not equality: at least Ask, and never Approve. That
// deliberately admits a future Reject if `--input` bodies ever become readable, without
// admitting the Approve that would be the hole.
func TestGH_ApiCreate_UnreadableBodyHoldsTheAskFloor(t *testing.T) {
	cmds := []string{
		"gh api -X POST repos/o/r/pulls --input payload.json",
		"gh api -X POST repos/o/r/pulls --input=payload.json",
		"gh api -X POST repos/o/r/pulls --input -",
		// MEASURED: with --input present the -f is a QUERY-STRING parameter, so this is NOT
		// a readable draft=true and must not reach the Approve.
		"gh api -X POST repos/o/r/pulls --input payload.json -f draft=true",
		"gh api -X POST repos/o/r/pulls -F draft=@draft.txt -f title=x",
	}
	for _, cmd := range cmds {
		got := evalGH(t, cmd)
		if got.Decision < hookio.Ask {
			t.Errorf("cmd %q: got %s (%s) — an unreadable draft value must hold at least the pg2-cl0v2 Ask floor",
				cmd, got.Decision, got.Reason)
		}
	}
}

// TestGH_ApiPullRequestCreate_DraftAware pins the raw-API draft gate's own three verdicts
// across every spelling gh accepts for a body parameter, and INDEPENDENTLY OF FLAG POSITION
// — the acceptance criterion that the verdict not depend on where in argv the parameter
// sits. Every spelling below was MEASURED on gh 2.97.0, 2026-08-14; see api.go's BODY
// PARAMETERS block for the dumped request bodies.
func TestGH_ApiPullRequestCreate_DraftAware(t *testing.T) {
	tests := []struct {
		cmd  string
		want hookio.Decision
	}{
		// draft=true, in each spelling that carries a value.
		{"gh api -X POST repos/o/r/pulls -f draft=true", hookio.Approve},
		{"gh api -X POST repos/o/r/pulls -F draft=true", hookio.Approve},
		{"gh api -X POST repos/o/r/pulls --field draft=true", hookio.Approve},
		{"gh api -X POST repos/o/r/pulls --raw-field draft=true", hookio.Approve},
		{"gh api -X POST repos/o/r/pulls --field=draft=true", hookio.Approve}, // =-glued long
		{"gh api -X POST repos/o/r/pulls -fdraft=true", hookio.Approve},       // glued to the short
		{"gh api -X POST repos/o/r/pulls -f draft=1", hookio.Approve},         // ParseBool truthy
		{"gh api -X POST repos/o/r/pulls -f draft='true'", hookio.Approve},    // glued quotes (unwrapGluedQuotes)
		{`gh api -X POST repos/o/r/pulls -f draft="true"`, hookio.Approve},    //
		// POSITION INDEPENDENCE: before the endpoint, after it, and with other flags between.
		{"gh api -X POST -f draft=true repos/o/r/pulls", hookio.Approve},
		{"gh api -f draft=true -X POST repos/o/r/pulls", hookio.Approve},
		{"gh api repos/o/r/pulls -f title=x -X POST -f draft=true", hookio.Approve},
		{"gh api -X POST repos/o/r/pulls -H 'Accept: application/json' -f draft=true", hookio.Approve},
		// The POST default: -f alone makes it a POST, so no -X is needed to reach the gate.
		{"gh api repos/o/r/pulls -f draft=true", hookio.Approve},
		{"gh api repos/o/r/pulls -f title=x", hookio.Reject},
		// draft present but FALSE, in each spelling.
		{"gh api -X POST repos/o/r/pulls -f draft=false", hookio.Reject},
		{"gh api -X POST repos/o/r/pulls -F draft=false", hookio.Reject},
		{"gh api -X POST repos/o/r/pulls --raw-field draft=0", hookio.Reject},
		{"gh api -X POST repos/o/r/pulls -f draft=", hookio.Reject},        // an empty STRING, not the bare flag
		{"gh api -X POST repos/o/r/pulls -f draft=garbage", hookio.Reject}, // unparseable -> not true
		// draft absent entirely.
		{"gh api -X POST repos/o/r/pulls", hookio.Reject},
		{"gh api -X POST repos/o/r/pulls -f title=x -f body=y", hookio.Reject},
		{"gh api -X post repos/o/r/pulls -f title=x", hookio.Reject}, // gh upper-cases the method
		{"gh api -X POST /repos/o/r/pulls -f title=x", hookio.Reject},
		{"gh api -X POST repos/{owner}/{repo}/pulls -f title=x", hookio.Reject},
		// A `draft` on the WRONG endpoint is not this gate: these keep the generic Ask.
		{"gh api -X PATCH repos/o/r/pulls/5 -f draft=false", hookio.Ask},
		{"gh api -X POST repos/o/r/pulls/5/reviews -f body=x", hookio.Ask},
		// ... and a GET of the collection is still just a read.
		{"gh api repos/o/r/pulls", hookio.Approve},
	}
	for _, tt := range tests {
		got := evalGH(t, tt.cmd)
		if got.Decision != tt.want {
			t.Errorf("cmd %q: got %s (%s), want %s", tt.cmd, got.Decision, got.Reason, tt.want)
		}
	}
}

// TestGH_ApiGraphQLRead_Approve is the pg2-44dsd win, and every row is a shape MEASURED in
// the asklog corpus (see graphql.go's doc block for the query and the counts).
//
// THE FIRST TWO GROUPS ARE THE MEASUREMENT. 236 of the 576 logged `gh api graphql`
// invocations use the bare `{ … }` shorthand with NO operation keyword, and 142 use the
// explicit `query` keyword; together they are the 66% that were paying an Ask.
//
// THE LAST GROUP IS THE pg2-5b901 TRAP, and it is why this is a scanner: in each row the
// word `mutation` appears in the document TEXT without being an operation. Measured
// accepted by gh 2.97.0 on 2026-08-14.
func TestGH_ApiGraphQLRead_Approve(t *testing.T) {
	cmds := []string{
		// THE GLUED-QUOTE SPELLING FIRST, because it is 567 of the 576 measured rows: the
		// quoted segment starts AFTER the `=`, which cmdparse does not unquote — see
		// unwrapGluedQuotes. If these regress, the fix wins nothing on real traffic.
		"gh api graphql -f query='{ viewer { login } }'",
		"gh api graphql -f query='{ rateLimit { cost remaining resetAt } }'",
		`gh api graphql -f query='{ repository(owner:"cli",name:"cli"){ pullRequests(first:1){ nodes{ number } } } }'`, // inner " inside outer '
		`gh api graphql -f query="{ viewer { login } }"`,
		// The bare shorthand — the operation keyword is OPTIONAL.
		"gh api graphql -f query={viewer{login}}",
		"gh api graphql -f 'query={ viewer { login } }'",
		"gh api graphql -f 'query={ rateLimit { cost remaining resetAt } }'",
		// The explicit keyword, named operations, variables and directives.
		"gh api graphql -f 'query=query { viewer { login } }'",
		"gh api graphql -f 'query=query Me { viewer { login } }'",
		`gh api graphql -f 'query=query($search: String!) { search(query: $search, type: ISSUE, first: 50) { issueCount } }' -f search=foo`,
		`gh api graphql -F query='query Q($n: Int = 5) { repository(owner:"o") { pullRequests(first: $n) { nodes { number } } } }'`,
		"gh api graphql -f 'query=query { a } fragment F on T { b }'", // a fragment alongside a query
		"gh api graphql -f 'query=query @skip(if: false) { viewer { login } }'",
		// Variables supplied from a FILE are irrelevant: a query cannot be turned into a
		// mutation by its variable values, and the DOCUMENT is still argv-visible.
		"gh api graphql -f 'query={ viewer { login } }' -F variables=@vars.json",
		// The pg2-5b901 trap: `mutation` as TEXT, never as an operation.
		`gh api graphql -f 'query={ repository(owner:"o",name:"r") { mutation } }'`,                                   // a FIELD named mutation
		`gh api graphql -f 'query=query($mutation: String) { search(query: $mutation, type: ISSUE) { issueCount } }'`, // a VARIABLE named mutation
		"gh api graphql -f 'query=# mutation\n{ viewer { login } }'",                                                  // in a COMMENT
		`gh api graphql -f 'query={ search(query: "mutation { x }", type: ISSUE) { issueCount } }'`,                   // in a STRING
		"gh api graphql -f 'query=query mutation { viewer { login } }'",                                               // an OPERATION NAMED mutation
	}
	for _, cmd := range cmds {
		got := evalGH(t, cmd)
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve — a read-only GraphQL document is not a write (pg2-44dsd)",
				cmd, got.Decision, got.Reason)
		}
	}
}

// TestGH_ApiGraphQL_NotApproved is the fail-safe half of pg2-44dsd: everything that is a
// GraphQL WRITE, or that cannot be SHOWN to be a read, must not be auto-approved.
//
// The `@file` and `--input` rows are the bead's explicit requirement. MEASURED, gh 2.97.0,
// 2026-08-14: `-F query=@q.graphql` really does read the file (body
// `{"query":"{ viewer { login } }"}`), so the document is genuinely not in argv and no
// scanner can see it. `-f query=@q.graphql` sends the literal string `@q.graphql` instead,
// which is not a GraphQL document either — the same Ask, by a different route.
func TestGH_ApiGraphQL_NotApproved(t *testing.T) {
	cmds := []string{
		// Genuine mutations.
		"gh api graphql -f query=mutation{}",
		"gh api graphql -f 'query=mutation { addStar(input: {starrableId: \"x\"}) { clientMutationId } }'",
		"gh api graphql -f 'query=mutation M($id: ID!) { convertPullRequestToDraft(input: {pullRequestId: $id}) { clientMutationId } }'",
		"gh api graphql -f 'query=query A { a } mutation B { b }'", // multi-operation: any mutation wins
		"gh api graphql -f 'query=subscription { x }'",             // grouped with writes deliberately
		// The document is NOT argv-visible.
		"gh api graphql -F query=@q.graphql",
		"gh api graphql --field query=@q.graphql",
		"gh api graphql --input payload.json",
		"gh api graphql --input -",
		"gh api graphql --input payload.json -f query={viewer{login}}", // --input wins; measured
		"gh api graphql -f query=@q.graphql",                           // a literal `@…`, not a document
		// No document at all, or one that does not scan. A BARE `gh api graphql` is absent
		// deliberately: with no body parameter it is an effective GET (measured), so the
		// incumbent method reading approves it as a read and this bead does not change that
		// — it sends no document, and GitHub's GraphQL endpoint refuses GET, so it is inert.
		"gh api graphql -f foo=bar",
		"gh api graphql -f 'query={ viewer { login }'",    // unbalanced
		"gh api graphql -f 'query=type Query { a: Int }'", // SDL, not an executable document
		"gh api graphql -f 'query=fragment F on T { a }'", // fragments only: no operation
		`gh api graphql -f 'query={ a(x: "unterminated) { b } }'`,
	}
	for _, cmd := range cmds {
		got := evalGH(t, cmd)
		if got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got APPROVE (%s) — only a document PROVEN read-only may be approved (pg2-44dsd fail-safe)",
				cmd, got.Reason)
		}
		if got.Decision < hookio.Ask {
			t.Errorf("cmd %q: got %s (%s) — must hold at least the pg2-cl0v2 Ask floor, never the Abstain that an auto-approving session accepts",
				cmd, got.Decision, got.Reason)
		}
	}
}

// TestGH_ApiGraphQLCreatePullRequest_Pinned holds the level pg2-h8h3f chose for the GraphQL
// half of PR creation. `gh api graphql` carrying a `createPullRequest` mutation creates a PR
// exactly as `POST .../pulls` does, but its draft argument lives in the document — often
// behind a variable whose value is a separate `-f variables=<json>` blob — so there is no
// argv-visible VALUE to sort Approve from Reject on. It is therefore PINNED at Ask: never
// Approve (which the pg2-44dsd read branch must never reach for it), and never the Abstain
// an auto-approving session accepts. See apiGraphQLVerdict for what would justify Reject.
//
// The assertion is EXACT on Ask, not a range, because that is what "pinned" means here: a
// future Reject is a ruling, and a ruling should have to update this test.
func TestGH_ApiGraphQLCreatePullRequest_Pinned(t *testing.T) {
	cmds := []string{
		`gh api graphql -f 'query=mutation { createPullRequest(input: {repositoryId: "x", title: "t", headRefName: "h", baseRefName: "main"}) { pullRequest { number } } }'`,
		`gh api graphql -f 'query=mutation { createPullRequest(input: {repositoryId: "x", draft: true}) { pullRequest { number } } }'`,
		`gh api graphql -f 'query=mutation New($in: CreatePullRequestInput!) { createPullRequest(input: $in) { pullRequest { number } } }' -f in={}`,
		// The pin is checked BEFORE the read classification, so even a document that would
		// otherwise scan as a query cannot carry the name into an Approve.
		`gh api graphql -f 'query={ createPullRequest { number } }'`,
	}
	for _, cmd := range cmds {
		got := evalGH(t, cmd)
		if got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s (%s), want ask — the GraphQL PR create is pinned at the Ask floor (pg2-h8h3f)",
				cmd, got.Decision, got.Reason)
		}
	}
}

// TestGH_ApiGraphQLCreate_NotWeakerThanApiCreate is the CONSISTENCY guard between the two
// raw-API routes to one operation, written as a comparison so relaxing either alone fails
// here. A `createPullRequest` GraphQL mutation must never be less restrictive than a
// `POST .../pulls` whose draft value is equally unreadable — both are "this creates a PR and
// we cannot see whether it is a draft", so both must land on the same floor.
func TestGH_ApiGraphQLCreate_NotWeakerThanApiCreate(t *testing.T) {
	unreadableREST := evalGH(t, "gh api -X POST repos/o/r/pulls --input payload.json")
	viaGraphQL := evalGH(t, `gh api graphql -f 'query=mutation { createPullRequest(input: {repositoryId: "x"}) { pullRequest { number } } }'`)
	if viaGraphQL.Decision < unreadableREST.Decision {
		t.Errorf("the GraphQL PR create is WEAKER than the equally-unreadable REST create: graphql=%s (%s) vs rest=%s (%s)",
			viaGraphQL.Decision, viaGraphQL.Reason, unreadableREST.Decision, unreadableREST.Reason)
	}
}

// TestGH_ClassifyGraphQLDocument pins the scanner directly, so a regression is reported as a
// classification fact and not only as a changed verdict — the same reason
// TestGH_ParseGhAPICall exists for the arity walk.
//
// The `wantNames` column is only asserted for the rows where it matters (the
// `createPullRequest` pin), because Names is a full token set and pinning it everywhere
// would fail on any harmless scanning change.
func TestGH_ClassifyGraphQLDocument(t *testing.T) {
	tests := []struct {
		name     string
		doc      string
		wantKind graphqlDocKind
		wantPR   bool
	}{
		{"bare shorthand", "{ viewer { login } }", graphqlRead, false},
		{"shorthand, no spaces", "{viewer{login}}", graphqlRead, false},
		{"query keyword", "query { viewer { login } }", graphqlRead, false},
		{"named query", "query Me { viewer { login } }", graphqlRead, false},
		{"variables", "query Q($n: Int!) { x(n: $n) { y } }", graphqlRead, false},
		{"variable default is an object literal", "query Q($f: F = {a: 1}) { x }", graphqlRead, false},
		{"directive on the operation", "query @skip(if: false) { x }", graphqlRead, false},
		{"leading comment", "# mutation\n{ viewer { login } }", graphqlRead, false},
		{"trailing comment", "{ viewer { login } } # mutation", graphqlRead, false},
		{"field named mutation", `{ repository(owner:"o") { mutation } }`, graphqlRead, false},
		{"variable named mutation", "query($mutation: String) { x(q: $mutation) }", graphqlRead, false},
		{"operation named mutation", "query mutation { x }", graphqlRead, false},
		{"mutation inside a string", `{ search(query: "mutation { x }") { n } }`, graphqlRead, false},
		{"mutation inside a block string", `{ search(query: """mutation { x }""") { n } }`, graphqlRead, false},
		{"query plus fragment", "query { ...F } fragment F on T { a }", graphqlRead, false},
		{"inline fragment", "{ node { ... on PullRequest { number } } }", graphqlRead, false},
		{"list argument", "{ x(ids: [1, 2, 3]) { y } }", graphqlRead, false},
		{"commas as whitespace", "{ a, b, c }", graphqlRead, false},

		{"mutation keyword", "mutation { addStar(input: {starrableId: \"x\"}) { id } }", graphqlWrite, false},
		{"mutation, empty selection", "mutation{}", graphqlWrite, false},
		{"named mutation", "mutation M { x }", graphqlWrite, false},
		{"subscription counts as a write", "subscription { x }", graphqlWrite, false},
		{"query then mutation", "query A { a } mutation B { b }", graphqlWrite, false},
		{"mutation then query", "mutation B { b } query A { a }", graphqlWrite, false},

		{"empty", "", graphqlOpaque, false},
		{"whitespace only", "   \n\t ", graphqlOpaque, false},
		{"comment only", "# nothing here", graphqlOpaque, false},
		{"fragments only: no operation", "fragment F on T { a }", graphqlOpaque, false},
		{"SDL is not executable", "type Query { a: Int }", graphqlOpaque, false},
		{"unbalanced braces", "{ viewer { login }", graphqlOpaque, false},
		{"stray closer", "} { a }", graphqlOpaque, false},
		{"mismatched closer", "{ a ) }", graphqlOpaque, false},
		{"unterminated string", `{ a(x: "oops) { b } }`, graphqlOpaque, false},
		{"unterminated block string", `{ a(x: """oops) { b } }`, graphqlOpaque, false},
		{"a literal @file reference, not a document", "@q.graphql", graphqlOpaque, false},
		{"header never opens a selection set", "query Q($n: Int!)", graphqlOpaque, false},

		{"createPullRequest mutation", `mutation { createPullRequest(input: {repositoryId: "x", draft: true}) { pullRequest { number } } }`, graphqlWrite, true},
		{"createPullRequest through a variable", "mutation M($in: CreatePullRequestInput!) { createPullRequest(input: $in) { pullRequest { number } } }", graphqlWrite, true},
		// The name in a STRING is not a token, so the pin does NOT fire — the same
		// discipline as the Kind classification.
		{"createPullRequest inside a string", `{ search(query: "createPullRequest") { n } }`, graphqlRead, false},
		{"createPullRequest inside a comment", "# createPullRequest\n{ viewer { login } }", graphqlRead, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyGraphQLDocument(tt.doc)
			if got.Kind != tt.wantKind {
				t.Errorf("classifyGraphQLDocument(%q).Kind = %d, want %d", tt.doc, got.Kind, tt.wantKind)
			}
			if got.CreatesPullRequest() != tt.wantPR {
				t.Errorf("classifyGraphQLDocument(%q).CreatesPullRequest() = %v, want %v", tt.doc, got.CreatesPullRequest(), tt.wantPR)
			}
		})
	}
}

// TestGH_ParseGhAPICallBodyParams pins the body-parameter VALUE reader (pg2-h8h3f) directly,
// including which of its three states each spelling produces. Every expectation is MEASURED
// against gh 2.97.0, 2026-08-14 — see api.go's BODY PARAMETERS block for the dumped bodies.
//
// THE THREE STATES ARE THE WHOLE POINT: a presence boolean cannot express the middle one,
// and it is the middle one that made a Reject unsafe before this existed.
func TestGH_ParseGhAPICallBodyParams(t *testing.T) {
	tests := []struct {
		args      []string
		param     string
		wantValue string
		wantState bodyParamState
	}{
		{[]string{"-f", "draft=true", "repos/o/r/pulls"}, "draft", "true", bodyParamValue},
		{[]string{"-f", "draft=false", "repos/o/r/pulls"}, "draft", "false", bodyParamValue},
		{[]string{"-F", "draft=false", "repos/o/r/pulls"}, "draft", "false", bodyParamValue},
		{[]string{"--field", "draft=true", "repos/o/r/pulls"}, "draft", "true", bodyParamValue},
		{[]string{"--raw-field", "draft=true", "repos/o/r/pulls"}, "draft", "true", bodyParamValue},
		{[]string{"--field=draft=true", "repos/o/r/pulls"}, "draft", "true", bodyParamValue},
		{[]string{"-fdraft=true", "repos/o/r/pulls"}, "draft", "true", bodyParamValue},
		{[]string{"-f", "draft=", "repos/o/r/pulls"}, "draft", "", bodyParamValue},
		// A value containing '=' keeps everything after the FIRST one — a GraphQL document
		// routinely holds one.
		{[]string{"-f", "query={ a(x: 1) }"}, "query", "{ a(x: 1) }", bodyParamValue},
		// Absent, and KNOWN absent: the whole body is argv-visible.
		{[]string{"-f", "title=x", "repos/o/r/pulls"}, "draft", "", bodyParamAbsent},
		{[]string{"repos/o/r/pulls"}, "draft", "", bodyParamAbsent},
		// `@` is a FILE reference for -F/--field ONLY.
		{[]string{"-F", "draft=@d.txt", "repos/o/r/pulls"}, "draft", "", bodyParamUnreadable},
		{[]string{"--field", "query=@q.graphql", "graphql"}, "query", "", bodyParamUnreadable},
		// ... and a LITERAL for -f/--raw-field, so its value really is visible.
		{[]string{"-f", "query=@q.graphql", "graphql"}, "query", "@q.graphql", bodyParamValue},
		// --input hides the body wholly, and DEMOTES an argv -f to a query-string parameter,
		// so it wins over one.
		{[]string{"--input", "body.json", "repos/o/r/pulls"}, "draft", "", bodyParamUnreadable},
		{[]string{"--input=body.json", "repos/o/r/pulls"}, "draft", "", bodyParamUnreadable},
		{[]string{"--input", "-", "repos/o/r/pulls"}, "draft", "", bodyParamUnreadable},
		{[]string{"--input", "body.json", "-f", "draft=true", "repos/o/r/pulls"}, "draft", "", bodyParamUnreadable},
		// A parameter with no '=' is not a parameter; gh refuses the spelling.
		{[]string{"-f", "draft", "repos/o/r/pulls"}, "draft", "", bodyParamAbsent},
		// THE GLUED-QUOTE REPAIR (unwrapGluedQuotes). cmdparse strips quotes only when the
		// WHOLE token is quoted, and `key='value'` is 98% of real `gh api graphql` traffic —
		// so these rows are the ones the measured win depends on.
		{[]string{"-f", "query='{ viewer { login } }'"}, "query", "{ viewer { login } }", bodyParamValue},
		{[]string{"-f", `query="{ viewer { login } }"`}, "query", "{ viewer { login } }", bodyParamValue},
		{[]string{"-f", `query='{ repository(owner:"o") { x } }'`}, "query", `{ repository(owner:"o") { x } }`, bodyParamValue},
		{[]string{"-f", "draft='true'", "repos/o/r/pulls"}, "draft", "true", bodyParamValue},
		{[]string{"-F", "query='@q.graphql'", "graphql"}, "query", "", bodyParamUnreadable}, // still a file reference
		// DECLINED, and each declining leaves the caller on its restrictive branch: a value
		// whose interior holds the wrapper character is a multi-segment concatenation this
		// deliberately does not reconstruct, and a value genuinely wrapped in the OTHER
		// quote keeps its inner quotes so it fails ParseBool / does not scan as GraphQL.
		{[]string{"-f", "title='a'x'b'", "repos/o/r/pulls"}, "title", "'a'x'b'", bodyParamValue},
		{[]string{"-f", `draft="'true'"`, "repos/o/r/pulls"}, "draft", "'true'", bodyParamValue},
		{[]string{"-f", "draft='true", "repos/o/r/pulls"}, "draft", "'true", bodyParamValue}, // unbalanced
	}
	for _, tt := range tests {
		call := parseGhAPICall(tt.args)
		gotValue, gotState := call.bodyParam(tt.param)
		if gotValue != tt.wantValue || gotState != tt.wantState {
			t.Errorf("parseGhAPICall(%q).bodyParam(%q) = (%q, %d), want (%q, %d)",
				tt.args, tt.param, gotValue, gotState, tt.wantValue, tt.wantState)
		}
	}
}

// TestGH_ParseGhAPICall pins the resolved method and endpoint directly, so a
// regression in the arity walk is reported as a parse fact rather than only as a
// changed verdict. Every expectation is MEASURED against gh 2.96.0 — see api.go's
// doc block for the command that produced each request line.
func TestGH_ParseGhAPICall(t *testing.T) {
	tests := []struct {
		args         []string
		wantMethod   string
		wantEndpoint string
	}{
		{[]string{"repos/o/r/pulls/5"}, "GET", "repos/o/r/pulls/5"},
		{[]string{"-X", "PATCH", "repos/o/r/pulls/5", "-f", "draft=false"}, "PATCH", "repos/o/r/pulls/5"},
		{[]string{"--method", "PUT", "repos/o/r/pulls/5/merge"}, "PUT", "repos/o/r/pulls/5/merge"},
		{[]string{"-X", "POST", "repos/o/r/pulls", "-f", "title=x"}, "POST", "repos/o/r/pulls"},
		{[]string{"-XPUT", "repos/o/r/pulls/5/merge"}, "PUT", "repos/o/r/pulls/5/merge"},
		{[]string{"--method=PUT", "repos/o/r/pulls/5/merge"}, "PUT", "repos/o/r/pulls/5/merge"},
		{[]string{"-X=PUT", "repos/o/r/pulls/5/merge"}, "PUT", "repos/o/r/pulls/5/merge"},
		{[]string{"-iXPUT", "repos/o/r/pulls/5/merge"}, "PUT", "repos/o/r/pulls/5/merge"},
		{[]string{"-iX", "PUT", "repos/o/r/pulls/5/merge"}, "PUT", "repos/o/r/pulls/5/merge"},
		{[]string{"-X", "get", "repos/o/r/pulls"}, "get", "repos/o/r/pulls"},
		// The -f POST default, and the explicit GET that BEATS it.
		{[]string{"repos/o/r/pulls", "-f", "title=x"}, "POST", "repos/o/r/pulls"},
		{[]string{"--input", "/dev/null", "repos/o/r/pulls"}, "POST", "repos/o/r/pulls"},
		{[]string{"-X", "GET", "repos/o/r/pulls", "-f", "state=open"}, "GET", "repos/o/r/pulls"},
		// Flag ARITY: a value-taking short's VALUE is never scanned for flag letters,
		// and a separated long value is never mistaken for the endpoint.
		{[]string{"-H", "X-Foo: PUT", "repos/o/r"}, "GET", "repos/o/r"},
		{[]string{"-HX-Foo:PUT", "repos/o/r"}, "GET", "repos/o/r"},
		{[]string{"--cache", "60m", "repos/o/r"}, "GET", "repos/o/r"},
		{[]string{"-q", ".forks", "repos/o/r"}, "GET", "repos/o/r"},
		{[]string{"-i", "repos/o/r"}, "GET", "repos/o/r"}, // boolean short: endpoint still found
		// Last -X wins, matching pflag's last-one-wins for a repeated scalar flag.
		{[]string{"-X", "GET", "-X", "PUT", "repos/o/r"}, "PUT", "repos/o/r"},
		// End-of-options: the next token is the endpoint even so.
		{[]string{"--", "repos/o/r"}, "GET", "repos/o/r"},
		// -X with no value at all: Method stays "" and IsMutating fails closed.
		{[]string{"-X"}, "", ""},
	}
	for _, tt := range tests {
		got := parseGhAPICall(tt.args)
		if got.Method != tt.wantMethod || got.Endpoint != tt.wantEndpoint {
			t.Errorf("parseGhAPICall(%q) = {method:%q endpoint:%q}, want {method:%q endpoint:%q}",
				tt.args, got.Method, got.Endpoint, tt.wantMethod, tt.wantEndpoint)
		}
	}
}

// TestGH_GlobalFlagBeforeCommandPath is the pg2-by1ij regression fixture. cobra lets a
// global flag precede — or sit inside — gh's command path, and while Evaluate read the path
// positionally EVERY such spelling resolved `resource` to a flag, matched no branch and
// reached the final Abstain, which an auto-approving session accepts. It bypassed the whole
// rule, so this pins one row per branch that has a NON-ABSTAIN verdict, not just pr.
//
// Each row is stated as a PAIR and asserted twice: the prefixed spelling must reach the
// named verdict AND the same verdict as its plain form. Written that way, a future change
// that relaxed both together — the failure mode a lone `want` table cannot see — still
// fails the plain-form fixtures elsewhere in this file, while a change that reopens the
// bypass fails the parity half here.
//
// Every `--repo`/`-R`/`-X` spelling below was MEASURED ACCEPTED by gh 2.97.0 on 2026-08-12
// (see gh.go's measurement table) EXCEPT the three rows marked NO --repo: measured, gh
// answers `unknown flag: --repo` for `status`, `auth status` and `repo view`, none of which
// takes that flag (`gh repo view` names its repository POSITIONALLY). Those three pin the
// EXTRACTION rather than a runnable spelling, and they are here because their branches
// Approve — a reading of the path that reached them by a different route must still land on
// the same verdict. `search` is NOT one of them: `gh search issues --repo` is its own flag
// and the spelling runs.
func TestGH_GlobalFlagBeforeCommandPath(t *testing.T) {
	tests := []struct {
		plain    string
		prefixed string
		want     hookio.Decision
	}{
		// The draft-first gate (pg2-25oru), in every spelling of the inherited flag.
		{"gh pr create", "gh --repo o/r pr create", hookio.Reject},
		{"gh pr create", "gh --repo=o/r pr create", hookio.Reject},
		{"gh pr create", "gh -R o/r pr create", hookio.Reject},
		{"gh pr create", "gh -Ro/r pr create", hookio.Reject},
		{"gh pr create", "gh -R=o/r pr create", hookio.Reject},
		{"gh pr create", "gh pr --repo o/r create", hookio.Reject}, // INSIDE the path
		{"gh pr create", "gh pr -R o/r create", hookio.Reject},     //
		{"gh pr new", "gh --repo o/r pr new", hookio.Reject},       // the `new` alias
		{"gh pr create --draft", "gh --repo o/r pr create --draft", hookio.Approve},
		{"gh pr create -d", "gh -R o/r pr create -d", hookio.Approve},
		{"gh pr create -d", "gh -Ro/r pr create -d", hookio.Approve},
		{"gh pr create --web", "gh --repo o/r pr create --web", hookio.Approve},
		{"gh pr ready", "gh --repo o/r pr ready", hookio.Ask},
		{"gh pr ready --undo", "gh --repo o/r pr ready --undo", hookio.Approve},
		// The landed merge controls.
		{"gh pr merge", "gh --repo o/r pr merge", hookio.Reject},
		{"gh pr merge", "gh pr -R o/r merge", hookio.Reject},
		{"gh pr merge --auto", "gh --repo o/r pr merge --auto", hookio.NoOpinion},
		{"gh pr merge --auto=false", "gh --repo o/r pr merge --auto=false", hookio.Reject},
		// `gh api` (pg2-cl0v2). The PUT row is the LIVE route: measured, `gh -X PUT api
		// repos/o/r/pulls/5/merge` dumps `> PUT /api/v3/repos/o/r/pulls/5/merge`, so the
		// method really is honoured from before the `api` word — which is why apiVerdict
		// is handed the argv WITH those flags and not the branches' `rest`.
		{"gh api -X PUT repos/o/r/pulls/5/merge", "gh -X PUT api repos/o/r/pulls/5/merge", hookio.Reject},
		{"gh api -XPUT repos/o/r/pulls/5/merge", "gh -XPUT api repos/o/r/pulls/5/merge", hookio.Reject},
		// Reject since pg2-h8h3f: a raw-API create with no `draft=true` is the same
		// operation `gh pr create` is Rejected for. It was Ask here only while the draft
		// body parameter was unreadable.
		{"gh api -X POST repos/o/r/pulls -f title=x", "gh -X POST api repos/o/r/pulls -f title=x", hookio.Reject},
		{"gh api -X POST repos/o/r/pulls -f draft=true", "gh -X POST api repos/o/r/pulls -f draft=true", hookio.Approve},
		{"gh api repos/o/r", "gh --hostname github.com api repos/o/r", hookio.Approve},
		// The pg2-44dsd GraphQL read, which must survive the same pre-path flag skipping.
		{"gh api graphql -f query={viewer{login}}", "gh --hostname github.com api graphql -f query={viewer{login}}", hookio.Approve},
		// The read-only branches.
		{"gh issue create", "gh --repo o/r issue create", hookio.Ask},
		{"gh pr view", "gh --repo o/r pr view", hookio.Approve},
		{"gh pr list", "gh -R o/r pr list", hookio.Approve},
		{"gh issue list", "gh -R o/r issue list", hookio.Approve},
		{"gh run list", "gh --repo o/r run list", hookio.Approve},
		{"gh release list", "gh --repo o/r release list", hookio.Approve},
		{"gh search issues", "gh -R o/r search issues", hookio.Approve},
		{"gh repo view", "gh --repo o/r repo view", hookio.Approve},     // NO --repo
		{"gh auth status", "gh --repo o/r auth status", hookio.Approve}, // NO --repo
		{"gh status", "gh --repo o/r status", hookio.Approve},           // NO --repo
	}
	for _, tt := range tests {
		gotPrefixed := evalGH(t, tt.prefixed)
		if gotPrefixed.Decision != tt.want {
			t.Errorf("cmd %q: got %s (%s), want %s — a global flag before the command path must not bypass the rule",
				tt.prefixed, gotPrefixed.Decision, gotPrefixed.Reason, tt.want)
		}
		if gotPlain := evalGH(t, tt.plain); gotPrefixed.Decision != gotPlain.Decision {
			t.Errorf("cmd %q got %s but its plain form %q got %s — the two spellings run the same command",
				tt.prefixed, gotPrefixed.Decision, tt.plain, gotPlain.Decision)
		}
	}
}

// TestGH_GlobalFlagBeforeApi_NotAbstain pins the ONE parity gap pg2-by1ij leaves, so it is
// recorded rather than discovered later. `--repo` is not an inherited flag of `gh api`
// (measured: `gh --repo o/r api repos/o/r` answers `unknown flag: --repo`), so it is absent
// from api.go's MEASURED arity tables and parseGhAPICall reads it as boolean — which makes
// its VALUE the endpoint operand, exactly the mis-attribution that file's doc already
// records. The direction is the safe one and this asserts it: the verdict is the generic
// mutation Ask, never Approve and never the fall-through Abstain that was the bypass.
//
// It is not closed by adding `repo`/`R` to those tables because they are read off
// `gh api --help` and gh REFUSES this spelling outright, so the entry would encode a flag
// api does not have in order to sharpen a command that cannot run. What would justify it:
// gh making `--repo` inherited by `api`, which is a re-measurement of `gh api --help`.
func TestGH_GlobalFlagBeforeApi_NotAbstain(t *testing.T) {
	cmds := []string{
		"gh --repo o/r api repos/o/r/pulls/5/merge -X PUT",
		"gh -R o/r api repos/o/r/pulls -f title=x",
	}
	for _, cmd := range cmds {
		got := evalGH(t, cmd)
		if got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s (%s), want ask — the endpoint is mis-attributed by design, but the mutation floor must hold",
				cmd, got.Decision, got.Reason)
		}
	}
}

// TestGH_CommandPath pins the resolution itself, so a regression is reported as a PARSE
// fact and not only as a changed verdict — the same reason TestGH_ParseGhAPICall exists.
// Every expectation is MEASURED against gh 2.97.0, 2026-08-12; gh.go's block above
// ghNoValueLongFlags names the command that produced each reading.
func TestGH_CommandPath(t *testing.T) {
	tests := []struct {
		name             string
		args             []string
		resource, subcmd string
		resourceArgs     []string
		rest             []string
	}{
		{
			name: "plain path is unchanged by the skip", args: []string{"pr", "create"},
			resource: "pr", subcmd: "create",
			resourceArgs: []string{"create"}, rest: nil,
		},
		{
			name: "separated long value", args: []string{"--repo", "o/r", "pr", "create"},
			resource: "pr", subcmd: "create",
			resourceArgs: []string{"--repo", "o/r", "create"}, rest: []string{"--repo", "o/r"},
		},
		{
			name: "'='-glued long is ONE token", args: []string{"--repo=o/r", "pr", "create"},
			resource: "pr", subcmd: "create",
			resourceArgs: []string{"--repo=o/r", "create"}, rest: []string{"--repo=o/r"},
		},
		{
			name: "bare short takes the next token", args: []string{"-R", "o/r", "pr", "create"},
			resource: "pr", subcmd: "create",
			resourceArgs: []string{"-R", "o/r", "create"}, rest: []string{"-R", "o/r"},
		},
		{
			name: "short with a glued value", args: []string{"-Ro/r", "pr", "create"},
			resource: "pr", subcmd: "create",
			resourceArgs: []string{"-Ro/r", "create"}, rest: []string{"-Ro/r"},
		},
		{
			name: "short with an '='-glued value", args: []string{"-R=o/r", "pr", "create"},
			resource: "pr", subcmd: "create",
			resourceArgs: []string{"-R=o/r", "create"}, rest: []string{"-R=o/r"},
		},
		{
			name: "flag INSIDE the path", args: []string{"pr", "--repo", "o/r", "create"},
			resource: "pr", subcmd: "create",
			resourceArgs: []string{"--repo", "o/r", "create"}, rest: []string{"--repo", "o/r"},
		},
		{
			// The load-bearing api row: the method precedes the `api` word, and
			// resourceArgs must still carry it AND the endpoint.
			name:     "api keeps its endpoint and its pre-path method",
			args:     []string{"-X", "PUT", "api", "repos/o/r/pulls/5/merge"},
			resource: "api", subcmd: "repos/o/r/pulls/5/merge",
			resourceArgs: []string{"-X", "PUT", "repos/o/r/pulls/5/merge"}, rest: []string{"-X", "PUT"},
		},
		{
			name: "a REGISTERED global bool consumes nothing", args: []string{"--help", "pr", "create"},
			resource: "pr", subcmd: "create",
			resourceArgs: []string{"--help", "create"}, rest: []string{"--help"},
		},
		{
			// -h is NOT registered on gh's root, so cobra consumes `pr` as its value and
			// resolves no `pr` command at all. Measured: `unknown command "create"`.
			name: "an UNREGISTERED short consumes the next token", args: []string{"-h", "pr", "create"},
			resource: "create", subcmd: "",
			resourceArgs: []string{"-h", "pr"}, rest: []string{"-h", "pr"},
		},
		{
			name: "nothing after `--` is a command word", args: []string{"--", "pr", "create"},
			resource: "", subcmd: "",
			resourceArgs: []string{"--", "pr", "create"}, rest: []string{"--", "pr", "create"},
		},
		{
			// `--` must SURVIVE in rest: pg2-25oru's `gh pr create -- --draft` Reject
			// depends on cmdparse.HasLongFlag seeing the terminator.
			name: "`--` survives in rest", args: []string{"pr", "create", "--", "--draft"},
			resource: "pr", subcmd: "create",
			resourceArgs: []string{"create", "--", "--draft"}, rest: []string{"--", "--draft"},
		},
		{
			name:     "a lone `-` is skipped, not treated as a value-taking flag",
			args:     []string{"-", "pr", "create"},
			resource: "pr", subcmd: "create",
			resourceArgs: []string{"-", "create"}, rest: []string{"-"},
		},
		{
			name: "a flag needing a value at the end leaves no command word", args: []string{"--repo"},
			resource: "", subcmd: "",
			resourceArgs: []string{"--repo"}, rest: []string{"--repo"},
		},
		{
			name: "resource with no subcommand", args: []string{"api"},
			resource: "api", subcmd: "",
			resourceArgs: nil, rest: nil,
		},
		{
			name: "no args at all", args: nil,
			resource: "", subcmd: "",
			resourceArgs: nil, rest: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource, subcmd, resourceArgs, rest := ghCommandPath(tt.args)
			if resource != tt.resource || subcmd != tt.subcmd {
				t.Errorf("ghCommandPath(%q) path = %q/%q, want %q/%q",
					tt.args, resource, subcmd, tt.resource, tt.subcmd)
			}
			if !slices.Equal(resourceArgs, tt.resourceArgs) {
				t.Errorf("ghCommandPath(%q) resourceArgs = %q, want %q", tt.args, resourceArgs, tt.resourceArgs)
			}
			if !slices.Equal(rest, tt.rest) {
				t.Errorf("ghCommandPath(%q) rest = %q, want %q", tt.args, rest, tt.rest)
			}
		})
	}
}

func TestGH_NonGh_Abstain(t *testing.T) {
	r := New(nil)
	input := &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "git status"}),
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("git status: got %s, want abstain", got.Decision)
	}
}

func TestGH_NonBash_Abstain(t *testing.T) {
	r := New(nil)
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/tmp/x"}),
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("Read: got %s, want abstain", got.Decision)
	}
}

func TestGH_Name(t *testing.T) {
	r := New(nil)
	if got := r.Name(); got != "gh" {
		t.Errorf("Name() = %q, want gh", got)
	}
}

func TestGH_RunRerun(t *testing.T) {
	errFailed := errors.New("simulated failure")

	tests := []struct {
		name     string
		cmd      string
		resolver BranchResolver
		want     hookio.Decision
	}{
		{
			name:     "branches match",
			cmd:      "gh run rerun 12345",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "feature-x"},
			want:     hookio.Approve,
		},
		{
			name:     "branches differ",
			cmd:      "gh run rerun 12345",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "main"},
			want:     hookio.NoOpinion,
		},
		{
			name:     "current branch error",
			cmd:      "gh run rerun 12345",
			resolver: &stubResolver{currentErr: errFailed},
			want:     hookio.NoOpinion,
		},
		{
			name:     "run branch error (timeout)",
			cmd:      "gh run rerun 12345",
			resolver: &stubResolver{currentBranch: "feature-x", runErr: errFailed},
			want:     hookio.NoOpinion,
		},
		{
			name:     "flags before run ID",
			cmd:      "gh run rerun --failed 12345",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "feature-x"},
			want:     hookio.Approve,
		},
		{
			name:     "flags after run ID",
			cmd:      "gh run rerun 12345 --failed",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "feature-x"},
			want:     hookio.Approve,
		},
		{
			name:     "no run ID",
			cmd:      "gh run rerun",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "feature-x"},
			want:     hookio.NoOpinion,
		},
		{
			name:     "nil resolver",
			cmd:      "gh run rerun 12345",
			resolver: nil,
			want:     hookio.NoOpinion,
		},
		{
			name:     "non-numeric run ID",
			cmd:      "gh run rerun not-a-number",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "feature-x"},
			want:     hookio.NoOpinion,
		},
		{
			// pg2-by1ij: the run branch is reached through the same resolution as every
			// other, so the global-flag spellings must land on it too. extractRunID needs
			// no change of its own — it finds the `rerun` token and scans after it.
			name:     "global flag before the command path",
			cmd:      "gh --repo o/r run rerun 12345",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "feature-x"},
			want:     hookio.Approve,
		},
		{
			name:     "global flag inside the command path",
			cmd:      "gh run -R o/r rerun 12345",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "feature-x"},
			want:     hookio.Approve,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New(tt.resolver)
			input := &hookio.HookInput{
				ToolName:  "Bash",
				ToolInput: mustJSON(map[string]string{"command": tt.cmd}),
				CWD:       "/tmp/test-repo",
			}
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != tt.want {
				t.Errorf("cmd %q: got %s, want %s (reason: %s)", tt.cmd, got.Decision, tt.want, got.Reason)
			}
		})
	}
}

// TestGH_RunRerunResolverFailureIsAGenuineError pins ADR 0043's CANONICAL error site
// — the one its Context quotes verbatim — on the channel, not just on the verdict.
//
// TestGH_RunRerun above already asserts that a resolver failure still lets the chain
// continue, which is the decision-preservation half. It cannot see the other half:
// before ADR 0043 the failure was folded into the loop sentinel and the error was
// DISCARDED, so "the resolver timed out" and "this rule does not apply" were the same
// value. Only the assertions below separate them, and that separation is what lets a
// systematically-failing `git rev-parse` be counted per rule instead of vanishing.
//
// The two NOT-applicable rows are asserted in the same place on purpose: they are the
// nearby sites that MUST NOT have been converted to errors, since neither is a
// failure (no run ID in the command; no resolver injected at construction).
func TestGH_RunRerunResolverFailureIsAGenuineError(t *testing.T) {
	errFailed := errors.New("simulated failure")
	input := func(cmd string) *hookio.HookInput {
		return &hookio.HookInput{
			ToolName:  "Bash",
			ToolInput: mustJSON(map[string]string{"command": cmd}),
			CWD:       "/tmp/test-repo",
		}
	}

	t.Run("current branch resolver failure", func(t *testing.T) {
		_, err := New(&stubResolver{currentErr: errFailed}).Evaluate(input("gh run rerun 12345"))
		if err == nil || errors.Is(err, hookio.ErrNotApplicable) {
			t.Fatalf("err = %v, want a GENUINE error (not ErrNotApplicable): the rule owns `gh run rerun` "+
				"and merely could not resolve the branch", err)
		}
		if !errors.Is(err, errFailed) {
			t.Errorf("err = %v, want it to wrap the resolver's own error so the cause survives", err)
		}
	})

	t.Run("run branch resolver failure", func(t *testing.T) {
		_, err := New(&stubResolver{currentBranch: "feature-x", runErr: errFailed}).Evaluate(input("gh run rerun 12345"))
		if err == nil || errors.Is(err, hookio.ErrNotApplicable) {
			t.Fatalf("err = %v, want a GENUINE error (not ErrNotApplicable)", err)
		}
		if !errors.Is(err, errFailed) {
			t.Errorf("err = %v, want it to wrap the resolver's own error", err)
		}
	})

	t.Run("no run ID is not-applicable, not an error", func(t *testing.T) {
		_, err := New(&stubResolver{currentBranch: "feature-x"}).Evaluate(input("gh run rerun"))
		if !errors.Is(err, hookio.ErrNotApplicable) {
			t.Errorf("err = %v, want ErrNotApplicable: there is nothing to resolve, which is not a failure", err)
		}
	})

	t.Run("no resolver is not-applicable, not an error", func(t *testing.T) {
		_, err := New(nil).Evaluate(input("gh run rerun 12345"))
		if !errors.Is(err, hookio.ErrNotApplicable) {
			t.Errorf("err = %v, want ErrNotApplicable: an uninjected resolver is a CONSTRUCTION condition, "+
				"not a runtime failure, so it must not inflate the per-rule failure count", err)
		}
	})
}
