// Package pgpr approves the read-only half of the first-party `pg-pr` CLI's
// `pr <verb>` command family (bead pg2-lmqy1).
//
// # Why this exists
//
// pg2-xf564's enumeration found pg-pr entirely unrecognized by every rule in this
// chain — including `pg-pr pr list --json`, the originally-reported gap. That
// investigation's own premise turned out to be stale by one day: `pg-pr pr list`
// and `pg-pr pr view` were deleted outright in commit 6a7647d9 ("pg-pr: delete
// retiring CLI command groups (sync, changes, open, migrate, pr
// list/view/wip/hide/unhide/ready, create --wip)"), landed 2026-09-21, a day
// before this bead was filed and already an ancestor of the branch this rule
// lands on. Re-reading packages/pg-pr/cmd/pg-pr/pr.go and pr_write.go directly
// (not the bead's prose, and not the many docs/specs mentions of the old
// surface, several of which are historical/aspirational) confirms today's `pr`
// cobra tree is exactly:
//
//	read-only:  files, commits
//	mutating:   create, update, close, draft, automerge {on,off}, merge
//
// `list` and `view` are consequently NOT matched here: approving a subcommand
// name that does not exist would classify nothing (cobra itself refuses an
// unknown command before this rule would ever see a side effect), and baking in
// a dormant Approve for a retired shape only invites the WRONG verdict if either
// name is ever reintroduced with different semantics. A future re-addition needs
// its own read of the cobra tree at that time, exactly as this bead required.
//
// # The classification
//
// Approve, unconditionally, any flag combination — read-only, no side effect
// (packages/pg-pr/cmd/pg-pr/pr.go's prCmd.Long: "Read-only inspection of pull
// requests"):
//
//	pr files, pr commits
//
// Deliberately left UNCLAIMED (falls through to NotApplicable) — each needs its
// own review the way `gh pr merge`/`gh pr create` got theirs, out of scope for
// this bead:
//
//	pr create, pr update, pr close, pr draft, pr automerge {on,off}, pr merge,
//	review submit (and every other pg-pr resource/verb: review draft/post,
//	comment add/resolve, worktree/branch/auth/ci/config/issue/migrate/version —
//	none of those are "pr", so the resource check below never reaches them)
//
// # Command-path resolution — why this does NOT reuse gh.CommandPath
//
// pg-pr is its own cobra binary, not a `gh` extension, but it has the same
// STRUCTURAL bypass concern gh.go's pg2-by1ij doc describes: cobra resolves a
// command by stripping flags from argv, so a flag may legally sit before or
// inside the `pr <verb>` path, and a naive positional read (pc.Args[0]/[1])
// would mis-resolve it. gh.CommandPath is exported specifically for this
// resolution, and ghstack.go reuses it verbatim — but doing the same here would
// bake in gh's OWN flag registration into a foreign binary's argv, and MEASURED
// against this repo's pg-pr binary (2026-09-22, `go build ./cmd/pg-pr/`), that
// substitution is wrong in one concrete way: `pg-pr --help pr files` resolves to
// `unknown command "files" for "pg-pr"` — i.e. --help swallowed "pr" as a VALUE,
// it was not skipped as a bare bool the way gh.CommandPath's ghNoValueLongFlags
// assumes --help/--version are. That is because cobra's InitDefaultHelpFlag /
// InitDefaultVersionFlag register those two flags lazily, inside
// Command.execute(), which runs AFTER Find() has already resolved the command
// path — so at the moment pg-pr's own Find() walks argv, --help/--version are
// simply unregistered like any other pg-pr flag, and fall to cobra's default
// "unregistered long flag consumes the next token" rule. gh must therefore be
// registering (or exposing) --help/--version earlier in its own startup than a
// bare cobra.Command does; pg-pr does not. pgprCommandPath below models pg-pr's
// MEASURED behavior directly instead: there is no no-value exception list at
// all, because nothing is exempt at Find()-time for this binary.
//
// This does not create a gap this rule needs to close: pg-pr registers every
// one of `pr`'s flags directly on the LEAF subcommand (never persistent on
// `pr` or on the root), so a flag misplaced before or inside the path does not
// make the real pg-pr binary execute a DIFFERENT command with that flag applied
// (unlike gh's persistent --repo) — it makes cobra either resolve to `pr`
// itself (which has no RunE) or fail with "unknown flag", both inert. The
// resolver below is still worth having independently of that: it is what makes
// this rule's OWN resource/verb match agree with cobra's real resolution
// instead of silently drifting from it, and it is what the regression tests
// below pin.
package pgpr

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// approvedReadOnlyPRVerbs are the `pg-pr pr <verb>` subcommands
// packages/pg-pr/cmd/pg-pr/pr.go registers as read-only (prCmd.Long: "Read-only
// inspection of pull requests"). Argument-blind: neither takes a flag that
// changes what it reads, only how it is rendered (--json) or which base ref it
// diffs against (--base).
var approvedReadOnlyPRVerbs = map[string]bool{
	"files": true, "commits": true,
}

type Rule struct{}

// New constructs the pg-pr rule. It takes no configuration: like
// pnwf/ghstack/pnworkspace/pgccaudit, the classification is fixed by pg-pr's
// own cobra source and applies uniformly, with no per-consumer data to inject.
func New() *Rule { return &Rule{} }

func (r *Rule) Name() string { return "pg-pr" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := hookio.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("pg-pr: read bash command: %w", err)
	}
	for _, pc := range parsed {
		if filepath.Base(pc.Executable) != "pg-pr" {
			continue
		}
		resource, subcmd, _ := pgprCommandPath(pc.Args)
		if resource != "pr" {
			// Every other pg-pr resource (review, comment, worktree, branch, auth,
			// ci, config, issue, migrate, version) — and an unrecognized/no
			// resource at all — is out of scope for this bead. Leave unclaimed
			// rather than guess.
			continue
		}
		if approvedReadOnlyPRVerbs[subcmd] {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "pg-pr pr " + subcmd + ": read-only PR inspection, no side effect (pg-pr pr --help: \"Read-only inspection of pull requests\")",
				Module:   r.Name(),
			}, nil
		}
		// Every `pr` write verb (create/update/close/draft/automerge/merge) and
		// any unrecognized `pr` subcommand (including the retired `list`/`view` —
		// see the package doc): leave unclaimed rather than guess. Each needs its
		// own review the way `gh pr merge`/`gh pr create` got theirs.
		continue
	}
	return hookio.NotApplicable()
}

// pgprCommandPath resolves pg-pr's own two-word command path — resource (e.g.
// "pr") and subcmd (e.g. "files") — from args, the tokens AFTER the pg-pr
// executable, skipping flags the way pg-pr's own cobra Find() does when
// searching for its command path. rest is args with both path words removed
// (unused today — no verdict below needs it, since every approved verb is
// argument-blind — but returned for symmetry with gh.CommandPath and so a
// future verb-specific verdict does not have to re-derive it).
//
// See the package doc's "why this does NOT reuse gh.CommandPath" section for
// why this needs its OWN no-value-flag model rather than gh.CommandPath's:
// pg-pr has no flag that is registered by the time Find() walks argv, so
// EVERY long flag defaults to consuming the next token as its value, and
// every bare two-character short flag does likewise. There is no equivalent
// of gh's ghNoValueLongFlags exception list here.
func pgprCommandPath(args []string) (resource, subcmd string, rest []string) {
	words := pgprCommandWordIndexes(args, 2)
	if len(words) > 0 {
		resource = args[words[0]]
	}
	if len(words) > 1 {
		subcmd = args[words[1]]
	}
	return resource, subcmd, omitIndexes(args, words)
}

// pgprCommandWordIndexes returns the indexes in args of the first `want`
// COMMAND WORDS, skipping flags the way cobra's own command search does: a
// long flag (`--repo`) consumes the NEXT token unless it is '='-glued (no
// no-value exception — see the package doc); a BARE short (`-R`, exactly two
// characters) consumes the next token; a short carrying its value in the same
// token (`-Ro/r`, `-R=o/r`) consumes nothing; and nothing after a `--`
// end-of-options terminator is a command word. A lone `-` is neither flag nor
// command word and is skipped without consuming anything, mirroring
// gh.go's ghCommandWordIndexes (which measured the identical `gh - pr create`
// case for gh).
func pgprCommandWordIndexes(args []string, want int) []int {
	out := make([]int, 0, want)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return out
		}
		if strings.HasPrefix(a, "--") {
			if _, _, glued := strings.Cut(a[2:], "="); !glued {
				i++ // `--repo o/r`: the next token is this flag's VALUE
			}
			continue
		}
		if len(a) == 2 && a[0] == '-' {
			i++ // `-R o/r`: a BARE short's value is the next token
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue // a cluster or a glued short value, or a lone `-`: one token
		}
		out = append(out, i)
		if len(out) >= want {
			return out
		}
	}
	return out
}

// omitIndexes returns args without the tokens at the ASCENDING indexes in
// drop, identical in shape to gh.go's own helper of the same name (kept as a
// separate copy rather than exported/shared: two four-line functions do not
// justify a new cross-package dependency).
func omitIndexes(args []string, drop []int) []string {
	if len(drop) == 0 {
		return args
	}
	out := make([]string, 0, len(args)-len(drop))
	next := 0
	for i, a := range args {
		if next < len(drop) && drop[next] == i {
			next++
			continue
		}
		out = append(out, a)
	}
	return out
}
