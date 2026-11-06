package git

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// chdirInput builds a Bash HookInput for cmd with the given CWD.
func chdirInput(cmd, cwd string) *hookio.HookInput {
	return &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": cmd}),
		CWD:       cwd,
	}
}

const projectCWD = "/home/user/project"

// homeCWD is a fixed HOME for these tests, pinned so `~` expands to a path that
// is genuinely OUTSIDE every zone the evaluator knows. It MUST NOT be a
// t.TempDir(): inside a nix build sandbox that lands under /nix/**, whose
// READ-ONLY zone Evaluate checks BEFORE anything home-relative — so `~/.ssh`
// would come back readable and the unreadable-zone assertions below would
// silently invert. (Same hazard already documented in the pathsafety rule's
// tests; see also `phillipg-nix-repo-base` ADR 0021 — the shared mkGoTest
// builder exports HOME=$TMPDIR, which on darwin IS /nix-rooted.)
const homeCWD = "/home/testuser"

// newWithProject returns a git rule wired to an evaluator rooted at projectCWD,
// with HOME pinned so the zone table below actually holds.
// Zones: <project>/** and /tmp/** are read-write; /nix/store/** is read-only;
// everything else (/etc, /usr/bin, ~/.ssh) is unknown (neither read nor write).
// HOME is read once at evaluator construction, so it is pinned here.
func newWithProject(t *testing.T) *Rule {
	t.Helper()
	t.Setenv("HOME", homeCWD)
	return New(patheval.New(projectCWD))
}

// A read-only subcommand with a -C dir in a READABLE zone keeps its Approve.
func TestGit_Chdir_ReadOnlySub_ReadableZone_Approve(t *testing.T) {
	r := newWithProject(t)
	approve := []string{
		"git -C /home/user/project status", // read-write zone (readable)
		"git -C /home/user/project/sub log",
		"git -C /nix/store/abc123-foo log", // read-only zone (still readable)
		"git -C /tmp/scratch diff",
	}
	for _, cmd := range approve {
		got := hookio.Verdict(r.Evaluate(chdirInput(cmd, projectCWD)))
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve (readable -C dir, read-only sub)", cmd, got.Decision, got.Reason)
		}
	}
}

// A read-only subcommand with a -C dir OUTSIDE any readable zone demotes to Abstain.
func TestGit_Chdir_ReadOnlySub_UnsafeZone_Abstain(t *testing.T) {
	r := newWithProject(t)
	abstain := []string{
		"git -C /etc status",
		"git -C /usr/bin log",
		"git -C ~/.ssh status",     // tilde expands to the pinned home; out of the synthetic project zone
		"git -C ../outside status", // relative, escapes project via CWD
	}
	for _, cmd := range abstain {
		got := hookio.Verdict(r.Evaluate(chdirInput(cmd, projectCWD)))
		if got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want abstain (unreadable -C dir)", cmd, got.Decision, got.Reason)
		}
	}
}

// Read-vs-write asymmetry: same read-only-zone dir Approves a read sub but
// Abstains a modifying sub (which needs a WRITABLE dir).
func TestGit_Chdir_ReadWriteAsymmetry(t *testing.T) {
	r := newWithProject(t)
	// read-only zone dir, read sub -> Approve
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C /nix/store/abc123-foo log", projectCWD))); got.Decision != hookio.Approve {
		t.Errorf("git -C <ro> log: got %s, want approve", got.Decision)
	}
	// same read-only zone dir, modifying subs -> Abstain
	modAbstain := []string{
		"git -C /nix/store/abc123-foo add .",
		"git -C /nix/store/abc123-foo commit -m x",
	}
	for _, cmd := range modAbstain {
		got := hookio.Verdict(r.Evaluate(chdirInput(cmd, projectCWD)))
		if got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want abstain (read-only dir, write-class sub)", cmd, got.Decision, got.Reason)
		}
	}
}

// A modifying subcommand with a -C dir in a WRITABLE zone keeps its Approve.
func TestGit_Chdir_ModifyingSub_WritableZone_Approve(t *testing.T) {
	r := newWithProject(t)
	approve := []string{
		"git -C /home/user/project commit -m msg",
		"git -C /home/user/project/sub add .",
		"git -C /tmp/scratch stash",
	}
	for _, cmd := range approve {
		got := hookio.Verdict(r.Evaluate(chdirInput(cmd, projectCWD)))
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve (writable -C dir, modifying sub)", cmd, got.Decision, got.Reason)
		}
	}
}

// A modifying subcommand with a -C dir that is NOT writable demotes to Abstain.
func TestGit_Chdir_ModifyingSub_NonWritable_Abstain(t *testing.T) {
	r := newWithProject(t)
	abstain := []string{
		"git -C /etc commit -m msg",
		"git -C /etc add .",
		"git -C /usr/bin branch feat",
	}
	for _, cmd := range abstain {
		got := hookio.Verdict(r.Evaluate(chdirInput(cmd, projectCWD)))
		if got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want abstain (non-writable -C dir, modifying sub)", cmd, got.Decision, got.Reason)
		}
	}
}

// Relative -C is resolved against the invocation CWD.
func TestGit_Chdir_RelativeResolvedAgainstCWD(t *testing.T) {
	r := newWithProject(t)
	// CWD in the writable zone; ./sub stays inside it.
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C ./sub status", projectCWD))); got.Decision != hookio.Approve {
		t.Errorf("git -C ./sub (cwd in zone): got %s, want approve", got.Decision)
	}
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C ./sub add .", projectCWD))); got.Decision != hookio.Approve {
		t.Errorf("git -C ./sub add (cwd in zone): got %s, want approve", got.Decision)
	}
	// CWD out of zone; a relative ./sub is also out of zone.
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C ./sub status", "/etc"))); got.Decision != hookio.NoOpinion {
		t.Errorf("git -C ./sub (cwd out of zone): got %s, want abstain", got.Decision)
	}
	// Multiple -C compound (git applies each relative one on top of the running dir).
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C /home/user/project -C sub status", projectCWD))); got.Decision != hookio.Approve {
		t.Errorf("git -C /home/user/project -C sub status: got %s, want approve", got.Decision)
	}
}

// -C classification for the Approve-returning subcommands OUTSIDE both maps:
// checkout / soft reset are write-class; read-only `git remote` is read-class.
func TestGit_Chdir_OutsideMapSubcommands(t *testing.T) {
	r := newWithProject(t)
	// checkout is write-class: writable zone Approves, read-only zone Abstains.
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C /home/user/project checkout main", projectCWD))); got.Decision != hookio.Approve {
		t.Errorf("git -C <rw> checkout: got %s, want approve", got.Decision)
	}
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C /nix/store/abc123-foo checkout main", projectCWD))); got.Decision != hookio.NoOpinion {
		t.Errorf("git -C <ro> checkout: got %s, want abstain (write-class)", got.Decision)
	}
	// soft reset is write-class.
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C /nix/store/abc123-foo reset --soft HEAD~1", projectCWD))); got.Decision != hookio.NoOpinion {
		t.Errorf("git -C <ro> reset --soft: got %s, want abstain (write-class)", got.Decision)
	}
	// read-only remote is read-class: read-only zone still Approves, unknown zone Abstains.
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C /nix/store/abc123-foo remote -v", projectCWD))); got.Decision != hookio.Approve {
		t.Errorf("git -C <ro> remote -v: got %s, want approve (read-class)", got.Decision)
	}
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C /etc remote -v", projectCWD))); got.Decision != hookio.NoOpinion {
		t.Errorf("git -C <unknown> remote -v: got %s, want abstain", got.Decision)
	}
}

// TestGit_Chdir_SymbolicRef (pg2-1k8sd) pins symbolic-ref's read-class -C handling
// alongside remote's, per chdirSafe's doc: symbolicRefVerdict's only Approve path
// is the one-operand read, so by the time chdirSafe runs for it the read/write
// question is already settled and only readability need be checked.
func TestGit_Chdir_SymbolicRef(t *testing.T) {
	r := newWithProject(t)
	// read-only zone dir, read form -> Approve (read-class: readable is enough).
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C /nix/store/abc123-foo symbolic-ref --short HEAD", projectCWD))); got.Decision != hookio.Approve {
		t.Errorf("git -C <ro> symbolic-ref --short HEAD: got %s (%s), want approve (read-class)", got.Decision, got.Reason)
	}
	// unreadable zone, read form -> Abstain (demoted from the would-be Approve).
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C /etc symbolic-ref HEAD", projectCWD))); got.Decision != hookio.NoOpinion {
		t.Errorf("git -C <unknown> symbolic-ref HEAD: got %s (%s), want abstain", got.Decision, got.Reason)
	}
	// writable zone, read form -> still Approve (writable implies readable here).
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C /home/user/project symbolic-ref HEAD", projectCWD))); got.Decision != hookio.Approve {
		t.Errorf("git -C <rw> symbolic-ref HEAD: got %s (%s), want approve", got.Decision, got.Reason)
	}
	// the two-operand SET form is already NoOpinion with no -C at all, so chdirSafe
	// never runs (chdirSafe only demotes a would-be Approve) — an unsafe -C dir
	// must not turn it into anything ELSE, matching TestGit_Chdir_NonApproveVerdicts_Unaffected.
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C /home/user/project symbolic-ref HEAD refs/heads/other", projectCWD))); got.Decision != hookio.NoOpinion {
		t.Errorf("git -C <rw> symbolic-ref HEAD refs/heads/other: got %s (%s), want abstain (write form, unaffected by -C)", got.Decision, got.Reason)
	}
}

// The demotion only touches a would-be Approve. Ask/Reject verdicts are
// unaffected by an unsafe -C dir (most-restrictive aggregation already covers them).
func TestGit_Chdir_NonApproveVerdicts_Unaffected(t *testing.T) {
	r := newWithProject(t)
	// THE ASK WITNESS. `git clean -fd` used to be it, but pg2-u0e0c made every `clean`
	// spelling an Abstain, and Abstain IS the demotion target — so it can no longer
	// distinguish "left alone" from "demoted". `git config core.hooksPath` carried the
	// claim next, but pg2-3zgcf (2026-09-07) escalated it to Reject, so
	// `git config credential.helper` — a configSink that ruling did NOT touch — is the
	// witness now.
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C /etc config credential.helper /tmp/ch", projectCWD))); got.Decision != hookio.Ask {
		t.Errorf("git -C /etc config credential.helper: got %s, want ask (an unsafe -C dir must not demote an Ask)", got.Decision)
	}
	// core.hooksPath itself is asserted too, at its NEW level: an unsafe -C dir must
	// not demote a Reject either, and this is the one pg2-3zgcf escalation this file
	// otherwise would not exercise.
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C /etc config core.hooksPath /tmp/h", projectCWD))); got.Decision != hookio.Reject {
		t.Errorf("git -C /etc config core.hooksPath: got %s, want reject (pg2-3zgcf; an unsafe -C dir must not demote a Reject)", got.Decision)
	}
	// `clean` is asserted anyway, for the same reason `reset --hard` is below: the -C
	// gate must not turn an already-abstaining verdict into anything ELSE.
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C /etc clean -fd", projectCWD))); got.Decision != hookio.NoOpinion {
		t.Errorf("git -C /etc clean -fd: got %s, want abstain (pg2-u0e0c; the -C gate leaves a non-Approve alone)", got.Decision)
	}
	// `reset --hard` is an Abstain since pg2-ur9zc (operator ruling pg2-4yy4r item
	// 4), so it can no longer witness "an unsafe -C dir does not demote a
	// non-Approve" — Abstain IS the demotion target. It is asserted here anyway,
	// because the -C gate must not turn it into anything ELSE: chdirSafe fires only
	// on a would-be Approve, so an already-abstaining verdict passes through
	// untouched, reason and all.
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C /etc reset --hard HEAD", projectCWD))); got.Decision != hookio.NoOpinion {
		t.Errorf("git -C /etc reset --hard: got %s, want abstain (the -C gate leaves a non-Approve alone)", got.Decision)
	}
	// push --force is a Reject since pg2-bohpm (was Ask); either way the -C gate
	// must leave it alone.
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C /etc push --force", projectCWD))); got.Decision != hookio.Reject {
		t.Errorf("git -C /etc push --force: got %s, want reject (unchanged by the -C gate)", got.Decision)
	}
	// tag -> Reject even with an unsafe -C dir.
	if got := hookio.Verdict(r.Evaluate(chdirInput("git -C /etc tag v1.0", projectCWD))); got.Decision != hookio.Reject {
		t.Errorf("git -C /etc tag: got %s, want reject (unchanged)", got.Decision)
	}
}

// A bare git command (no -C) MUST keep its verdict regardless of CWD zone —
// the -C gate must never demote a chdir-less command.
func TestGit_Chdir_BareGit_Unaffected_ByCWDZone(t *testing.T) {
	r := newWithProject(t)
	// CWD out of every zone; bare git must still Approve.
	approve := []string{"git status", "git add .", "git commit -m x", "git log"}
	for _, cmd := range approve {
		got := hookio.Verdict(r.Evaluate(chdirInput(cmd, "/etc")))
		if got.Decision != hookio.Approve {
			t.Errorf("bare %q (cwd out of zone): got %s (%s), want approve (no -C gate)", cmd, got.Decision, got.Reason)
		}
	}
}

// A nil evaluator preserves legacy behavior: the -C path check is skipped.
func TestGit_Chdir_NilEvaluator_Legacy_Approve(t *testing.T) {
	r := New(nil)
	approve := []string{
		"git -C /etc status",
		"git -C /nix/store/abc add .",
	}
	for _, cmd := range approve {
		got := hookio.Verdict(r.Evaluate(chdirInput(cmd, projectCWD)))
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q (nil eval): got %s, want approve (legacy behavior)", cmd, got.Decision)
		}
	}
}

// RCE guard regression: a pre-subcommand -c still Abstains (for a non-escalated key)
// or Rejects (for one of pg2-3zgcf's escalated keys), even with a safe -C dir and a
// configured evaluator (the guard fires before the -C path logic either way).
func TestGit_Chdir_ConfigInjection_StillAbstains(t *testing.T) {
	r := newWithProject(t)
	if got := hookio.Verdict(r.Evaluate(chdirInput(`git -C /home/user/project -c credential.helper="touch /tmp/pwned" log`, projectCWD))); got.Decision != hookio.NoOpinion {
		t.Errorf("git -C <rw> -c credential.helper=EVIL log: got %s, want abstain (RCE guard)", got.Decision)
	}
	// core.pager is one of pg2-3zgcf's nine escalated keys (2026-09-07): its -c route
	// rejects outright rather than abstaining, and that decisive verdict must
	// likewise survive an otherwise-safe -C directory.
	if got := hookio.Verdict(r.Evaluate(chdirInput(`git -C /home/user/project -c core.pager="touch /tmp/pwned" log`, projectCWD))); got.Decision != hookio.Reject {
		t.Errorf("git -C <rw> -c core.pager=EVIL log: got %s, want reject (RCE guard, escalated per pg2-3zgcf)", got.Decision)
	}
}

const (
	casNew  = "1111111111111111111111111111111111111111"
	casOld  = "2222222222222222222222222222222222222222"
	casZero = "0000000000000000000000000000000000000000"
)

// The diverged-repo reconcile (operator ruling 2026-11-03, Phillip: "2b should be
// agents do all of it") runs two commands against the canonical clone from a
// worktree: the primary-branch compare-and-swap and `reset --keep`. Narrow approval
// chosen: the exact three-operand CAS shape, not a script-by-name rule. The -C
// target is the canonical clone, a writable zone of the session, so chdirSafe
// does not withdraw either approval.
func TestGit_Chdir_ReconcileCommands_CanonicalClone(t *testing.T) {
	r := newWithProject(t)
	const canon = projectCWD // the canonical clone; the session runs in a worktree under it
	wtCWD := canon + "/.worktrees/drain"
	for _, cmd := range []string{
		"git -C " + canon + " update-ref refs/heads/main " + casNew + " " + casOld,
		"git -C " + canon + " update-ref refs/heads/master " + casNew + " " + casOld,
		"git -C " + canon + " reset --keep " + casNew,
		"git -C " + canon + " read-tree -m -u " + casOld + " " + casNew,
	} {
		if got := hookio.Verdict(r.Evaluate(chdirInput(cmd, wtCWD))); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve", cmd, got.Decision, got.Reason)
		}
	}
	for _, cmd := range []string{
		"git -C " + canon + " update-ref refs/heads/main " + casNew,                           // two-operand: unconditional overwrite
		"git -C " + canon + " update-ref -d refs/heads/main",                                  // delete
		"git -C " + canon + " update-ref -d refs/heads/main " + casOld,                        // delete with old
		"git -C " + canon + " update-ref --delete refs/heads/main " + casOld,                  // delete
		"git -C " + canon + " update-ref --stdin",                                             // batch
		"git -C " + canon + " update-ref -m why refs/heads/main " + casNew + " " + casOld,     // any flag
		"git -C " + canon + " update-ref --no-deref refs/heads/main " + casNew + " " + casOld, // any flag
		"git -C " + canon + " update-ref refs/heads/feature " + casNew + " " + casOld,         // non-primary ref
		"git -C " + canon + " update-ref HEAD " + casNew + " " + casOld,                       // not refs/heads/<primary>
		"git -C " + canon + " update-ref refs/remotes/origin/main " + casNew + " " + casOld,   // remote-tracking ref
		"git -C " + canon + " update-ref refs/tags/main " + casNew + " " + casOld,             // tag
		"git -C " + canon + " update-ref refs/heads/main " + casZero + " " + casOld,           // zero new id deletes
		"git -C " + canon + " update-ref refs/heads/main " + casNew + " " + casZero,           // zero old id: not a swap
		"git -C " + canon + " update-ref refs/heads/main abc123 " + casOld,                    // abbreviated id
		"git -C " + canon + " update-ref refs/heads/main " + casNew + " " + casOld + " x",     // four operands
		"git -C " + canon + " reset --hard " + casNew,
		"git -C " + canon + " reset --hard",
		"git -C " + canon + " read-tree -m " + casOld + " " + casNew,                   // no -u
		"git -C " + canon + " read-tree -u -m " + casOld + " " + casNew,                // flag order
		"git -C " + canon + " read-tree -mu " + casOld + " " + casNew,                  // combined flags
		"git -C " + canon + " read-tree --reset -u " + casNew,                          // reset form
		"git -C " + canon + " read-tree -m -u " + casNew,                               // one tree
		"git -C " + canon + " read-tree -m -u " + casOld + " " + casNew + " " + casOld, // three trees
		"git -C " + canon + " read-tree -m -u HEAD " + casNew,                          // ref name
		"git -C " + canon + " read-tree -m -u abc123 " + casNew,                        // abbreviated id
		"git -C " + canon + " read-tree -m -u " + casZero + " " + casNew,               // zero id
		"git -C " + canon + " read-tree -m -u --prefix=x/ " + casOld + " " + casNew,    // extra flag
		"git -C " + canon + " read-tree -m -u " + casOld + " " + casNew + " --empty",   // trailing flag
		"git -C /etc read-tree -m -u " + casOld + " " + casNew,                         // unsafe -C zone
		"GIT_DIR=/other git read-tree -m -u " + casOld + " " + casNew,
		"git -C /etc update-ref refs/heads/main " + casNew + " " + casOld, // unsafe -C zone
		"git -C /etc reset --keep " + casNew,
		"GIT_DIR=/other git update-ref refs/heads/main " + casNew + " " + casOld,
	} {
		if got := hookio.Verdict(r.Evaluate(chdirInput(cmd, wtCWD))); got.Decision == hookio.Approve {
			t.Errorf("cmd %q: got approve (%s), want not approved", cmd, got.Reason)
		}
	}
}

// TestGit_Chdir_WorktreeVerbGrants pins CETA's verdict for the `git -C <wt> <verb>`
// shapes that the per-verb permission grants for worker roles rely on. The
// permission pattern matcher is broad (`git -C * <verb> *`), so these rules are
// the only guard for the denied forms. Expected verdicts are recorded from the
// rule's behavior at the time the table was written.
func TestGit_Chdir_WorktreeVerbGrants(t *testing.T) {
	r := newWithProject(t)
	wt := projectCWD + "/.worktrees/x"
	cases := []struct {
		name string
		cmd  string
		want hookio.Decision
	}{
		// -c placed AFTER -C must still hit the config-key injection reject.
		{"c after C fsmonitor", "git -C " + wt + " -c core.fsmonitor=x status", hookio.Reject},
		{"c after C hooksPath", "git -C " + wt + " -c core.hooksPath=/dev/null status", hookio.Reject},
		// Destructive verbs are not approved (abstain defers to the prompt).
		{"reset hard", "git -C " + wt + " reset --hard", hookio.NoOpinion},
		{"clean fdx", "git -C " + wt + " clean -fdx", hookio.NoOpinion},
		{"force push", "git -C " + wt + " push --force origin x", hookio.Reject},
		{"config write", "git -C " + wt + " config user.name x", hookio.Reject},
		{"gc", "git -C " + wt + " gc", hookio.NoOpinion},
		{"prune", "git -C " + wt + " prune", hookio.NoOpinion},
		// force-with-lease is approved on purpose: the force-push reject message
		// steers agents to it. filter-branch rewrites history and abstains.
		{"force-with-lease push (approved, intended)", "git -C " + wt + " push --force-with-lease origin x", hookio.Approve},
		{"filter-branch", "git -C " + wt + " filter-branch", hookio.NoOpinion},
		// Positive controls that must stay approved.
		{"checkout detach FETCH_HEAD", "git -C " + wt + " checkout --detach FETCH_HEAD", hookio.Approve},
		{"add -A", "git -C " + wt + " add -A", hookio.Approve},
		{"commit -q -F", "git -C " + wt + " commit -q -F f", hookio.Approve},
		{"status", "git -C " + wt + " status", hookio.Approve},
		{"push HEAD to explicit ref", "git push origin HEAD:refs/heads/x", hookio.Approve},
	}
	for _, tc := range cases {
		got := hookio.Verdict(r.Evaluate(chdirInput(tc.cmd, projectCWD)))
		if got.Decision != tc.want {
			t.Errorf("%s: cmd %q: got %s (%s), want %s", tc.name, tc.cmd, got.Decision, got.Reason, tc.want)
		}
	}
}
