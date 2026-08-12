package gh

import (
	"encoding/json"
	"errors"
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
