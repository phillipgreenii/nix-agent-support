package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// usageLine is the short synopsis printed to stderr on a usage error.
const usageLine = "usage: pr-pool [--version | --help] [run [--only <selector>]... [--disable <selector>]... | run-until-idle [--only <selector>]... [--disable <selector>]... | run-query [--json] <role> | run-role [--json] <role> <bead> | config (--print-defaults | --show [--json]) | sessions | reconcile | push-inject [--json] [--socket <path>] [--token <tok>] <json> | pause [<gate>] | resume [<gate> | --all] | status [--json] [--socket <path>] [--token <tok>] | ingest-event [--socket <path>] [--token <tok>] | self-status [--socket <path>] [--token <tok>]]"

// helpText is the full help printed to stdout for --help/help.
const helpText = usageLine + `

pr-pool routes typed events from configured sources through a durable queue to
configured handler roles (INTF-SOURCE -> queue -> INTF-HANDLER). "run" boots the
core as a long-running daemon; "run-until-idle" boots it, fires one discovery
pass, drains the queue to idle, then exits (a one-shot pass equivalent to the
former "drain"). Bare "pr-pool" (no subcommand) prints usage and exits non-zero
— an explicit subcommand is REQUIRED.

Subcommands:
  run                     boot the core and run indefinitely, producing + dispatching on a
                          fixed poll interval, until SIGINT/SIGTERM requests shutdown
  run-until-idle          boot the core, discover once, drain the queue to idle, then exit
                          (also reachable as "drain", kept as a deprecated alias)
  run-query [--json] <role>
                          run a role's discovery query and print matches (read-only); --json emits
                          one JSON object ({role, queries, total, matches}) instead of the
                          tab-separated lines
  run/run-until-idle --only <selector> / --disable <selector>
                          run-scoped selectors (STORY-OP-3): restrict which configured
                          sources/handlers this ONE run activates, without editing
                          config.toml. Repeatable; a selector is role:<name> or
                          query:<name>. --only (allow-list), if given, narrows to just the
                          named participants; --disable (deny-list) then excludes any named
                          participant from what's left. Env equivalents PR_POOL_ONLY /
                          PR_POOL_DISABLE (comma-separated) are UNIONED with the flags, not
                          overridden by them. A selector naming an unconfigured role/query
                          is a usage error. See docs/decisions/cli.md's DEC-CLI-1.
  run-role [--json] <role> <bead>
                          dispatch one bead through a role, then tear down (smoke test); --json
                          emits a small JSON report ({role, bead, accepted}) on success instead of
                          nothing
  config --print-defaults print the built-in default config.toml (copy-paste starting point)
  config --show [--json]  print the resolved config path, role set, and worker dispatch scalars
                          (permission-mode / allowed-tools / autonomous / budget); --json emits the
                          same information as one JSON object. --json is valid only with --show.
  sessions                list this pool's sessions (bead/role) from session metadata (read-only)
  reconcile               report stranded self-owned feedback cycles, then run the pg-pr ACL: ensure a review-pr bead per open PR (reads 'pg-pr pr list'; mutates beads; exit-0-on-partial)
  push-inject <json>      inject one operator-supplied event into the RUNNING core (the same core-side
                          enqueue as the ingest-event callback, operator-initiated). Text by default,
                          JSON with --json. Locates the core via --socket/--token, else
                          PR_POOL_SOCKET/PR_POOL_TOKEN, else discovery under the log dir. It NEVER
                          starts a core: with none running it fails with "no running core" (exit 1).
  status                  inspect the RUNNING core: resolved configuration, live deliveries, and
                          per-type queue depths, plus gates/mode/listeners/sources/unmatched
                          bindings/recent activity. Text by default, JSON with --json. Locates the
                          core via --socket/--token, else PR_POOL_SOCKET/PR_POOL_TOKEN, else
                          discovery under the log dir. It NEVER starts a core: with none running it
                          fails with "no running core" (exit 1).
  pause [<gate>]          set gate <gate> (default quota-paused) directly on its file-backed state
                          (INV-LIFE-2): exits 0 even with NO core running, reporting that the change
                          takes effect at the next start (a currently running "run" picks it up on
                          its next tick). FILE-DIRECT: unlike every operator subcommand above,
                          pause/resume NEVER Discover or Dial a core — this deliberately breaks the
                          verb-named-subcommand-is-a-socket-client symmetry that push-inject/
                          ingest-event/self-status follow. A socket-level pause/resume verb also
                          exists (Phase 3) for a client already holding a connection.
  resume [<gate>] | --all clear gate <gate> (default quota-paused), or every outstanding gate at once
                          with --all; a bare "resume" clears ONLY the default gate, so an
                          automation-owned gate (cicd-down) is never cleared by accident.
                          "resume --all <gate>" (both at once) is a usage error. Same FILE-DIRECT,
                          no-core-required mechanics as pause.
  version                 print the version and exit
  help                    print this help and exit

Manager -> core callback subcommands (NOT for operators; the core hands a
participant these commands with --socket/--token already baked in, and the
participant runs them):
  ingest-event            deliver events to the RUNNING core. Request JSON on stdin, reply JSON on
                          stdout; exit 0 ok / 1 error / 2 usage / 9 busy. Locates the core via
                          --socket/--token, else PR_POOL_SOCKET/PR_POOL_TOKEN, else discovery under
                          the log dir. It NEVER starts a core: with none running it fails with
                          "no running core" (exit 1).
  self-status             push the caller's OWN status (healthy/degraded/unavailable) to the
                          RUNNING core, naming the participantId it registered under. Request JSON
                          on stdin, reply JSON on stdout; exit 0 ok / 1 error / 2 usage / 9 busy.
                          Every registered participant kind gets this callback, unlike ingest-event
                          (a source's alone). Locates the core the same way ingest-event does, and
                          never starts one.

Roles are configured in <RepoRoot>/.pr-pool/config.toml (override the path with
PR_POOL_CONFIG). With no config file, pr-pool uses the built-in feedback + worker
roles. <role> is the role's configured name. Run "pr-pool config --print-defaults"
to see the schema and defaults.

Pool-wide settings come from PR_POOL_* environment variables:
  PR_POOL_REPO_ROOT        monorepo root the drain operates in (default: cwd)
  PR_POOL_BEADS_PREFIX     expected bead prefix, asserted at precheck (default zr)
  PR_POOL_BUDGET_TOKENS    per-worker token budget; 0 = unlimited (default 0)
  PR_POOL_BUDGET_COST      per-worker cost budget in cents; 0 = unlimited (default 0)
  PR_POOL_BUDGET_TIME      per-worker wall-clock budget in seconds (default 1500)
  PR_POOL_MODEL            claude model override (default: ccpool's default)
  PR_POOL_EFFORT           claude --effort value (default max)
  PR_POOL_PERMISSION_MODE  claude --permission-mode for workers (default dontAsk; bypassPermissions is the opt-in escape)
  PR_POOL_ALLOWED_TOOLS    claude --allowed-tools allowlist for workers (default: conservative deny-by-default set; empty clears the flag)
  PR_POOL_CONFIG           explicit config.toml path (default <RepoRoot>/.pr-pool/config.toml)
  PR_POOL_ACTIVITY_RING    dispatch-outcome activity ring buffer capacity (default 512)
  PR_POOL_LOG_DIR          event-log/state directory: gates/, events.jsonl, the discovery record
                           (default: the XDG state dir, e.g. ~/.local/state/pr-pool)
  PR_POOL_QUOTA_PAUSED     quota-paused gate file path override (default <PR_POOL_LOG_DIR>/gates/quota-paused)
  PR_POOL_CICD_DOWN        cicd-down gate file path override (default <PR_POOL_LOG_DIR>/gates/cicd-down)
  PR_POOL_ONLY             run/run-until-idle only: comma-separated run-scoped allow-list,
                           each entry role:<name> or query:<name> (DEC-CLI-1); unioned with
                           any --only flags on the same invocation
  PR_POOL_DISABLE          run/run-until-idle only: comma-separated run-scoped deny-list,
                           same grammar as PR_POOL_ONLY; unioned with any --disable flags

Precedence for every scalar above that a [pool] key can also set (including the two gate
paths): [pool] wins over PR_POOL_* env, which wins over the built-in default — matching
internal/config's package doc and 'config --print-defaults's header. The XDG-global config
($XDG_CONFIG_HOME/pr-pool/config.toml, else ~/.config/pr-pool/config.toml) contributes
[pool].budget only, beneath the repo-local file and above env; it sets nothing else.

REMOVED: PR_POOL_MAX_WORKER, PR_POOL_MAX_FEEDBACK, PR_POOL_FEEDBACK_ENABLED,
PR_POOL_WORKER_ENABLED, PR_POOL_SKILL_MD, PR_POOL_WORKER_SKILL_MD. Set
role.enabled / the role's prompt in config.toml instead; the former per-role
MAX_WORKER/MAX_FEEDBACK cap is not a config knob at all any more (INV-CONC-1)
— a handler expresses its own capacity limit as a pre-accept busy decline.`

// routeKind enumerates what the program should do after parsing argv. Keeping
// the decision pure (no I/O, no side effects) is what guarantees a help/version
// request or a parse error can never fall through to a real drain — a drain
// dispatches sessions and tears down every pr-pool-* tmux session, so fail-open on
// a parse error is a real foot-gun (pg2-52rn).
type routeKind int

const (
	routeVersion      routeKind = iota // print the version and exit 0
	routeHelp                          // print usage and exit 0
	routeUsageErr                      // print .msg + usage to stderr and exit 2
	routeRun                           // boot the core as a long-running daemon (INV-LIFE-1)
	routeRunUntilIdle                  // boot the core, discover once, drain to idle, exit (INV-LIFE-1; also "drain")
	routeRunRole                       // dispatch one bead through a role (.role, .bead)
	routeRunQuery                      // run a role's discovery query read-only (.role)
	routeConfig                        // print/show config (.configMode)
	routeSessions                      // list this pool's sessions from metadata (read-only)
	routeReconcile                     // report stranded self-owned feedback cycles, then run the pg-pr ACL (mutates beads)
	routeIngestEvent                   // manager->core callback: forward events on stdin to the running core (.rest)
	routePushInject                    // operator: inject one event into the running core (.rest)
	routeStatus                        // operator: inspect the running core (Task 3.8, .rest)
	routeSelfStatus                    // manager->core callback: push the caller's own self-status to the running core (.rest)
	routePause                         // file-direct: set gate .gate directly on its file-backed state (INV-LIFE-2); never Discover/Dial
	routeResume                        // file-direct: clear gate .gate, or every gate with .allGates; never Discover/Dial
)

type routeResult struct {
	kind       routeKind
	rest       []string // drain subcommand args (routeDrain only)
	msg        string   // diagnostic for routeUsageErr
	role       string   // run-role / run-query role name
	bead       string   // run-role bead id
	configMode string   // "print-defaults" | "show" (routeConfig only)
	// gate / allGates are routePause/routeResume's TYPED fields (Task 1.2b): the
	// gate name (already validated against the two known gates, defaulted to
	// quota-paused when omitted) and, for routeResume only, whether --all was
	// given. Parsed here in route()'s helpers, never re-parsed from .rest.
	gate     string
	allGates bool
	// only / disable carry the raw --only/--disable flag OCCURRENCES for
	// routeRun/routeRunUntilIdle (STORY-OP-3, DEC-CLI-1) — NOT yet combined
	// with PR_POOL_ONLY/PR_POOL_DISABLE, since reading the environment is I/O
	// route() otherwise avoids; runRun/runRunUntilIdle fold the environment in
	// via resolveSelectors (selectors.go).
	only    []string
	disable []string
	// json is the --json flag (Task 1.5b): config --show / run-query / run-role
	// only, per DEC-CLI-1's global --json option. Per Task 0.4's wire decision
	// (recorded in docs/decisions/cli.md's DEC-CLI-1 "--json's versioning"
	// note), a subcommand's --json output is UNVERSIONED by default — this
	// field only says whether the flag was given, not anything about the
	// output shape's versioning.
	json bool
}

// route inspects the full argv and decides what to do, without side effects. No
// subcommand ⇒ drain. Top-level --version/-v and --help/-h are recognised here
// so they cannot be misrouted to a drain; any other leading flag, or any unknown
// subcommand, is a usage error rather than a silent fall-through to drain.
//
// Per CLI convention (GNU Coding Standards §4.7; clap/cobra/click), a leading
// --version/-v or --help/-h SHORT-CIRCUITS: it is honoured and exits 0,
// deliberately ignoring any trailing arguments (so `pr-pool --version drain`
// prints the version, not a usage error). A subcommand-scoped flag like
// `pr-pool drain --version` is NOT a global request — drain defines no such
// flag, so it is reported as an unknown flag (exit 2), matching cobra/docker.
func route(argv []string) routeResult {
	args := argv[1:] // strip program name
	if len(args) == 0 {
		// A subcommand is REQUIRED (bead pg2-f3mcb.2 — a deliberate compatibility
		// break): bare "pr-pool" used to default to a drain pass (routeDrain);
		// it now prints usage and exits non-zero like any other missing-arg
		// usage error, rather than silently dispatching sessions.
		return routeResult{kind: routeUsageErr, msg: "pr-pool: a subcommand is required"}
	}
	switch args[0] {
	case "version", "--version", "-v":
		return routeResult{kind: routeVersion}
	case "help", "--help", "-h":
		return routeResult{kind: routeHelp}
	case "run":
		return parseRunLikeArgs(routeRun, args[1:])
	case "run-until-idle":
		return parseRunLikeArgs(routeRunUntilIdle, args[1:])
	case "drain":
		// Deprecated alias for run-until-idle (bead pg2-f3mcb.2): the pre-
		// convergence internal/orchestrator + internal/eventbus "drain" path is
		// retired; every caller that still says "pr-pool drain" gets the exact
		// same queue-driven one-shot pass run-until-idle runs.
		return parseRunLikeArgs(routeRunUntilIdle, args[1:])
	case "run-role":
		return parseRunRoleArgs(args[1:])
	case "run-query":
		return parseRunQueryArgs(args[1:])
	case "config":
		return parseConfigArgs(args[1:])
	case "sessions":
		return parseSessionsArgs(args[1:])
	case "reconcile":
		return parseReconcileArgs(args[1:])
	case "ingest-event":
		// The callback subcommand parses its OWN flags in its handler rather than
		// here, because it renders its own subcommand-prefixed diagnostic alongside
		// the --socket/--token it alone accepts. It reaches the SAME usage exit code
		// routeUsageErr would produce (ADR 0042's Decision made 2 usage everywhere),
		// so this is a diagnostic split, not an exit-code one.
		return routeResult{kind: routeIngestEvent, rest: args[1:]}
	case "push-inject":
		// Same reason as ingest-event: its own flags, its own diagnostic (including
		// the "quote the event JSON" hint), and the same usage exit code.
		return routeResult{kind: routePushInject, rest: args[1:]}
	case "status":
		// Task 3.8: its own --json/--socket/--token flags, parsed in its own
		// handler, with the same usage exit code every operator subcommand uses.
		return routeResult{kind: routeStatus, rest: args[1:]}
	case "self-status":
		// Same reason as ingest-event: its own --socket/--token flags, parsed in its
		// own handler, with the same usage exit code (routeUsageErr would produce).
		return routeResult{kind: routeSelfStatus, rest: args[1:]}
	case "pause":
		return parsePauseArgs(args[1:])
	case "resume":
		return parseResumeArgs(args[1:])
	}
	if strings.HasPrefix(args[0], "-") {
		return routeResult{kind: routeUsageErr, msg: "unknown flag: " + args[0]}
	}
	return routeResult{kind: routeUsageErr, msg: "unknown subcommand: " + args[0]}
}

// parseRunLikeArgs validates a subcommand that takes only the run-scoped
// selector flags (--only/--disable, STORY-OP-3) and no positionals — run,
// run-until-idle, and the deprecated drain alias for run-until-idle. It stays
// pure aside from collecting those flag occurrences (no environment read, no
// config I/O): it reports a routeKind and never boots a core itself, so the
// caller can refuse to touch config/precheck/the queue on a parse error or
// help request (pg2-52rn's "no fall-through to a real dispatch on bad input"
// guarantee, carried over from the retired parseDrainArgs). A help flag
// yields routeHelp; any other parse failure (or an unexpected positional)
// yields routeUsageErr.
func parseRunLikeArgs(kind routeKind, args []string) routeResult {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render usage/errors ourselves; suppress flag's defaults
	only, disable := registerSelectorFlags(fs)
	pos, err := parseInterspersed(fs, args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return routeResult{kind: routeHelp}
	case err != nil:
		// The only flags this subcommand defines are --only/--disable, so an
		// unrecognized dash-prefixed token is still the offender. Report it in
		// the same "unknown flag: X" phrasing as the top-level route (the
		// stdlib's "flag provided but not defined: -x" single-dashes the flag
		// and reads differently).
		return routeResult{kind: routeUsageErr, msg: "unknown flag: " + firstFlag(args)}
	case len(pos) > 0:
		return routeResult{kind: routeUsageErr, msg: "unexpected argument: " + pos[0]}
	}
	return routeResult{kind: kind, only: only.values, disable: disable.values}
}

// parseRunRoleArgs validates `run-role [--json] <role> <bead>`. Pure: it checks
// only that a role TOKEN and a bead id are present (and no extra args), after
// pulling out an optional --json occurring anywhere in args (extractJSONFlag).
// The role NAME is NOT validated here — that needs the loaded config, so it
// moves to the handler. A dash-prefixed first positional is a missing role (a
// flag, not a name). (pg2-52rn)
func parseRunRoleArgs(args []string) routeResult {
	asJSON, pos := extractJSONFlag(args)
	if len(pos) < 1 || pos[0] == "" || strings.HasPrefix(pos[0], "-") {
		return routeResult{kind: routeUsageErr, msg: "run-role: missing role (usage: run-role [--json] <role> <bead>)"}
	}
	if len(pos) < 2 || pos[1] == "" {
		return routeResult{kind: routeUsageErr, msg: "run-role: missing bead id"}
	}
	if len(pos) > 2 {
		return routeResult{kind: routeUsageErr, msg: "run-role: unexpected argument: " + pos[2]}
	}
	return routeResult{kind: routeRunRole, role: pos[0], bead: pos[1], json: asJSON}
}

// parseRunQueryArgs validates `run-query [--json] <role>`. Pure, same fail-fast
// contract (extractJSONFlag pulls --json out first); the role name is validated
// in the handler after config load.
func parseRunQueryArgs(args []string) routeResult {
	asJSON, pos := extractJSONFlag(args)
	if len(pos) < 1 || pos[0] == "" || strings.HasPrefix(pos[0], "-") {
		return routeResult{kind: routeUsageErr, msg: "run-query: missing role (usage: run-query [--json] <role>)"}
	}
	if len(pos) > 1 {
		return routeResult{kind: routeUsageErr, msg: "run-query: unexpected argument: " + pos[1]}
	}
	return routeResult{kind: routeRunQuery, role: pos[0], json: asJSON}
}

// parseSessionsArgs validates `sessions` (no args; read-only).
func parseSessionsArgs(args []string) routeResult {
	if len(args) > 0 {
		return routeResult{kind: routeUsageErr, msg: "sessions: unexpected argument: " + args[0]}
	}
	return routeResult{kind: routeSessions}
}

// parseReconcileArgs validates `reconcile` (no args; read-only).
func parseReconcileArgs(args []string) routeResult {
	if len(args) > 0 {
		return routeResult{kind: routeUsageErr, msg: "reconcile: unexpected argument: " + args[0]}
	}
	return routeResult{kind: routeReconcile}
}

// parseConfigArgs validates `config (--print-defaults | --show [--json])`.
// --json (extracted wherever it appears via extractJSONFlag, so `config --json
// --show` and `config --show --json` are both accepted) is valid only with
// --show: --print-defaults' output is the built-in config.toml as TEXT, and
// Task 1.5b defines no JSON encoding for it.
func parseConfigArgs(args []string) routeResult {
	asJSON, pos := extractJSONFlag(args)
	if len(pos) != 1 {
		return routeResult{kind: routeUsageErr, msg: "config: usage: config (--print-defaults | --show [--json])"}
	}
	switch pos[0] {
	case "--print-defaults":
		if asJSON {
			return routeResult{kind: routeUsageErr, msg: "config: --json is valid only with --show"}
		}
		return routeResult{kind: routeConfig, configMode: "print-defaults"}
	case "--show":
		return routeResult{kind: routeConfig, configMode: "show", json: asJSON}
	}
	return routeResult{kind: routeUsageErr, msg: "config: unknown flag " + pos[0] + " (want --print-defaults or --show)"}
}

// parsePauseArgs validates `pause [<gate>]` (Task 1.2b, INV-LIFE-2). Pure: no
// I/O, no config load — the gate identity (quota-paused, cicd-down) is a fixed
// CLI-level fact, not something config resolves, so validating it here costs
// nothing config-dependent. An omitted gate defaults to quota-paused
// (interfaces.md's "Operator pause/resume"); an unknown gate name or a
// dash-prefixed token is a usage error, matching every other subcommand's
// fail-fast-on-bad-input contract (pg2-52rn).
func parsePauseArgs(args []string) routeResult {
	var gate string
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "-"):
			return routeResult{kind: routeUsageErr, msg: "unknown flag: " + a}
		case gate != "":
			return routeResult{kind: routeUsageErr, msg: "pause: unexpected argument: " + a}
		default:
			gate = a
		}
	}
	if gate == "" {
		gate = gateQuotaPaused
	} else if !validGate(gate) {
		return routeResult{kind: routeUsageErr, msg: fmt.Sprintf("pause: unknown gate %q (want %s or %s)", gate, gateQuotaPaused, gateCICDDown)}
	}
	return routeResult{kind: routePause, gate: gate}
}

// parseResumeArgs validates `resume [<gate>] | --all` (Task 1.2b, INV-LIFE-2).
// Pure, same fail-fast contract as parsePauseArgs. "resume --all <gate>" (both
// at once) is a usage error — interfaces.md draws no meaning for that
// combination, and silently picking one would surprise an operator who typed
// the other.
func parseResumeArgs(args []string) routeResult {
	var gate string
	allGates := false
	for _, a := range args {
		switch {
		case a == "--all":
			allGates = true
		case strings.HasPrefix(a, "-"):
			return routeResult{kind: routeUsageErr, msg: "unknown flag: " + a}
		case gate != "":
			return routeResult{kind: routeUsageErr, msg: "resume: unexpected argument: " + a}
		default:
			gate = a
		}
	}
	if allGates && gate != "" {
		return routeResult{kind: routeUsageErr, msg: "resume: --all takes no gate argument"}
	}
	if !allGates {
		if gate == "" {
			gate = gateQuotaPaused
		} else if !validGate(gate) {
			return routeResult{kind: routeUsageErr, msg: fmt.Sprintf("resume: unknown gate %q (want %s or %s)", gate, gateQuotaPaused, gateCICDDown)}
		}
	}
	return routeResult{kind: routeResume, gate: gate, allGates: allGates}
}

// extractJSONFlag pulls a --json flag out of args, wherever it occurs, returning
// whether it was present and the remaining args in their original relative
// order. run-query/run-role/config parse their own positionals by hand rather
// than through flag.FlagSet (run-query/run-role's first positional is a role
// NAME that must never be mistaken for a flag, and config's "modes" are
// themselves spelled as flag-shaped tokens) — this lets --json appear anywhere
// in the invocation (before, after, or between the existing positionals) without
// disturbing that hand-rolled parsing, matching parseInterspersed's
// anywhere-in-the-invocation flag placement for run/run-until-idle.
func extractJSONFlag(args []string) (asJSON bool, rest []string) {
	rest = make([]string, 0, len(args))
	for _, a := range args {
		if a == "--json" {
			asJSON = true
			continue
		}
		rest = append(rest, a)
	}
	return asJSON, rest
}

// firstFlag returns the first dash-prefixed token in args (the offending flag on
// a drain parse error), or "flag" if none is found.
func firstFlag(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			return a
		}
	}
	return "flag"
}

// parseInterspersed parses a FlagSet allowing flags to appear before, after, or
// between positional arguments. Go's stdlib flag stops at the first positional,
// silently dropping any flags after it. This walks the args, collecting
// positionals and re-parsing the remainder. It returns the positionals and the
// first parse error (including flag.ErrHelp); callers MUST inspect the error —
// swallowing it is what let -h/--help and unknown flags trigger a drain.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return positionals, err
		}
		if fs.NArg() == 0 {
			return positionals, nil
		}
		positionals = append(positionals, fs.Arg(0))
		args = fs.Args()[1:]
	}
}
