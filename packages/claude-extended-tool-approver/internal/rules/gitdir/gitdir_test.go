package gitdir

import (
	"encoding/json"
	"errors"
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

// matchedBash reports whether the rule RECOGNISED a `.git` access in command, read
// straight off the detector.
//
// Until ADR 0043 the tests below inferred this from `Reason != ""`: a matched
// non-decisive read returned Abstain WITH a reason and a non-match returned Abstain
// with none, so the reason doubled as a recognition flag. ADR 0043's decision 2 makes
// BOTH cases `RuleResult{}, ErrNotApplicable` — deliberately, so a rule cannot smuggle
// a verdict past the engine on a not-applicable return — which retires that proxy.
// Asking the detector directly is stricter than the proxy was: it pins the recognition
// itself instead of a side effect of it, and it cannot be satisfied by a rule that
// merely happens to set a reason.
func matchedBash(command string) bool {
	_, matched := bashAccess(command, command)
	return matched
}

// TestGitDir_Bash is the rule-scope decision table.
//
// `matched` exists because the read verdict is non-decisive (tc-403c), which makes
// a MATCHED plain read and a NON-match textually identical in their Decision. They
// are not the same thing: a matched read means "this rule looked, saw an access it
// has no verdict on, and handed the leaf to the rest of the chain", while a
// non-match means the rule never recognised an access at all — and it is the
// non-match that the pg2-3hk7t false-positive classes are about. Without this
// column a regression that stopped recognising `.git/` paths entirely would pass
// every read row here.
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
		{"cat .git config", "cat .git/config", hookio.NoOpinion, true},
		{"nested .git", "cat repo/.git/HEAD", hookio.NoOpinion, true},
		{"trailing /.git", "ls foo/.git", hookio.NoOpinion, true},
		{"stat a ref", "stat .git/refs/heads/main", hookio.NoOpinion, true},
		{"readlink a hook", "readlink .git/hooks/pre-commit", hookio.NoOpinion, true},
		{"test -e a hook", "[ -e .git/hooks/pre-commit ]", hookio.NoOpinion, true},
		// The fixture carries a COMPLETE `if … fi`. It used to be truncated after the
		// condition, which the outgoing byte-scanning front end happily split into
		// keyword pseudo-leaves; a real grammar rejects a loop or conditional with no
		// terminator, so the truncated form is an I1b parse failure and exercises the
		// engine's floor rather than this rule's path inference.
		{"if test -e a hook", "if [ -e .git/hooks/pre-commit ]; then :; fi", hookio.NoOpinion, true},
		{"wc a ref", "wc -l .git/info/exclude", hookio.NoOpinion, true},
		{"head a ref", "head -5 .git/HEAD", hookio.NoOpinion, true},
		{"diff two refs", "diff .git/HEAD /tmp/head", hookio.NoOpinion, true},
		{"sed WITHOUT -i streams to stdout", "sed -n '1p' .git/config", hookio.NoOpinion, true},
		{"grep a hook file", "grep prek .git/hooks/pre-commit", hookio.NoOpinion, true},
		{"stdin FROM .git is a read", "wc -l < .git/config", hookio.NoOpinion, true},
		// A redirection that captures NOTHING leaves a read a read — the shape the
		// corpus actually carries (rows 474/475, 3200/3204: `… 2>/dev/null`).
		{"stderr discard does not capture the file", "ls -la .git/hooks 2>/dev/null", hookio.NoOpinion, true},
		{"stdout to /dev/null captures nothing", "cat .git/config > /dev/null", hookio.NoOpinion, true},

		// Non-accesses: nothing is matched at all.
		{"bare git command not blocked", "git status", hookio.NoOpinion, false},
		{"git config not blocked", "git config user.name", hookio.NoOpinion, false},
		{"gitignore not blocked", "cat .gitignore", hookio.NoOpinion, false},
		{"git-suffixed name not blocked", "cat .git.bak", hookio.NoOpinion, false},
		{"unrelated", "ls -la", hookio.NoOpinion, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(bashInput(tt.command)))
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v", got.Decision, tt.want)
			}
			if matched := matchedBash(tt.command); matched != tt.matched {
				t.Errorf("matched = %v, want matched = %v", matched, tt.matched)
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
//
// pg2-pcm1m NARROWS the positive verdict, but not the mechanism this test pins:
// every fixture above still names `.git/config` (or the whole `.git` directory,
// which carries `.git/config`'s bytes along on a recursive copy) precisely so it
// stays credential-bearing and Asks. The negative-for-a-different-reason case —
// a captured listing of a NON-credential `.git/*` path, which now Abstains
// instead of Asking — is its own fixture below, alongside the other "not a
// copy-out" cases, since it is no longer a copy-out this rule has a verdict on.
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
		// A path BOUND to a variable and copied out through it is the same access.
		{"bound path is cp'd out", "f=/repo/.git/config\ncp \"$f\" /tmp/backup", hookio.Ask},

		// NOT a copy-out: nothing is captured.
		{"stderr discard", "cat .git/config 2>/dev/null", hookio.NoOpinion},
		{"stderr discard on a listing", "ls -la .git/hooks 2>/dev/null", hookio.NoOpinion},
		{"stdout to /dev/null", "cat .git/config > /dev/null", hookio.NoOpinion},
		{"stdout to the tty", "cat .git/config > /dev/tty", hookio.NoOpinion},
		{"stdout to an inherited fd", "cat .git/config > /dev/fd/3", hookio.NoOpinion},
		{"stdin FROM gitmeta is not a capture", "wc -l < .git/config", hookio.NoOpinion},
		// pg2-pcm1m: a captured listing of a NON-credential-bearing .git/* path
		// (.git/hooks cannot itself hold a remote-URL token) now Abstains, same
		// as a plain read of it — this used to Ask under the old blanket policy.
		{"capture of a listing of a non-credential path no longer asks", "ls -la .git/hooks > /tmp/list", hookio.NoOpinion},

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
				if d := hookio.Verdict(r.Evaluate(in)).Decision; d > got {
					got = d
				}
			}
			if got != tt.want {
				t.Errorf("Decision = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGitDir_CredentialBearingCopyOutDiscrimination is pg2-pcm1m's own pinning
// test: the SAME blanket Ask that used to fire for every `.git/*` copy-out now
// discriminates by whether the matched path can itself carry a credential
// (isCredentialBearingGitPath). pg2-kpf8f's analysis named `.git/index` and
// `.git/rebase-merge/message` verbatim as rows that prompted with no security
// payoff; both are pinned here as the acceptance criterion's named examples,
// alongside the credential-bearing paths that must still Ask.
func TestGitDir_CredentialBearingCopyOutDiscrimination(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		// Still Ask: the matched path can itself hold a credential.
		{"cp .git/config still asks", "cp .git/config /tmp/backup", hookio.Ask},
		{"cat .git/config redirected still asks", "cat .git/config > /tmp/backup", hookio.Ask},
		{"cp of the whole .git dir still asks (recursive copy carries config)", "cp -r .git /tmp/backup", hookio.Ask},
		{"a submodule's own config still asks", "cp .git/modules/vendor/config /tmp/backup", hookio.Ask},
		{"a bound path to a submodule config still asks", "f=.git/modules/vendor/config\ncp \"$f\" /tmp/backup", hookio.Ask},

		// Now Allow/NotApplicable: these cannot structurally carry a credential.
		{"cp .git/index no longer asks", "cp .git/index /tmp/backup", hookio.NoOpinion},
		{"cp .git/rebase-merge/message no longer asks", "cp .git/rebase-merge/message /tmp/backup", hookio.NoOpinion},
		{"cat .git/index redirected no longer asks", "cat .git/index > /tmp/backup", hookio.NoOpinion},
		{"ln -s .git/HEAD no longer asks", "ln -s .git/HEAD /tmp/link", hookio.NoOpinion},
		{"pipe of .git/rebase-merge/message to tee no longer asks", "cat .git/rebase-merge/message | tee /tmp/backup", hookio.NoOpinion},
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
				if d := hookio.Verdict(r.Evaluate(in)).Decision; d > got {
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
		// pg2-1wt3b widened the SHARED cmdparse.MutatingFlags["yq"] map (not a
		// gitdir-local list) to also carry -s/--split-exp/--split-exp-file, so
		// this rule inherits the fix automatically: yq's -s/--split-exp write ONE
		// NEW FILE PER RESULT, named from the expression, and MEASURABLY used to
		// fall through to readOrCapture here (misclassified as a read of
		// .git/config, since only -i was in the map) rather than dirWrite.
		{"yq -s writes one file per result", "yq -s '.a' .git/config", hookio.Reject},
		{"yq --split-exp long form", "yq --split-exp '.a' .git/config", hookio.Reject},
		{"yq --split-exp-file", "yq --split-exp-file e.txt .git/config", hookio.Reject},
		{"find WITHOUT a mutating flag still reads", "find .git/objects -type f", hookio.NoOpinion},
		{"sort WITHOUT -o still reads", "sort .git/config", hookio.NoOpinion},

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
				if d := hookio.Verdict(r.Evaluate(in)).Decision; d > got {
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
// the ASYMMETRY: on `.git/config`, a read defers, a copy-out prompts, a write is a
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
//
// pg2-pcm1m: the demonstration path MUST be credential-bearing, or the copy-out
// leg no longer Asks and the three-way asymmetry this test exists to pin
// collapses to two — `.git/config` is the canonical case (see
// isCredentialBearingGitPath). `.git/hooks` — this test's path before pg2-pcm1m
// — is exactly the shape the fix now Abstains on; TestGitDir_CopyOutIsNotAPlainRead
// pins that case on its own.
func TestGitDir_ReadWriteAsymmetry(t *testing.T) {
	r := New()
	const path = ".git/config"

	read := hookio.Verdict(r.Evaluate(bashInput("ls -la " + path)))
	copyOut := hookio.Verdict(r.Evaluate(bashInput("cp -r " + path + " /tmp/backup")))
	write := hookio.Verdict(r.Evaluate(bashInput("rm -rf " + path)))

	// The asymmetry itself: same path, three distinct verdicts in increasing
	// restrictiveness. This is the assertion a collapse of the direction split must
	// break, whichever pair it collapses. The property is NAMED rather than inlined
	// under a `!` so it reads in the positive form the doc comment states it in
	// (staticcheck QF1001 rejects the inline negated conjunction).
	strictlyIncreasing := read.Decision < copyOut.Decision && copyOut.Decision < write.Decision
	if !strictlyIncreasing {
		t.Fatalf("directions on %s are not strictly increasing: read %v, copy-out %v, write %v",
			path, read.Decision, copyOut.Decision, write.Decision)
	}

	// …and the exact verdicts, so "increasing" cannot drift to some other triple.
	//
	// Abstain, not Approve: a plain read is NON-DECISIVE, so `secrets` and the
	// zone checks below this rule still run (tc-403c). An Approve
	// here would end the chain for the leaf and is exactly the defect.
	if read.Decision != hookio.NoOpinion {
		t.Errorf("read Decision = %v, want Abstain", read.Decision)
	}
	// The read row's Reason/Module used to be asserted here, as the only thing
	// separating a MATCHED read from the rule recognising no access at all. ADR
	// 0043's decision 2 makes a not-applicable return `RuleResult{}` — no Reason, no
	// Module — so that separation now comes from the detector, asserted directly.
	// Strictly stronger: it also pins the DIRECTION, which the reason string only
	// described in prose.
	if dir, matched := bashAccess("ls -la "+path, "ls -la "+path); !matched || dir != dirRead {
		t.Errorf("bashAccess(ls -la %s) = (%v, %v), want (dirRead, true) — the rule must RECOGNISE the read, "+
			"not merely fail to gate it", path, dir, matched)
	}
	// And the not-applicable channel itself, so a future silent switch to a terminal
	// NoOpinion verdict (which would shadow secrets/the zone checks
	// for a plain `.git` read) fails here.
	if _, err := r.Evaluate(bashInput("ls -la " + path)); !errors.Is(err, hookio.ErrNotApplicable) {
		t.Errorf("read err = %v, want ErrNotApplicable — the chain MUST continue past a plain .git read", err)
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
	if want := "refusing to write git metadata under .git/ directly — modify it through git commands only " +
		"(permitted only when the effective git directory resolves under a temporary root — see " +
		"docs/adr/0059-ceta-temp-repo-carve-out.md in phillipgreenii-nix-agent-support)"; write.Reason != want {
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
		{"row 126856 shape: heredoc payload in an assignment", "PAYLOAD=$(cat <<'EOF'\n{\n  \"title\": \"infra-block: acme .git/index is 0 bytes\"\n}\nEOF\n)"},
		{"commit message mentioning a path", "git commit -m 'stop reading .git/config directly'"},
		{"echoed prose mentioning a path", "echo 'see .git/config for the remote'"},
		{"bead title in a double-quoted arg", "bd create \"repo .git/index is 0 bytes\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hookio.Verdict(r.Evaluate(bashInput(tt.command))).Decision; got != hookio.NoOpinion {
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
			if got := hookio.Verdict(r.Evaluate(bashInput(tt.command))).Decision; got != hookio.NoOpinion {
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
			if got := hookio.Verdict(r.Evaluate(bashInput(tt.command))).Decision; got != hookio.NoOpinion {
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
			want: hookio.NoOpinion,
		},
		{
			name: "row 163591 shape: bound hooks path read inside a substitution → read (Abstain)",
			expr: "h=\"$r/.git/hooks\"\necho \"active -> $(grep -m1 prek \"$h/pre-commit\")\"",
			want: hookio.NoOpinion,
		},
		{
			// The `if` is CLOSED with `fi`, which the corpus row it models of course was.
			// The truncated fixture was only viable while the front end split keyword
			// pseudo-leaves out of invalid bash; a real grammar makes it a parse failure,
			// so leaving it truncated would test the I1b floor instead of this rule.
			name: "row 184010 shape: bound hook path tested and readlink'd → read (Abstain)",
			expr: "h=\"$r/.git/hooks/pre-commit\"\nif [ -e \"$h\" ]\nthen echo \"present ($(readlink \"$h\"))\"\nfi",
			want: hookio.NoOpinion,
		},
		{
			name: "row 305013 shape: default-expansion binding read by ls/head → read (Abstain)",
			expr: "HP=\"${HP:-$CC/.git/hooks}\"\nls -la \"$HP/pre-push\"\nhead -5 \"$HP/pre-push\"",
			want: hookio.NoOpinion,
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
			// "Did this leaf match?" is read off the DETECTOR rather than off a
			// Reason string: ADR 0043's decision 2 empties the RuleResult on a
			// not-applicable return, so a matched-but-non-decisive read and a
			// non-match are identical in the verdict channel. bashAccess is the
			// thing this test is actually about anyway — the DIRECTION it assigns.
			got := hookio.Decision(hookio.Approve)
			spoke := false
			for _, leaf := range leavesOf(tt.expr) {
				in := &hookio.HookInput{
					ToolName:       "Bash",
					ToolInput:      bashJSON(leaf),
					RootExpression: tt.expr,
				}
				if _, matched := bashAccess(leaf, tt.expr); !matched {
					continue
				}
				res := hookio.Verdict(r.Evaluate(in))
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
	got := hookio.Verdict(r.Evaluate(bashInput("f=\"$r/.git/info/exclude\""))).Decision
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
		{"read .git config defers", "Read", "/repo/.git/config", hookio.NoOpinion, true},
		{"grep .git defers", "Grep", "/repo/.git/objects", hookio.NoOpinion, true},
		{"glob .git defers", "Glob", "/repo/.git", hookio.NoOpinion, true},
		{"write .git ref rejects", "Write", ".git/refs/heads/main", hookio.Reject, true},
		{"edit .git config rejects", "Edit", "/repo/.git/config", hookio.Reject, true},
		{"delete .git ref rejects", "Delete", "/repo/.git/refs/heads/main", hookio.Reject, true},
		{"edit non-git", "Edit", "/repo/src/main.go", hookio.NoOpinion, false},
		{"read gitignore", "Read", "/repo/.gitignore", hookio.NoOpinion, false},
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
			got := hookio.Verdict(r.Evaluate(input))
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v", got.Decision, tt.want)
			}
			if matched := isGitMetadataPath(tt.path); matched != tt.matched {
				t.Errorf("matched = %v, want matched = %v", matched, tt.matched)
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

// TestIsCredentialBearingGitPath pins pg2-pcm1m's discriminator directly, at
// detector scope, separately from the rule-level Decision assertions above.
func TestIsCredentialBearingGitPath(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		// Credential-bearing: the top-level config, bare or nested under a repo root.
		{".git/config", true},
		{"repo/.git/config", true},
		{"/abs/repo/.git/config", true},
		{"\"$r/.git/config\"", true},
		// Credential-bearing: the .git directory as a whole (a recursive copy
		// carries .git/config's bytes along with everything else).
		{".git", true},
		{".git/", true},
		{"repo/.git", true},
		{"\"$r/.git\"", true},
		// Credential-bearing: a submodule's own config, one level deep.
		{".git/modules/vendor/config", true},
		{"repo/.git/modules/vendor/config", true},

		// NOT credential-bearing: every other .git/* path.
		{".git/index", false},
		{".git/HEAD", false},
		{".git/hooks", false},
		{".git/hooks/pre-commit", false},
		{".git/rebase-merge/message", false},
		{".git/refs/heads/main", false},
		{".git/objects", false},
		{".git/info/exclude", false},
		// A submodule-of-a-submodule config is deliberately out of scope.
		{".git/modules/a/modules/b/config", false},
		// A bare "config" with no .git ancestor at all is not this rule's business.
		{"config", false},
		{"/tmp/config", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := isCredentialBearingGitPath(tt.in); got != tt.want {
				t.Errorf("isCredentialBearingGitPath(%q) = %v, want %v", tt.in, got, tt.want)
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

// TestGitDir_PipeToWritingSinkIsACopyOut pins tc-vul7: the THIRD spelling of a
// copy-out, a PIPE into a stage that writes what it receives.
//
//	cat .git/config | tee /tmp/backup
//
// copies exactly what `cp .git/config /tmp/backup` and `cat .git/config >
// /tmp/backup` copy — tc-403c already Asks on both — yet it auto-approved, because
// cmdparse consumed `|` exactly like `;`/`&&` and recorded no relation between the
// stages. Standing at the `cat` leaf the rule could not tell `| tee /tmp/x` from
// `| grep url`. The relation is now recorded (cmdparse PipelineID/PipelineIndex)
// and read through cmdparse.DownstreamStages.
//
// The FILTER half is asserted just as hard as the writer half, and is the reason
// this is an allowlist rather than a denylist of sinks: `| grep`, `| head`, `| jq`
// are how `.git/config` is read in practice, so a fix that prompted on them would
// re-create the friction that softened the read side twice already (tc-k2m3).
func TestGitDir_PipeToWritingSinkIsACopyOut(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		// The bead's named shape and its relatives: the sink PERSISTS the payload.
		{"pipe to tee", "cat .git/config | tee /tmp/backup", hookio.Ask},
		{"pipe to tee -a", "cat .git/config | tee -a /tmp/backup", hookio.Ask},
		{"pipe to dd of=", "cat .git/config | dd of=/tmp/x", hookio.Ask},
		{"pipe to sponge", "cat .git/config | sponge /tmp/x", hookio.Ask},
		{"pipe to a stage that redirects", "cat .git/config | cat > /tmp/x", hookio.Ask},
		{"a filter that redirects is still a capture", "cat .git/config | grep url > /tmp/x", hookio.Ask},
		{"the writer can be further down the pipeline", "cat .git/config | grep url | tee /tmp/x", hookio.Ask},
		{"an UNKNOWN sink fails closed", "cat .git/config | frobnicate", hookio.Ask},
		{"a sink that runs an arbitrary command fails closed", "cat .git/config | xargs -I{} echo {}", hookio.Ask},
		{"a shell as the sink fails closed", "cat .git/config | sh", hookio.Ask},
		{"a filter with a writing flag", "cat .git/config | sort -o /tmp/x", hookio.Ask},

		// pg2-pcm1m: a listing of a NON-credential-bearing .git/* path piped to a
		// writing sink now Abstains — .git/hooks cannot itself hold a remote-URL
		// token, so this is the pipe spelling of the same narrowing
		// TestGitDir_CopyOutIsNotAPlainRead pins for the plain-redirection
		// spelling. The SINK classification this test exists to pin (tee IS a
		// writer) is unaffected; only the credential question changed the verdict.
		{"a listing of a non-credential path piped to a writer no longer asks", "ls -la .git/hooks | tee /tmp/list", hookio.NoOpinion},

		// The FILTER half: a stage that consumes without persisting is not a copy-out.
		{"pipe to grep", "cat .git/config | grep url", hookio.NoOpinion},
		{"pipe to head", "cat .git/config | head -5", hookio.NoOpinion},
		{"pipe to wc", "cat .git/config | wc -l", hookio.NoOpinion},
		{"pipe to jq", "cat .git/config | jq .", hookio.NoOpinion},
		{"pipe to sed without -i", "cat .git/config | sed 's/a/b/'", hookio.NoOpinion},
		{"pipe to awk", "cat .git/config | awk '{print $1}'", hookio.NoOpinion},
		{"pipe to sort without -o", "cat .git/config | sort", hookio.NoOpinion},
		{"a chain of filters", "cat .git/config | grep url | head -1 | cut -d= -f2", hookio.NoOpinion},
		{"a filter discarding into /dev/null", "cat .git/config | grep url > /dev/null", hookio.NoOpinion},
		{"the git read is DOWNSTREAM, not upstream", "cat /tmp/x | grep -c url .git/config", hookio.NoOpinion},

		// `|` must not be confused with the separators that carry no data.
		{"&& is not a pipe", "cat .git/config && tee /tmp/x", hookio.NoOpinion},
		{"; is not a pipe", "cat .git/config ; tee /tmp/x", hookio.NoOpinion},
		{"|| is not a pipe", "cat .git/config || tee /tmp/x", hookio.NoOpinion},
		{"a tee in a LATER pipeline is not this one's sink", "cat .git/config | grep url && echo hi | tee /tmp/x", hookio.NoOpinion},
		{"no pipe at all", "cat .git/config", hookio.NoOpinion},

		// The write side is untouched by any of this.
		{"a write piped to a filter still Rejects", "sed -i 's/a/b/' .git/config | grep x", hookio.Reject},
		{"a write piped to a writer still Rejects", "rm -rf .git/objects | tee /tmp/x", hookio.Reject},
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
				if d := hookio.Verdict(r.Evaluate(in)).Decision; d > got {
					got = d
				}
			}
			if got != tt.want {
				t.Errorf("Decision = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGitDir_PipeSinkWithoutRootExpression pins the DIRECT-call path (tc-vul7):
// with no RootExpression the rule falls back to the leaf text as its own scope, so
// a whole pipeline handed to Evaluate in one piece is still classified. This is the
// shape `ceta check` and any non-engine caller produce, and without the fallback
// they would silently keep the old auto-approve.
func TestGitDir_PipeSinkWithoutRootExpression(t *testing.T) {
	r := New()
	tests := []struct {
		command string
		want    hookio.Decision
	}{
		{"cat .git/config | tee /tmp/backup", hookio.Ask},
		{"cat .git/config | grep url", hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			in := &hookio.HookInput{ToolName: "Bash", ToolInput: bashJSON(tt.command)}
			if got := hookio.Verdict(r.Evaluate(in)).Decision; got != tt.want {
				t.Errorf("Decision = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGitDir_SkipGrepPatternGluedQuoteParity pins pg2-52eod's fix for pathOperands'
// use of cmdparse.SkipGrepPattern — a FOURTH caller of cmdparse.GluedFlagValue this
// bead's audit found (pg2-6f2gu's own decision record named only three: this
// package's grep/rg/sed/awk file-flag operand extraction was not among them). A
// glued file-flag value naming a `.git` path must be recognised the same way
// whether quoted or not, and malformed quoting this rule cannot resolve must fail
// SAFE to its own documented default — dirWrite — rather than silently going
// unrecognised.
func TestGitDir_SkipGrepPatternGluedQuoteParity(t *testing.T) {
	r := New()

	tests := []struct {
		name    string
		command string
		want    hookio.Decision
		matched bool
	}{
		{"grep --file, unquoted glued, names .git", "grep --file=.git/config x.log", hookio.NoOpinion, true},
		{"grep --file, quoted glued — must match the unquoted spelling", "grep --file='.git/config' x.log", hookio.NoOpinion, true},
		{
			"grep --file, malformed glued quoting fails SAFE to dirWrite (Reject)",
			"grep --file=''.git/config'' x.log", hookio.Reject, true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(bashInput(tt.command)))
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v (reason %q)", got.Decision, tt.want, got.Reason)
			}
			if matched := matchedBash(tt.command); matched != tt.matched {
				t.Errorf("matched = %v, want matched = %v", matched, tt.matched)
			}
		})
	}
}

// TestGitDir_GluedFlagValueSpuriousEqualsBoundary is pg2-su2eh's regression guard:
// pg2-52eod's centralization of cmdparse.GluedFlagValue made it detect and report
// "malformed" (shell quoting it cannot resolve) for ANY glued-looking token whose
// FIRST "=" — found by a quote-blind strings.Cut — happens to land inside quoted
// content. `awk` is a patternFirstCmds member, so its args route through
// cmdparse.SkipGrepPattern, which folds that malformed signal into pathOperands'
// own return; this rule's bashAccess treats an unresolvable glued value as an
// unclassifiable operand and fails safe to dirWrite (Reject) — its own documented
// default, "anything not positively known to be read-only is a write" — EVEN
// THOUGH THE COMMAND NEVER MENTIONS `.git` AT ALL. Measured on main @07b9600b (a
// 360,523-row corpus replay): `awk -F"=" ...` pipelines with no `.git` reference
// wrongly Rejected with "refusing to write git metadata under .git/ directly".
//
// `-F"="` is an ordinary awk field-separator flag — a short option glued to a
// double-quoted value that happens to contain a literal "=", with no `=`-flag
// convention of its own. Confirmed to Reject (matched=true) before this bead's
// fix to cmdparse.GluedFlagValue and to correctly Abstain (matched=false) after
// it, by temporarily reverting internal/cmdparse/argflags.go and re-running this
// exact fixture.
func TestGitDir_GluedFlagValueSpuriousEqualsBoundary(t *testing.T) {
	r := New()

	tests := []struct {
		name    string
		command string
	}{
		{"awk -F with a double-quoted field separator, no .git reference", `awk -F"=" '{print $1}' file.txt`},
		{"awk -F with a double-quoted field separator and other args", `awk -F"=" '{print $2}' /tmp/data.csv`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(bashInput(tt.command)))
			if got.Decision != hookio.NoOpinion {
				t.Errorf("Decision = %v (reason %q), want Abstain — a spurious \"=\" inside a quoted, non-.git flag value must not fail safe to a write", got.Decision, got.Reason)
			}
			if matched := matchedBash(tt.command); matched {
				t.Errorf("matched = %v, want matched = false — this command never references .git at all", matched)
			}
		})
	}
}

// TestGitDir_ExcludeFromNamesAFileNotAPattern pins pg2-33mai's ADR 0055 mode-1
// fix: `-X`/`--exclude-from` (tar) and `--ignore-file` (fd, ripgrep) name a FILE
// the command OPENS to read patterns from, unlike `--exclude`/`--exclude-dir`/`-I`,
// whose value is an inline glob PATTERN the command never opens. Before this fix
// all of these lived in the same excludeValueFlags table and the file-opening
// three were skipped exactly like the pattern ones — so `tar --exclude-from
// .git/info/exclude …` never reached isGitMetadataPath at all (NoOpinion,
// unmatched) even though tar is not on readCmds/copyLikeCmds/moveCmds and would
// have failed safe to dirWrite (Reject) had the candidate been surfaced.
func TestGitDir_ExcludeFromNamesAFileNotAPattern(t *testing.T) {
	r := New()

	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"tar --exclude-from reads a file, tar is not a read-allowlisted command", "tar --exclude-from .git/info/exclude -czf /tmp/out.tar.gz .", hookio.Reject},
		{"tar -X (short form) reads a file", "tar -X .git/info/exclude -czf /tmp/out.tar.gz .", hookio.Reject},
		{"fd --ignore-file reads a file", "fd --ignore-file .git/info/exclude --type f .", hookio.NoOpinion},
		// Contrast: --exclude/--exclude-dir/-I still name an inline PATTERN, not a
		// file, and must stay exempt.
		{"tar --exclude is still a pattern, not a path", "tar --exclude .git -czf /tmp/out.tar.gz /repo", hookio.NoOpinion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(bashInput(tt.command)))
			if got.Decision != tt.want {
				t.Errorf("Decision = %v (reason %q), want %v", got.Decision, got.Reason, tt.want)
			}
		})
	}
}

// TestGitDir_CopyLikeTrailingValueFlagIsNotTheDestination pins pg2-33mai's ADR
// 0055 mode-4 fix in lastOperand: install/cp/ln's own value-taking flags
// (-g/-m/-o/-S) can be GNU-permuted to appear AFTER the source/destination
// operands, which is legal getopt ordering. Before this fix, lastOperand's pure
// backward scan had no notion of these flags and read the trailing VALUE
// ("root") as if it were the destination, so `install /tmp/evil
// .git/hooks/pre-commit -o root` measured a mere copy-out (Ask) instead of the
// Reject a write into a git hook must get.
func TestGitDir_CopyLikeTrailingValueFlagIsNotTheDestination(t *testing.T) {
	r := New()

	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"install writes a hook, -o trails the real destination", "install /tmp/evil .git/hooks/pre-commit -o root", hookio.Reject},
		{"install writes a hook, -m trails the real destination", "install /tmp/evil .git/hooks/pre-commit -m 755", hookio.Reject},
		{"cp writes gitconfig, -S trails the real destination", "cp /tmp/evil .git/config -S .bak", hookio.Reject},
		{"ln writes a hook, -S trails the real destination", "ln -sf /tmp/evil .git/hooks/pre-commit -S .bak", hookio.Reject},
		// Contrast: the SOURCE is gitmeta and the trailing flag value is not it —
		// still a copy-out (Ask), unchanged from before this fix.
		{"install FROM gitmeta, -o trails a harmless owner value", "install .git/config /tmp/out -o root", hookio.Ask},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(bashInput(tt.command)))
			if got.Decision != tt.want {
				t.Errorf("Decision = %v (reason %q), want %v", got.Decision, got.Reason, tt.want)
			}
		})
	}
}
