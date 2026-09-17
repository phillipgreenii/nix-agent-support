package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// usageLine is the short synopsis printed to stderr on a usage error.
const usageLine = "usage: pg-router [--version | --help] [run [--only <selector>]... [--disable <selector>]... [--metrics-addr <host:port>] | run-until-idle [--only <selector>]... [--disable <selector>]... | run-query [--json] query:<name> | run-role [--json] <role> <json> | config (--print-defaults | --show [--json]) | push-inject [--json] [--socket <path>] [--token <tok>] <json> | pause [<gate>] | resume [<gate> | --all] | status [--json] [--socket <path>] [--token <tok>] | tui [--socket <path>] [--token <tok>] | ingest-event [--socket <path>] [--token <tok>] | self-status [--socket <path>] [--token <tok>]]"

// helpText is the full help printed to stdout for --help/help.
const helpText = usageLine + `

pg-router routes typed events from configured sources through a durable queue to
configured handler roles (INTF-SOURCE -> queue -> INTF-HANDLER). "run" boots the
core as a long-running daemon; "run-until-idle" boots it, fires one discovery
pass, and drains the queue to idle before exiting. Bare "pg-router" (no subcommand)
prints usage and exits non-zero — an explicit subcommand is REQUIRED.

Subcommands:
  run [--metrics-addr <host:port>]
                          boot the core and run indefinitely, producing + dispatching on a
                          fixed poll interval, until SIGINT/SIGTERM requests shutdown.
                          --metrics-addr opts into an OTel Prometheus /metrics HTTP endpoint
                          serving the metric catalog (internal/metrics); omitted (the
                          default), no listener is opened. run-until-idle defines no such
                          flag — a drain-and-exit pass has nothing long-lived to scrape.
  run-until-idle          boot the core, discover once, drain the queue to idle, then exit
  run-query [--json] query:<name>
                          smoke-test one named source's query once, read-only, and print the
                          matches it would emit (sets PG_ROUTER_TEST_MODE=1, below); --json emits
                          one JSON object ({query, total, matches}) instead of the tab-separated
                          lines. Respects --only/--disable (below) even though it takes
                          no --only/--disable flags of its own: a source excluded by
                          PG_ROUTER_ONLY/PG_ROUTER_DISABLE stays unreachable by this command too.
  run-role [--json] <role> <json>
                          dispatch one caller-supplied event through a role, then tear down
                          (smoke test); sets PG_ROUTER_TEST_MODE=1 (below); --json emits a small
                          JSON report ({role, item, accepted}) on success instead of nothing.
                          Respects --only/--disable the same way run-query does: an excluded role
                          stays unreachable. <json> is the FULL event JSON (schemaVersion/id/
                          type/payload, the same shape push-inject <json> takes) — quote it so
                          the shell keeps it as one word. There is no bead-id shorthand any more:
                          pg-router is event-generic, not beads-specific, and no longer resolves a
                          bead through a beads.Runner of its own — the caller must build and
                          supply the full event JSON directly.
  run/run-until-idle --only <selector> / --disable <selector>
                          run-scoped selectors (STORY-OP-3): restrict which configured
                          sources/handlers this ONE run activates, without editing
                          config.toml. Repeatable; a selector is role:<name> or
                          query:<name>. --only (allow-list), if given, narrows to just the
                          named participants; --disable (deny-list) then excludes any named
                          participant from what's left. Env equivalents PG_ROUTER_ONLY /
                          PG_ROUTER_DISABLE (comma-separated) are UNIONED with the flags, not
                          overridden by them. A selector naming an unconfigured role/query
                          is a usage error. run-role/run-query also respect these (above),
                          reading the environment form only (they take no --only/--disable
                          flags of their own — each already names its ONE target directly).
                          PG_ROUTER_ONLY/PG_ROUTER_DISABLE are PER-INVOCATION selectors and MUST NOT
                          be exported persistently (e.g. in a shell profile): set that way, they
                          silently narrow or exclude participants on every subsequent run and
                          smoke test, not just the one invocation they were meant for. See
                          docs/decisions/cli.md's DEC-CLI-1.
  config --print-defaults print the built-in default config.toml (copy-paste starting point)
  config --show [--json]  print the resolved config path, role set, and worker dispatch scalars
                          (permission-mode / allowed-tools / autonomous / budget); --json emits the
                          same information as one JSON object. --json is valid only with --show.
  push-inject <json>      inject one operator-supplied event into the RUNNING core (the same core-side
                          enqueue as the ingest-event callback, operator-initiated). Text by default,
                          JSON with --json. Locates the core via --socket/--token, else
                          PG_ROUTER_SOCKET/PG_ROUTER_TOKEN, else discovery under the log dir. It NEVER
                          starts a core: with none running it fails with "no running core" (exit 1).
  status                  inspect the RUNNING core: resolved configuration, live deliveries, and
                          per-type queue depths, plus gates/mode/listeners/sources/unmatched
                          bindings/recent activity. Text by default, JSON with --json. Locates the
                          core via --socket/--token, else PG_ROUTER_SOCKET/PG_ROUTER_TOKEN, else
                          discovery under the log dir. It NEVER starts a core: with none running it
                          fails with "no running core" (exit 1).
  tui                     continuous-interactive view: polls status's activity ring and offers
                          pause/resume from the same screen (never a third affordance). No --json
                          (it is a terminal UI, not a scriptable reply). Locates the core via
                          --socket/--token, else PG_ROUTER_SOCKET/PG_ROUTER_TOKEN, else discovery under
                          the log dir, on the interval PG_ROUTER_TUI_INTERVAL sets (below). Unlike
                          every other operator subcommand, it NEVER fails on "no running core": it
                          renders a no-core screen and keeps polling instead (ADR 0036).
  pause [<gate>]          set gate <gate> (default operator-paused) directly on its file-backed state
                          (INV-LIFE-2): exits 0 even with NO core running, reporting that the change
                          takes effect at the next start (a currently running "run" picks it up on
                          its next tick). FILE-DIRECT: unlike every operator subcommand above,
                          pause/resume NEVER Discover or Dial a core — this deliberately breaks the
                          verb-named-subcommand-is-a-socket-client symmetry that push-inject/
                          ingest-event/self-status follow. A socket-level pause/resume verb also
                          exists (Phase 3) for a client already holding a connection.
  resume [<gate>] | --all clear gate <gate> (default operator-paused), or every outstanding gate at once
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
                          --socket/--token, else PG_ROUTER_SOCKET/PG_ROUTER_TOKEN, else discovery under
                          the log dir. It NEVER starts a core: with none running it fails with
                          "no running core" (exit 1).
  self-status             push the caller's OWN status (healthy/degraded/unavailable) to the
                          RUNNING core, naming the participantId it registered under. Request JSON
                          on stdin, reply JSON on stdout; exit 0 ok / 1 error / 2 usage / 9 busy.
                          Every registered participant kind gets this callback, unlike ingest-event
                          (a source's alone). Locates the core the same way ingest-event does, and
                          never starts one.

Roles are configured in <RepoRoot>/.pg-router/config.toml (override the path with
PG_ROUTER_CONFIG). Resolved via 'git rev-parse --git-common-dir' from RepoRoot, so a
linked worktree reads through to the CANONICAL clone's config.toml rather than
falling back to built-ins just because it has no .pg-router/ of its own. With no
config file at that resolved location, pg-router logs a WARN (see
PG_ROUTER_NO_CONFIG_WARN below) and uses the built-in feedback + worker roles.
<role> is the role's configured name. Run "pg-router config --print-defaults" to see
the schema and defaults.

Pool-wide settings come from PG_ROUTER_* environment variables:
  PG_ROUTER_REPO_ROOT        monorepo root the drain operates in (default: cwd)
  PG_ROUTER_BEADS_PREFIX     expected bead prefix, asserted at precheck (default zr)
  PG_ROUTER_BUDGET_TOKENS    per-worker token budget; 0 = unlimited (default 0)
  PG_ROUTER_BUDGET_COST      per-worker cost budget in cents; 0 = unlimited (default 0)
  PG_ROUTER_BUDGET_TIME      per-worker wall-clock budget in seconds (default 1500)
  PG_ROUTER_MODEL            claude model override (default: ccpool's default)
  PG_ROUTER_EFFORT           claude --effort value (default max)
  PG_ROUTER_PERMISSION_MODE  claude --permission-mode for workers (default dontAsk; bypassPermissions is the opt-in escape)
  PG_ROUTER_ALLOWED_TOOLS    claude --allowed-tools allowlist for workers (default: conservative deny-by-default set; empty clears the flag)
  PG_ROUTER_CONFIG           explicit config.toml path (default <RepoRoot>/.pg-router/config.toml,
                           resolved via --git-common-dir read-through — see above)
  PG_ROUTER_NO_CONFIG_WARN   set to suppress the WARN Load() logs when no config.toml is found at
                           the resolved location (falls back to INFO); the explicit opt-out for
                           a deployment that intentionally runs on built-in roles (default false)
  PG_ROUTER_ACTIVITY_RING    dispatch-outcome activity ring buffer capacity (default 512)
  PG_ROUTER_METRICS_ADDR     listen address (host:port) for run's OTel Prometheus /metrics
                           endpoint (default disabled); run's --metrics-addr flag > this env
                           var > disabled, the same precedence PG_ROUTER_TUI_INTERVAL uses
  PG_ROUTER_TUI_INTERVAL     tui's poll interval; floor-clamped to 250ms (default 1s). CLI flag >
                           PG_ROUTER_TUI_INTERVAL env > built-in default; a value that fails to
                           parse as a duration is a usage error naming the bad value.
  PG_ROUTER_LOG_DIR          event-log/state directory: gates/, events.jsonl, the discovery record
                           (default: the XDG state dir, e.g. ~/.local/state/pg-router)
  PG_ROUTER_OPERATOR_PAUSED  operator-paused gate file path override (default <PG_ROUTER_LOG_DIR>/gates/operator-paused)
  PG_ROUTER_CICD_DOWN        cicd-down gate file path override (default <PG_ROUTER_LOG_DIR>/gates/cicd-down)
  PG_ROUTER_ONLY             comma-separated run-scoped allow-list, each entry role:<name> or
                           query:<name> (DEC-CLI-1); unioned with any --only flags on
                           run/run-until-idle; run-role/run-query respect it too (no flags of
                           their own — PER-INVOCATION, see the warning above; do not export)
  PG_ROUTER_DISABLE          comma-separated run-scoped deny-list, same grammar and the same
                           run-role/run-query reach as PG_ROUTER_ONLY; unioned with any
                           --disable flags on run/run-until-idle
  PG_ROUTER_TEST_MODE        set to 1 by run-role/run-query for the duration of that one smoke
                           test, so a participant it dispatches (or a command-backed source it
                           shells out to) knows a test is in flight; advisory only — a
                           participant MAY use it to alter its own side-effectful behavior, but
                           the core neither requires nor inspects how, or whether, it responds.
                           Not meant to be set by an operator directly.

Precedence for every scalar above that a [pool] key can also set (including the two gate
paths): [pool] wins over PG_ROUTER_* env, which wins over the built-in default — matching
internal/config's package doc and 'config --print-defaults's header. The XDG-global config
($XDG_CONFIG_HOME/pg-router/config.toml, else ~/.config/pg-router/config.toml) contributes
[pool].budget only, beneath the repo-local file and above env; it sets nothing else.

REMOVED: PG_ROUTER_MAX_WORKER, PG_ROUTER_MAX_FEEDBACK, PG_ROUTER_FEEDBACK_ENABLED,
PG_ROUTER_WORKER_ENABLED, PG_ROUTER_SKILL_MD, PG_ROUTER_WORKER_SKILL_MD. Set
role.enabled / the role's prompt in config.toml instead; the former per-role
MAX_WORKER/MAX_FEEDBACK cap is not a config knob at all any more (INV-CONC-1)
— a handler expresses its own capacity limit as a pre-accept busy decline.`

// routeKind enumerates what the program should do after parsing argv. Keeping
// the decision pure (no I/O, no side effects) is what guarantees a help/version
// request or a parse error can never fall through to a real drain — a drain
// dispatches sessions and tears down every pg-router-* tmux session, so fail-open on
// a parse error is a real foot-gun (pg2-52rn).
type routeKind int

const (
	routeVersion      routeKind = iota // print the version and exit 0
	routeHelp                          // print usage and exit 0
	routeUsageErr                      // print .msg + usage to stderr and exit 2
	routeRun                           // boot the core as a long-running daemon (INV-LIFE-1)
	routeRunUntilIdle                  // boot the core, discover once, drain to idle, exit (INV-LIFE-1)
	routeRunRole                       // dispatch one caller-supplied event through a role (.role, .eventJSON)
	routeRunQuery                      // smoke one named query source read-only (.query)
	routeConfig                        // print/show config (.configMode)
	routeIngestEvent                   // manager->core callback: forward events on stdin to the running core (.rest)
	routePushInject                    // operator: inject one event into the running core (.rest)
	routeStatus                        // operator: inspect the running core (Task 3.8, .rest)
	routeTUI                           // operator: continuous-interactive view over status/pause/resume (Task 4.2, .rest); never fails on "no running core" (ADR 0036)
	routeSelfStatus                    // manager->core callback: push the caller's own self-status to the running core (.rest)
	routePause                         // file-direct: set gate .gate directly on its file-backed state (INV-LIFE-2); never Discover/Dial
	routeResume                        // file-direct: clear gate .gate, or every gate with .allGates; never Discover/Dial
)

type routeResult struct {
	kind  routeKind
	rest  []string // drain subcommand args (routeDrain only)
	msg   string   // diagnostic for routeUsageErr
	role  string   // run-role's role name
	query string   // run-query's "query:<name>" source name (Task 1.5c)
	// eventJSON is run-role's <json> positional (pg2-oju6w.15): the caller's
	// raw event JSON blob, passed through unparsed — route() stays pure (no
	// I/O, no schema check), so validation/decode happens in the handler
	// (runRunRole), same division of labor push-inject already uses for its
	// own <json> positional. Replaces the retired <bead> positional: pg-router
	// is event-generic, not beads-specific, and no longer has a beads.Runner
	// of its own to resolve a bead id through.
	eventJSON  string
	configMode string // "print-defaults" | "show" (routeConfig only)
	// gate / allGates are routePause/routeResume's TYPED fields (Task 1.2b): the
	// gate name (already validated against the two known gates, defaulted to
	// operator-paused when omitted) and, for routeResume only, whether --all was
	// given. Parsed here in route()'s helpers, never re-parsed from .rest.
	gate     string
	allGates bool
	// only / disable carry the raw --only/--disable flag OCCURRENCES for
	// routeRun/routeRunUntilIdle (STORY-OP-3, DEC-CLI-1) — NOT yet combined
	// with PG_ROUTER_ONLY/PG_ROUTER_DISABLE, since reading the environment is I/O
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
	// metricsAddr is routeRun's own --metrics-addr occurrence (an OTel
	// Prometheus /metrics direct-scrape endpoint): empty when omitted, the
	// default of "disabled" every other opt-in listener in this codebase
	// uses. Unlike only/disable it is NOT shared with routeRunUntilIdle:
	// --metrics-addr is daemon-mode-only (a drain-and-exit pass has nothing
	// long-lived to scrape), so parseRunLikeArgs registers the flag only
	// when kind == routeRun — passing it to run-until-idle is therefore an
	// unknown-flag usage error, not a silently-ignored one.
	metricsAddr string
}

// route inspects the full argv and decides what to do, without side effects. No
// subcommand ⇒ drain. Top-level --version/-v and --help/-h are recognised here
// so they cannot be misrouted to a drain; any other leading flag, or any unknown
// subcommand, is a usage error rather than a silent fall-through to drain.
//
// Per CLI convention (GNU Coding Standards §4.7; clap/cobra/click), a leading
// --version/-v or --help/-h SHORT-CIRCUITS: it is honoured and exits 0,
// deliberately ignoring any trailing arguments (so `pg-router --version drain`
// prints the version, not a usage error). A subcommand-scoped flag like
// `pg-router drain --version` is NOT a global request — drain defines no such
// flag, so it is reported as an unknown flag (exit 2), matching cobra/docker.
func route(argv []string) routeResult {
	args := argv[1:] // strip program name
	if len(args) == 0 {
		// A subcommand is REQUIRED (bead pg2-f3mcb.2 — a deliberate compatibility
		// break): bare "pg-router" used to default to a drain pass (routeDrain);
		// it now prints usage and exits non-zero like any other missing-arg
		// usage error, rather than silently dispatching sessions.
		return routeResult{kind: routeUsageErr, msg: "pg-router: a subcommand is required"}
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
	case "run-role":
		return parseRunRoleArgs(args[1:])
	case "run-query":
		return parseRunQueryArgs(args[1:])
	case "config":
		return parseConfigArgs(args[1:])
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
	case "tui":
		// Task 4.2: its own --socket/--token flags (no --json — it is a
		// terminal UI), parsed in its own handler. Unlike every other
		// operator subcommand it never fails on "no running core" (ADR
		// 0036); that divergence lives in runTUI, not in routing.
		return routeResult{kind: routeTUI, rest: args[1:]}
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
// selector flags (--only/--disable, STORY-OP-3) and no positionals — run and
// run-until-idle. It stays pure aside from collecting those flag occurrences
// (no environment read, no config I/O): it reports a routeKind and never
// boots a core itself, so the
// caller can refuse to touch config/precheck/the queue on a parse error or
// help request (pg2-52rn's "no fall-through to a real dispatch on bad input"
// guarantee, carried over from the retired parseDrainArgs). A help flag
// yields routeHelp; any other parse failure (or an unexpected positional)
// yields routeUsageErr.
func parseRunLikeArgs(kind routeKind, args []string) routeResult {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render usage/errors ourselves; suppress flag's defaults
	only, disable := registerSelectorFlags(fs)
	// --metrics-addr is registered ONLY for routeRun (see routeResult.metricsAddr's
	// own doc comment): run-until-idle defines no such flag, so passing it
	// there falls through to the same "unknown flag" usage error every other
	// unrecognized dash-prefixed token gets below.
	var metricsAddr string
	if kind == routeRun {
		fs.StringVar(&metricsAddr, "metrics-addr", "", "listen address (host:port) for the OTel Prometheus /metrics endpoint; disabled by default")
	}
	pos, err := parseInterspersed(fs, args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return routeResult{kind: routeHelp}
	case err != nil:
		// The only flags this subcommand defines are --only/--disable (plus
		// --metrics-addr for routeRun), so an unrecognized dash-prefixed token
		// is still the offender. Report it in the same "unknown flag: X"
		// phrasing as the top-level route (the stdlib's "flag provided but not
		// defined: -x" single-dashes the flag and reads differently).
		return routeResult{kind: routeUsageErr, msg: "unknown flag: " + firstFlag(args)}
	case len(pos) > 0:
		return routeResult{kind: routeUsageErr, msg: "unexpected argument: " + pos[0]}
	}
	return routeResult{kind: kind, only: only.values, disable: disable.values, metricsAddr: metricsAddr}
}

// parseRunRoleArgs validates `run-role [--json] <role> <json>`. Pure: it
// checks only that a role TOKEN and an event JSON blob are present (and no
// extra args), after pulling out an optional --json occurring anywhere in
// args (extractJSONFlag). Neither the role NAME nor the event JSON's own
// shape is validated here — the role needs the loaded config and the event
// needs a schema check, so both move to the handler (runRunRole), matching
// push-inject's own division of labor for its <json> positional. A
// dash-prefixed first positional is a missing role (a flag, not a name).
// (pg2-52rn)
//
// pg2-oju6w.15 removed the old <bead> positional: pg-router is
// event-generic, not beads-specific, and run-role no longer resolves a bead
// ID through a beads.Runner it no longer has. The caller now supplies the
// full event JSON directly (an accepted, intentional loss of the old "just
// type a bead id" convenience — no convenience tool for constructing that
// JSON is being built).
func parseRunRoleArgs(args []string) routeResult {
	asJSON, pos := extractJSONFlag(args)
	if len(pos) < 1 || pos[0] == "" || strings.HasPrefix(pos[0], "-") {
		return routeResult{kind: routeUsageErr, msg: "run-role: missing role (usage: run-role [--json] <role> <json>)"}
	}
	if len(pos) < 2 || pos[1] == "" {
		return routeResult{kind: routeUsageErr, msg: "run-role: missing event JSON (usage: run-role [--json] <role> <json>)"}
	}
	if len(pos) > 2 {
		return routeResult{kind: routeUsageErr, msg: "run-role: unexpected argument: " + pos[2] +
			"\nrun-role takes ONE event JSON argument; quote it so the shell keeps it as one word"}
	}
	return routeResult{kind: routeRunRole, role: pos[0], eventJSON: pos[1], json: asJSON}
}

// parseRunQueryArgs validates `run-query [--json] query:<name>`. Pure, same
// fail-fast contract as the other operator subcommands (extractJSONFlag pulls
// --json out first); a leading "query:" is stripped here (pure string
// parsing, no I/O) into .query, naming exactly one configured source. Any
// token with no such prefix — including the pre-Task-1.5c bare-role
// form — is an ordinary usage error; there is no live consumer of that form
// to carry a mapping diagnostic for (operator ruling, 2026-09-02).
func parseRunQueryArgs(args []string) routeResult {
	asJSON, pos := extractJSONFlag(args)
	if len(pos) < 1 || pos[0] == "" || strings.HasPrefix(pos[0], "-") {
		return routeResult{kind: routeUsageErr, msg: "run-query: missing query (usage: run-query [--json] query:<name>)"}
	}
	if len(pos) > 1 {
		return routeResult{kind: routeUsageErr, msg: "run-query: unexpected argument: " + pos[1]}
	}
	name, ok := strings.CutPrefix(pos[0], "query:")
	if !ok || name == "" {
		return routeResult{kind: routeUsageErr, msg: "run-query: not a query (usage: run-query [--json] query:<name>): " + pos[0]}
	}
	return routeResult{kind: routeRunQuery, query: name, json: asJSON}
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
// I/O, no config load — the gate identity (operator-paused, cicd-down) is a fixed
// CLI-level fact, not something config resolves, so validating it here costs
// nothing config-dependent. An omitted gate defaults to operator-paused
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
		gate = gateOperatorPaused
	} else if !validGate(gate) {
		return routeResult{kind: routeUsageErr, msg: fmt.Sprintf("pause: unknown gate %q (want %s or %s)", gate, gateOperatorPaused, gateCICDDown)}
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
			gate = gateOperatorPaused
		} else if !validGate(gate) {
			return routeResult{kind: routeUsageErr, msg: fmt.Sprintf("resume: unknown gate %q (want %s or %s)", gate, gateOperatorPaused, gateCICDDown)}
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
