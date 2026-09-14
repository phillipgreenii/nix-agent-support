package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/discover"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/orchestrator"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// envTestMode is PG_ROUTER_TEST_MODE (docs/decisions/cli.md's DEC-CLI-2): both
// smoke commands (run-role, run-query) set it to "1" before doing any work, so
// a participant knows a test is in flight (interfaces.md's "Test-mode
// signal" — advisory only; the core neither requires nor inspects how, or
// whether, a participant responds). Setting it in THIS process's environment
// is enough to reach every subprocess a smoke test can spawn — os/exec.Cmd
// with a nil Env inherits os.Environ(), and neither execCmd (internal/ccpool)
// nor OSCommander.Run (internal/query) sets Env explicitly — without threading
// a new parameter through the orchestrator/executor.
const envTestMode = "PG_ROUTER_TEST_MODE"

// setTestMode marks the process environment as "a test is in flight" (Task
// 1.5c). Exists as its own function so the "smoke commands set
// PG_ROUTER_TEST_MODE=1" contract is unit-testable independent of the full
// config.Load/precheck plumbing runRunRole/runRunQuery need.
func setTestMode() { _ = os.Setenv(envTestMode, "1") }

// resolveRole finds a configured role by its name. Unknown names are rejected HERE
// (in the handler, after config load) rather than at arg-parse time, so arg parsing
// stays pure — no config I/O — per the pg2-52rn "no fall-through to a real dispatch
// on bad input" guarantee. The CLI token is the role's Name (one name to learn).
func resolveRole(rs roles.RoleSet, name string) (roles.Role, bool) {
	for _, r := range rs {
		if r.Name == name {
			return r, true
		}
	}
	return roles.Role{}, false
}

// roleNames lists the configured role names for an unknown-role diagnostic.
func roleNames(rs roles.RoleSet) string {
	names := make([]string, 0, len(rs))
	for _, r := range rs {
		names = append(names, r.Name)
	}
	return strings.Join(names, ", ")
}

// runRunRole dispatches a single, caller-supplied event JSON through one
// role, then dispatches that role's preShutdown hook (pg2-oju6w.15 — RunOne
// no longer tears its own session down; ccpool session lifecycle is
// entirely the registered handler participant's own business now). It does
// NOT run discovery: the event is explicit.
//
// pg2-oju6w.15 removed the old <bead> positional: pg-router is event-generic,
// not beads-specific, and run-role no longer has a beads.Runner of its own to
// resolve a bead ID through. The caller now builds and supplies the full
// event JSON directly (the same "operator supplies raw event JSON"
// convention `push-inject <json>` already established) — an accepted,
// intentional loss of the old "just type a bead id" convenience (operator
// ruling); no convenience tool for constructing that JSON is being built.
//
// asJSON (Task 1.5b) governs only the SUCCESS report: on success it prints one
// JSON object (renderRunRoleJSON) instead of nothing (text mode's existing
// silent-success behavior, unchanged). Every error path below prints its usual
// stderr diagnostic regardless of asJSON — unlike push-inject, every failure
// here happens BEFORE any dispatch outcome exists to report (a config/precheck
// failure, an unknown role, a malformed event, a bad derived context), so
// there is no richer "accepted: false" body worth echoing beyond the
// diagnostic already on stderr; this is a deliberate, narrower choice than
// push-inject's still-JSON-on-failure convention, not an oversight.
func runRunRole(roleName, eventJSON string, asJSON bool) int {
	setTestMode()
	ctx := context.Background()
	// Schema-validate FIRST, exactly as push-inject does (internal/emit.Emit's
	// conformance.CheckBytes(pushInjectSchema, ...) before DecodeEvent) and
	// BEFORE loading config: eventqueue.DecodeEvent's own doc comment states
	// it "deliberately does NOT validate against the JSON Schema" — skipping
	// this step would let a typo'd field (e.g. "typ" instead of "type")
	// silently produce a malformed event instead of a clear rejection. Doing
	// this ahead of config.Load() means a bad event argument fails fast
	// without the I/O cost of a config load only to discard it.
	if err := conformance.CheckBytes("event", []byte(eventJSON)); err != nil {
		fmt.Fprintln(os.Stderr, "run-role:", err)
		return exitGeneric
	}
	evt, err := eventqueue.DecodeEvent([]byte(eventJSON))
	if err != nil {
		fmt.Fprintln(os.Stderr, "run-role:", err)
		return exitGeneric
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return exitPrecheck
	}
	role, ok := resolveRole(cfg.Roles, roleName)
	if !ok {
		printUsageErr(fmt.Sprintf("run-role: unknown role %q (configured: %s)", roleName, roleNames(cfg.Roles)))
		return exitUsage
	}
	// Smoke scoping (Task 1.5c, interfaces.md's "Run-scoped selectors": the
	// restriction scopes "which participants that run activates and which a
	// smoke test may reach"): a role the operator has excluded via
	// PG_ROUTER_ONLY/PG_ROUTER_DISABLE stays unreachable even when named directly.
	if err := checkSmokeReachable(selectorKindRole, role.Name, resolveSelectors(nil, nil)); err != nil {
		printUsageErr("run-role: " + err.Error())
		return exitUsage
	}
	// Validate the DERIVED context (design Q-meta: run-role takes an event, the
	// context is derived at dispatch) so a half-filled dispatch fails fast —
	// discover.DeriveContextFromQueueEvent is the SAME derivation the
	// queue-driven roleListener.Offer path already uses (Section 4).
	d := discover.DeriveContextFromQueueEvent(role, evt)
	if err := d.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "run-role:", err)
		return exitUsage
	}
	o := &orchestrator.Orchestrator{
		Reg: cfg.Roles,
		Cfg: cfg,
	}
	if err := o.RunOne(ctx, role, evt); err != nil {
		fmt.Fprintln(os.Stderr, "run-role:", err)
		return exitGeneric
	}
	// RunOne no longer closes the session it launched (Section 4's own
	// disclosed behavior change): mirror run.go's new preShutdown bracket at
	// this single-dispatch scope, so the one session run-role just made is
	// torn down (or preserved, if needs_input) the same way the daemon's own
	// per-role sweep does at its shutdown. A nil o.Handler (Task 5.4's own
	// CommandFor/bootCore wiring gap, out of scope here) is guarded the same
	// way postStartupAll/preShutdownAll guard it in run.go.
	if o.Handler != nil {
		if _, err := o.Handler.PreShutdown(ctx, role); err != nil {
			slog.Warn("run-role: preShutdown failed", "role", role.Name, "err", err)
		}
	}
	if asJSON {
		renderRunRoleJSON(os.Stdout, role.Name, d.Item.ID)
	}
	return exitOK
}

// runRoleReport is `run-role --json`'s success report: bare identity, echoing
// back which role this smoke test dispatched and the derived item id (the
// one field of the caller-supplied event this package's own downstream
// bookkeeping reads, discover.DeriveContextFromQueueEvent's own doc). RunOne
// returns no richer per-dispatch result to this caller, so there is nothing
// beyond identity+outcome worth reporting here (unlike push-inject's
// queue-durable enqueue, which has a socket/event/core worth echoing). Per
// Task 0.4's wire decision (docs/decisions/cli.md's DEC-CLI-1 "--json's
// versioning" note): UNVERSIONED, no schemaVersion field, not a
// schemas/-registered wire shape.
type runRoleReport struct {
	Role     string `json:"role"`
	Item     string `json:"item"`
	Accepted bool   `json:"accepted"`
}

// renderRunRoleJSON writes run-role's --json success report.
func renderRunRoleJSON(w io.Writer, role, item string) {
	writeJSON(w, runRoleReport{Role: role, Item: item, Accepted: true})
}

// runRunQuery is `run-query`'s entry point: it smokes exactly ONE named
// source (queryArg, the "query:<name>" form — args.go's parseRunQueryArgs
// strips the "query:" prefix and rejects anything else as a usage error, so
// queryArg is always non-empty here).
func runRunQuery(queryArg string, asJSON bool) int {
	setTestMode()
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return exitPrecheck
	}
	return runRunQuerySource(ctx, cfg, queryArg, asJSON)
}

// runRunQuerySource smokes exactly ONE named query source, read-only: the
// Task 1.5c "query:<name>" form. Unlike the retired role-fan-out form, there
// is exactly one source and no role/handler involved at all.
func runRunQuerySource(ctx context.Context, cfg config.Config, name string, asJSON bool) int {
	src, ok := findSource(cfg.Queries, name)
	if !ok {
		printUsageErr(fmt.Sprintf("run-query: unknown source %q (configured: %s)", name, sourceNames(cfg.Queries)))
		return exitUsage
	}
	// Smoke scoping (Task 1.5c, interfaces.md's "Run-scoped selectors": the
	// restriction scopes "which participants that run activates and which a
	// smoke test may reach"): a source the operator has excluded via
	// PG_ROUTER_ONLY/PG_ROUTER_DISABLE stays unreachable even when named directly.
	if err := checkSmokeReachable(selectorKindQuery, src.Name, resolveSelectors(nil, nil)); err != nil {
		printUsageErr("run-query: " + err.Error())
		return exitUsage
	}
	env := query.Env{RepoRoot: cfg.RepoRoot, Cmd: query.OSCommander{}}
	evts, err := src.Query.Run(ctx, env)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run-query:", err)
		return exitGeneric
	}
	matches := make([]runQueryMatch, 0, len(evts))
	for _, e := range evts {
		matches = append(matches, runQueryMatch{ID: e.Item.ID, Type: e.Item.Type, Title: e.Item.Title})
	}
	if asJSON {
		renderRunQueryJSON(os.Stdout, src.Name, matches)
	} else {
		renderRunQueryText(os.Stdout, src.Name, matches)
	}
	return exitOK
}

// findSource resolves a configured query source by its name
// (query.Source.Name) — run-query's query:<name> counterpart to resolveRole.
func findSource(ss query.SourceSet, name string) (query.Source, bool) {
	for _, s := range ss {
		if s.Name == name {
			return s, true
		}
	}
	return query.Source{}, false
}

// sourceNames lists the configured query source names for an unknown-source
// diagnostic — run-query's counterpart to roleNames.
func sourceNames(ss query.SourceSet) string {
	names := make([]string, 0, len(ss))
	for _, s := range ss {
		names = append(names, s.Name)
	}
	return strings.Join(names, ", ")
}

// runQueryMatch is one resolved item, as both output forms report it.
type runQueryMatch struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

// runQueryReport is `run-query --json`'s report. Per Task 0.4's wire decision
// (docs/decisions/cli.md's DEC-CLI-1 "--json's versioning" note): UNVERSIONED,
// no schemaVersion field, not a schemas/-registered wire shape — same reasoning
// as configShowReport/runRoleReport. Query names the smoked source (Task
// 1.5c); there is no per-report "how many sources" count any more — run-query
// smokes exactly one, always.
type runQueryReport struct {
	Query   string          `json:"query"`
	Total   int             `json:"total"`
	Matches []runQueryMatch `json:"matches"`
}

// renderRunQueryText writes run-query's default text form: one tab-separated
// line per match, then a "# N event(s) from source Q" summary.
func renderRunQueryText(w io.Writer, source string, matches []runQueryMatch) {
	for _, m := range matches {
		fmt.Fprintf(w, "%s\t%s\t%s\n", m.ID, m.Type, m.Title)
	}
	fmt.Fprintf(w, "# %d event(s) from source %s\n", len(matches), source)
}

// renderRunQueryJSON writes run-query's --json form: one JSON object.
func renderRunQueryJSON(w io.Writer, source string, matches []runQueryMatch) {
	writeJSON(w, runQueryReport{Query: source, Total: len(matches), Matches: matches})
}
