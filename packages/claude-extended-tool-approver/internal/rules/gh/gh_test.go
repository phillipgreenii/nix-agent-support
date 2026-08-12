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
		got := r.Evaluate(input)
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
		got := r.Evaluate(input)
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
	got := r.Evaluate(input)
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
		got := r.Evaluate(input)
		if got.Decision != hookio.Abstain {
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
	got := r.Evaluate(input)
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
		{"gh pr merge --auto", hookio.Abstain},
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
		if got := evalGH(t, cmd); got.Decision != hookio.Abstain {
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
		if got := evalGH(t, cmd); got.Decision != hookio.Abstain {
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
	return New(nil).Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": cmd}),
		CWD:       "/tmp/test-repo",
	})
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

// TestGH_ApiOtherMutation_Ask pins the PATH-BLIND floor: every mutation that is not
// the pull-request merge gets the conservative Ask, matching the verdict the
// equivalent porcelain already carries (`gh issue create` is Ask; `gh pr create` was
// too until pg2-25oru made it draft-aware — see apiVerdict's PR-CREATION paragraph for
// why the api path deliberately stays at Ask rather than following it to Reject).
// The `pulls` row is the one the blanket approval measurably bypassed.
func TestGH_ApiOtherMutation_Ask(t *testing.T) {
	cmds := []string{
		"gh api -X POST repos/o/r/pulls -f title=x", // probed VERBATIM; bypassed the Ask `gh pr create` carried then
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

// TestGH_ApiCreate_NotWeakerThanDraftCreate is the same consistency guard for the OTHER
// control the blanket approval bypassed, RESTATED where pg2-25oru moved the porcelain.
//
// It used to compare against `gh pr create` directly, when that was an Ask. Since
// pg2-25oru the porcelain has TWO verdicts — Approve with --draft, Reject without — and
// `gh api -X POST repos/o/r/pulls` cannot be sorted between them: GitHub's `draft` is a
// BODY PARAMETER, and parseGhAPICall reads body parameters only as a presence boolean,
// never their values (`--input file.json` hides them entirely). So the guard is stated as
// what still holds: the api path must be STRICTLY MORE restrictive than the blessed draft
// create, and at least the Ask floor pg2-cl0v2 established.
//
// THE RESIDUAL GAP IS DELIBERATE AND NAMED: Ask is auto-accepted in an auto-approving
// session, so `gh api -X POST repos/o/r/pulls` can still create a NON-DRAFT PR that
// `gh pr create` would Reject. Following the porcelain to Reject was not done here
// because it would also reject `-f draft=true`, the blessed create, with no in-session
// override. Closing it needs a draft-body-parameter reader (`-f`/`-F`/`--field`/
// `--raw-field` values, plus a fail-closed reading of `--input`) and the matching
// `graphql` mutation case — its own change, not a tidy-up.
func TestGH_ApiCreate_NotWeakerThanDraftCreate(t *testing.T) {
	draftPorcelain := evalGH(t, "gh pr create --draft")
	viaAPI := evalGH(t, "gh api -X POST repos/o/r/pulls -f title=x")
	if viaAPI.Decision <= draftPorcelain.Decision {
		t.Errorf("gh api PR create is not MORE restrictive than the blessed draft create: api=%s (%s) vs porcelain=%s (%s)",
			viaAPI.Decision, viaAPI.Reason, draftPorcelain.Decision, draftPorcelain.Reason)
	}
	if viaAPI.Decision < hookio.Ask {
		t.Errorf("gh api PR create is below the pg2-cl0v2 Ask floor: got %s (%s)", viaAPI.Decision, viaAPI.Reason)
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
		{"gh pr merge --auto", "gh --repo o/r pr merge --auto", hookio.Abstain},
		{"gh pr merge --auto=false", "gh --repo o/r pr merge --auto=false", hookio.Reject},
		// `gh api` (pg2-cl0v2). The PUT row is the LIVE route: measured, `gh -X PUT api
		// repos/o/r/pulls/5/merge` dumps `> PUT /api/v3/repos/o/r/pulls/5/merge`, so the
		// method really is honoured from before the `api` word — which is why apiVerdict
		// is handed the argv WITH those flags and not the branches' `rest`.
		{"gh api -X PUT repos/o/r/pulls/5/merge", "gh -X PUT api repos/o/r/pulls/5/merge", hookio.Reject},
		{"gh api -XPUT repos/o/r/pulls/5/merge", "gh -XPUT api repos/o/r/pulls/5/merge", hookio.Reject},
		{"gh api -X POST repos/o/r/pulls -f title=x", "gh -X POST api repos/o/r/pulls -f title=x", hookio.Ask},
		{"gh api repos/o/r", "gh --hostname github.com api repos/o/r", hookio.Approve},
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
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("git status: got %s, want abstain", got.Decision)
	}
}

func TestGH_NonBash_Abstain(t *testing.T) {
	r := New(nil)
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/tmp/x"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
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
			want:     hookio.Abstain,
		},
		{
			name:     "current branch error",
			cmd:      "gh run rerun 12345",
			resolver: &stubResolver{currentErr: errFailed},
			want:     hookio.Abstain,
		},
		{
			name:     "run branch error (timeout)",
			cmd:      "gh run rerun 12345",
			resolver: &stubResolver{currentBranch: "feature-x", runErr: errFailed},
			want:     hookio.Abstain,
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
			want:     hookio.Abstain,
		},
		{
			name:     "nil resolver",
			cmd:      "gh run rerun 12345",
			resolver: nil,
			want:     hookio.Abstain,
		},
		{
			name:     "non-numeric run ID",
			cmd:      "gh run rerun not-a-number",
			resolver: &stubResolver{currentBranch: "feature-x", runBranch: "feature-x"},
			want:     hookio.Abstain,
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
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("cmd %q: got %s, want %s (reason: %s)", tt.cmd, got.Decision, tt.want, got.Reason)
			}
		})
	}
}
