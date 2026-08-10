package gitdir

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/secretpath"
)

// leavesOf mirrors how engine.EvaluateExpression decomposes an expression into the
// per-leaf raw texts it hands to each rule, so an expression-scope test exercises
// the rule exactly as the engine drives it.
func leavesOf(expr string) []string {
	var out []string
	for _, pc := range cmdparse.Parse(expr) {
		out = append(out, pc.Raw)
	}
	return out
}

func bashJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

func fileJSON(path string) json.RawMessage {
	b, _ := json.Marshal(hookio.FileToolInput{FilePath: path})
	return b
}

func searchJSON(path string) json.RawMessage {
	b, _ := json.Marshal(hookio.SearchToolInput{Pattern: "x", Path: path})
	return b
}

func bashInput(cmd string) *hookio.HookInput {
	return &hookio.HookInput{ToolName: "Bash", ToolInput: bashJSON(cmd)}
}

// TestGitDir_Bash is the rule-scope decision table.
//
// `matched` exists because the read verdict is now Abstain (tc-403c), which makes
// a MATCHED plain read and a NON-match textually identical in their Decision. They
// are not the same thing: a matched read means "this rule looked, saw an access it
// has no verdict on, and handed the leaf to the rest of the chain", while a
// non-match means the rule never recognised an access at all — and it is the
// non-match that the pg2-3hk7t false-positive classes are about. The rule
// distinguishes them by carrying a Reason only when it matched, so the tests do
// too; without this column a regression that stopped recognising `.git/` paths
// entirely would pass every read row here.
func TestGitDir_Bash(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
		matched bool
	}{
		// Writes stay a hard block.
		{"write into .git", "echo x > .git/hooks/pre-commit", hookio.Reject, true},
		{"append into .git", "echo x >> .git/config", hookio.Reject, true},
		{"rm .git dir", "rm -rf .git/", hookio.Reject, true},
		{"rm bare .git", "rm -rf .git", hookio.Reject, true},
		{"sed -i on .git config", "sed -i 's/a/b/' .git/config", hookio.Reject, true},
		{"sed -i bundled on .git config", "sed -ri 's/a/b/' .git/config", hookio.Reject, true},
		{"tee into .git", "tee .git/config", hookio.Reject, true},
		{"chmod a hook", "chmod +x .git/hooks/pre-commit", hookio.Reject, true},
		{"truncate a ref", "truncate -s 0 .git/refs/heads/main", hookio.Reject, true},
		{"cp ONTO .git config", "cp /tmp/evil .git/config", hookio.Reject, true},
		{"mv ONTO .git config", "mv /tmp/evil .git/config", hookio.Reject, true},
		{"unknown command fails safe to write", "frobnicate .git/config", hookio.Reject, true},
		{"exec a hook script", ".git/hooks/pre-commit", hookio.Reject, true},
		// The two spellings pg2-24sc9 names verbatim as the guard's floor: the
		// false-positive fix MUST NOT have been a blanket removal of the guard.
		{"pg2-24sc9 floor: rm -rf .git/hooks", "rm -rf .git/hooks", hookio.Reject, true},
		{"pg2-24sc9 floor: truncating redirect onto .git/config", "echo x > .git/config", hookio.Reject, true},
		// The exclusion-flag set is named flag-by-flag rather than "any flag's
		// value", precisely so a destructive operand that happens to follow a flag
		// is still caught.
		{"rm operand following a flag still rejects", "rm -rf .git", hookio.Reject, true},
		{"rm -r operand following a flag still rejects", "rm -r .git/objects", hookio.Reject, true},
		{"git porcelain with a redirect INTO .git", "git status > .git/stolen", hookio.Reject, true},

		// A COPY-OUT — a read whose destination is a write — Asks (tc-403c).
		{"cp FROM .git copies metadata out", "cp .git/config /tmp/backup", hookio.Ask, true},
		{"ln publishes a second name for .git metadata", "ln -s .git/config /tmp/link", hookio.Ask, true},
		{"capturing redirect is the same copy by another spelling", "cat .git/config > /tmp/backup", hookio.Ask, true},

		// A PLAIN read is matched but non-decisive: no verdict, the chain continues.
		{"cat .git config", "cat .git/config", hookio.Abstain, true},
		{"nested .git", "cat repo/.git/HEAD", hookio.Abstain, true},
		{"trailing /.git", "ls foo/.git", hookio.Abstain, true},
		{"stat a ref", "stat .git/refs/heads/main", hookio.Abstain, true},
		{"readlink a hook", "readlink .git/hooks/pre-commit", hookio.Abstain, true},
		{"test -e a hook", "[ -e .git/hooks/pre-commit ]", hookio.Abstain, true},
		{"if test -e a hook", "if [ -e .git/hooks/pre-commit ]", hookio.Abstain, true},
		{"wc a ref", "wc -l .git/info/exclude", hookio.Abstain, true},
		{"head a ref", "head -5 .git/HEAD", hookio.Abstain, true},
		{"diff two refs", "diff .git/HEAD /tmp/head", hookio.Abstain, true},
		{"sed WITHOUT -i streams to stdout", "sed -n '1p' .git/config", hookio.Abstain, true},
		{"grep a hook file", "grep prek .git/hooks/pre-commit", hookio.Abstain, true},
		{"stdin FROM .git is a read", "wc -l < .git/config", hookio.Abstain, true},
		// A redirection that captures NOTHING leaves a read a read — the shape the
		// corpus actually carries (rows 474/475, 3200/3204: `… 2>/dev/null`).
		{"stderr discard does not capture the file", "ls -la .git/hooks 2>/dev/null", hookio.Abstain, true},
		{"stdout to /dev/null captures nothing", "cat .git/config > /dev/null", hookio.Abstain, true},

		// Non-accesses: nothing is matched at all.
		{"bare git command not blocked", "git status", hookio.Abstain, false},
		{"git config not blocked", "git config user.name", hookio.Abstain, false},
		{"gitignore not blocked", "cat .gitignore", hookio.Abstain, false},
		{"git-suffixed name not blocked", "cat .git.bak", hookio.Abstain, false},
		{"unrelated", "ls -la", hookio.Abstain, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Evaluate(bashInput(tt.command))
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v", got.Decision, tt.want)
			}
			if matched := got.Reason != ""; matched != tt.matched {
				t.Errorf("matched = %v (reason %q), want matched = %v", matched, got.Reason, tt.matched)
			}
		})
	}
}

// TestGitDir_CopyOutIsNotAPlainRead pins tc-403c's mechanism at rule scope.
//
// THE DEFECT: `cp .git/config /tmp/backup` is classified a READ — cp does not
// modify its source, a distinction this rule keeps on purpose — so under tc-k2m3's
// approving read verdict it AUTO-APPROVED, copying a file that can carry a token in
// a remote URL to an arbitrary destination with no prompt. Making the read verdict
// non-decisive does not fix that on its own: every later rule approves the shape
// too (`safe-commands` sees a readable source and a writable destination), so the
// copy-out needs a verdict of its own.
//
// THE FAILURE DIRECTION, which is the property under test: a read whose bytes LEAVE
// the guarded directory fails toward PROMPTING. Not Reject — nothing is modified and
// backing up `.git/config` before editing it is legitimate, and a non-overridable
// deny on a non-destructive operation is what softened the read side twice already.
// Not Abstain — deferring hands the decision to a layer that does not know the
// source is git metadata.
//
// The negative cases carry as much weight as the positives. Promoting every
// redirection would swallow `2>/dev/null`, which the corpus attaches to routine
// `ls`/`cat` inspections (rows 474, 475, 3200, 3204) and which captures none of the
// file; promoting a destination that stores nothing would swallow `> /dev/null`.
func TestGitDir_CopyOutIsNotAPlainRead(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		// The bead's named shape, and its equivalents.
		{"cp from gitmeta", "cp .git/config /tmp/backup", hookio.Ask},
		{"cp -p from gitmeta", "cp -p .git/config /tmp/backup", hookio.Ask},
		{"cp -r the whole dir out", "cp -r .git /tmp/backup", hookio.Ask},
		{"install from gitmeta", "install -m 644 .git/config /tmp/out", hookio.Ask},
		{"ln -s at gitmeta", "ln -s .git/config /tmp/link", hookio.Ask},
		{"ln -sf at gitmeta", "ln -sf .git/config /tmp/link", hookio.Ask},
		// Same copy, spelled as a capturing redirection.
		{"stdout capture", "cat .git/config > /tmp/backup", hookio.Ask},
		{"stdout append capture", "cat .git/config >> /tmp/backup", hookio.Ask},
		{"&> capture", "grep url .git/config &> /tmp/backup", hookio.Ask},
		{"capture of a listing", "ls -la .git/hooks > /tmp/list", hookio.Ask},
		// A path BOUND to a variable and copied out through it is the same access.
		{"bound path is cp'd out", "f=/repo/.git/config\ncp \"$f\" /tmp/backup", hookio.Ask},

		// NOT a copy-out: nothing is captured.
		{"stderr discard", "cat .git/config 2>/dev/null", hookio.Abstain},
		{"stderr discard on a listing", "ls -la .git/hooks 2>/dev/null", hookio.Abstain},
		{"stdout to /dev/null", "cat .git/config > /dev/null", hookio.Abstain},
		{"stdout to the tty", "cat .git/config > /dev/tty", hookio.Abstain},
		{"stdout to an inherited fd", "cat .git/config > /dev/fd/3", hookio.Abstain},
		{"stdin FROM gitmeta is not a capture", "wc -l < .git/config", hookio.Abstain},

		// A copy-out must never MASK a write: the write side still Rejects.
		{"cp ONTO gitmeta is still a write", "cp /tmp/evil .git/config", hookio.Reject},
		{"redirect ONTO gitmeta is still a write", "cat /tmp/evil > .git/config", hookio.Reject},
		{"mv out is destructive, not a copy-out", "mv .git/HEAD /tmp/x", hookio.Reject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hookio.Decision(hookio.Approve)
			for _, leaf := range leavesOf(tt.command) {
				in := &hookio.HookInput{
					ToolName:       "Bash",
					ToolInput:      bashJSON(leaf),
					RootExpression: tt.command,
				}
				if d := r.Evaluate(in).Decision; d > got {
					got = d
				}
			}
			if got != tt.want {
				t.Errorf("Decision = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGitDir_DestructiveOnSourceOperand pins the operand-GEOMETRY half of the
// direction split: a command can be destructive on an operand that a naive
// "last operand is the destination" model reads as a mere source.
//
// The defect this fixes: `mv` was grouped with cp/ln/install, so
// `mv .git/HEAD /tmp/x` classified as a READ and returned the read verdict. Moving git
// metadata away DESTROYS it at the source — a rename is a write to BOTH operands,
// and downgrading it from a hard block to an overridable prompt is exactly the
// hole the direction split was supposed to close. The original regression guards
// covered `sed -i`, redirections and `rm`, but no rename/move shape at all.
//
// The contrast cases matter as much as the fix: cp/ln/install genuinely only READ
// their source, so those must stay OFF the write side or the split becomes
// "everything is a write" and the Class-3 false positives come straight back. Since
// tc-403c they land on the copy-out verdict (Ask) rather than the plain-read one —
// still strictly less restrictive than `mv`'s Reject, which is the asymmetry these
// cases exist to pin.
func TestGitDir_DestructiveOnSourceOperand(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		// mv/rename destroy the SOURCE: both operands are writes.
		{"mv gitmeta AWAY", "mv .git/HEAD /tmp/x", hookio.Reject},
		{"mv gitmeta INTO", "mv /tmp/x .git/HEAD", hookio.Reject},
		{"mv the whole dir away", "mv .git /tmp/stash", hookio.Reject},
		{"mv -f a hook away", "mv -f .git/hooks/pre-commit /tmp/", hookio.Reject},
		{"rename over gitmeta", "rename 's/HEAD/OLD/' .git/HEAD", hookio.Reject},
		// A rename bound through a variable is a write too.
		{"bound path is mv'd away", "f=/repo/.git/HEAD\nmv \"$f\" /tmp/x", hookio.Reject},

		// cp/ln/install only READ their source — these MUST stay off the write side.
		{"cp FROM gitmeta", "cp .git/config /tmp/backup", hookio.Ask},
		{"cp -p FROM gitmeta", "cp -p .git/config /tmp/backup", hookio.Ask},
		{"ln -s AT gitmeta", "ln -s .git/config /tmp/link", hookio.Ask},
		{"ln -sf AT gitmeta", "ln -sf .git/config /tmp/link", hookio.Ask},
		{"install FROM gitmeta", "install -m 644 .git/config /tmp/out", hookio.Ask},

		// …but writing INTO gitmeta with the same commands is a write.
		{"cp INTO gitmeta", "cp /tmp/evil .git/config", hookio.Reject},
		{"ln -sf CLOBBERS a hook", "ln -sf /tmp/evil .git/hooks/pre-commit", hookio.Reject},
		{"install INTO a hook", "install -m 755 /tmp/evil .git/hooks/pre-commit", hookio.Reject},

		// The destination can also come from a FLAG, inverting the geometry so the
		// last operand is a SOURCE. Fail safe rather than read the wrong end.
		{"install -t INTO gitmeta dir", "install -t .git/hooks /tmp/evil", hookio.Reject},
		{"cp -t INTO gitmeta dir", "cp -t .git/hooks /tmp/evil", hookio.Reject},
		{"install -d CREATES gitmeta dirs", "install -d .git/hooks /tmp/other", hookio.Reject},

		// Commands on the read allowlist that a flag turns into writers.
		{"find -delete", "find .git/objects -type f -delete", hookio.Reject},
		{"find -exec", "find .git -type f -exec rm {} ;", hookio.Reject},
		{"sort -o writes its output file", "sort -o .git/config /tmp/in", hookio.Reject},
		{"yq -i edits in place", "yq -i '.a=1' .git/config", hookio.Reject},
		{"find WITHOUT a mutating flag still reads", "find .git/objects -type f", hookio.Abstain},
		{"sort WITHOUT -o still reads", "sort .git/config", hookio.Abstain},

		// dd names its operands as key=value, so a component walk over the whole
		// token misses the path entirely.
		{"dd of= writes gitmeta", "dd of=.git/HEAD if=/dev/zero", hookio.Reject},
		{"dd if= reads gitmeta but dd is not on the read list", "dd if=.git/HEAD of=/tmp/x", hookio.Reject},

		// Already-correct destructive commands, pinned so they cannot drift.
		{"truncate", "truncate -s 0 .git/HEAD", hookio.Reject},
		{"rsync --delete INTO gitmeta", "rsync -a --delete /tmp/src/ .git/", hookio.Reject},
		{"tar -x into gitmeta via -C", "tar -xf /tmp/a.tar -C .git", hookio.Reject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Fold the leaves most-restrictive-wins with RootExpression set, exactly as
			// engine.EvaluateExpression drives the rule, so the multi-leaf bound-path
			// case is exercised the same way as the single-leaf ones.
			got := hookio.Decision(hookio.Approve)
			for _, leaf := range leavesOf(tt.command) {
				in := &hookio.HookInput{
					ToolName:       "Bash",
					ToolInput:      bashJSON(leaf),
					RootExpression: tt.command,
				}
				if d := r.Evaluate(in).Decision; d > got {
					got = d
				}
			}
			if got != tt.want {
				t.Errorf("Decision = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGitDir_ReadWriteAsymmetry is the ONE test that pins EVERY direction the rule
// distinguishes TOGETHER, on the SAME path, in the same assertion — so that a
// refactor cannot collapse them into a shared verdict without failing here
// (tc-k2m3, extended by tc-403c).
//
// Why one grouped test and not three independent ones. The rule's entire value is
// the ASYMMETRY: on `.git/hooks`, a read defers, a copy-out prompts, a write is a
// hard block. Split across separate tests, a change that made verdict() return one
// decision for two directions fails only the half that moved, which reads as "one
// expectation drifted" rather than "the security property was deleted" — and if the
// collapse lands on the permissive side, the surviving failure is the harmless one.
// Pinning the ordering makes the property itself the unit under test: the three
// decisions must be strictly increasing in restrictiveness, in that order.
//
// The reasons are asserted verbatim as well, because they have been wrong before
// in a way no decision assertion could catch: the pre-pg2-3hk7t rule told the user
// it was refusing to let them MODIFY git metadata when they had only run `ls`. A
// reason that misdescribes the direction is its own defect.
func TestGitDir_ReadWriteAsymmetry(t *testing.T) {
	r := New()
	const path = ".git/hooks"

	read := r.Evaluate(bashInput("ls -la " + path))
	copyOut := r.Evaluate(bashInput("cp -r " + path + " /tmp/backup"))
	write := r.Evaluate(bashInput("rm -rf " + path))

	// The asymmetry itself: same path, three distinct verdicts in increasing
	// restrictiveness. This is the assertion a collapse of the direction split must
	// break, whichever pair it collapses.
	if !(read.Decision < copyOut.Decision && copyOut.Decision < write.Decision) {
		t.Fatalf("directions on %s are not strictly increasing: read %v, copy-out %v, write %v",
			path, read.Decision, copyOut.Decision, write.Decision)
	}

	// …and the exact verdicts, so "increasing" cannot drift to some other triple.
	//
	// Abstain, not Approve: a plain read is NON-DECISIVE, so `path-traversal`,
	// `secrets` and the zone checks below this rule still run (tc-403c). An Approve
	// here would end the chain for the leaf and is exactly the defect.
	if read.Decision != hookio.Abstain {
		t.Errorf("read Decision = %v, want Abstain", read.Decision)
	}
	if want := "reading git metadata under .git/ is a read-only inspection (no verdict; later rules decide)"; read.Reason != want {
		t.Errorf("read Reason = %q, want %q", read.Reason, want)
	}
	// A matched read MUST still carry a reason: that is the only thing separating it
	// from the rule having recognised no access at all.
	if read.Module != "git-directory" {
		t.Errorf("read Module = %q, want git-directory", read.Module)
	}
	if copyOut.Decision != hookio.Ask {
		t.Errorf("copy-out Decision = %v, want Ask", copyOut.Decision)
	}
	if want := "copying git metadata out of .git/ to another location — .git/config can carry a credential in a remote URL"; copyOut.Reason != want {
		t.Errorf("copy-out Reason = %q, want %q", copyOut.Reason, want)
	}
	if write.Decision != hookio.Reject {
		t.Errorf("write Decision = %v, want Reject", write.Decision)
	}
	if want := "refusing to write git metadata under .git/ directly — modify it through git commands only"; write.Reason != want {
		t.Errorf("write Reason = %q, want %q", write.Reason, want)
	}
}

// TestGitDir_SecretsDoesNotCoverGitPaths records the credential finding behind
// tc-k2m3's read approval, as an executable statement rather than a comment.
//
// `.git/config` routinely holds remote URLs, and a remote URL can embed a token
// (`https://x-access-token:ghp_…@github.com/…`). Approving `.git/` reads therefore
// raises the question of whether the `secrets` rule still prompts for such a read.
// It does not, and the reason is NOT the first-match-wins ordering that would
// short-circuit it anyway: secretpath.IsSecret does not match `.git/` paths at all,
// so the coverage is absent outright.
//
// This test exists so that fact cannot rot silently. If someone later widens
// secretpath to cover git metadata, this test fails and forces them to re-read
// gitdir's KNOWN GAP note — where the point is made that widening secretpath alone
// does NOT close the gap, because this rule short-circuits `secrets` regardless.
func TestGitDir_SecretsDoesNotCoverGitPaths(t *testing.T) {
	for _, path := range []string{
		".git/config",
		"/repo/.git/config",
		".git/hooks/pre-commit",
		".git/info/exclude",
	} {
		if secretpath.IsSecret(path) {
			t.Errorf("secretpath.IsSecret(%q) = true, want false — see gitdir's KNOWN GAP note: "+
				"widening secretpath does not close the credential gap while git-directory "+
				"short-circuits the secrets rule", path)
		}
	}
}

// TestGitDir_QuotedHeredocBodyIsNotAnAccess pins class 1 of pg2-3hk7t: a
// git-metadata path that appears only as PROSE inside a quoted heredoc body
// performs zero filesystem access and MUST NOT be matched. Corpus row 126856 —
// a notification payload whose bead title mentioned `.git/index` — was hard-DENIED
// by the former raw substring match.
func TestGitDir_QuotedHeredocBodyIsNotAnAccess(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
	}{
		{"row 126856 shape: heredoc payload in an assignment", "PAYLOAD=$(cat <<'EOF'\n{\n  \"title\": \"infra-block: ziprecruiter .git/index is 0 bytes\"\n}\nEOF\n)"},
		{"commit message mentioning a path", "git commit -m 'stop reading .git/config directly'"},
		{"echoed prose mentioning a path", "echo 'see .git/config for the remote'"},
		{"bead title in a double-quoted arg", "bd create \"repo .git/index is 0 bytes\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Evaluate(bashInput(tt.command)).Decision; got != hookio.Abstain {
				t.Errorf("Decision = %v, want Abstain (prose is not a path operand)", got)
			}
		})
	}
}

// TestGitDir_ExclusionRolesAreNotAccesses pins class 2 of pg2-3hk7t: a token whose
// SYNTACTIC ROLE is to exclude git metadata proves the command does not touch it.
func TestGitDir_ExclusionRolesAreNotAccesses(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
	}{
		{"negated ripgrep glob (rows 288917/288920)", "rg -c mkBashScript /repo -g '!**/.git/**'"},
		{"exclusion glob list in an assignment", "EX=\"-g !**/docs/** -g !**/.git/** --hidden\""},
		{"grep -v filters it OUT (row 244194)", "grep -v \"/.git/\""},
		{"grep -v in a pipeline arg", "grep -rn foo /repo | grep -v \"/.git/\""},
		{"grep PATTERN is not a path", "grep -rn \"/.git/\" /repo"},
		{"find -path … -prune (rows 237022/259890)", "find . -path ./.git -prune -o -type f -print"},
		{"find -name … -prune", "find . -name .git -prune -o -print"},
		{"find NEGATED -path without -prune", "find . -type d ! -path './.git/*'"},
		// Rows 101602/124447: `tree -I '.git'` is a read-only listing that EXCLUDES
		// git metadata. Reading `-I`'s value as an operand made these a NEW hard deny.
		{"tree -I ignore pattern (rows 101602/124447)", "tree -L 3 /repo -I \".git\""},
		{"tree -I before the path operand", "tree -L 3 -I '.git' --charset ascii"},
		{"fd -E exclude pattern", "fd -E .git --type f ."},
		{"grep --exclude-dir separate value", "grep -rn foo /repo --exclude-dir .git"},
		{"grep --exclude-dir glued value", "grep -rn foo /repo --exclude-dir=.git"},
		{"git config -f reads THROUGH git (row 309585)", "git config -f /repo/.git/config --get core.fsmonitor"},
		{"git --git-dir is a git command", "git --git-dir=/repo/.git rev-parse HEAD"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Evaluate(bashInput(tt.command)).Decision; got != hookio.Abstain {
				t.Errorf("Decision = %v, want Abstain (exclusion/git-mediated role)", got)
			}
		})
	}
}

// TestGitDir_CensusFalsePositiveSpellings pins pg2-24sc9: the EXCLUSION spellings
// the false-positive census actually recorded, at this rule's own scope.
//
// TestGitDir_ExclusionRolesAreNotAccesses already covers the `!` spelling
// (`! -path './.git/*'`) and `-path … -prune`, but every census row that negated
// rather than pruned used find's OTHER negation, `-not -path`, which reaches
// pathOperands as a distinct token sequence: `!` is skipped by the
// `strings.HasPrefix(a, "!")` arm, whereas `-not` is skipped as a flag and the
// pattern that follows is skipped only because args[i-1] is `-path`. Those are two
// different code paths to the same verdict, and only one of them was pinned here —
// the `-not` spelling was pinned solely in the engine integration suite, so a
// change to pathOperands could regress it without any failure in this package.
//
// Rows 4-7 of the census carried no `.git` token as PRINTED: they were truncated
// at the first segment of a compound whose LATER segment held the exclusion. The
// reconstructed compounds are asserted at chain scope in
// TestIntegration_GitDirCensusFalsePositives; what belongs here is the
// `.git`-bearing segment those rows really ended with. Rows 6 and 7 get their own
// entries below; row 4's tail is the same `-name` + `-not -path` shape as row 3,
// and row 5's completion is a SQL argument that names no path at all, so neither
// adds a distinct token sequence for this rule.
func TestGitDir_CensusFalsePositiveSpellings(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
	}{
		// Census row 1, verbatim: an absolute-root walk that EXCLUDES git metadata.
		{"census row 1: -not -path with an absolute root", "find /Users/phillipg/phillipg_mbp -name '*pr-pool-event-model*' -not -path '*/.git/*'"},
		// Census row 3's spelling: `-name` glob plus a `-not -path` exclusion. The
		// `-name` pattern is a walk pattern too, so neither operand is a path.
		{"census row 3: -name glob with -not -path", "find . -name '*.go' -not -path './.git/*'"},
		{"census row 3 with a globbed exclusion", "find . -name '*.go' -not -path '*/.git/*' -print"},
		// The `.git`-bearing tails the truncated rows 6/7 really ended with.
		{"truncated row 6 tail: -maxdepth walk excluding .git", "find . -maxdepth 4 -name '*.md' -not -path '*/.git/*'"},
		{"truncated row 7 tail: two -not -path exclusions", "find . -maxdepth 3 -type d -not -path '*/.git/*' -not -path '*/node_modules/*'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Evaluate(bashInput(tt.command)).Decision; got != hookio.Abstain {
				t.Errorf("Decision = %v, want Abstain (a `-not -path` operand is an exclusion, not an access)", got)
			}
		})
	}
}

// TestGitDir_BoundPathDirectionFollowsItsUse pins class 3 of pg2-3hk7t together
// with its regression guard. An assignment binds a path and accesses nothing, so
// the direction comes from the sibling leaf that CONSUMES the variable — which is
// visible only at EXPRESSION scope (hookio.HookInput.RootExpression). The two
// shapes below are byte-identical at the assignment leaf; only the sibling differs.
func TestGitDir_BoundPathDirectionFollowsItsUse(t *testing.T) {
	r := New()
	tests := []struct {
		name string
		expr string
		want hookio.Decision
	}{
		{
			name: "row 244438 shape: bound path is sed -i'd → still Reject",
			expr: "f=\"$r/.git/info/exclude\"\ncat \"$f\"\nsed -i '' '/^x$/d' \"$f\"",
			want: hookio.Reject,
		},
		{
			name: "row 167117 shape: bound path is only read → read (Abstain)",
			expr: "RM=/repo/.git/worktrees/slot-c/rebase-merge\nls -la \"$RM\"\ncat \"$RM/done\"",
			want: hookio.Abstain,
		},
		{
			name: "row 163591 shape: bound hooks path read inside a substitution → read (Abstain)",
			expr: "h=\"$r/.git/hooks\"\necho \"active -> $(grep -m1 prek \"$h/pre-commit\")\"",
			want: hookio.Abstain,
		},
		{
			name: "row 184010 shape: bound hook path tested and readlink'd → read (Abstain)",
			expr: "h=\"$r/.git/hooks/pre-commit\"\nif [ -e \"$h\" ]\nthen echo \"present ($(readlink \"$h\"))\"",
			want: hookio.Abstain,
		},
		{
			name: "row 305013 shape: default-expansion binding read by ls/head → read (Abstain)",
			expr: "HP=\"${HP:-$CC/.git/hooks}\"\nls -la \"$HP/pre-push\"\nhead -5 \"$HP/pre-push\"",
			want: hookio.Abstain,
		},
		{
			name: "bound path copied out → copy-out (Ask)",
			expr: "f=\"$r/.git/config\"\ncp \"$f\" /tmp/backup",
			want: hookio.Ask,
		},
		{
			name: "bound path written by a redirection → Reject",
			expr: "f=\"$r/.git/config\"\necho x > \"$f\"",
			want: hookio.Reject,
		},
		{
			name: "bound path never consumed is undecidable → Reject",
			expr: "f=\"$r/.git/config\"\necho done",
			want: hookio.Reject,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The engine hands the rule one leaf at a time with RootExpression set to
			// the whole expression, then folds most-restrictive-wins. This test drives
			// ONE rule, so it folds only the leaves where THIS rule MATCHED an access.
			//
			// A leaf is selected by whether it carries a REASON, not by its decision.
			// git-directory speaks only at the leaf holding the literal `.git/` token —
			// the assignment — and recognises nothing at the consuming leaves
			// (`ls -la "$RM"`), which see a bare variable. Selecting on the decision
			// worked only while every direction was decisive: since tc-403c a plain read
			// returns Abstain, which is byte-for-byte the "recognised nothing" verdict,
			// so a decision-based filter would drop the very leaf under test and then
			// report the fold's Approve seed for every read shape here. The reason is
			// the rule's own record that it matched, so it is the correct selector.
			//
			// NOTE the sibling leaves are not rescued by later rules either — the whole
			// expression really does land on Abstain through the full chain, because the
			// variable's value is not statically known to them. That is pinned as such
			// in engine_integration_test.go (rows 167117 and 163591). This test is
			// deliberately about the DIRECTION this rule assigns, and the end-to-end
			// verdict is pinned over there rather than inferred from here.
			//
			// So: skip leaves the rule did not match, and require that at least one leaf
			// spoke — otherwise a rule that went entirely silent would read as Approve.
			got := hookio.Decision(hookio.Approve)
			spoke := false
			for _, leaf := range leavesOf(tt.expr) {
				in := &hookio.HookInput{
					ToolName:       "Bash",
					ToolInput:      bashJSON(leaf),
					RootExpression: tt.expr,
				}
				res := r.Evaluate(in)
				if res.Reason == "" {
					continue
				}
				spoke = true
				if res.Decision > got {
					got = res.Decision
				}
			}
			if !spoke {
				t.Fatalf("no leaf produced a decision — the rule went silent on %q", tt.expr)
			}
			if got != tt.want {
				t.Errorf("folded Decision = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGitDir_LeafScopeFailsSafeWithoutRootExpression pins that a bound path with
// NO expression scope available (a direct call, RootExpression empty) still fails
// safe to a write rather than silently becoming a read.
func TestGitDir_LeafScopeFailsSafeWithoutRootExpression(t *testing.T) {
	r := New()
	got := r.Evaluate(bashInput("f=\"$r/.git/info/exclude\"")).Decision
	if got != hookio.Reject {
		t.Errorf("Decision = %v, want Reject (undecidable direction fails safe)", got)
	}
}

// TestGitDir_FileTools covers the non-Bash tools. The `matched` column carries the
// same weight it does in TestGitDir_Bash: a read now Abstains, so only the reason
// separates "matched an access this rule has no verdict on" from "recognised
// nothing". For the read tools the deferral is the point — `path-safety` owns the
// zone question for a Read/Grep/Glob and is the rule that must get to answer it.
func TestGitDir_FileTools(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		tool    string
		path    string
		want    hookio.Decision
		matched bool
	}{
		{"read .git config defers", "Read", "/repo/.git/config", hookio.Abstain, true},
		{"grep .git defers", "Grep", "/repo/.git/objects", hookio.Abstain, true},
		{"glob .git defers", "Glob", "/repo/.git", hookio.Abstain, true},
		{"write .git ref rejects", "Write", ".git/refs/heads/main", hookio.Reject, true},
		{"edit .git config rejects", "Edit", "/repo/.git/config", hookio.Reject, true},
		{"delete .git ref rejects", "Delete", "/repo/.git/refs/heads/main", hookio.Reject, true},
		{"edit non-git", "Edit", "/repo/src/main.go", hookio.Abstain, false},
		{"read gitignore", "Read", "/repo/.gitignore", hookio.Abstain, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ti json.RawMessage
			if tt.tool == "Grep" || tt.tool == "Glob" {
				ti = searchJSON(tt.path)
			} else {
				ti = fileJSON(tt.path)
			}
			input := &hookio.HookInput{ToolName: tt.tool, ToolInput: ti}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v", got.Decision, tt.want)
			}
			if matched := got.Reason != ""; matched != tt.matched {
				t.Errorf("matched = %v (reason %q), want matched = %v", matched, got.Reason, tt.matched)
			}
		})
	}
}

func TestIsGitMetadataPath(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{".git", true},
		{".git/", true},
		{".git/config", true},
		{"repo/.git/HEAD", true},
		{"foo/.git", true},
		{"/abs/repo/.git/objects", true},
		{"\"$r/.git/info/exclude\"", true},
		{"**/.git/**", true},
		{"", false},
		{"git", false},
		{".gitignore", false},
		{".gitmodules", false},
		{".git.bak", false},
		{"agit/x", false},
		{"my.git", false},
		{"the .git/index is 0 bytes", false},
		{"\"infra-block: repo .git/index is 0 bytes\"", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := isGitMetadataPath(tt.in); got != tt.want {
				t.Errorf("isGitMetadataPath(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestHasInPlaceFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"bare -i", []string{"-i", "s/a/b/"}, true},
		{"-i with suffix", []string{"-i.bak", "s/a/b/"}, true},
		{"bundled -ri", []string{"-ri", "s/a/b/"}, true},
		{"long form", []string{"--in-place", "s/a/b/"}, true},
		{"no in-place", []string{"-n", "1p"}, false},
		{"-E only", []string{"-E", "s/a/b/"}, false},
		{"script after -e is not a flag scan", []string{"-e", "s/i/x/"}, false},
		{"bare dash", []string{"-"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasInPlaceFlag(tt.args); got != tt.want {
				t.Errorf("hasInPlaceFlag(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}
