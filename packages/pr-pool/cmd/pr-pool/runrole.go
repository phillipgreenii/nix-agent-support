package main

import (
	"context"
	"fmt"
	"os"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/orchestrator"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// resolveRole maps a CLI role short-token to the registry's role. The arg parser
// already rejects unknown names; this is the defensive in-process resolution. Keep
// the accepted tokens in sync with knownRoles in args.go (both enumerate the valid
// role names until the planned TOML config replaces them).
func resolveRole(reg roles.Registry, name string) (roles.Role, bool) {
	switch name {
	case "feedback":
		return reg.Feedback, true
	case "worker":
		return reg.Worker, true
	}
	return roles.Role{}, false
}

// runRunRole dispatches a single bead through one role and tears down its session.
// It does NOT run discovery and does NOT resolve self_login: the bead is explicit,
// and workOne does not consume self_login today (it will return when DispatchContext
// gains a self_login field). precheck still validates the store/prefix.
func runRunRole(roleName, beadID string) int {
	ctx := context.Background()
	cfg := config.Load()
	br := beads.NewCLIRunnerForRepo(cfg.RepoRoot)
	if err := precheck(ctx, cfg, br); err != nil {
		fmt.Fprintln(os.Stderr, "precheck:", err)
		return exitPrecheck
	}
	reg := roles.NewRegistry(cfg)
	role, ok := resolveRole(reg, roleName)
	if !ok {
		printUsageErr("run-role: unknown role: " + roleName)
		return exitUsage
	}
	dctx := discover.DispatchContext{Role: role, BeadID: beadID}
	if err := dctx.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "run-role:", err)
		return exitUsage
	}
	o := &orchestrator.Orchestrator{
		CC:  ccpool.NewCLIRunner(cfg),
		BD:  br,
		Reg: reg,
		Cfg: cfg,
	}
	if err := o.RunOne(ctx, dctx); err != nil {
		fmt.Fprintln(os.Stderr, "run-role:", err)
		return exitGeneric
	}
	return exitOK
}

// runRunQuery runs one role's discovery query read-only and prints the matches
// (id, type, title). Discovery is label-based, so it needs no self_login.
func runRunQuery(roleName string) int {
	ctx := context.Background()
	cfg := config.Load()
	br := beads.NewCLIRunnerForRepo(cfg.RepoRoot)
	if err := precheck(ctx, cfg, br); err != nil {
		fmt.Fprintln(os.Stderr, "precheck:", err)
		return exitPrecheck
	}
	reg := roles.NewRegistry(cfg)
	role, ok := resolveRole(reg, roleName)
	if !ok {
		printUsageErr("run-query: unknown role: " + roleName)
		return exitUsage
	}
	dispatches, err := discover.ForRole(ctx, br, role)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run-query:", err)
		return exitGeneric
	}
	for _, d := range dispatches {
		iss, err := beads.ShowObj(ctx, br, d.BeadID)
		if err != nil {
			fmt.Printf("%s\t(show failed: %v)\n", d.BeadID, err)
			continue
		}
		fmt.Printf("%s\t%s\t%s\n", iss.ID, iss.Type, iss.Title)
	}
	fmt.Printf("# %d %s dispatch(es)\n", len(dispatches), role.Name)
	return exitOK
}
