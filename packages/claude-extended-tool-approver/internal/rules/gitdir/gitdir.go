// Package gitdir guards direct access to a repository's `.git` directory — the
// object store, refs, config and hooks — so git metadata is modified through the
// git porcelain rather than by an agent poking at the files (a hook-support
// parity capability; GitDirectoryEvaluator).
//
// Decision policy, by DIRECTION of the access (pg2-3hk7t, tc-k2m3, tc-403c,
// tc-vul7):
//
//   - a WRITE (`sed -i`, `>`, `rm`, `mv`/`cp` onto it, `tee`, `chmod`, …) is
//     Rejected. This is the hard security block, matching hook-support's DENY
//     with confidence 1.0 and consistent with ceta's other hard-block rules
//     (`assume` Rejects assume-role; `config-rules` Rejects blocked basenames).
//   - a COPY-OUT — a read whose DESTINATION is a write (`cp .git/config /tmp/x`,
//     `cat .git/config > /tmp/x`, `ln -s .git/config /tmp/link`,
//     `cat .git/config | tee /tmp/x`) — Asks.
//   - a plain READ (`ls`, `cat`, `grep`, `readlink`, `[ -e ]`, `head`, `wc`,
//     `stat`, `diff`) ABSTAINS: no verdict, the rest of the chain decides.
//   - an access whose direction cannot be determined is treated as a WRITE.
//
// ONLY THE READ SIDE HAS EVER MOVED, and it has now moved three times: a blanket
// Reject, then a decisive Ask (pg2-3hk7t), then a decisive Approve (tc-k2m3), and
// now Abstain (tc-403c). The write-side Reject is the load-bearing protection and
// is unchanged at every step. The first softening was because a non-overridable
// deny on a read-only inspection is the failure mode most likely to get the whole
// guard disabled — the rule reproduced against the orchestrator mid-triage and
// blocked read-only calls during the very run that found it. The second was the
// same argument carried to its end: of the 113 historical `deny` rows in the
// decision DB not one still replays as `deny`, and every one of the 14 rows that
// remained at `ask` was APPROVED by the operator — 100%, no denial — so the
// prompt was pure friction. Reads of `.git/config`, `.git/hooks/*` and
// `.git/worktrees/*/rebase-merge/*` are ordinary diagnostic traffic that no git
// porcelain command exposes; `.git/config` in particular is the standard
// diagnostic for the author-identity and `core.hooksPath` problems that recur
// throughout that corpus. The third is described next.
//
// # WHY A PLAIN READ ABSTAINS RATHER THAN APPROVING (tc-403c)
//
// engine.Evaluate is FIRST-MATCH-WINS and this rule sits at position 2 of
// setup.RuleChain — after the consumer `config-rules`, but BEFORE `secrets`,
// `path-safety` and `safe-commands`. A DECISIVE verdict of any kind ends the
// chain for that leaf. That was harmless while the read verdict was Ask, because
// Ask outranks anything those later rules could have contributed anyway. It was
// NOT harmless once the verdict became Approve, the least restrictive verdict
// there is: this rule then answered `allow` for every `.git/` read in ceta's
// name, and whole rules below it never ran.
//
//   - PATH TRAVERSAL. `cat ../../../../etc/passwd/../.git/config` reached a
//     decisive Ask from the `path-traversal` rule while this rule said Ask, and
//     auto-approved while it said Approve. That rule was DELETED by pg2-bn7sx
//     (operator ruling pg2-4yy4r item 6) because its coverage was an artifact of
//     a literal `../..` substring test rather than a policy — of five spellings
//     of this same read only the `../..` one was gated. The shape now reaches
//     NoOpinion via the zone check, pinned by TestIntegration_GitDirDirectionAndRole's
//     "traversal into gitmeta still declines to approve". So the traversal
//     SPELLING is no longer special, and this bullet is retained only to record
//     why the read verdict must stay non-decisive.
//
//   - OUT-OF-PROJECT READS. `cat /elsewhere/.git/config` lands outside the
//     project root, where the zone check yields no read permission and the
//     verdict should defer to Claude Code; it auto-approved instead.
//
//     CAVEAT, measured 2026-08-17 and NOT fixed here: this holds only where the
//     zone model actually withholds read permission. A `.git/config` inside a
//     READABLE zone — a sibling repo under the workspace root, or `/tmp/x/.git/config`
//     — still reaches allow/safe-commands, because secretpath classifies NO
//     `.git/` path as a secret (pinned by TestGitDir_SecretsDoesNotCoverGitPaths).
//     Six of eight measured spellings auto-approve. Tracked as pg2-dswtg; do not
//     read this bullet as asserting that out-of-project `.git` reads all defer.
//
//   - CREDENTIALS IN `.git/config`. A remote URL can carry an embedded token
//     (`https://x-access-token:ghp_…@github.com/…`), and `secrets` is the rule
//     that would prompt before handing a credential to a reader. Reaching
//     `secrets` is necessary but NOT sufficient here and the ordering was never
//     the only problem: secretpath.IsSecret is false for EVERY `.git/` path, so
//     that coverage is absent outright rather than merely masked (pinned by
//     TestGitDir_SecretsDoesNotCoverGitPaths). Widening secretpath is therefore a
//     separate, still-open question — but it could not have helped at all while
//     this rule short-circuited the chain.
//
// Abstain restores all three: the traversal Asks again, the out-of-project read
// defers, and every later rule is consulted. It is NOT a walk-back of tc-k2m3's
// operator decision, because that decision's own evidence still lands on `allow`
// END-TO-END — `safe-commands` approves an ordinary `cat`/`ls`/`stat` of a
// readable path and treats `readlink` as always-safe, which is why the four cited
// shapes are pinned end-to-end in engine_integration_test.go rather than at this
// rule's own scope. What Abstain gives up is only the ABILITY TO OVERRIDE a later
// rule's objection, which is exactly the property that was doing the damage.
//
// # WHY A COPY-OUT ASKS (tc-403c)
//
// Abstain alone does not close the sharp shape. `cp .git/config /tmp/backup` is
// classified a READ — cp genuinely does not modify its source, a distinction this
// rule keeps deliberately (see copyLikeCmds vs moveCmds) — and every later rule
// approves it: `safe-commands` sees a readable source and a writable destination
// and says yes. So the credential-bearing file still reached an arbitrary
// destination with no prompt, by the one command shape tc-k2m3's 14 rows never
// contained.
//
// THREE SPELLINGS, ONE ACCESS. `cp .git/config /tmp/backup`,
// `cat .git/config > /tmp/backup` and `cat .git/config | tee /tmp/backup` copy the
// same bytes to the same place, so they MUST reach the same verdict or the
// treatment is decoration. tc-403c closed the first two; the third stayed open
// only because cmdparse discarded the pipe relation at the split, leaving no way
// to tell `| tee /tmp/x` from `| grep url` at leaf scope. tc-vul7 records that
// relation in the parser rather than re-deriving it here — see pipeScope, and the
// argument for putting it there in cmdparse.ParsedCommand's PipelineID doc.
//
// SINKS ARE AN ALLOWLIST, not a denylist (cmdparse.PipeFilterCmds — relocated
// there by tc-yk2z when the ssh rule needed the same classification): a stage that is not
// positively known to consume-without-persisting is treated as a writer. That is
// tc-080p's settled direction and tc-403c's undeterminable-access rule, and its
// measured cost here is ZERO — replaying all 16,756 non-excluded corpus rows
// before and after moves exactly ONE decision class, and no row at all: of 111
// leaves that name a `.git` path with a downstream stage, every sink observed is
// `head`, `grep`, `sort`, `tail`, `wc`, `jq`, `cut`, `paste`, `xargs` or a `while
// read`, and the 11 rows carrying a non-filter sink are all `find … -not -path
// '*/.git/*'` exclusions the role model already declines to match.
//
// The FAILURE DIRECTION is the design: a copy-out fails toward PROMPTING. Ask is
// decisive, so it does short-circuit the chain — harmlessly, because Ask outranks
// everything below it except Reject, so nothing downstream could have made the
// verdict more restrictive. It is not Reject: a copy-out modifies no git metadata,
// backing `.git/config` up before editing it is legitimate, and a non-overridable
// deny on a non-destructive operation is the exact failure that softened the read
// side twice already. And it is not Abstain: deferring would hand the decision to
// a layer that has no idea the source is git metadata.
//
// SYNTACTIC ROLE, not bare text. A git-metadata path token is a violation only
// when it is a path the command actually OPERATES ON. The rule therefore parses
// the command and inspects operands by role, following
// safecmds.argsHaveDynamicExpansion's precedent of judging an argument's role
// rather than its text. It deliberately does NOT match:
//
//   - PROSE that merely mentions a `.git/…` path — a heredoc body, a commit
//     message, a bead title, a notification payload. A path operand contains no
//     whitespace, so prose can never satisfy isGitMetadataPath.
//   - an EXCLUSION: a `!`-prefixed glob (`rg -g '!**/.git/**'`), the PATTERN
//     operand of the grep/sed/awk family (`grep -v "/.git/"` filters it OUT), or
//     a `find … -path X -prune` operand. Such a command provably does not touch
//     git metadata.
//   - an argument to `git` ITSELF (notably `git config -f …/.git/config`). git is
//     the sanctioned porcelain this rule exists to funnel access through, so
//     rejecting a git command contradicted the rule's own reason string; the
//     dedicated `git` rule judges those.
package gitdir

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// maxScopeDepth bounds the substitution recursion of scopeLeaves. Deeply nested
// substitutions are not a real shape, and the bound makes a self-similar body
// (whose inner text re-parses to the same leaf) terminate.
const maxScopeDepth = 8

// direction is the access direction of a matched git-metadata path. The zero
// value is dirRead and each successive value is strictly more restrictive, so
// `worse` folds by numeric order exactly as hookio.MostRestrictive does for
// decisions.
//
// dirCopyOut sits BETWEEN the two because a copy-out is neither: it modifies no
// git metadata (so the write-side Reject would be wrong and would hard-block a
// legitimate backup), yet it is not a plain inspection either — it lands the
// bytes somewhere the guard no longer covers.
type direction int

const (
	dirRead direction = iota
	dirCopyOut
	dirWrite
)

func worse(a, b direction) direction {
	if b > a {
		return b
	}
	return a
}

type Rule struct{}

func New() *Rule { return &Rule{} }

func (r *Rule) Name() string { return "git-directory" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	switch input.ToolName {
	case "Bash":
		// ADR 0039 step 3: leaves is the SAME structure
		// `cmdparse.Parse(cmdStr)` used to derive from a fresh
		// `input.BashCommand()` read, obtained here through cmdparse.LeavesOf so
		// the engine's already-parsed leaf (threaded onto input.ParsedLeaf) is
		// reused instead of re-parsed. This is also now the ONE place that reads
		// the Bash command at all: the old unconditional `input.BashCommand()`
		// call further down is gone, because a synthetic per-leaf input no
		// longer carries a ToolInput JSON string to read it FROM (mustBashJSON
		// is deleted) — BashCommand() is called lazily, below, only on the path
		// that genuinely still needs the raw text.
		leaves, err := cmdparse.LeavesOf(input)
		if err != nil {
			// THE SPLIT ADR 0043's Consequences require. This used to `break` into
			// the shared final return below, so ONE Abstain literal carried two
			// unrelated meanings — "no .git path in this call" and "I could not read
			// the Bash command" — and the second was indistinguishable from the
			// first. It is now a genuine failure, recorded per rule; the chain
			// outcome (continue) is unchanged.
			return hookio.RuleResult{}, fmt.Errorf("git-directory: read bash command: %w", err)
		}
		// The engine hands this rule ONE leaf of a compound at a time, but a leaf
		// that merely BINDS a path (`f="$r/.git/info/exclude"`) is identical whether
		// the sibling consuming `"$f"` reads or writes it. RootExpression is the
		// expression the leaf came from and is the only place that fact exists; it
		// is empty for a direct (non-engine) call, where the leaf IS the whole
		// command — the engine ALWAYS sets it (to the real top-level command text)
		// for every synthetic per-leaf input, so this branch is unreached in
		// production and exists only for a hand-built test/direct call.
		scope := input.RootExpression
		var rootLeaves []cmdparse.ParsedCommand
		if scope == "" {
			cmd, err := input.BashCommand()
			if err != nil {
				return hookio.RuleResult{}, fmt.Errorf("git-directory: read bash command: %w", err)
			}
			scope = cmd
			// The leaf IS the whole command here, so its own already-parsed
			// leaves already ARE the scope's leaf set — reusing them avoids a
			// second parse of the identical text.
			rootLeaves = leaves
		} else {
			rootLeaves = cmdparse.RootLeavesOf(input)
		}
		if dir, matched := bashAccessLeaves(leaves, scope, rootLeaves); matched {
			return r.verdict(dir)
		}
	case "Read":
		path, err := input.FilePath()
		if err != nil {
			return hookio.RuleResult{}, fmt.Errorf("git-directory: read file_path: %w", err)
		}
		if isGitMetadataPath(path) {
			return r.verdict(dirRead)
		}
	case "Write", "Edit", "MultiEdit", "Delete":
		path, err := input.FilePath()
		if err != nil {
			return hookio.RuleResult{}, fmt.Errorf("git-directory: read file_path: %w", err)
		}
		if isGitMetadataPath(path) {
			return r.verdict(dirWrite)
		}
	case "Glob", "Grep":
		path, err := input.SearchPath()
		if err != nil {
			return hookio.RuleResult{}, fmt.Errorf("git-directory: read search path: %w", err)
		}
		if isGitMetadataPath(path) {
			return r.verdict(dirRead)
		}
	}
	// No .git path anywhere in this call: not this rule's business, and the generic
	// approvers registered after it MUST still run.
	return hookio.NotApplicable()
}

// verdict returns the (RuleResult, error) pair because its dirRead branch is a
// NOT-APPLICABLE, not a verdict: its own former Reason said so in words — "no
// verdict; later rules decide". A terminal NoOpinion there would stop the chain and
// prevent path-safety/safe-commands from deciding a plain `.git` READ, which is a
// decision change this bead forbids.
func (r *Rule) verdict(d direction) (hookio.RuleResult, error) {
	switch d {
	case dirWrite:
		return hookio.RuleResult{
			Decision: hookio.Reject,
			Reason:   "refusing to write git metadata under .git/ directly — modify it through git commands only",
			Module:   r.Name(),
		}, nil
	case dirCopyOut:
		return hookio.RuleResult{
			Decision: hookio.Ask,
			Reason:   "copying git metadata out of .git/ to another location — .git/config can carry a credential in a remote URL",
			Module:   r.Name(),
		}, nil
	}
	return hookio.NotApplicable()
}

// bashAccess reports whether leafText operates on a path inside a `.git`
// directory, and in which direction. scopeText is the whole expression leafText
// was split out of, used to resolve the direction of a path bound to a variable
// and to resolve where a stage's output is PIPED (see pipeScope).
//
// It is a TEXT-taking wrapper over bashAccessLeaves for a direct caller (a unit
// test, or any future non-engine caller) that has not threaded already-parsed
// structure: it parses leafText fresh and passes no root leaves, which makes
// pipeScope fall back to lazily parsing scopeText on first use — exactly this
// function's behaviour before ADR 0039 step 3.
func bashAccess(leafText, scopeText string) (direction, bool) {
	return bashAccessLeaves(cmdparse.Parse(leafText), scopeText, nil)
}

// bashAccessLeaves is bashAccess's core, over an ALREADY-PARSED leaf set —
// ADR 0039 step 3's entry point for Evaluate, which has already parsed both
// the leaf (via cmdparse.LeavesOf) and, when available, the root expression's
// leaves (rootLeaves, via cmdparse.RootLeavesOf) once each. rootLeaves is
// forwarded to newPipeScope so pipeScope.sinkDirection need not re-parse
// scopeText either; nil is a legitimate value (a direct caller with no
// pre-parsed root) and simply falls back to pipeScope's own lazy parse.
func bashAccessLeaves(leaves []cmdparse.ParsedCommand, scopeText string, rootLeaves []cmdparse.ParsedCommand) (direction, bool) {
	dir, matched := dirRead, false
	note := func(d direction) {
		dir = worse(dir, d)
		matched = true
	}
	pipes := newPipeScope(scopeText, rootLeaves)
	for _, pc := range leaves {
		// `git` is the sanctioned porcelain this rule funnels access through, so its
		// own ARGUMENTS are never a violation and the dedicated `git` rule judges
		// them. Scoped to the operands only: a redirection or an assignment on the
		// same leaf (`git status > .git/foo`) is NOT a git-mediated access and is
		// still checked below.
		gitPorcelain := false
		if base, _ := cmdparse.EffectiveExec(pc); base == "git" {
			gitPorcelain = true
		}
		if !gitPorcelain {
			tokens, opMalformed := pathOperands(pc)
			if opMalformed {
				// pg2-52eod: a patternFirstCmds glued flag value's shell quoting could
				// not be resolved (cmdparse.SkipGrepPattern), so this rule cannot tell
				// whether it names a path inside .git at all. Fail safe with this
				// file's own documented default for an unclassifiable operand:
				// "Anything not positively known to be read-only is a write."
				note(dirWrite)
			}
			for _, tok := range tokens {
				if isGitMetadataPath(tok) {
					note(commandDirection(pc, func(s string) bool { return s == tok }, pipes))
				}
			}
		}
		for _, rd := range pc.Redirections {
			if !isGitMetadataPath(rd.Path) {
				continue
			}
			if rd.Kind.IsWrite() {
				note(dirWrite)
			} else {
				note(dirRead)
			}
		}
		// An assignment BINDS a path; it accesses nothing itself. Its direction is
		// whatever the expression later does with the variable.
		for _, ev := range pc.EnvVars {
			if isGitMetadataPath(ev.Value) {
				note(bindingDirection(ev.Name, scopeText, pipes))
			}
		}
	}
	return dir, matched
}

// bindingDirection resolves the direction of a git-metadata path bound to the
// variable name by folding the direction of every command in the expression that
// CONSUMES it. A binding nothing consumes here is undecidable, so it fails safe
// to a write — that is what keeps a real `f=…/.git/info/exclude` + `sed -i "$f"`
// rejected while `RM=…/rebase-merge` + `ls "$RM"` folds to a plain read (and so to
// no verdict from this rule at all).
//
// RESIDUAL ASYMMETRY, deliberate: `git` is not in readCmds, so a path consumed
// only through git (`S=…/.git/config; git config --file "$S" --get x`) folds to a
// write and Rejects, whereas the same access spelled as a DIRECT git operand
// (`git config -f …/.git/config --get x`) is not a match at all. Both spellings
// Rejected before this change, so nothing regresses, and the conservative side is
// the right one to err on here: corpus row 237336 binds `.git/config` and then
// `git config --unset-all`s it, which is a genuine write no read/write table for
// bare `git` would catch without modelling every subcommand — the `git` rule's job.
func bindingDirection(name, scope string, pipes *pipeScope) direction {
	dir, used := dirRead, false
	for _, pc := range scopeLeaves(scope, 0) {
		if !leafReferencesVar(pc, name) {
			continue
		}
		used = true
		dir = worse(dir, commandDirection(pc, func(s string) bool { return referencesVar(s, name) }, pipes))
		if dir == dirWrite {
			break
		}
	}
	if !used {
		return dirWrite
	}
	return dir
}

// scopeLeaves flattens an expression into every command it can run: the top-level
// leaves plus, recursively, the leaves of each command/process substitution body.
// The recursion matters because a variable is most often consumed INSIDE a
// substitution (`echo "msgnum: $(cat "$RM/msgnum")"`), and cmdparse.Parse stops at
// the substitution boundary — the substitution's text stays glued into the outer
// leaf's token.
func scopeLeaves(expr string, depth int) []cmdparse.ParsedCommand {
	if depth > maxScopeDepth {
		return nil
	}
	var out []cmdparse.ParsedCommand
	for _, pc := range cmdparse.Parse(expr) {
		out = append(out, pc)
		for _, sub := range cmdparse.EnumerateSubstitutions(pc.Raw) {
			out = append(out, scopeLeaves(sub.Body, depth+1)...)
		}
	}
	return out
}

// leafReferencesVar reports whether the leaf's OWN operands reference $name.
func leafReferencesVar(pc cmdparse.ParsedCommand, name string) bool {
	if referencesVar(pc.Executable, name) {
		return true
	}
	for _, a := range pc.Args {
		if referencesVar(a, name) {
			return true
		}
	}
	for _, rd := range pc.Redirections {
		if referencesVar(rd.Path, name) {
			return true
		}
	}
	return false
}

// referencesVar reports whether s contains a $NAME / ${NAME…} reference OUTSIDE
// any nested substitution. Text inside a substitution belongs to the INNER
// command — which scopeLeaves surfaces as its own leaf — so counting it here
// would attribute the inner command's direction to the outer one: the `echo` of
// `echo "$(cat "$RM/x")"` does not itself touch $RM, the `cat` does.
func referencesVar(s, name string) bool {
	return containsVarRef(stripSubstitutions(s), name)
}

// stripSubstitutions removes every command/process substitution BODY from s,
// reusing the shared quote-aware enumerator rather than adding another scanner.
// EnumerateSubstitutions returns only TOP-LEVEL bodies, so removing those removes
// any nesting with them.
func stripSubstitutions(s string) string {
	for _, sub := range cmdparse.EnumerateSubstitutions(s) {
		if sub.Body != "" {
			s = strings.Replace(s, sub.Body, "", 1)
		}
	}
	return s
}

func containsVarRef(s, name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i+1 < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		rest := s[i+1:]
		rest = strings.TrimPrefix(rest, "{")
		if !strings.HasPrefix(rest, name) {
			continue
		}
		// A reference ends at the name: $RM matches `$RM` and `${RM:-x}` but not
		// `$RMDIR`.
		if tail := rest[len(name):]; tail == "" || !isNameByte(tail[0]) {
			return true
		}
	}
	return false
}

func isNameByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// isGitMetadataPath reports whether s, taken as a WHOLE path operand, addresses
// something inside a `.git` directory.
//
// Two properties do the work. The match is ANCHORED on a `/`-separated COMPONENT
// equal to `.git`, so `.gitignore`, `.git.bak` and the bare `git` executable are
// not matches. And a path operand contains NO WHITESPACE, which is what keeps
// prose out: `"infra-block: … .git/index is 0 bytes …"` is one token, not a path,
// where the former substring match (`strings.Contains(s, " .git/")` over the raw
// command) rejected it.
func isGitMetadataPath(s string) bool {
	s = unquoteOperand(s)
	if s == "" || strings.ContainsAny(s, " \t\n\r") {
		return false
	}
	for _, comp := range strings.Split(s, "/") {
		if comp == ".git" {
			return true
		}
	}
	return false
}

// unquoteOperand strips ONE pair of wrapping quotes. cmdparse already unquotes
// arg tokens, but it keeps an env-assignment VALUE verbatim, so
// `f="$r/.git/info/exclude"` arrives with its quotes attached.
func unquoteOperand(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// shellKeywords / effectiveExec / hasAnyFlag / mutatingFlags / capturesStdout and
// the sink allowlist all used to be defined HERE. They moved to cmdparse
// (cmdparse/pipesink.go, tc-yk2z) when the ssh rule needed the same sink
// classification and a rule may not import another rule's package. Only the
// MECHANISM moved: the DIRECTION POLICY below — readCmds, copyLikeCmds, moveCmds,
// destFlagCmds, and what each direction MEANS — is still this rule's own.

// readCmds never modify their path operands.
var readCmds = map[string]bool{
	"ls": true, "cat": true, "bat": true, "head": true, "tail": true, "wc": true,
	"stat": true, "file": true, "diff": true, "cmp": true, "readlink": true,
	"realpath": true, "basename": true, "dirname": true, "du": true,
	"find": true, "fd": true, "tree": true,
	"test": true, "[": true, "[[": true, "echo": true, "printf": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true, "ack": true,
	"awk": true, "gawk": true, "nawk": true, "cut": true, "tr": true, "sort": true,
	"uniq": true, "od": true, "xxd": true, "hexdump": true, "strings": true,
	"less": true, "more": true, "jq": true, "yq": true, "column": true,
	"md5sum": true, "shasum": true, "sha1sum": true, "sha256sum": true, "cksum": true,
}

// copyLikeCmds take a DESTINATION as their last operand and only READ their
// source, so their direction depends on WHICH operand the git-metadata path is:
// naming it as the destination writes it (Reject), naming it as a SOURCE copies
// it out (Ask — see commandDirection's copyLikeCmds arm and dirCopyOut).
//
// `mv` is deliberately NOT a member — see moveCmds. Do not add it back.
var copyLikeCmds = map[string]bool{
	"cp": true, "ln": true, "install": true,
}

// moveCmds are destructive on EVERY operand they name, source included: a rename
// REMOVES the path at the source, so `mv .git/HEAD /tmp/x` destroys git metadata
// just as surely as `mv /tmp/x .git/HEAD` overwrites it.
//
// This is the one place the copyLikeCmds geometry does not hold, and getting it
// wrong is expensive: grouping `mv` with cp/ln/install classified a rename of the
// source as a READ and downgraded a destructive operation from a hard block to a
// user-overridable prompt. `rename` (util-linux / perl) is the same shape — it
// mutates the files it is given in place. Both fall through to the default write
// verdict; the set exists so the intent is explicit at the decision point rather
// than an accident of allowlist membership.
var moveCmds = map[string]bool{
	"mv": true, "rename": true,
}

// destFlagCmds carry flags that INVERT copyLikeCmds' operand geometry, so the
// last operand is no longer the destination and reading direction off it would
// pick the wrong end:
//
//   - `-t` / `--target-directory`: the destination comes from the FLAG, so every
//     positional is a source (`install -t .git/hooks evil` writes into .git);
//   - `-d` / `--directory`: install CREATES each operand as a directory, so they
//     are all destinations wherever they sit.
//
// Any match under one of these is treated as a write. That over-blocks the
// read-only `cp -t /tmp .git/config`, which is the correct side to err on.
var destFlagCmds = map[string]bool{
	"-t": true, "--target-directory": true, "-d": true, "--directory": true,
}

// ddPathPrefixes are the `key=value` operand prefixes dd uses for its input and
// output files. dd is the one common command that names paths this way, and the
// whole token (`of=.git/config`) has no `.git` path COMPONENT, so a component walk
// over it silently misses the write entirely. Both directions are surfaced and dd
// is not read-allowlisted, so either resolves to a write.
var ddPathPrefixes = []string{"if=", "of="}

// patternFirstCmds take a PATTERN or SCRIPT as their first bare operand, not a
// path, and carry value-consuming flags whose values are equally not paths.
// `grep -v "/.git/"` filters that text OUT of its input and touches no git
// metadata at all. Operand extraction for these defers to the shared
// cmdparse.SkipGrepPattern, which already models both.
var patternFirstCmds = map[string]bool{
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true, "ack": true,
	"sed": true, "gsed": true, "awk": true, "gawk": true, "nawk": true,
}

// findPathPredicates are the find predicates whose operand is a PATTERN matched
// against the walk, not a path find descends into. find never opens these, so they
// are excluded unconditionally — `-path ./.git -prune` and `! -path './.git/*'`
// are both exclusions, and requiring `-prune` would miss the negated spelling.
var findPathPredicates = map[string]bool{
	"-path": true, "-wholename": true, "-ipath": true, "-iwholename": true,
	"-name": true, "-iname": true, "-regex": true, "-iregex": true,
}

// excludeValueFlags name flags whose FOLLOWING token is an ignore/exclude PATTERN
// rather than a path the command opens: `tree -I '.git'`, `fd -E .git`,
// `tar --exclude .git`. Two corpus rows (101602, 124447) were `tree -L 3 -I '.git'`
// — a read-only listing that EXCLUDES git metadata, which an earlier cut of this
// rule turned into a brand-new hard deny by reading `-I`'s value as an operand.
//
// Naming the flags individually is deliberate. The tempting general rule — "the
// token after any flag is not an operand" — would swallow the operand of
// `rm -rf .git`, so it is not available. These entries are consulted only for
// commands OUTSIDE patternFirstCmds, whose own value-flag vocabulary
// cmdparse.SkipGrepPattern already owns (grep's `-E` is boolean, rg's is not).
//
// pg2-33mai (ADR 0055 mode 1): `-X`/`--exclude-from` (tar) and `--ignore-file`
// (fd, ripgrep) are DELIBERATELY ABSENT even though they share the "exclude"
// family name. Unlike `--exclude`/`--exclude-dir`, whose value is a glob
// PATTERN the command never opens, these three name a FILE the command reads
// its patterns FROM — the value IS a path the command opens, exactly the
// shape ADR 0055's mode 1 warns against ("a flag whose value the command
// opens never belongs in a skip table"). Listing them here used to silently
// drop `tar --exclude-from .git/info/exclude …` from screening entirely: tar
// is not in readCmds/copyLikeCmds/moveCmds, so a correctly surfaced candidate
// fails safe to dirWrite (Reject), but the skipped value never reached
// isGitMetadataPath at all, so the leaf fell through to NotApplicable
// instead. Leaving them out of this table lets their value fall through to
// the ordinary candidate-operand path below (the switch's default
// `out = append(out, a)`) like any other operand this rule has no specific
// reason to exempt.
var excludeValueFlags = map[string]bool{
	"-I": true, "--ignore": true, "-E": true,
	"--exclude": true, "--exclude-dir": true,
}

// commandDirection classifies how pc accesses the operand(s) selected by target.
// Anything not positively known to be read-only is a write (fail safe).
func commandDirection(pc cmdparse.ParsedCommand, target func(string) bool, pipes *pipeScope) direction {
	// A redirection ONTO the target is a write whatever the command is: the writer
	// is the shell, not the executable, so `echo x > "$f"` must not inherit echo's
	// read-only classification.
	for _, rd := range pc.Redirections {
		if rd.Kind.IsWrite() && target(rd.Path) {
			return dirWrite
		}
	}
	base, args := cmdparse.EffectiveExec(pc)
	switch {
	case moveCmds[base]:
		// Destructive on the source as well as the destination — checked BEFORE
		// copyLikeCmds so no operand of a rename is ever read as a mere source.
		return dirWrite
	case base == "sed" || base == "gsed":
		// Only `-i` makes sed a writer; without it sed streams to stdout.
		if hasInPlaceFlag(args) {
			return dirWrite
		}
		return readOrCapture(pc, pipes)
	case copyLikeCmds[base]:
		// A destination-bearing flag inverts the geometry, so the last operand is
		// no longer the destination and must not be read as one.
		if cmdparse.HasAnyFlag(args, destFlagCmds) {
			return dirWrite
		}
		if dest, ok := lastOperand(base, args); ok && target(dest) {
			return dirWrite
		}
		// The git-metadata path is a SOURCE. These commands do not modify it — that
		// is why they are not moveCmds — but every one of them exists to make its
		// source reachable at a destination they WRITE: cp/install duplicate the
		// bytes, `ln` publishes a second name that resolves to them. Either way the
		// content leaves the directory this rule guards, so it is a copy-out and not
		// a plain read.
		return dirCopyOut
	case readCmds[base]:
		if cmdparse.HasAnyFlag(args, cmdparse.MutatingFlags[base]) {
			return dirWrite
		}
		return readOrCapture(pc, pipes)
	}
	return dirWrite
}

// readOrCapture classifies a command already established to READ its git-metadata
// operand: a plain read, unless its OUTPUT is captured into a file, which makes it
// a copy-out by a different spelling.
//
// `cat .git/config > /tmp/backup` copies exactly what `cp .git/config /tmp/backup`
// copies, so the two must reach the same verdict or the cp treatment is decoration.
// Only the STDOUT-bearing kinds count: `2>/dev/null` discards diagnostics and
// captures none of the file, and `ls -la .git/hooks 2>/dev/null` is a routine
// inspection that must not be promoted. A target that captures nothing —
// /dev/null, the tty, an inherited fd — is likewise not a capture
// (hookio.IsSafeRedirectTarget).
//
// A PIPE to a writing sink (`cat .git/config | tee /tmp/backup`) is the same
// exfiltration by a third spelling and is classified here too (tc-vul7), via
// pipeScope. It was tc-403c's KNOWN RESIDUE for a reason that has since been
// removed rather than worked around: cmdparse discarded the pipe relation at the
// split, so no rule could tell a writing sink from a `| grep url` filter. The
// relation is now recorded (cmdparse.ParsedCommand.PipelineID / PipelineIndex) and
// this rule reads it through cmdparse.DownstreamStages.
func readOrCapture(pc cmdparse.ParsedCommand, pipes *pipeScope) direction {
	if cmdparse.CapturesStdout(pc) {
		return dirCopyOut
	}
	return pipes.sinkDirection(pc.Raw)
}

// pipeScope answers, for a leaf of the expression it was built from, where that
// leaf's STDOUT goes. It exists because the pipe relation lives at EXPRESSION
// scope — cmdparse.Parse of the leaf alone shows a single stage with no
// downstream — and because building it once per Evaluate keeps the scope parsed a
// bounded number of times.
//
// It is deliberately built from cmdparse.Parse(scope) and NOT from scopeLeaves:
// pipeline numbering is per-Parse-call, so folding a substitution body's leaves
// into the same slice would let its pipeline 0 collide with the outer one and
// relate stages that never shared a pipe. A substitution body reaches this rule as
// its OWN RootExpression (the engine recurses it through EvaluateExpression), so
// nothing is lost by leaving it out.
//
// The scope parse is LAZY and cached, UNLESS the caller already has it: this
// rule sits at position 2 of the chain, so it runs on EVERY Bash leaf, and the
// overwhelming majority name no git metadata at all — parsing the whole
// expression eagerly for an answer almost nobody asks for would make the cost
// quadratic in a compound's leaf count. ADR 0039 step 3 removes even that cost
// for the common case: Evaluate has ALREADY parsed the root expression once
// (through cmdparse.RootLeavesOf, which reads the engine's threaded
// ParsedRoot), so newPipeScope accepts those leaves directly and this type
// merely CACHES what its caller supplies rather than deferring a parse call
// that would otherwise never happen.
type pipeScope struct {
	scope  string
	parsed bool
	leaves []cmdparse.ParsedCommand
}

// newPipeScope builds a pipeScope for scope, optionally pre-seeded with
// scope's already-parsed leaves. A non-nil leaves marks the scope as already
// parsed, so sinkDirection never re-parses scope itself; nil (a direct caller
// with no pre-parsed root, e.g. bashAccess's text-taking wrapper) preserves
// the original lazy-parse-on-first-use behaviour.
func newPipeScope(scope string, leaves []cmdparse.ParsedCommand) *pipeScope {
	return &pipeScope{scope: scope, leaves: leaves, parsed: leaves != nil}
}

// sinkDirection classifies where the stage whose raw text is leafRaw sends its
// output: dirRead when it goes nowhere it can be kept (no pipe, or only filtering
// stages downstream), dirCopyOut when any downstream stage might WRITE it.
//
// A leafRaw that matches no stage yields dirRead — the same answer as "not in a
// pipeline", which is the pre-tc-vul7 verdict and so never a new prompt.
func (p *pipeScope) sinkDirection(leafRaw string) direction {
	if !p.parsed {
		p.leaves = cmdparse.Parse(p.scope)
		p.parsed = true
	}
	if cmdparse.PipedToWriter(p.leaves, leafRaw) {
		return dirCopyOut
	}
	return dirRead
}

// hasInPlaceFlag reports whether args carry sed's in-place flag in any spelling:
// `-i`, `-i.bak`, bundled `-ri`, or `--in-place`. The bundled scan stops at `e`
// or `f`, which consume the rest of the token as a script/filename value.
func hasInPlaceFlag(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			if strings.HasPrefix(a, "--in-place") {
				return true
			}
			continue
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			continue
		}
		for i := 1; i < len(a); i++ {
			if a[i] == 'i' {
				return true
			}
			if a[i] == 'e' || a[i] == 'f' {
				break
			}
		}
	}
	return false
}

// copyLikeValueFlags name, per copyLikeCmds member, the flags whose space-separated
// spelling consumes the NEXT token as a value — install's -g/--group, -m/--mode,
// -o/--owner; all three commands' -S/--suffix. `-t`/`--target-directory` and
// `-d`/`--directory` are deliberately absent: those are destFlagCmds, checked by
// presence alone BEFORE lastOperand ever runs (commandDirection's HasAnyFlag guard),
// not by skipping their value here.
//
// pg2-33mai (ADR 0055 mode 4): without this table, lastOperand's backward scan
// cannot tell a value-taking flag's VALUE from a genuine trailing operand, so a
// GNU-permuted invocation with the flag AFTER the source/dest — a legal getopt
// ordering, not a contrived one — makes the flag's value look like "the last
// non-flag token" and steals the destination role from the real one:
// `install /tmp/evil .git/hooks/pre-commit -o root` measured dirCopyOut (Ask) before
// this fix, because lastOperand returned "root" (the -o value) instead of
// ".git/hooks/pre-commit" (the actual write target) — the write into a git hook
// downgraded from Reject to an overridable Ask. Glued spellings (`-oroot`,
// `--owner=root`) are already single tokens and need no entry here.
var copyLikeValueFlags = map[string]map[string]bool{
	"install": {
		"-g": true, "--group": true,
		"-m": true, "--mode": true,
		"-o": true, "--owner": true,
		"-S": true, "--suffix": true,
	},
	"cp": {"-S": true, "--suffix": true},
	"ln": {"-S": true, "--suffix": true},
}

// lastOperand returns the final non-flag arg — the destination for copyLikeCmds —
// for base's own argv. Unlike a pure backward scan, it walks FORWARD so a
// copyLikeValueFlags entry's value can be skipped along with its flag: a flag and
// its value are one unit regardless of which end of args they land on.
func lastOperand(base string, args []string) (string, bool) {
	valueFlags := copyLikeValueFlags[base]
	last, found := "", false
	i := 0
	for i < len(args) {
		a := args[i]
		if valueFlags[a] {
			i += 2
			continue
		}
		if strings.HasPrefix(a, "-") {
			i++
			continue
		}
		last, found = a, true
		i++
	}
	return last, found
}

// pathOperands returns the args of pc that are candidate PATHS — the operands the
// command actually acts on — with flags and every EXCLUSION role removed — and
// whether cmdparse.SkipGrepPattern found a glued flag value whose shell quoting it
// could not resolve (pg2-52eod: SkipGrepPattern is a FOURTH caller of
// cmdparse.GluedFlagValue this bead's audit found, by way of gitdir being a fourth
// consumer of SkipGrepPattern itself — not one of the three pg2-6f2gu's own decision
// record enumerated). malformed is always false outside patternFirstCmds, since only
// that branch routes through SkipGrepPattern.
func pathOperands(pc cmdparse.ParsedCommand) (tokens []string, malformed bool) {
	base, args := cmdparse.EffectiveExec(pc)
	out := make([]string, 0, len(args)+1)
	if pc.Executable != "" {
		out = append(out, pc.Executable)
	}
	if patternFirstCmds[base] {
		// Reuse the shared grep/rg operand extractor (cmdparse.SkipGrepPattern,
		// relocated to cmdparse precisely so sibling rules could share it): it drops
		// the positional PATTERN and every value-consuming flag's value, which
		// covers `grep -v "/.git/"`, `grep --exclude-dir .git` and `rg -g '!…'` in
		// one place. The rg vocabulary is honored only for rg, since -r/-E/-T are
		// boolean in grep.
		vocab := "grep"
		if base == "rg" {
			vocab = "rg"
		}
		files, isMalformed := cmdparse.SkipGrepPattern(vocab, args)
		return append(out, files...), isMalformed
	}
	isFind := base == "find"
	isDD := base == "dd"
	for i, a := range args {
		if isDD {
			// dd names its files as `if=PATH` / `of=PATH`; the whole token carries no
			// `.git` path component, so the VALUE half must be surfaced on its own.
			for _, p := range ddPathPrefixes {
				if v, ok := strings.CutPrefix(a, p); ok {
					out = append(out, v)
				}
			}
		}
		switch {
		case strings.HasPrefix(a, "-"):
			continue // a flag, not an operand
		case strings.HasPrefix(a, "!"):
			// A NEGATED glob (`fd -E '!x'`, and find's own `!` token) names what must
			// NOT be touched.
			continue
		case i > 0 && excludeValueFlags[args[i-1]]:
			continue // an ignore/exclude PATTERN, e.g. `tree -I '.git'`
		case isFind && i > 0 && findPathPredicates[args[i-1]]:
			continue // a walk PATTERN, e.g. `find . -path ./.git -prune`
		}
		out = append(out, a)
	}
	return out, false
}
