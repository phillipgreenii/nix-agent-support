package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/event"
	"github.com/phillipgreenii/pr-pool/internal/orchestrator"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// envTestMode is PR_POOL_TEST_MODE (docs/decisions/cli.md's DEC-CLI-2): both
// smoke commands (run-role, run-query) set it to "1" before doing any work, so
// a participant knows a test is in flight (interfaces.md's "Test-mode
// signal" — advisory only; the core neither requires nor inspects how, or
// whether, a participant responds). Setting it in THIS process's environment
// is enough to reach every subprocess a smoke test can spawn — os/exec.Cmd
// with a nil Env inherits os.Environ(), and neither execCmd (internal/ccpool)
// nor OSCommander.Run (internal/query) sets Env explicitly — without threading
// a new parameter through the orchestrator/executor.
const envTestMode = "PR_POOL_TEST_MODE"

// setTestMode marks the process environment as "a test is in flight" (Task
// 1.5c). Exists as its own function so the "smoke commands set
// PR_POOL_TEST_MODE=1" contract is unit-testable independent of the full
// config.Load/precheck plumbing runRunRole/runRunQuery need.
func setTestMode() { os.Setenv(envTestMode, "1") }

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

// runRunRole dispatches a single item through one role and tears down its
// session. It does NOT run discovery: the bead is explicit. precheck validates
// the store/prefix.
//
// asJSON (Task 1.5b) governs only the SUCCESS report: on success it prints one
// JSON object (renderRunRoleJSON) instead of nothing (text mode's existing
// silent-success behavior, unchanged). Every error path below prints its usual
// stderr diagnostic regardless of asJSON — unlike push-inject, every failure
// here happens BEFORE any dispatch outcome exists to report (a config/precheck
// failure, an unknown role, a bad derived context), so there is no richer
// "accepted: false" body worth echoing beyond the diagnostic already on
// stderr; this is a deliberate, narrower choice than push-inject's
// still-JSON-on-failure convention, not an oversight.
func runRunRole(roleName, beadID string, asJSON bool) int {
	setTestMode()
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return exitPrecheck
	}
	br := beads.NewCLIRunnerForRepo(cfg.RepoRoot)
	if err := precheck(ctx, cfg, br); err != nil {
		fmt.Fprintln(os.Stderr, "precheck:", err)
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
	// PR_POOL_ONLY/PR_POOL_DISABLE stays unreachable even when named directly.
	if err := checkSmokeReachable(selectorKindRole, role.Name, resolveSelectors(nil, nil)); err != nil {
		printUsageErr("run-role: " + err.Error())
		return exitUsage
	}
	ev, err := buildRunRoleEvent(ctx, br, role, beadID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run-role:", err)
		return exitGeneric
	}
	// Validate the DERIVED context (design Q-meta: run-role takes an event, the
	// context is derived at dispatch) so a half-filled dispatch fails fast.
	if err := discover.DeriveContext(role, ev).Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "run-role:", err)
		return exitUsage
	}
	o := &orchestrator.Orchestrator{
		CC:  ccpool.NewCLIRunner(cfg),
		BD:  br,
		Reg: cfg.Roles,
		Cfg: cfg,
	}
	if err := o.RunOne(ctx, role, ev); err != nil {
		fmt.Fprintln(os.Stderr, "run-role:", err)
		return exitGeneric
	}
	if asJSON {
		renderRunRoleJSON(os.Stdout, role.Name, beadID)
	}
	return exitOK
}

// runRoleReport is `run-role --json`'s success report: bare identity, echoing
// back which role/bead this smoke test dispatched. RunOne tears the session
// down itself and returns no richer per-dispatch result to this caller, so
// there is nothing beyond identity+outcome worth reporting here (unlike
// push-inject's queue-durable enqueue, which has a socket/event/core worth
// echoing). Per Task 0.4's wire decision (docs/decisions/cli.md's DEC-CLI-1
// "--json's versioning" note): UNVERSIONED, no schemaVersion field, not a
// schemas/-registered wire shape.
type runRoleReport struct {
	Role     string `json:"role"`
	Bead     string `json:"bead"`
	Accepted bool   `json:"accepted"`
}

// renderRunRoleJSON writes run-role's --json success report.
func renderRunRoleJSON(w io.Writer, role, bead string) {
	writeJSON(w, runRoleReport{Role: role, Bead: bead, Accepted: true})
}

// buildRunRoleEvent builds the self-contained event for the direct-bead run-role
// path (design Q-meta: run-role consumes an EVENT). It loads the bead via `bd
// show` and maps its metadata into the event's Item through the same
// query.FromIssue adapter the query/drain path uses (pg2-jpci), so the review
// prompt template renders the real pr_number/repo/head_sha instead of <no value>.
// The event type is the role's first bind (falling back to "run-role"); the type
// is provenance only — RunOne derives the dispatch context from the event's Item.
func buildRunRoleEvent(ctx context.Context, br beads.Runner, role roles.Role, beadID string) (event.Event, error) {
	iss, err := beads.ShowObj(ctx, br, beadID)
	if err != nil {
		return event.Event{}, fmt.Errorf("load bead %s: %w", beadID, err)
	}
	eventType := "run-role"
	if len(role.Binds) > 0 {
		eventType = role.Binds[0]
	}
	return event.NewItemEvent(eventType, "run-role", query.FromIssue(iss)), nil
}

// runRunQuery is `run-query`'s entry point. Task 1.5c splits it into two forms
// on the SAME subcommand: queryArg carries the "query:<name>" form (smoke
// exactly ONE named source, the new canonical grammar — args.go's
// parseRunQueryArgs strips the "query:" prefix), while roleArg carries a bare
// token — the deprecated pre-1.5c "run-query <role>" form, which no longer
// runs anything; it reports the mapping diagnostic instead
// (runRunQueryLegacyRole). Exactly one of the two is non-empty (args.go's
// contract).
func runRunQuery(roleArg, queryArg string, asJSON bool) int {
	setTestMode()
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return exitPrecheck
	}
	br := beads.NewCLIRunnerForRepo(cfg.RepoRoot)
	if err := precheck(ctx, cfg, br); err != nil {
		fmt.Fprintln(os.Stderr, "precheck:", err)
		return exitPrecheck
	}
	if queryArg == "" {
		return runRunQueryLegacyRole(cfg, roleArg)
	}
	return runRunQuerySource(ctx, cfg, br, queryArg, asJSON)
}

// runRunQueryLegacyRole handles the deprecated `run-query <role>` form
// (pre-Task-1.5c: run-query took a ROLE and smoked every source feeding it).
// It runs nothing — printing the mapping diagnostic naming a REAL source the
// role used to be fed by (mappingDiagnostic) is strictly more useful than
// silently reinterpreting the bare token as a (very likely wrong) source
// name. A token that is neither a configured role nor a configured source is
// reported as a plain unknown-source usage error.
func runRunQueryLegacyRole(cfg config.Config, roleArg string) int {
	role, ok := resolveRole(cfg.Roles, roleArg)
	if !ok {
		printUsageErr(fmt.Sprintf("run-query: %q is not a source (usage: run-query [--json] query:<name>; configured: %s)", roleArg, sourceNames(cfg.Queries)))
		return exitUsage
	}
	fmt.Fprintln(os.Stderr, mappingDiagnostic(cfg, role))
	return exitUsage
}

// mappingDiagnostic renders Task 1.5c's mapping diagnostic for the deprecated
// run-query <role> form. It names a source discover.QueriesForRole would have
// fed role's discovery under the pre-1.5c behavior, so the "try: query:<name>"
// hint is something the operator can literally copy and run rather than a
// fabricated example.
func mappingDiagnostic(cfg config.Config, role roles.Role) string {
	example := "<name>"
	if sources := discover.QueriesForRole(cfg.Queries, role); len(sources) > 0 {
		example = sources[0].Name
	}
	return fmt.Sprintf("'%s' is a role; run-query now names a source (try: query:%s)", role.Name, example)
}

// runRunQuerySource smokes exactly ONE named query source, read-only: the
// Task 1.5c "query:<name>" form. Unlike the retired role-fan-out form, there
// is exactly one source and no role/handler involved at all.
func runRunQuerySource(ctx context.Context, cfg config.Config, br beads.Runner, name string, asJSON bool) int {
	src, ok := findSource(cfg.Queries, name)
	if !ok {
		printUsageErr(fmt.Sprintf("run-query: unknown source %q (configured: %s)", name, sourceNames(cfg.Queries)))
		return exitUsage
	}
	// Smoke scoping (Task 1.5c, interfaces.md's "Run-scoped selectors": the
	// restriction scopes "which participants that run activates and which a
	// smoke test may reach"): a source the operator has excluded via
	// PR_POOL_ONLY/PR_POOL_DISABLE stays unreachable even when named directly.
	if err := checkSmokeReachable(selectorKindQuery, src.Name, resolveSelectors(nil, nil)); err != nil {
		printUsageErr("run-query: " + err.Error())
		return exitUsage
	}
	env := query.Env{BD: br, RepoRoot: cfg.RepoRoot, Cmd: query.OSCommander{}}
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
