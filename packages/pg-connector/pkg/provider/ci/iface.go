// Package ci declares the ci capability's provider interface — a small,
// capability-scoped Go interface (never named after a backend/system)
// (INV-CAP-1) that a Tier-2 CI backend's concrete provider implements. It
// matches this repo's existing small-per-capability-interface convention
// (e.g. pr.Provider in packages/pg-connector/pkg/provider/pr) rather than
// one interface spanning multiple systems (INV-CAP-1). This interface is
// named ci.Provider — for the CI CAPABILITY itself — rather than reusing
// packages/pg-pr/pkg/provider/cicd's old system-shaped "cicd" package name,
// correcting that under INV-CAP-1's "scoped by capability, never by
// system" rule (naming_convention_test.go).
//
// This package sits alongside pkg/schema and pkg/scriptout as part of the
// module's shared surface importable across backend boundaries — see
// cmd/pg-connector's layout-convention check.
package ci

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Provider is the ci capability's provider interface. ListRuns is a
// fan-out-shaped list op: connector.ci is list-valued, matching pr/issue
// (INV-REG-1), and a CI run query is naturally "every backend that might
// have a run for this PR," not "the one backend for this id"
// (INV-EXIT-1) — so cmd/pg-connector's own "ci list" verb queries
// every registered ci backend and uses the fan-out exit scheme (0/2/3),
// unlike GetLogs/RerunFailed below, which are targeted ops (resolve to the
// one backend that owns the given id) using the targeted exit scheme
// (0/4/1) (INV-EXIT-1).
//
// The method set carries over the shape of pg-pr's existing
// packages/pg-pr/pkg/provider/cicd.Provider{ListRuns, GetLogs, RerunFailed}
// [carry-over basis, per this packet's contract], translated to this
// capability's own schema.CIRun result type.
type Provider interface {
	// ListRuns returns every CI run this backend knows about for the PR
	// identified by prID (interfaces.md's op catalog). Each returned
	// schema.CIRun MUST carry its own AsOf/Stale pair (bead pg2-4aoeg,
	// mirroring pkg/provider/pr.Provider.Show's own established contract,
	// bead pg2-681xo): AsOf is this read's own as-of time, and Stale is
	// this Provider's own as-of/stale determination — true only when this
	// read served (rather than freshly fetched) a cached copy of the
	// underlying CI run facts that has aged past this Provider's own
	// staleness bound, because the upstream CI system was degraded or
	// unreachable for this call. A Provider with no such cache always
	// returns Stale false with AsOf set to the read's own call time.
	ListRuns(ctx context.Context, prID string) ([]schema.CIRun, error)

	// GetLogs returns the raw log bytes for the CI run identified by runID,
	// scoped to repo (owner/name form) — a caller-supplied argument since
	// CISchemaVersion's 2 -> 3 bump (bead pg2-2j5ac.28.4), reversing
	// the 2026-09-06 operator ruling on pg2-f327j that had kept GetLogs
	// id-only and left every implementation to resolve repo internally (a
	// backend-local correlation store, e.g. pg-connector-ci-github-actions's
	// run_store.go). This is not an additive overload: a backend MUST NOT
	// resolve repo internally any more. The caller (the umbrella's targeted-
	// op dispatch, cmd/pg-connector/ci.go) supplies repo from a run it
	// already holds, e.g. a prior list_runs/ListRuns response's own
	// schema.CIRun.Repo.
	GetLogs(ctx context.Context, runID, repo string) ([]byte, error)

	// RerunFailed re-runs the failed portion of the CI run(s) for the PR
	// identified by prID. Whether repeat calls are idempotent-safe on the
	// same prID is left as a freedom-boundary choice — the design does not
	// decide this.
	RerunFailed(ctx context.Context, prID string) error
}
