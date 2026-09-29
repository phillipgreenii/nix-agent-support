// Package pgconnector approves the read/write `pg-connector issue
// show/comment/update/close` verb family — exactly the four grants
// PG_ROUTER_CCPOOL_HANDLER_CONFIG's allowedTools already lists (bead
// pg2-s9zh5's widening: packages/pg-router-ccpool-handler/internal/config/
// config.go's baseAllowedTools, mirrored in
// home/programs/pg-router-ccpool-handler/default.nix's defaultAllowedTools) —
// for `permissionMode=dontAsk` dispatch (bead pg2-r848s).
//
// # Root cause this closes
//
// pg2-fjnyi's live verification (test bead pg2-itxtv, 2026-09-29) found that a
// pg2-escalation-triager dispatch got `pg-connector issue *` (even `--help`)
// auto-denied under `permissionMode=dontAsk`, while `pg-connector --help`,
// `pg-router ...`, and `ccpool ...` calls under the SAME allowedTools grant
// succeeded — even though pg2-fjnyi separately confirmed (2026-09-26) that the
// live allowedTools config contains all 8 grants verbatim, including
// "pg-connector issue show/comment/update/close".
//
// Neither this hook's engine (setup.RuleChain, factory.go) nor any of its rule
// modules recognized "pg-connector" as a basename before this bead (grep
// confirms zero prior mentions anywhere under packages/claude-extended-tool-
// approver/internal), so every `pg-connector issue ...` invocation fell all
// the way through the chain to NoOpinion (abstain) and deferred to Claude
// Code's OWN native `--allowedTools`/settings.json permission matching.
//
// That native matching is a literal TEXT-PREFIX match against the raw command
// string (this hook's own internal/settingseval/matchers.go's bashMatcher
// documents and replicates the same behavior for its offline baseline/compare
// tooling). And the escalation-triager role is NOT invoked bare: per
// docs/superpowers/specs/2026-09-22-pg-router-ccpool-escalation-design.md's
// "Tracker targeting" section, its prompt instructs it to prefix EVERY
// `pg-connector` invocation with `PG_CONNECTOR_ISSUE_BEADS_DIR=<dir>` (an
// inline env-var assignment; pg-connector-issue-beads does not resolve its
// target tracker from CWD, by design — bead pg2-1q9c0). The actual invoked
// text is therefore `PG_CONNECTOR_ISSUE_BEADS_DIR=<dir> pg-connector issue
// show <id>`, which does NOT literally start with "pg-connector issue show"
// — so it never matches the `Bash(pg-connector issue show:*)`-shaped
// allowedTools entry at all, and permissionMode=dontAsk auto-denies whatever
// no rule explicitly approved. This is the SAME class of bug already measured
// and fixed once for `export`/inline-env-var prefixes (main.go's
// handlePreToolUse doc comment, ADR 0070, bead tc-7m85u item 3): an
// env-var-prefixed command breaks a literal-text allowlist match unless
// something explicitly approves the REWRITTEN/parsed shape rather than
// relying on Claude Code's own raw-text comparison.
//
// The fix follows the SAME architecture this hook already uses for every
// other first-party CLI command family (pgpr, pgccaudit, pnworkspace, pnwf,
// killprobe, rcpreflight, ghstack): a dedicated rule that recognizes the
// invocation from cmdparse's ALREADY-PARSED leaf (Executable/Args), which
// cmdparse has already lifted any leading `VAR=value` assignment out of and
// into EnvVars (internal/cmdparse/parser.go's ParsedCommand.EnvVars) before
// any rule ever sees it. Approving here therefore makes ceta's own decision
// authoritative regardless of the env-var-prefix invocation shape, exactly
// mirroring the config's existing four-verb grant instead of depending on a
// native match this hook has no control over.
//
// `pg-connector issue --help` (and `pg-connector help issue`, or any other
// `pg-connector <resource> --help`) is a SEPARATE fix, made in
// internal/rules/safecmds's `hasSubcommands` map rather than here: pg-connector
// has the identical "$command $subcommand" shape safecmds' generic
// isHelpRequest already recognizes for git/gh/kubectl/npm/bd/etc, so adding it
// there covers --help for every pg-connector resource (issue, pr, ci, scm,
// ...) uniformly rather than special-casing "issue" alone in this file.
package pgconnector

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// approvedIssueVerbs are the `pg-connector issue <verb>` subcommands
// PG_ROUTER_CCPOOL_HANDLER_CONFIG's allowedTools already grants verbatim
// (config.go's baseAllowedTools / default.nix's defaultAllowedTools):
// "Bash(pg-connector issue show:*)", "...comment:*)", "...update:*)",
// "...close:*)". Deliberately NOT "create"/"list"/"transition"/"deps"/
// "changes" (issue.go's other verb-group members) or any OTHER pg-connector
// resource (pr/ci/scm/thread/calendar/agentsession/attention/search/ledger/
// cache/auth/config) — none of those are in the grant this bead's fix mirrors,
// and approving beyond it would widen auto-approval past what the config
// (and the operator ruling that produced it, bead pg2-s9zh5) actually
// authorized.
var approvedIssueVerbs = map[string]bool{
	"show": true, "comment": true, "update": true, "close": true,
}

type Rule struct{}

// New constructs the pg-connector rule. It takes no configuration: like
// pgpr/pgccaudit/pnwf/pnworkspace, the classification mirrors a fixed,
// already-authorized allowedTools grant and applies uniformly, with no
// per-consumer data to inject.
func New() *Rule { return &Rule{} }

func (r *Rule) Name() string { return "pg-connector" }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return hookio.NotApplicable()
	}
	parsed, err := hookio.LeavesOf(input)
	if err != nil {
		return hookio.RuleResult{}, fmt.Errorf("pg-connector: read bash command: %w", err)
	}
	for _, pc := range parsed {
		if filepath.Base(pc.Executable) != "pg-connector" {
			continue
		}
		resource, subcmd, _ := pgConnectorCommandPath(pc.Args)
		if resource != "issue" {
			// Every other pg-connector resource (pr, ci, scm, thread, calendar,
			// agentsession, attention, search, ledger, cache, auth, config) — and
			// an unrecognized/no resource at all — is out of scope for this bead's
			// grant. Leave unclaimed rather than guess.
			continue
		}
		if approvedIssueVerbs[subcmd] {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "pg-connector issue " + subcmd + ": within PG_ROUTER_CCPOOL_HANDLER_CONFIG's existing allowedTools grant (bead pg2-s9zh5); approved directly here (rather than left to Claude Code's own --allowedTools text-prefix match) because the escalation-triager's prompt-mandated PG_CONNECTOR_ISSUE_BEADS_DIR=<dir> env-var prefix breaks that literal match (bead pg2-r848s) — cmdparse already lifts the prefix into EnvVars before this rule ever sees Executable/Args, so this decision is invocation-shape-independent",
				Module:   r.Name(),
			}, nil
		}
		// create/list/transition/deps/changes, and any unrecognized `issue`
		// subcommand: leave unclaimed rather than guess. Each is outside this
		// bead's grant and needs its own review if it ever needs one.
		continue
	}
	return hookio.NotApplicable()
}

// pgConnectorCommandPath resolves pg-connector's own two-word command path —
// resource (e.g. "issue") and subcmd (e.g. "show") — from args, the tokens
// AFTER the pg-connector executable, skipping flags the way pg-connector's
// own cobra Find() does when searching for its command path. rest is args
// with both path words removed (unused today — no verdict above needs it,
// since every approved verb is argument-blind beyond its path — but returned
// for symmetry with pgpr.pgprCommandPath/gh.CommandPath).
//
// pg-connector registers exactly two flags that could precede/interleave the
// resource/verb path: root's persistent --output (root.go/output.go's
// addOutputFlag) and each leaf's own --backend (backend_flag.go's
// addBackendFlag) — both String flags (always consume a value), never a bool.
// There is no equivalent of gh's ghNoValueLongFlags no-value exception list
// for this binary (same conclusion pgpr.go's own doc reaches for pg-pr, for
// the same reason: nothing here is registered as a no-value flag either).
func pgConnectorCommandPath(args []string) (resource, subcmd string, rest []string) {
	words := pgConnectorCommandWordIndexes(args, 2)
	if len(words) > 0 {
		resource = args[words[0]]
	}
	if len(words) > 1 {
		subcmd = args[words[1]]
	}
	return resource, subcmd, omitIndexes(args, words)
}

// pgConnectorCommandWordIndexes returns the indexes in args of the first
// `want` COMMAND WORDS, skipping flags: a long flag (`--output`) consumes the
// NEXT token unless it is '='-glued; a bare short (exactly two characters,
// e.g. `-o`) consumes the next token; a short carrying its value in the same
// token consumes nothing; and nothing after a `--` end-of-options terminator
// is a command word. A lone `-` is neither flag nor command word and is
// skipped without consuming anything. Mirrors pgpr.pgprCommandWordIndexes /
// gh.go's ghCommandWordIndexes.
func pgConnectorCommandWordIndexes(args []string, want int) []int {
	out := make([]int, 0, want)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return out
		}
		if strings.HasPrefix(a, "--") {
			if _, _, glued := strings.Cut(a[2:], "="); !glued {
				i++ // `--output json`: the next token is this flag's VALUE
			}
			continue
		}
		if len(a) == 2 && a[0] == '-' {
			i++ // `-o json`: a BARE short's value is the next token
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
// drop, identical in shape to pgpr.go/gh.go's own helper of the same name
// (kept as a separate copy rather than exported/shared: two four-line
// functions do not justify a new cross-package dependency).
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
