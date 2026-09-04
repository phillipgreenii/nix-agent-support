// Package ci declares the ci capability's provider interface — a small,
// capability-scoped Go interface (never named after a backend/system)
// [design: §3] that a Tier-2 CI backend's concrete provider implements. It
// matches this repo's existing small-per-capability-interface convention
// (e.g. pr.Provider in packages/pg-connector/pkg/provider/pr) rather than
// one interface spanning multiple systems [design: §3]. This interface is
// named ci.Provider — for the CI CAPABILITY itself — rather than reusing
// packages/pg-pr/pkg/provider/cicd's old system-shaped "cicd" package name,
// correcting that under §3's "scoped by capability, never by system" rule
// [design: §3 acceptance criteria].
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
// [design: §4.1], and a CI run query is naturally "every backend that might
// have a run for this PR," not "the one backend for this id"
// [design: §4.1, §4.5] — so cmd/pg-connector's own "ci list" verb queries
// every registered ci backend and uses the fan-out exit scheme (0/2/3),
// unlike GetLogs/RerunFailed below, which are targeted ops (resolve to the
// one backend that owns the given id) using the targeted exit scheme
// (0/4/1) [design: §4.5].
//
// The method set carries over the shape of pg-pr's existing
// packages/pg-pr/pkg/provider/cicd.Provider{ListRuns, GetLogs, RerunFailed}
// [carry-over basis, per this packet's contract], translated to this
// capability's own schema.CIRun result type.
type Provider interface {
	// ListRuns returns every CI run this backend knows about for the PR
	// identified by prID [design: §2].
	ListRuns(ctx context.Context, prID string) ([]schema.CIRun, error)

	// GetLogs returns the raw log bytes for the CI run identified by runID.
	GetLogs(ctx context.Context, runID string) ([]byte, error)

	// RerunFailed re-runs the failed portion of the CI run(s) for the PR
	// identified by prID. Whether repeat calls are idempotent-safe on the
	// same prID is left as a freedom-boundary choice — the design does not
	// decide this.
	RerunFailed(ctx context.Context, prID string) error
}
