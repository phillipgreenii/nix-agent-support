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

// runRunQuery runs one role's discovery query read-only and prints the matches
// (id, type, title) straight from the resolved items. asJSON (Task 1.5b) picks
// renderRunQueryJSON over the default renderRunQueryText; every error path is
// unaffected by asJSON (same reasoning as runRunRole's doc comment: a query
// failure happens before any matches exist to report).
func runRunQuery(roleName string, asJSON bool) int {
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
		printUsageErr(fmt.Sprintf("run-query: unknown role %q (configured: %s)", roleName, roleNames(cfg.Roles)))
		return exitUsage
	}
	// A role no longer embeds a query; it binds event types. Resolve the
	// producers that FEED this role (emit any of its bound types) and run each,
	// collecting the items the emitted events carry — the same read-only smoke
	// view as before, now through the event model.
	env := query.Env{BD: br, RepoRoot: cfg.RepoRoot, Cmd: query.OSCommander{}}
	sources := discover.QueriesForRole(cfg.Queries, role)
	matches := make([]runQueryMatch, 0)
	for _, s := range sources {
		evts, err := s.Query.Run(ctx, env)
		if err != nil {
			fmt.Fprintln(os.Stderr, "run-query:", err)
			return exitGeneric
		}
		for _, e := range evts {
			matches = append(matches, runQueryMatch{ID: e.Item.ID, Type: e.Item.Type, Title: e.Item.Title})
		}
	}
	if asJSON {
		renderRunQueryJSON(os.Stdout, role.Name, len(sources), matches)
	} else {
		renderRunQueryText(os.Stdout, role.Name, len(sources), matches)
	}
	return exitOK
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
// as configShowReport/runRoleReport.
type runQueryReport struct {
	Role    string          `json:"role"`
	Queries int             `json:"queries"`
	Total   int             `json:"total"`
	Matches []runQueryMatch `json:"matches"`
}

// renderRunQueryText writes run-query's default text form: one tab-separated
// line per match, then the "# N role dispatch(es) from M quer(ies)" summary —
// byte-for-byte the same output the pre-Task-1.5b implementation produced.
func renderRunQueryText(w io.Writer, role string, queryCount int, matches []runQueryMatch) {
	for _, m := range matches {
		fmt.Fprintf(w, "%s\t%s\t%s\n", m.ID, m.Type, m.Title)
	}
	fmt.Fprintf(w, "# %d %s dispatch(es) from %d quer(ies)\n", len(matches), role, queryCount)
}

// renderRunQueryJSON writes run-query's --json form: one JSON object.
func renderRunQueryJSON(w io.Writer, role string, queryCount int, matches []runQueryMatch) {
	writeJSON(w, runQueryReport{Role: role, Queries: queryCount, Total: len(matches), Matches: matches})
}
