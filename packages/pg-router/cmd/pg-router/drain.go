package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/x/gitclient"
)

// Exit codes. The general codes (ok / unexpected / usage) are global across these
// apps and have exactly ONE declaration — package conformance — so this file
// cannot restate the convention and drift from it again (ADR 0042's
// Consequences). Only the app-specific code is local, per "≥3 app-specific".
const (
	exitOK       = conformance.ExitOK
	exitGeneric  = conformance.ExitError
	exitUsage    = conformance.ExitUsage
	exitPrecheck = 3 // app-specific: a config or precheck failure, before any work
)

// warnDroppedRoleEnv warns if any removed role env var is set, so a stale
// deployment relying on it learns the value is now ignored (spec C decision 7:
// role identity lives in config.toml, not env).
func warnDroppedRoleEnv() {
	for _, k := range []string{
		"PG_ROUTER_MAX_WORKER", "PG_ROUTER_MAX_FEEDBACK",
		"PG_ROUTER_FEEDBACK_ENABLED", "PG_ROUTER_WORKER_ENABLED",
		"PG_ROUTER_SKILL_MD", "PG_ROUTER_WORKER_SKILL_MD",
	} {
		if _, ok := os.LookupEnv(k); ok {
			slog.Warn("ignoring removed role env var; set role.cap/role.enabled/prompt in .pg-router/config.toml", "var", k)
		}
	}
}

// warnTrackedConfig warns if <RepoRoot>/.pg-router/config.toml is tracked by git, so
// repo-local prompts are not accidentally committed (e.g. to an employer's
// monorepo). Best-effort: a git error / untracked file is silently ignored.
// Read-only, but still routed through x/gitclient's StatusReader role
// (IsTracked) rather than a bare exec.CommandContext: git's own repository
// discovery consults the ambient environment (GIT_DIR/GIT_WORK_TREE) before
// -C, and a leak there would otherwise make this check answer about the
// wrong repository (pg2-bh09g). gitclient's Client builds every child's
// environment from its own allowlist, so this call site needs no explicit
// hermetic-env helper of its own anymore (pg2-a8bhp).
func warnTrackedConfig(ctx context.Context, cfg config.Config) {
	client, err := gitclient.New(ctx, cfg.RepoRoot)
	if err != nil {
		return
	}
	tracked, err := client.IsTracked(ctx, ".pg-router/config.toml")
	if err == nil && tracked {
		slog.Warn("`.pg-router/config.toml` is tracked by git; prompts may be committed — add `.pg-router/` to .git/info/exclude", "repo", cfg.RepoRoot)
	}
}

// warnStrandedFeedback (the pg2-eo4n stranded-self-owned-feedback-cycle guard)
// and its backing internal/reconcile package are DELETED outright, not
// updated: this was the standalone/pre-flight pair to the deleted `reconcile`
// subcommand's own StrandedSelfCycles call (reconcile_cmd.go, also deleted),
// and the same "no live consumer" reasoning that authorizes the outright
// subcommand deletion applies here — reconcile's read-only guard has no
// remaining caller once the subcommand is gone (docket pg2-oju6w's Task 5.10,
// ADR 0065's "No deprecation shim for sessions/reconcile" section).

// warnStubQueries warns for any configured query whose type is a not-yet-
// implemented stub (it will error when run); surfaces it at pre-flight instead.
func warnStubQueries(cfg config.Config) {
	for _, s := range cfg.Queries {
		if s.Query != nil && query.IsStub(s.Query) {
			slog.Warn("query uses a stub type (not yet implemented; it will error when run)", "query", s.Name)
		}
	}
}

// precheck/precheckPrefix/resolveSelf/parseSelfLogin/readBeadsPrefix — the
// R14 startup pre-flight blockers (bd unreachable, beads-prefix mismatch,
// pg-pr config show self-login) — MOVED OUT of this file entirely (docket
// pg2-oju6w's Task 5.8, ADR 0065's "Source-side boundary" section, closing
// register row R14 / bead pg2-d4gvb): they were three extra checks run ahead
// of INV-WORKFLOW-1's own closed six-check set (docs/behavior/README.md's
// former register row for INV-WORKFLOW-1), and they no longer run in
// pg-router at all. They now live as this module's own startup pre-flight
// in packages/pg-router-ccpool-handler/cmd/pg-router-ccpool-handler/
// preflight.go, exercised from that module's `query` subcommand.
