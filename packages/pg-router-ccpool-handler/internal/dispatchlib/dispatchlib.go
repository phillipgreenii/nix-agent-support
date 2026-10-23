// Package dispatchlib holds the role-agnostic pieces of a dispatch that both
// adapter binaries of this module import (bead pg2-12gr4.10.3): rebuilding the
// item from the event payload, stamping the per-attempt ccpool ExternalID, the
// role's bd actor and the production executor.Deps wiring. Each adapter keeps
// its own runDispatch; there is ONE copy of the external-id stamping, whose
// format the adoption of old sessions depends on.
//
// Moved verbatim out of package main of cmd/pg-router-ccpool-handler
// (dispatch.go); no behavior change.
package dispatchlib

import (
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/executor"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/handlerstate"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/sessionlock"
)

// ItemFromPayload reconstructs an item.Item from a dispatch event's opaque
// payload object — this module's own local replacement for
// packages/pg-router/internal/discover.ItemFromPayload, which is
// unreachable from here (Go's internal-package visibility rule) and, as of
// docket pg2-oju6w's Task 5.6, deleted entirely: the core-side
// discover.ToQueueEvent now writes the item's fields directly at Payload's
// top level (no wrapping key), and this function reads that same flat
// shape — the SAME field names ItemFromPayload used to read, just no
// longer nested under an "item" key (Task 5.6's "same shape, different
// side" framing; docs/adr/0065's Addendum). A payload missing the expected
// fields yields a zero item.Item rather than an error, matching
// ItemFromPayload's own "absent path is a non-match, not an error" posture.
func ItemFromPayload(payload map[string]any) item.Item {
	var it item.Item
	if v, ok := payload["id"].(string); ok {
		it.ID = v
	}
	if v, ok := payload["type"].(string); ok {
		it.Type = v
	}
	if v, ok := payload["title"].(string); ok {
		it.Title = v
	}
	if v, ok := payload["metadata"].(map[string]any); ok {
		it.Metadata = v
	}
	return it
}

// externalIDStampLayout is the per-attempt ccpool ExternalID's timestamp
// layout. This module's own internal/dtest.TestStamp fixture
// ("20260616T010203") already reserves the second-precision "20060102T150405"
// layout as this codebase's stamp-generation convention (matching
// internal/ccpool/cli_test.go's "pg-router-worker-zr-1-20260616T010203"
// fixture) — reused here rather than inventing an unrelated format — with
// nanosecond precision appended so two attempts stamped within the same
// wall-clock second still mint distinct ids; a bare second-resolution stamp
// cannot promise that on its own.
const externalIDStampLayout = "20060102T150405.000000000"

// StampExternalID builds role's per-attempt ccpool ExternalID
// (roles.Role.ExternalID's own documented <prefix><name>-<beadid>-<stamp>
// shape), stamping it fresh from now on every call (pg2-nmpvs). now nil
// (BuildDeps never sets Deps.Now in production) defaults to time.Now,
// mirroring executor.Deps.Now's own "clock seam; nil ⇒ time.Now" doc
// comment — Deps' own clock() method implementing that default is
// unexported, so this package cannot call it directly and
// re-implements the same nil-check here instead of inventing a second,
// independent clock seam.
func StampExternalID(role roles.Role, prefix, beadID string, now func() time.Time) string {
	if now == nil {
		now = time.Now
	}
	return role.ExternalID(prefix, beadID, now().UTC().Format(externalIDStampLayout))
}

// BuildDeps wires the executor.Deps seam bag for production use from cfg and
// role: a real ccpool.CLIRunner and beads.CLIRunner, everything else left at
// its own nil-safe default (Deps.git/gitOpener/clock/reader/commander/
// waitPoll — internal/executor/executor.go).
//
// role.CCPool.PoolDir (bead pg2-mr0sl), when this role sets one, scopes the
// ccpool.CLIRunner to that role's own dedicated pool via
// ccpool.NewCLIRunnerForPool — every ccpool call this dispatch call makes
// (the admission-gate Capacity check, Ensure/Send, and every List/Close call
// the wait-loop and worktree cleanup make) then targets that pool, not
// whatever CCPOOL_POOL this process inherited. This is scoped to BuildDeps'
// one production call site (runDispatch) deliberately: preshutdown.go's
// once-per-process-lifetime sweep and query.go's own reconciliation call
// still build a plain ccpool.NewCLIRunner(cfg) — both act once across every
// enabled role sharing this process, with no single role to scope to (see
// each of those call sites' own doc comments on that pre-existing,
// unchanged-by-this-bead posture).
func BuildDeps(cfg config.Config, role roles.Role) executor.Deps {
	// Per-role tool grants (bead pg2-nk6th.5): cfg is a by-value parameter, so
	// merging the role's ExtraAllowedTools onto the handler-wide AllowedTools
	// here scopes the grants to this role's runner (and its Deps.Cfg) only.
	if role.CCPool != nil {
		cfg.AllowedTools = config.MergeAllowedTools(cfg.AllowedTools, role.CCPool.ExtraAllowedTools)
		// Per-role effort (bead pg2-svkob, INV-CCH-31): a non-empty role value
		// replaces the handler-wide launch-config effort for this role's runner
		// (and its Deps.Cfg) only; "" leaves it unchanged.
		if role.CCPool.Effort != "" {
			cfg.Effort = role.CCPool.Effort
		}
	}
	cc := ccpool.NewCLIRunner(cfg)
	if role.CCPool != nil && role.CCPool.PoolDir != "" {
		cc = ccpool.NewCLIRunnerForPool(cfg, role.CCPool.PoolDir)
	}
	// Every bd call for this role's items resolves against the tracker the
	// items live in: the role's own BeadsDir when set (pg2-2grpj, e.g. the pg2
	// tracker for the escalation triager), else the monorepo at cfg.RepoRoot.
	bdDir := cfg.RepoRoot
	if role.CCPool != nil && role.CCPool.BeadsDir != "" {
		bdDir = role.CCPool.BeadsDir
	}
	return executor.Deps{
		CC:  cc,
		BD:  beads.NewCLIRunnerForRepo(bdDir, RoleActor(role)),
		Cfg: cfg,
		Log: handlerstate.NewEventLog(cfg),
		// Per-session flocks under the handler state directory (the same one as
		// events.jsonl) serialize the orphan reconcile against an absorbing
		// dispatch (pg2-g2u9m, INV-CCH-18).
		LockDir: sessionlock.Dir(handlerstate.Dir(cfg)),
	}
}

// RoleActor is the bd identity the handler's own bd writes for role are
// attributed to (bead pg2-lhi3b): the role's ccpool actor, i.e. the same
// identity its dispatched worker sessions claim under via BEADS_ACTOR
// (internal/executor/ccpool.go). "" for a role with no ccpool block.
func RoleActor(role roles.Role) string {
	if role.CCPool != nil {
		return role.CCPool.Actor
	}
	return ""
}
