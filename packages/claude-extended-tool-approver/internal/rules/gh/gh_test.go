package gh

import (
	"encoding/json"
	"errors"
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
	modifying := []string{
		"gh pr create",
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
// equivalent porcelain already carries (`gh pr create`, `gh issue create` are Ask).
// The `pulls` row is the one the blanket approval measurably bypassed.
func TestGH_ApiOtherMutation_Ask(t *testing.T) {
	cmds := []string{
		"gh api -X POST repos/o/r/pulls -f title=x", // probed VERBATIM; bypassed modifyingPR's Ask
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

// TestGH_ApiCreate_MirrorsPrCreateVerdict is the same consistency guard for the
// OTHER control the blanket approval bypassed: `gh pr create` is an Ask, so creating
// a PR through `gh api` must be at least an Ask.
func TestGH_ApiCreate_MirrorsPrCreateVerdict(t *testing.T) {
	porcelain := evalGH(t, "gh pr create")
	viaAPI := evalGH(t, "gh api -X POST repos/o/r/pulls -f title=x")
	if viaAPI.Decision < porcelain.Decision {
		t.Errorf("gh api PR create is WEAKER than gh pr create: api=%s (%s) vs porcelain=%s (%s)",
			viaAPI.Decision, viaAPI.Reason, porcelain.Decision, porcelain.Reason)
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
