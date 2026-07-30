package git

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

var readOnlySubcommands = map[string]bool{
	// Porcelain inspection
	"log": true, "diff": true, "status": true, "show": true, "blame": true,
	"describe": true, "shortlog": true, "reflog": true, "grep": true,
	"show-branch": true, "whatchanged": true, "range-diff": true,
	// Plumbing: ref/object inspection
	"for-each-ref": true, "ls-files": true, "ls-remote": true, "ls-tree": true,
	"merge-base": true, "rev-list": true, "rev-parse": true, "show-ref": true,
	"name-rev": true, "cat-file": true, "count-objects": true,
	// Plumbing: diff variants
	"diff-tree": true, "diff-index": true, "diff-files": true,
	// Plumbing: verification/integrity
	"verify-commit": true, "verify-tag": true, "verify-pack": true, "fsck": true,
	// Plumbing: gitignore/gitattributes checks
	"check-ignore": true, "check-attr": true, "check-mailmap": true, "check-ref-format": true,
}

var remoteBlockedSubcommands = map[string]bool{
	"add": true, "remove": true, "rm": true, "rename": true,
	"set-url": true, "set-head": true, "set-branches": true,
}

var modifyingSubcommands = map[string]bool{
	"add": true, "commit": true, "branch": true, "fetch": true,
	"push": true, "stash": true, "config": true, "mu": true,
	"mv": true, "rm": true, "cherry-pick": true, "merge": true,
	"worktree": true,
}

type Rule struct {
	// eval gates a `-C <path>` chdir against path-safety zones. When nil (legacy
	// construction) the `-C` path check is skipped and behavior is unchanged.
	eval *patheval.PathEvaluator
}

func New(eval *patheval.PathEvaluator) *Rule {
	return &Rule{eval: eval}
}

func (r *Rule) Name() string {
	return "git"
}

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	if input.ToolName != "Bash" {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	parsed := cmdparse.Parse(cmdStr)
	for _, pc := range parsed {
		if !isGitExecutable(pc.Executable) {
			continue
		}
		// A pre-subcommand -c / --config-env injects arbitrary git config (e.g.
		// -c core.pager="touch /tmp/pwned") that runs on an otherwise read-only
		// subcommand — a known RCE class. Defer to Claude's prompt. Scoped to the
		// pre-subcommand span so `git commit -c <commit>` (a different flag) and
		// `git -C <path>` are NOT falsely abstained.
		if hasGitConfigInjection(pc.Args) {
			return hookio.RuleResult{Decision: hookio.Abstain, Reason: "git: -c/--config-env injects config; deferring to prompt", Module: r.Name()}
		}
		chdirs, subcmd, rest := cmdparse.GitInvocation(pc.Args)
		if subcmd == "" {
			return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
		}
		res := r.classify(pc, subcmd, rest)
		// A `-C <path>` chdir runs the subcommand against a directory other than
		// the invocation CWD. When the rule would otherwise Approve, demote to
		// Abstain if that directory is unsafe for the subcommand's access class:
		// a read-only subcommand needs the dir to be readable, a modifying one
		// needs it writable. Most-restrictive aggregation then defers to the
		// prompt (Abstain, never a hard Reject — the check uses CanRead/CanWrite,
		// not IsDeny*) instead of auto-approving a write into an unknown zone
		// (pg2-b3eow). Gated to a non-empty -C so a bare git command keeps its
		// verdict regardless of the CWD's zone.
		if res.Decision == hookio.Approve && !r.chdirSafe(input.CWD, chdirs, subcmd) {
			return hookio.RuleResult{
				Decision: hookio.Abstain,
				Reason:   "git: -C target directory is unsafe for a " + subcmd + " (deferred to claude-code)",
				Module:   r.Name(),
			}
		}
		return res
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

// classify returns the base verdict for a git subcommand, independent of any
// `-C` path-safety concern (which Evaluate layers on top via chdirSafe).
func (r *Rule) classify(pc cmdparse.ParsedCommand, subcmd string, rest []string) hookio.RuleResult {
	// push: the force / remote-ref-destroying spellings are REJECTED (pg2-bohpm,
	// see pushVerdict for the ruling and the rationale). Every other push falls
	// through to the modifying-subcommand Approve below, so ordinary pushes and
	// same-branch --force-with-lease keep their verdict.
	if subcmd == "push" {
		if res, ok := r.pushVerdict(rest); ok {
			return res
		}
	}
	if isDestructive(subcmd, rest) {
		return hookio.RuleResult{
			Decision: hookio.Ask,
			Reason:   "destructive git command",
			Module:   r.Name(),
		}
	}
	if readOnlySubcommands[subcmd] {
		return hookio.RuleResult{
			Decision: hookio.Approve,
			Reason:   "read-only git command",
			Module:   r.Name(),
		}
	}
	if subcmd == "checkout" {
		if hasRedirectEnvVar(pc) {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}
		}
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "git checkout", Module: r.Name()}
	}
	// rebase: approve unless interactive without automated editor
	if subcmd == "rebase" {
		if hasFlag(rest, "-i") || hasFlag(rest, "--interactive") {
			if !hasSequenceEditorEnvVar(pc) {
				return hookio.RuleResult{Decision: hookio.Abstain, Reason: "git rebase -i requires editor", Module: r.Name()}
			}
		}
		if hasRedirectEnvVar(pc) {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}
		}
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "modifying git command", Module: r.Name()}
	}
	// filter-branch: approve (history rewriting used by agents for commit cleanup)
	if subcmd == "filter-branch" {
		if hasRedirectEnvVar(pc) {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}
		}
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "modifying git command", Module: r.Name()}
	}
	// tag: always reject — tags cause confusion in this workflow
	if subcmd == "tag" {
		return hookio.RuleResult{Decision: hookio.Reject, Reason: "git: git tag is prohibited — tags cause confusion in this workflow", Module: r.Name()}
	}
	// remote: special handling
	if subcmd == "remote" {
		remoteSub := ""
		if len(rest) > 0 {
			remoteSub = rest[0]
		}
		if remoteBlockedSubcommands[remoteSub] {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "git remote modifying command", Module: r.Name()}
		}
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "read-only git remote", Module: r.Name()}
	}
	// modifying: approve (includes tag, mv, rm, worktree, etc.)
	if modifyingSubcommands[subcmd] {
		if hasRedirectEnvVar(pc) {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}
		}
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "modifying git command", Module: r.Name()}
	}
	// reset: approve unless --hard
	if subcmd == "reset" {
		if hasFlag(rest, "--hard") {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "git:destructive: git reset --hard is destructive", Module: r.Name()}
		}
		if hasRedirectEnvVar(pc) {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "git command with redirected context", Module: r.Name()}
		}
		return hookio.RuleResult{Decision: hookio.Approve, Reason: "git:modifying: git reset (soft) is safe", Module: r.Name()}
	}
	if subcmd == "clean" {
		return hookio.RuleResult{Decision: hookio.Ask, Reason: "git:destructive: git clean is destructive", Module: r.Name()}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

// chdirSafe reports whether the `-C` target directory is in a zone appropriate
// for the subcommand's access class. Read-only subcommands — and a read-only
// `git remote` — require the directory be READABLE; every other approvable
// subcommand (checkout, rebase, filter-branch, soft reset, and the modifying
// set) writes and requires it be WRITABLE. Returns true (no gate) when no `-C`
// is present or no evaluator is configured, preserving legacy behavior.
func (r *Rule) chdirSafe(cwd string, chdirs []string, subcmd string) bool {
	if r.eval == nil || len(chdirs) == 0 {
		return true
	}
	access := r.eval.Evaluate(effectiveDir(cwd, chdirs))
	if readOnlySubcommands[subcmd] || subcmd == "remote" {
		return access.CanRead()
	}
	return access.CanWrite()
}

// effectiveDir folds git's `-C` chdir values onto cwd the way git does: an
// absolute chdir resets the running directory, a relative one is joined onto
// it. A leading `~` is expanded to the user's home so an unexpanded tilde
// cannot be mistaken for an in-project directory (a shell would expand it
// before git runs). Mirrors primarycommit.effectiveDir (unexported there); any
// env var in the folded path is expanded by patheval.PathEvaluator.Evaluate.
func effectiveDir(cwd string, chdirs []string) string {
	dir := cwd
	for _, c := range chdirs {
		c = expandTilde(c)
		if filepath.IsAbs(c) {
			dir = c
		} else {
			dir = filepath.Join(dir, c)
		}
	}
	return dir
}

// expandTilde expands a leading `~` or `~/` to the user's home directory,
// mirroring patheval's cleanPath so a `-C ~/...` value is resolved to an
// absolute path before zone classification. Non-tilde paths are returned as-is.
func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func isGitExecutable(exec string) bool {
	return exec == "git" || filepath.Base(exec) == "git"
}

// hasGitConfigInjection reports whether a pre-subcommand -c or --config-env
// flag is present. It scans only the option span before the git subcommand
// (mirroring cmdparse.GitInvocation's flag-consuming walk) so that a -c appearing
// AFTER the subcommand (e.g. `git commit -c <commit>`) is not matched.
func hasGitConfigInjection(args []string) bool {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "-c" {
			return true
		}
		if a == "--config-env" || strings.HasPrefix(a, "--config-env=") {
			return true
		}
		switch a {
		case "-C", "--git-dir", "--work-tree", "--namespace":
			i += 2
			continue
		default:
			if strings.HasPrefix(a, "-") {
				i++
				continue
			}
			return false // first non-flag token is the subcommand
		}
	}
	return false
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func hasRedirectEnvVar(pc cmdparse.ParsedCommand) bool {
	for _, ev := range pc.EnvVars {
		if ev.Name == "GIT_DIR" || ev.Name == "GIT_WORK_TREE" {
			return true
		}
	}
	return false
}

func hasSequenceEditorEnvVar(pc cmdparse.ParsedCommand) bool {
	for _, ev := range pc.EnvVars {
		if ev.Name == "GIT_SEQUENCE_EDITOR" {
			return true
		}
	}
	return false
}

// isDestructive reports whether a subcommand warrants the shared destructive
// Ask. It is `git branch -D` ONLY: pg2-bohpm split the `git push` cases out to
// pushVerdict, which REJECTS them, and `branch -D` MUST keep its present Ask
// because re-classifying it is a separate, still-unreviewed question.
//
// The `branch` case MUST stay in this function to keep that Ask. Removing it
// does not make `git branch -D` unhandled — it makes it fall through to
// modifyingSubcommands["branch"] below and become an APPROVE, which is strictly
// worse than the Ask it replaces. The exact-token `-D` test is likewise
// deliberate: widening it (e.g. to a clustered-short scan) would change this
// subcommand's verdicts, which is out of scope here.
func isDestructive(subcmd string, args []string) bool {
	if subcmd == "branch" {
		for _, a := range args {
			if a == "-D" {
				return true
			}
		}
	}
	return false
}

// Long-flag ABBREVIATION MINIMUMS for the `git push` options pushVerdict gates.
// git's parse-options accepts any UNAMBIGUOUS PREFIX of a long option, so
// `--force-w`, `--del` and `--m` are all real spellings of flags this rule must
// see; cmdparse.HasLongFlag matches one exact name by design and documents that
// a caller needing abbreviations must ask for each spelling.
//
// Each value is the SHORTEST prefix real git accepted, MEASURED with `git push
// <spelling> origin main` against git 2.54.0 on 2026-07-30; one character
// shorter, git answered `error: ambiguous option`. Re-measure before changing
// one. A future git option that makes a listed prefix ambiguous cannot cause a
// false Reject — git refuses the ambiguous spelling itself — it only makes the
// extra spelling dead.
const (
	minAbbrevForce          = len("force")   // `--force-` is ambiguous: --force-with-lease / --force-if-includes
	minAbbrevForceWithLease = len("force-w") // same ambiguity one character shorter
	minAbbrevDelete         = len("de")      // `--d` is ambiguous: --delete / --dry-run
	minAbbrevMirror         = len("m")       // no other `git push` option starts with m
)

// hasPushLongFlag reports whether args carries long flag name in any spelling
// git would accept — the full name, or an unambiguous prefix down to minLen
// characters — and returns the value of the `=`-glued form (see
// cmdparse.HasLongFlag for what an empty value means). It asks
// cmdparse.HasLongFlag once per candidate spelling, LONGEST FIRST, so the glued
// value is read from the longest spelling actually present.
//
// A `--no-<name>` token does not match, which is correct: `--no-force` turns
// force off.
func hasPushLongFlag(args []string, name string, minLen int) (string, bool) {
	for n := len(name); n >= minLen; n-- {
		if v, ok := cmdparse.HasLongFlag(args, name[:n]); ok {
			return v, true
		}
	}
	return "", false
}

// pushShortFlagTokens returns args with every short-flag cluster truncated at
// its first `o` — `git push`'s ONLY short option that takes a value (`-o <opt>`,
// and glued as `-oci.skip`, measured accepted on git 2.54.0). Everything after
// that `o` is the option's VALUE, not more flag letters, so scanning it would let
// a value that happens to contain `f` or `d` (`-oconfidential`) manufacture a
// false force/delete Reject — the same false-positive class pg2-5b901 records,
// arrived at through flag arity instead of command text. cmdparse.HasShortFlag
// documents that it knows no arity and pushes exactly this question to its
// caller.
//
// `--`, long flags and a lone `-` are returned untouched so HasShortFlag's own
// end-of-options and operand handling still applies.
func pushShortFlagTokens(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if len(a) > 1 && a[0] == '-' && a[1] != '-' {
			if o := strings.IndexByte(a, 'o'); o > 0 {
				a = a[:o]
			}
		}
		out[i] = a
	}
	return out
}

// pushVerdict returns the verdict for a `git push` — rest being the args AFTER
// the `push` subcommand — and false when the push is none of the prohibited
// shapes, in which case classify lets it fall through to the ordinary modifying
// Approve.
//
// WHY REJECT AND NOT ASK. Operator ruling, 2026-07-30: an agent must never
// force-push. Until pg2-bohpm the rule matched `--force`/`-f` by exact token
// equality, so every other spelling of the SAME operation reached
// modifyingSubcommands["push"] and was approved outright — measured `allow` for
// `git push origin +main`, `git push origin :main`, `git push
// --force-with-lease=other origin main:other` and `git push -fu origin main`.
// Ask cannot implement that ruling: it asks a person a question the ruling has
// already answered, and an Ask on the two visible spellings beside four silent
// approvals teaches its own bypass — the agent is told "--force is prohibited"
// and retries with `+main`. Reject hands the reason back to the agent and leaves
// no approvable spelling of the operation.
//
// WHAT WOULD JUSTIFY CHANGING IT: a new operator ruling. Needing one force push
// is not one — publishing is operator-authorized anyway, so the operator can run
// it themselves; relaxing any of these to Ask requires the ruling to change.
//
// TEXT VS PARSED: every test here reads PARSED tokens (post-unquote
// cmdparse.ParsedCommand.Args) and the rule runs only when
// isGitExecutable(pc.Executable), so `--force` inside a commit message, a
// heredoc body or a `bd comment` body is TEXT and never matches. That is the
// pg2-5b901 failure mode this deliberately avoids; do not reintroduce a
// strings.Contains over command text.
func (r *Rule) pushVerdict(rest []string) (hookio.RuleResult, bool) {
	reject := func(reason string) (hookio.RuleResult, bool) {
		return hookio.RuleResult{Decision: hookio.Reject, Reason: reason, Module: r.Name()}, true
	}
	shorts := pushShortFlagTokens(rest)
	refspecs := cmdparse.ClassifyPushRefspecs(rest)

	// FORCE — the ruling itself. All three spellings are the same operation: the
	// `+` refspec prefix forces just that refspec, and `-f`/`--force` force every
	// one. ClassifyPushRefspecs deliberately does not reflect the flags, and
	// HasShortFlag deliberately does not match longs, so all three are asked.
	if _, ok := hasPushLongFlag(rest, "force", minAbbrevForce); ok {
		return reject("git: force-push is prohibited — an agent must never force-push (operator ruling 2026-07-30). Every spelling is refused (--force, -f, a clustered -f…, and a '+' refspec prefix), so retrying with another one will not work; use --force-with-lease on the SAME branch, or hand the push to the operator")
	}
	if cmdparse.HasShortFlag(shorts, 'f') {
		return reject("git: force-push is prohibited — -f is --force (operator ruling 2026-07-30). Every spelling is refused, including a clustered -f… such as -fu and a '+' refspec prefix; use --force-with-lease on the SAME branch, or hand the push to the operator")
	}
	for _, rs := range refspecs {
		if rs.Force {
			return reject("git: force-push is prohibited — the '+' prefix in refspec " + rs.Raw + " IS a force (operator ruling 2026-07-30). Drop the '+'; if the push then fails as non-fast-forward, rebase, or use --force-with-lease on the SAME branch")
		}
	}

	// --mirror deletes every remote ref that is absent locally, so it is a
	// remote-ref delete of unbounded width — strictly broader than the
	// single-branch delete below, and never a legitimate agent operation here.
	if _, ok := hasPushLongFlag(rest, "mirror", minAbbrevMirror); ok {
		return reject("git: git push --mirror is prohibited — it DELETES every remote ref absent locally, an unbounded remote-ref deletion (pg2-bohpm, 2026-07-30). Push the one ref you mean by name")
	}

	// REMOTE-REF DELETE. Not force-push, so outside the literal ruling, but it
	// destroys a remote ref, which for another clone may be the only copy, and
	// nothing an agent can do restores it. Rejected for the same reason the flag
	// spellings are: pinning only the `:main` refspec form and leaving `--delete`
	// open would teach the flag form as the bypass. Both are the same operation.
	// Revisiting this needs an operator ruling on remote-ref deletion, not a
	// workflow that finds it inconvenient — deleting a merged remote branch is
	// the platform's job (GitHub does it on merge), not a push's.
	if _, ok := hasPushLongFlag(rest, "delete", minAbbrevDelete); ok {
		return reject("git: deleting a remote ref is prohibited — --delete destroys a ref that may be another clone's only copy (pg2-bohpm, 2026-07-30). Every spelling is refused (--delete, -d, and a ':ref' refspec); let the platform delete a merged branch, or hand it to the operator")
	}
	if cmdparse.HasShortFlag(shorts, 'd') {
		return reject("git: deleting a remote ref is prohibited — -d is --delete (pg2-bohpm, 2026-07-30). Every spelling is refused; let the platform delete a merged branch, or hand it to the operator")
	}
	for _, rs := range refspecs {
		if rs.Delete {
			return reject("git: deleting a remote ref is prohibited — the empty source in refspec " + rs.Raw + " IS a delete (pg2-bohpm, 2026-07-30). Every spelling is refused; let the platform delete a merged branch, or hand it to the operator")
		}
	}

	// --force-with-lease: CROSS-BRANCH is Reject, SAME-BRANCH stays approvable.
	//
	// Cross-branch is a Reject on measured evidence (2026-07-30): pushing `main`
	// onto a divergent `other` with a FRESH lease exited 0 with `+ d3167d6...3cdea6c
	// main -> other (forced update)` and DESTROYED the unique commit on `other`.
	// The lease only pins the ref it NAMED, so it gives zero protection against
	// naming the wrong branch — the safety property the same-branch idiom relies on
	// is simply absent here, which is why this half cannot stay an Ask while the
	// force spellings above are Rejects.
	//
	// Same-branch --force-with-lease is the correct post-rebase idiom and is in
	// daily use, so it MUST fall through to Approve.
	//
	// SEMANTIC TRAP: in `--force-with-lease=<refname>:<expect>` the colon separates
	// the ref from the EXPECTED OBJECT ID, not local from remote. So
	// `--force-with-lease=main:abc123 origin main` is a SAME-branch push carrying an
	// explicit lease — the safest form there is. The lease VALUE is therefore
	// deliberately NOT read here; cross-branch-ness comes only from the push
	// REFSPEC OPERANDS (`main:other`), which is what ClassifyPushRefspecs returns.
	if _, ok := hasPushLongFlag(rest, "force-with-lease", minAbbrevForceWithLease); ok {
		for _, rs := range refspecs {
			// SameRef treats `HEAD:main` as cross-branch: HEAD cannot be resolved
			// from a token, and for a gate the safe reading is cross-branch. Name the
			// branch (`main`, or `main:main`) to get the same-branch verdict.
			if !rs.SameRef() {
				return reject("git: --force-with-lease onto a DIFFERENT remote branch is prohibited — refspec " + rs.Raw + " pushes to another ref, and the lease protects only the ref it NAMES (measured 2026-07-30: it force-updated the destination and destroyed its unique commit). Push to the same branch name instead")
			}
		}
		// A non-origin remote keeps the Ask it has today. The lease is same-branch,
		// so the ruling's Reject does not reach it, but nothing about pg2-bohpm
		// justifies LOOSENING an existing Ask either — this rule's changes are
		// one-directional.
		if remote, _ := cmdparse.FirstOperand(rest); remote != "" && remote != "origin" {
			return hookio.RuleResult{
				Decision: hookio.Ask,
				Reason:   "git: --force-with-lease to a remote other than origin (" + remote + ")",
				Module:   r.Name(),
			}, true
		}
	}
	return hookio.RuleResult{}, false
}
