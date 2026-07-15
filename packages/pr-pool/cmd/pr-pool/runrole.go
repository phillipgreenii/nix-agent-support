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
	dctx, err := buildRunRoleDispatch(ctx, br, role, beadID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run-role:", err)
		return exitGeneric
	}
	if err := dctx.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "run-role:", err)
		return exitUsage
	}
	o := &orchestrator.Orchestrator{
		CC:  ccpool.NewCLIRunner(cfg),
		BD:  br,
		Reg: cfg.Roles,
		Cfg: cfg,
	}
	if err := o.RunOne(ctx, dctx); err != nil {
		fmt.Fprintln(os.Stderr, "run-role:", err)
		return exitGeneric
	}
	return exitOK
}

// buildRunRoleDispatch builds the (role, item) dispatch for the direct-bead run-role
// path. It loads the bead via `bd show` and maps its metadata into the dispatched
// Item through the same query.FromIssue adapter the query/drain path uses (pg2-jpci),
// so the review prompt template renders the real pr_number/repo/head_sha instead of
// <no value>.
func buildRunRoleDispatch(ctx context.Context, br beads.Runner, role roles.Role, beadID string) (discover.DispatchContext, error) {
	iss, err := beads.ShowObj(ctx, br, beadID)
	if err != nil {
		return discover.DispatchContext{}, fmt.Errorf("load bead %s: %w", beadID, err)
	}
	return discover.DispatchContext{Role: role, Item: query.FromIssue(iss)}, nil
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
	env := query.Env{BD: br, RepoRoot: cfg.RepoRoot, Cmd: query.OSCommander{}}
	dispatches, err := discover.ForRole(ctx, env, role)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run-query:", err)
		return exitGeneric
	}
	for _, d := range dispatches {
		fmt.Printf("%s\t%s\t%s\n", d.Item.ID, d.Item.Type, d.Item.Title)
	}
	fmt.Printf("# %d %s dispatch(es)\n", len(dispatches), role.Name)
	return exitOK
}
