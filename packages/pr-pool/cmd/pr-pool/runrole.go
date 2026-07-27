package main

import (
	"context"
	"fmt"
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

// runRunRole dispatches a single item through one role and tears down its session.
// It does NOT run discovery: the bead is explicit. precheck validates the store/prefix.
func runRunRole(roleName, beadID string) int {
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
	return exitOK
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
// (id, type, title) straight from the resolved items.
func runRunQuery(roleName string) int {
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
	// printing the items the emitted events carry — the same read-only smoke view
	// as before, now through the event model.
	env := query.Env{BD: br, RepoRoot: cfg.RepoRoot, Cmd: query.OSCommander{}}
	sources := discover.QueriesForRole(cfg.Queries, role)
	total := 0
	for _, s := range sources {
		evts, err := s.Query.Run(ctx, env)
		if err != nil {
			fmt.Fprintln(os.Stderr, "run-query:", err)
			return exitGeneric
		}
		for _, e := range evts {
			fmt.Printf("%s\t%s\t%s\n", e.Item.ID, e.Item.Type, e.Item.Title)
		}
		total += len(evts)
	}
	fmt.Printf("# %d %s dispatch(es) from %d quer(ies)\n", total, role.Name, len(sources))
	return exitOK
}
