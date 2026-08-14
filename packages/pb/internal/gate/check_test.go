package gate

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pb/internal/bd"
	"github.com/phillipgreenii/pb/internal/discover"
	"github.com/phillipgreenii/pb/internal/patchid"
	"github.com/phillipgreenii/pb/internal/pn"
	"github.com/phillipgreenii/pb/internal/run"
)

// one repo applied at "tip", .beads resolves to a single DB at /ws.
const checkInfoJSON = `{"wsid":"home","root":"/ws","terminal":"m",
	"repos":[{"name":"repo-a","path":"/ws/repo-a","applied_ref":"tip","dirty":false}]}`

func stubDiscover(dirs ...string) func([]string, string) ([]discover.DB, error) {
	return func(_ []string, _ string) ([]discover.DB, error) {
		out := make([]discover.DB, len(dirs))
		for i, d := range dirs {
			out[i] = discover.DB{Dir: d, Identity: "id-" + d}
		}
		return out, nil
	}
}

func checkDeps(f run.Runner, disc func([]string, string) ([]discover.DB, error)) CheckDeps {
	return CheckDeps{PN: pn.Client{R: f}, BD: bd.Client{R: f}, PatchID: patchid.Client{R: f}, Discover: disc}
}

// lockInfoJSON is checkInfoJSON with repo-a described as a repo the terminal pins
// as a flake input: pn schema 2 (so terminal_input/locked_rev carry information),
// applied at local HEAD "tip", built from locked rev lockedRev.
func lockInfoJSON(lockedRev string) string {
	return fmt.Sprintf(`{"wsid":"home","root":"/ws","terminal":"m","repos":[
		{"name":"repo-a","path":"/ws/repo-a","applied_ref":"tip","dirty":false,
		 "applied_state_schema":2,"terminal_input":true,"locked_rev":%q}]}`, lockedRev)
}

// overrideInfoJSON is lockInfoJSON with the pn applied-state schema and the
// `overridden` flag under test, so a single fixture can express every state of the
// override record: schema 3 + overridden (built from the local clone), schema 3 +
// not overridden (genuinely lock-built), and schema 2 (a pn that recorded no
// override set at all — pass overridden=false, since such a record cannot carry the
// field).
func overrideInfoJSON(schema int, lockedRev string, overridden bool) string {
	return fmt.Sprintf(`{"wsid":"home","root":"/ws","terminal":"m","repos":[
		{"name":"repo-a","path":"/ws/repo-a","applied_ref":"tip","dirty":false,
		 "applied_state_schema":%d,"terminal_input":true,"locked_rev":%q,"overridden":%t}]}`,
		schema, lockedRev, overridden)
}

// scriptResolvable scripts the `bd gate resolve g-1` call so it SUCCEEDS. Every
// must-not-resolve test calls this, and that is load-bearing rather than
// decorative: leaving it unscripted makes FakeRunner error on the resolve, which
// lands in Skipped and leaves Resolved empty anyway — so the test would pass even
// with the lock condition deleted. Scripted, an empty Resolved can only mean the
// condition actually held the gate back.
func scriptResolvable(f *run.FakeRunner) {
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "resolve", "g-1"}, run.Result{}, nil)
}

// scriptGateScan scripts the gate list plus the condition-1 scan of
// base1..tip, whose patch-id output maps patch-id "abc123" to commit sha.
func scriptGateScan(f *run.FakeRunner, sha string) {
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[{"id":"g-1","issue_type":"gate","await_type":"pn:applied",
			"await_id":"home:repo-a:abc123","created_at":"2026-06-26T00:00:00Z","metadata":{"applied_baseline":"base1"}}]}`}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "base1", "tip"}, run.Result{}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "log", "-p", "--no-merges", "base1..tip"}, run.Result{Stdout: "diff"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "patch-id", "--stable"},
		run.Result{Stdout: "abc123 " + sha + "\n"}, nil)
}

func runCheck(t *testing.T, f *run.FakeRunner) CheckResult {
	t.Helper()
	out, err := Check(context.Background(), checkDeps(f, stubDiscover("/ws")), CheckParams{
		WorkspaceDir: "/ws", LastN: 100, StaleAfter: 72 * time.Hour, StaleHandler: "convert-to-human",
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return out
}

// ancestorProbes returns the (ancestor, descendant) pairs Check asked git about.
func ancestorProbes(f *run.FakeRunner) [][2]string {
	var got [][2]string
	for _, c := range f.Calls() {
		if c.Name == "git" && len(c.Args) == 6 && c.Args[2] == "merge-base" && c.Args[3] == "--is-ancestor" {
			got = append(got, [2]string{c.Args[4], c.Args[5]})
		}
	}
	return got
}

func probed(f *run.FakeRunner, ancestor, descendant string) bool {
	for _, p := range ancestorProbes(f) {
		if p[0] == ancestor && p[1] == descendant {
			return true
		}
	}
	return false
}

// TestCheck_unpushedCommitInPinnedRepoDoesNotResolve is THE regression test for
// bead pg2-ft60a. repo-a is pinned by the terminal as a flake input; the gated
// commit is on the local checkout the apply ran over (condition 1 passes) but the
// rev that apply built the input from ("locked1") does not contain it, because the
// commit was never pushed and relocked. The gate MUST stay blocked: resolving it
// releases a verification bead against code no build has ever seen.
//
// Against the pre-change logic (condition 1 alone) this resolves — that is the bug.
func TestCheck_unpushedCommitInPinnedRepoDoesNotResolve(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: lockInfoJSON("locked1")}, nil)
	scriptGateScan(f, "gatedsha")
	scriptResolvable(f)
	// The gated commit is NOT in the rev the apply built from.
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "gatedsha", "locked1"},
		run.Result{ExitCode: 1}, fmt.Errorf("not ancestor"))

	out := runCheck(t, f)
	if len(out.Resolved) != 0 || len(out.WouldResolve) != 0 {
		t.Fatalf("resolved=%v would=%v; an unpushed commit in a flake-pinned repo is NOT in the applied system",
			out.Resolved, out.WouldResolve)
	}
	if len(out.Blocked) != 1 || !strings.Contains(out.Blocked[0].Reason, "push it, relock the terminal") {
		t.Fatalf("blocked = %+v; the reason must say why, or the operator sees only a silently stuck gate", out.Blocked)
	}
	if len(out.Skipped) != 0 {
		t.Fatalf("skipped = %+v; a DETERMINED 'not in the build' is not an undeterminable gate — routing it "+
			"to Skipped makes the apply post-hook exit non-zero on every normal pending gate", out.Skipped)
	}
	if !probed(f, "gatedsha", "locked1") {
		t.Fatalf("must test the gated commit against the RECORDED locked rev; probes = %v", ancestorProbes(f))
	}
}

// TestCheck_terminalRepoStillResolves pins the clause that keeps the sound class
// working with no special case. The terminal is built from its LOCAL directory, so
// it has no locked_revs entry (terminal_input=false) and condition 1 is the whole
// truth for it. It MUST resolve, and no lock probe should be attempted.
func TestCheck_terminalRepoStillResolves(t *testing.T) {
	f := run.NewFakeRunner()
	// schema 2 (a current pn wrote this) but NOT a flake input of the terminal.
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: `{"wsid":"home","root":"/ws","terminal":"repo-a","repos":[
		{"name":"repo-a","path":"/ws/repo-a","applied_ref":"tip","dirty":false,
		 "applied_state_schema":2,"terminal_input":false,"locked_rev":""}]}`}, nil)
	scriptGateScan(f, "gatedsha")
	scriptResolvable(f)

	out := runCheck(t, f)
	if len(out.Resolved) != 1 || out.Resolved[0] != "g-1" {
		t.Fatalf("resolved = %v skipped = %+v; a terminal-repo gate must keep resolving on condition 1 alone",
			out.Resolved, out.Skipped)
	}
	for _, p := range ancestorProbes(f) {
		if p[0] == "gatedsha" {
			t.Fatalf("no lock probe should be made for a repo with no locked_revs entry; probes = %v", ancestorProbes(f))
		}
	}
}

// TestCheck_pushedButLockNotAdvancedFailsClosed is the ANCESTOR case the operator
// explicitly accepted as fail-closed. The gated commit is pushed — it is now an
// ancestor of the repo's remote tip "remotetip" — but the terminal has not been
// relocked, so the rev the apply built from ("locked1") predates it. The gate MUST
// NOT resolve: being on the remote is not being in the build.
func TestCheck_pushedButLockNotAdvancedFailsClosed(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: lockInfoJSON("locked1")}, nil)
	scriptGateScan(f, "gatedsha")
	scriptResolvable(f)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "gatedsha", "locked1"},
		run.Result{ExitCode: 1}, fmt.Errorf("not ancestor"))
	// Deliberately scripted to SUCCEED: if the check ever consulted the remote tip
	// (or anything other than the recorded locked rev) the gate would resolve.
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "gatedsha", "remotetip"},
		run.Result{}, nil)

	out := runCheck(t, f)
	if len(out.Resolved) != 0 || len(out.Blocked) != 1 {
		t.Fatalf("resolved=%v blocked=%+v; pushed-but-not-relocked must fail closed", out.Resolved, out.Blocked)
	}
	if probed(f, "gatedsha", "remotetip") {
		t.Fatalf("the check must consult ONLY the rev recorded with the apply; probes = %v", ancestorProbes(f))
	}
}

// TestCheck_relockAfterApplyDoesNotResolve covers the ORDERING HOLE the ruling's
// refinement exists for, and it is what distinguishes "the lock recorded at apply
// time" from "the lock as it stands now". Apply at T1 built from "locked1"; a
// relock at T2 > T1 moved the lock to "locked2", which DOES contain the gated
// commit. No apply has run since. The T1 apply is the one resolving the gate, so
// the gate MUST NOT resolve — the running system was built before the relock.
//
// Against a plain "is the lock now past the commit?" check this resolves.
func TestCheck_relockAfterApplyDoesNotResolve(t *testing.T) {
	f := run.NewFakeRunner()
	// info reports the rev recorded WITH the T1 apply.
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: lockInfoJSON("locked1")}, nil)
	scriptGateScan(f, "gatedsha")
	scriptResolvable(f)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "gatedsha", "locked1"},
		run.Result{ExitCode: 1}, fmt.Errorf("not ancestor"))
	// The post-relock rev DOES contain the commit; consulting it would resolve.
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "gatedsha", "locked2"},
		run.Result{}, nil)

	out := runCheck(t, f)
	if len(out.Resolved) != 0 || len(out.Blocked) != 1 {
		t.Fatalf("resolved=%v blocked=%+v; a relock AFTER the apply must not retroactively resolve that "+
			"apply's gate", out.Resolved, out.Blocked)
	}
	if probed(f, "gatedsha", "locked2") {
		t.Fatalf("the post-relock rev must never be consulted; probes = %v", ancestorProbes(f))
	}
}

// TestCheck_resolvesWhenLockContainsGatedCommit is the happy path for a pinned
// repo: the apply built from a rev that contains the gated commit (pushed and
// relocked before the apply), so BOTH conditions hold.
func TestCheck_resolvesWhenLockContainsGatedCommit(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: lockInfoJSON("locked2")}, nil)
	scriptGateScan(f, "gatedsha")
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "gatedsha", "locked2"},
		run.Result{}, nil)
	scriptResolvable(f)

	out := runCheck(t, f)
	if len(out.Resolved) != 1 {
		t.Fatalf("resolved = %v skipped = %+v; both conditions hold, the gate must resolve", out.Resolved, out.Skipped)
	}
}

// TestCheck_cherryPickedCopyInLockResolves covers the slice half of the scan
// result. One diff can appear twice in the range (a cherry-pick); the copies are
// diff-identical but differ in ancestry, so the check must accept ANY copy being in
// the lock. Testing only the first would fail closed on a shipped change.
func TestCheck_cherryPickedCopyInLockResolves(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: lockInfoJSON("locked2")}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[{"id":"g-1","issue_type":"gate","await_type":"pn:applied",
			"await_id":"home:repo-a:abc123","created_at":"2026-06-26T00:00:00Z","metadata":{"applied_baseline":"base1"}}]}`}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "base1", "tip"}, run.Result{}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "log", "-p", "--no-merges", "base1..tip"}, run.Result{Stdout: "diff"}, nil)
	// The same patch-id twice, from two different commits.
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "patch-id", "--stable"},
		run.Result{Stdout: "abc123 shipped\nabc123 localonly\n"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "shipped", "locked2"},
		run.Result{}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "localonly", "locked2"},
		run.Result{ExitCode: 1}, fmt.Errorf("not ancestor"))
	scriptResolvable(f)

	out := runCheck(t, f)
	if len(out.Resolved) != 1 {
		t.Fatalf("resolved = %v skipped = %+v; one copy of the patch IS in the lock, so the change shipped",
			out.Resolved, out.Skipped)
	}
}

// TestCheck_unresolvedLockedRevFailsClosed covers the state where the apply knows
// the repo IS a terminal flake input but could not establish the rev it built from
// (unreadable flake.lock, a follows-only input). Falling back to condition 1 there
// is precisely the unsound case, so it MUST fail closed — and say so.
func TestCheck_unresolvedLockedRevFailsClosed(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: lockInfoJSON("")}, nil)
	scriptGateScan(f, "gatedsha")
	scriptResolvable(f)

	out := runCheck(t, f)
	if len(out.Resolved) != 0 {
		t.Fatalf("resolved = %v; an apply that cannot say what it built the input from must fail closed",
			out.Resolved)
	}
	if len(out.Skipped) != 1 || !strings.Contains(out.Skipped[0].Reason, "recorded no locked rev") {
		t.Fatalf("skipped = %+v; the unprovable case must be reported, not silent", out.Skipped)
	}
	if len(out.Blocked) != 0 {
		t.Fatalf("blocked = %+v; an UNDETERMINABLE gate belongs in Skipped (which drives the non-zero "+
			"exit), not in Blocked", out.Blocked)
	}
	if len(ancestorProbes(f)) > 1 { // only the baseline probe
		t.Fatalf("no lock probe is possible without a rev; probes = %v", ancestorProbes(f))
	}
}

// TestCheck_preLockedRevsRecordStillResolves is the backwards-compatibility case,
// and it is why the branch keys on the SCHEMA VERSION rather than on "the map is
// empty". A record written by a pn predating locked_revs carries no lock
// information at all, so terminal_input=false there is not evidence — treating it
// as evidence would be harmless, but treating the MISSING map as "unprovable" would
// make every gate unresolvable until a new pn is built, pushed, relocked and
// applied. The fix itself ships through that path, so that would be a bootstrap
// stall. Skip the condition for old records.
func TestCheck_preLockedRevsRecordStillResolves(t *testing.T) {
	f := run.NewFakeRunner()
	// checkInfoJSON has no applied_state_schema at all → 0.
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: checkInfoJSON}, nil)
	scriptGateScan(f, "gatedsha")
	scriptResolvable(f)

	out := runCheck(t, f)
	if len(out.Resolved) != 1 {
		t.Fatalf("resolved = %v skipped = %+v; a pre-locked_revs record must not block every gate",
			out.Resolved, out.Skipped)
	}
}

// TestApplyBuiltGatedCommit_schemaGuardIgnoresLockFields exercises the schema guard
// on its own, which no end-to-end Check test can do: a pn predating locked_revs
// emits neither terminal_input nor locked_rev, so in practice the schema-0 branch
// and the not-a-terminal-input branch agree and either one alone would skip.
//
// The guard states the policy the agreement hides — a record reporting schema 0
// carries NO lock information, so the lock fields beside it are not evidence and
// MUST NOT be enforced. Without the guard this state fails closed, and every gate
// in a workspace whose applied-state predates the upgrade becomes unresolvable
// until pn is rebuilt, pushed, relocked and applied: a bootstrap stall, and this
// very fix ships through that path.
func TestApplyBuiltGatedCommit_schemaGuardIgnoresLockFields(t *testing.T) {
	f := run.NewFakeRunner()
	d := checkDeps(f, stubDiscover("/ws"))
	// Schema 0 alongside lock fields that, if enforced, would block: the commit is
	// not in locked1 (no is-ancestor response is scripted, so the probe fails).
	repo := pn.Repo{
		Name: "repo-a", Path: "/ws/repo-a", AppliedRef: "tip",
		AppliedStateSchema: 0, TerminalInput: true, LockedRev: "locked1",
	}
	if verdict, reason := d.applyBuiltGatedCommit(context.Background(), repo, []string{"gatedsha"}); verdict != lockSatisfied {
		t.Fatalf("schema 0 must skip the lock condition outright, got verdict %d: %s", verdict, reason)
	}
	if len(ancestorProbes(f)) != 0 {
		t.Fatalf("schema 0 must not probe the lock at all; probes = %v", ancestorProbes(f))
	}
}

// TestCheck_overriddenInputResolvesWithoutTheLock is THE regression test for the
// pg2-14yqh operator ruling (option (c)), and the case the whole change exists for.
// repo-a IS a terminal flake input, but the apply passed `--override-input` for it,
// so nix built it from the LOCAL CLONE at eval-time HEAD and never consulted the
// lock. locked_rev therefore trails what was built and the gated commit is not an
// ancestor of it — yet the change IS in the running system, so the gate MUST
// resolve. Condition 1 already proves the apply ran over a checkout holding it.
//
// Against the pre-change code (condition 2 unconditional) this BLOCKS, which is the
// defect: /drain-beads lands locally and deliberately does not push (rule U-5), so
// every verification bead it gates sat blocked until someone pushed and relocked.
//
// The probe assertion is the load-bearing half. Without it the test would still pass
// if condition 2 ran and happened to succeed; asserting that the lock is never
// consulted pins the SKIP rather than a lucky verdict.
func TestCheck_overriddenInputResolvesWithoutTheLock(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"},
		run.Result{Stdout: overrideInfoJSON(3, "locked1", true)}, nil)
	scriptGateScan(f, "gatedsha")
	scriptResolvable(f)
	// Scripted to FAIL: if condition 2 were still applied, the gate would block.
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "gatedsha", "locked1"},
		run.Result{ExitCode: 1}, fmt.Errorf("not ancestor"))

	out := runCheck(t, f)
	if len(out.Resolved) != 1 || out.Resolved[0] != "g-1" {
		t.Fatalf("resolved=%v blocked=%+v skipped=%+v; an OVERRIDDEN input is built from the local clone, "+
			"so condition 1 is the whole truth for it and the gate must resolve",
			out.Resolved, out.Blocked, out.Skipped)
	}
	if probed(f, "gatedsha", "locked1") {
		t.Fatalf("condition 2 must be SKIPPED for an overridden input, not merely satisfied; probes = %v",
			ancestorProbes(f))
	}
}

// TestCheck_lockBuiltInputStillBlocks is the other side of the ruling: it is
// CONDITIONAL, not deleted. A terminal input the apply did NOT override really was
// resolved through the lock, so its locked rev really is what the build carries and
// condition 2 MUST still block a commit that is absent from it. Reached in practice
// when the repo has no clone for nix to be pointed at.
//
// This is the test that fails if the change is implemented as "drop condition 2".
func TestCheck_lockBuiltInputStillBlocks(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"},
		run.Result{Stdout: overrideInfoJSON(3, "locked1", false)}, nil)
	scriptGateScan(f, "gatedsha")
	scriptResolvable(f)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "gatedsha", "locked1"},
		run.Result{ExitCode: 1}, fmt.Errorf("not ancestor"))

	out := runCheck(t, f)
	if len(out.Resolved) != 0 || len(out.Blocked) != 1 {
		t.Fatalf("resolved=%v blocked=%+v; a repo the apply did NOT override was built from the lock, so "+
			"condition 2 still applies", out.Resolved, out.Blocked)
	}
	if !probed(f, "gatedsha", "locked1") {
		t.Fatalf("a lock-built input must still be tested against its recorded locked rev; probes = %v",
			ancestorProbes(f))
	}
	if strings.Contains(out.Blocked[0].Reason, "schema") {
		t.Fatalf("blocked reason = %q; the schema caveat belongs ONLY on a record that cannot say whether it "+
			"overrode the input — here the record says so explicitly", out.Blocked[0].Reason)
	}
}

// TestCheck_preOverrideRecordFailsClosed is the DOCUMENTED FALLBACK for a record
// written by an older pn: schema 2 carries locked_revs but no override set, so
// `overridden` is absent — indistinguishable, on the wire, from "recorded, and
// genuinely lock-built". The fallback leans FAIL-CLOSED: condition 2 is enforced.
//
// This is deliberately the OPPOSITE lean from the schema < 2 fallback, and the
// asymmetry is the point. At schema < 2 condition 2 is UNEVALUABLE and skipping is
// the only alternative to blocking every gate in the workspace forever. At schema 2
// it is fully evaluable and its verdict is determinate, so leaning open would ASSERT
// an override the record does not evidence — and a false "the change shipped" is the
// pg2-ft60a harm, while a false "still blocked" costs one stale-handler escalation.
// The window is bounded to one apply: the next apply rewrites the record at schema 3.
//
// The reason string MUST name the assumption, or this conservative verdict is
// indistinguishable from a wrong one.
func TestCheck_preOverrideRecordFailsClosed(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"},
		run.Result{Stdout: overrideInfoJSON(2, "locked1", false)}, nil)
	scriptGateScan(f, "gatedsha")
	scriptResolvable(f)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "gatedsha", "locked1"},
		run.Result{ExitCode: 1}, fmt.Errorf("not ancestor"))

	out := runCheck(t, f)
	if len(out.Resolved) != 0 || len(out.Blocked) != 1 {
		t.Fatalf("resolved=%v blocked=%+v; a record that cannot say whether the apply overrode the input "+
			"must fail CLOSED", out.Resolved, out.Blocked)
	}
	if !strings.Contains(out.Blocked[0].Reason, "does not record whether the apply OVERRODE this input") {
		t.Fatalf("blocked reason = %q; it must name the fail-closed assumption, or a conservative verdict "+
			"reads as a wrong one", out.Blocked[0].Reason)
	}
}

// TestApplyBuiltGatedCommit_overriddenIgnoredBelowSchema3 exercises the schema guard
// on the override field alone, which no end-to-end Check test can do: a pn predating
// the override set emits no `overridden` key, so in practice the guard and the plain
// `!repo.Overridden` test agree and either would enforce.
//
// The guard states the policy the agreement hides, and it is the fail-OPEN direction,
// so it is the one that must not be reachable by accident: a record reporting schema
// 2 carries NO override information, so an `overridden` beside it is not evidence and
// MUST NOT skip condition 2. Without the guard, any producer that emitted the field
// at an older schema — or a hand-edited record — would silently disable the condition.
func TestApplyBuiltGatedCommit_overriddenIgnoredBelowSchema3(t *testing.T) {
	f := run.NewFakeRunner()
	d := checkDeps(f, stubDiscover("/ws"))
	// Schema 2 alongside an override claim the record cannot legitimately carry. The
	// is-ancestor probe is unscripted, so it fails ⇒ condition 2 blocks if enforced.
	repo := pn.Repo{
		Name: "repo-a", Path: "/ws/repo-a", AppliedRef: "tip",
		AppliedStateSchema: 2, TerminalInput: true, LockedRev: "locked1", Overridden: true,
	}
	verdict, reason := d.applyBuiltGatedCommit(context.Background(), repo, []string{"gatedsha"})
	if verdict != lockMissing {
		t.Fatalf("verdict = %d (%s), want lockMissing — an override claim below schema %d is not evidence",
			verdict, reason, pnOverrideRecordSchema)
	}
	if !probed(f, "gatedsha", "locked1") {
		t.Fatalf("the lock must still be consulted; probes = %v", ancestorProbes(f))
	}
}

// TestApplyBuiltGatedCommit_overriddenSkipsBeforeTheEmptyRevFailClosed pins the
// BRANCH ORDER, which is not interchangeable. An overridden input whose locked rev
// could not be established (a follows-only input, an unreadable flake.lock) must
// SKIP, not fail closed: nothing was built from that rev, so its absence has no
// bearing on whether the change shipped. Order the empty-rev check first and every
// such gate becomes permanently unresolvable while the change is demonstrably in the
// running system.
func TestApplyBuiltGatedCommit_overriddenSkipsBeforeTheEmptyRevFailClosed(t *testing.T) {
	f := run.NewFakeRunner()
	d := checkDeps(f, stubDiscover("/ws"))
	repo := pn.Repo{
		Name: "repo-a", Path: "/ws/repo-a", AppliedRef: "tip",
		AppliedStateSchema: 3, TerminalInput: true, LockedRev: "", Overridden: true,
	}
	if verdict, reason := d.applyBuiltGatedCommit(context.Background(), repo, []string{"gatedsha"}); verdict != lockSatisfied {
		t.Fatalf("verdict = %d (%s), want lockSatisfied — an unresolvable rev the build never used cannot "+
			"make the change unprovable", verdict, reason)
	}
}

func TestCheck_resolvesWhenPatchIDInHistory(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: checkInfoJSON}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[{"id":"g-1","issue_type":"gate","await_type":"pn:applied",
			"await_id":"home:repo-a:abc123","created_at":"2026-06-26T00:00:00Z","metadata":{"applied_baseline":"base1"}}]}`}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "base1", "tip"}, run.Result{}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "log", "-p", "--no-merges", "base1..tip"}, run.Result{Stdout: "diff"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "patch-id", "--stable"}, run.Result{Stdout: "abc123 sha\n"}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "resolve", "g-1"}, run.Result{}, nil)

	out, err := Check(context.Background(), checkDeps(f, stubDiscover("/ws")), CheckParams{
		WorkspaceDir: "/ws", LastN: 100, StaleAfter: 72 * time.Hour, StaleHandler: "convert-to-human",
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.Resolved) != 1 || out.Resolved[0] != "g-1" {
		t.Fatalf("resolved = %v", out.Resolved)
	}
}

func TestCheck_dryRunMutatesNothing(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: checkInfoJSON}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[{"id":"g-1","issue_type":"gate","await_type":"pn:applied",
			"await_id":"home:repo-a:abc123","created_at":"2026-06-26T00:00:00Z","metadata":{"applied_baseline":"base1"}}]}`}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "base1", "tip"}, run.Result{}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "log", "-p", "--no-merges", "base1..tip"}, run.Result{Stdout: "diff"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "patch-id", "--stable"}, run.Result{Stdout: "abc123 sha\n"}, nil)
	out, err := Check(context.Background(), checkDeps(f, stubDiscover("/ws")), CheckParams{
		WorkspaceDir: "/ws", LastN: 100, DryRun: true, StaleAfter: 72 * time.Hour,
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.WouldResolve) != 1 || len(out.Resolved) != 0 {
		t.Fatalf("dry-run: resolved=%v would=%v", out.Resolved, out.WouldResolve)
	}
	for _, c := range f.Calls() {
		if c.Name == "bd" && len(c.Args) >= 4 && c.Args[2] == "gate" && c.Args[3] == "resolve" {
			t.Fatal("dry-run issued a gate resolve")
		}
	}
}

func TestCheck_unknownRepoSkipsAndReports(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: checkInfoJSON}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[{"id":"g-2","issue_type":"gate","await_type":"pn:applied",
			"await_id":"home:ghost:zzz","created_at":"2026-06-26T00:00:00Z"}]}`}, nil)
	out, err := Check(context.Background(), checkDeps(f, stubDiscover("/ws")), CheckParams{
		WorkspaceDir: "/ws", LastN: 100, StaleAfter: 72 * time.Hour,
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.Skipped) != 1 || out.Skipped[0].Repo != "ghost" {
		t.Fatalf("skipped = %+v", out.Skipped)
	}
}

// Stale boundary: a never-applied repo (applied_ref="") so no scan; one young
// gate (left alone) + one old gate (acted on) in the same run.
func TestCheck_staleBoundaryYoungerVsOlder(t *testing.T) {
	f := run.NewFakeRunner()
	info := `{"wsid":"home","root":"/ws","terminal":"m",
		"repos":[{"name":"repo-a","path":"/ws/repo-a","applied_ref":"","dirty":false}]}`
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: info}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[
			{"id":"g-young","issue_type":"gate","await_type":"pn:applied","await_id":"home:repo-a:y","created_at":"2026-06-25T23:30:00Z"},
			{"id":"g-old","issue_type":"gate","await_type":"pn:applied","await_id":"home:repo-a:o","created_at":"2026-06-24T00:00:00Z"}
		]}`}, nil)
	// only the old gate is acted on (convert-to-human → AddLabel)
	f.AddResponse("bd", []string{"-C", "/ws", "update", "g-old", "--add-label", "human"}, run.Result{}, nil)
	out, err := Check(context.Background(), checkDeps(f, stubDiscover("/ws")), CheckParams{
		WorkspaceDir: "/ws", LastN: 100, StaleAfter: 24 * time.Hour, StaleHandler: "convert-to-human",
		Now: time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.StaleActions) != 1 || out.StaleActions[0].GateID != "g-old" {
		t.Fatalf("stale = %+v (only g-old, ~2d old, should be acted on; g-young ~30m)", out.StaleActions)
	}
}

// Multi-DB: the gate lives in the second DB and must be resolved in THAT DB.
func TestCheck_multiDBResolvesInOwnDB(t *testing.T) {
	f := run.NewFakeRunner()
	info := `{"wsid":"home","root":"/ws","terminal":"m","repos":[
		{"name":"repo-a","path":"/ws/repo-a","applied_ref":"tipa","dirty":false},
		{"name":"repo-b","path":"/ws/repo-b","applied_ref":"tipb","dirty":false}]}`
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: info}, nil)
	// DB /ws has no pn:applied gates for us.
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[]}`}, nil)
	// DB /ws/repo-b holds the gate.
	f.AddResponse("bd", []string{"-C", "/ws/repo-b", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[{"id":"g-b","issue_type":"gate","await_type":"pn:applied",
			"await_id":"home:repo-b:pidb","created_at":"2026-06-26T00:00:00Z","metadata":{"applied_baseline":"baseb"}}]}`}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-b", "merge-base", "--is-ancestor", "baseb", "tipb"}, run.Result{}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-b", "log", "-p", "--no-merges", "baseb..tipb"}, run.Result{Stdout: "diff"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-b", "patch-id", "--stable"}, run.Result{Stdout: "pidb sha\n"}, nil)
	// resolve MUST target /ws/repo-b (the gate's own DB).
	f.AddResponse("bd", []string{"-C", "/ws/repo-b", "gate", "resolve", "g-b"}, run.Result{}, nil)

	out, err := Check(context.Background(), checkDeps(f, stubDiscover("/ws", "/ws/repo-b")), CheckParams{
		WorkspaceDir: "/ws", LastN: 100, StaleAfter: 72 * time.Hour,
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.Resolved) != 1 || out.Resolved[0] != "g-b" {
		t.Fatalf("resolved = %v", out.Resolved)
	}
	// verify the resolve call's -C dir was /ws/repo-b
	found := false
	for _, c := range f.Calls() {
		if c.Name == "bd" && len(c.Args) >= 5 && c.Args[2] == "gate" && c.Args[3] == "resolve" && c.Args[4] == "g-b" {
			if c.Args[1] != "/ws/repo-b" {
				t.Fatalf("resolve -C dir = %q, want /ws/repo-b", c.Args[1])
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no resolve call recorded")
	}
}

// Baseline set but NOT an ancestor of applied_ref → fall back to -n N scan.
func TestCheck_baselineNotAncestorFallsBackToLastN(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: checkInfoJSON}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[{"id":"g-1","issue_type":"gate","await_type":"pn:applied",
			"await_id":"home:repo-a:abc123","created_at":"2026-06-26T00:00:00Z","metadata":{"applied_baseline":"stale-base"}}]}`}, nil)
	// merge-base --is-ancestor fails (non-zero) → not an ancestor
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "merge-base", "--is-ancestor", "stale-base", "tip"},
		run.Result{ExitCode: 1}, fmt.Errorf("not ancestor"))
	// MUST scan the last-N form, not stale-base..tip
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "log", "-p", "--no-merges", "-n", "100", "tip"}, run.Result{Stdout: "diff"}, nil)
	f.AddResponse("git", []string{"-C", "/ws/repo-a", "patch-id", "--stable"}, run.Result{Stdout: "abc123 sha\n"}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "resolve", "g-1"}, run.Result{}, nil)
	out, err := Check(context.Background(), checkDeps(f, stubDiscover("/ws")), CheckParams{
		WorkspaceDir: "/ws", LastN: 100, StaleAfter: 72 * time.Hour,
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.Resolved) != 1 {
		t.Fatalf("resolved = %v (should fall back to -n N scan, not false-miss)", out.Resolved)
	}
}

func TestCheck_strictSkipsDirty(t *testing.T) {
	f := run.NewFakeRunner()
	info := `{"wsid":"home","root":"/ws","terminal":"m",
		"repos":[{"name":"repo-a","path":"/ws/repo-a","applied_ref":"tip","dirty":true}]}`
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: info}, nil)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: `{"data":[{"id":"g-1","issue_type":"gate","await_type":"pn:applied",
			"await_id":"home:repo-a:abc123","created_at":"2026-06-26T00:00:00Z"}]}`}, nil)
	out, err := Check(context.Background(), checkDeps(f, stubDiscover("/ws")), CheckParams{
		WorkspaceDir: "/ws", LastN: 100, Strict: true, StaleAfter: 72 * time.Hour,
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.Skipped) != 1 || !strings.Contains(out.Skipped[0].Reason, "dirty") {
		t.Fatalf("skipped = %+v (strict should skip dirty repo)", out.Skipped)
	}
}

// >50 gates: proves --limit 0 returns all and the loop processes every one
// (here all reference an unknown repo → all are skipped).
func TestCheck_over50Gates(t *testing.T) {
	f := run.NewFakeRunner()
	f.AddResponse("pn", []string{"workspace", "info", "--json"}, run.Result{Stdout: checkInfoJSON}, nil)
	var b strings.Builder
	b.WriteString(`{"data":[`)
	for i := range 60 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":"g-%d","issue_type":"gate","await_type":"pn:applied","await_id":"home:ghost%d:p","created_at":"2026-06-26T00:00:00Z"}`, i, i)
	}
	b.WriteString(`]}`)
	f.AddResponse("bd", []string{"-C", "/ws", "gate", "list", "--limit", "0", "--json"},
		run.Result{Stdout: b.String()}, nil)
	out, err := Check(context.Background(), checkDeps(f, stubDiscover("/ws")), CheckParams{
		WorkspaceDir: "/ws", LastN: 100, StaleAfter: 72 * time.Hour,
		Now: time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(out.Skipped) != 60 {
		t.Fatalf("processed %d gates, want 60 (proves --limit 0, no 50-cap truncation)", len(out.Skipped))
	}
}
