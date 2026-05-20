# pg-pr bead schema

**Status**: Accepted
**Date**: 2026-05-20
**Deciders**: Phillip Green II

## Context

Multiple actors (gascity background agents, claude sessions, human CLI use) need to operate on the same PR-related work queue. `bd ready` must just work for any agent with no pg-pr-specific flags — agents that have never seen pg-pr should claim the next ready item and make progress. Earlier `gc/zr` usage introduced typed beads, but trying to use `bd dep` for cascading closure created deadlocks: `A depends on B` means A is blocked until B closes, and we wanted closure to flow the other way.

## Decision

Four bead types in the schema:

- **`merge-request`** (custom type, already registered): PR tracker. Auto-excluded from `bd ready` by bd's type rules. CLI manages lifecycle (open on first detection, closed when upstream PR closes/merges).
- **`task`** (builtin): processing-cycle work unit. Title prefixed `process-feedback:`. Surfaces in `bd ready` as the primary work unit. CLI creates one per active processing cycle; LLM claims and closes.
- **`feedback`** (new custom type, registered additively): per-upstream-event record. Created with `status=hooked` so it is excluded from `bd ready` while remaining closeable with a reason. CLI creates; LLM closes during processing.
- **`task` or `bug`** (builtin): action bead. LLM picks `bug` for unambiguous breakage, `task` otherwise. Surfaces in `bd ready` as the actual fix work.

Hierarchy via bd's `parent-child` dep type (non-blocking; shows `↑ parent` in `bd show`). Action-to-feedback link via `discovered-from` (soft, non-blocking). CLI implements cascade-on-PR-close explicitly because bd has no native cascading closure.

Repo-level sync errors live in `$XDG_STATE_HOME/pg-pr/repo-state.json`, not in beads.

## Consequences

### Positive

- Zero pg-pr-specific flags on `bd ready`. Any agent can use it.
- Mixed-mode actors share a clean contract: gascity, claude, human all see the same shape.
- Custom type `feedback` registered additively, preserving the existing `types.custom` list.

### Negative

- `status=hooked` is a slight semantic stretch — bd nominally uses it for hook-gated work. Acceptable workaround validated in scoping work.
- CLI must implement cascade-on-PR-close manually in `internal/sync/cascade.go`.

### Neutral

- Repo errors in a state file instead of beads is a small bifurcation of where to look for state.

## Alternatives Considered

### Labels for hierarchy + builtin types only

Rejected. `bd parent-child` deps already work and produce nicer `bd show` output with parent links.

### `message` type for feedback beads

Rejected. `message` IDs get a `wisp-` prefix and may be subject to TTL-based compaction (wisp tier). Compaction would threaten referential integrity for action-to-feedback `discovered-from` links.

### Custom `action` type

Rejected. Builtin `task`/`bug` suffice and reduce type proliferation. Action kind is metadata, not a separate bd type.

### `sync-status` bead per repo for errors

Rejected. Repo-level errors are not work units; representing them as beads would clutter `bd ready` and require special-case filtering. State file is cleaner.

## Related Decisions

- [0007-pg-pr-go-cli-consolidation.md](0007-pg-pr-go-cli-consolidation.md)
- [0008-pg-pr-extension-via-exec-scripts.md](0008-pg-pr-extension-via-exec-scripts.md)

See also: `docs/superpowers/specs/2026-05-19-pg-pr-design.md` §"Bead schema".
