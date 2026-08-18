# ccpool MCP consent: default-deny unclassified, with optional read-only canonical-decisions consultation

**Status**: Accepted
**Date**: 2026-08-18
**Deciders**: Phillip Green II

## Context

Every ccpool session launch pre-records MCP-server consent for its working directory before
`Tmux.NewSession`, in the same pre-launch window as `EnsureTrusted`
(`packages/ccpool/internal/session/session.go`, `ensureLocked`, already noted in
[ADR 0038](0038-ccpool-session-cwd-caller-owned.md)'s Context: "pre-records MCP consent for that
directory's `.mcp.json` (`mcpconsent.PreDisableUnclassified`, `session.go:279`)"). That step exists
so an automated `claude` launch does not stall on the interactive "New MCP server found in this
project" prompt, which nothing in a headless pool worker can answer — bead `pg2-80ji` established
this rationale and explicitly rejected both adding `AskUserQuestion` support for it and using
`enableAllProjectMcpServers` (it would expand the OAuth/injection surface). Claude's MCP consent is
exact-match on the server name (no wildcard), so a durable "reject all unknown servers" pre-record
must enumerate every server declared in the worktree's `.mcp.json`.

### What ships today, read off the code

`mcpconsent.PreDisableUnclassified(worktreeDir string) error` reads `<worktreeDir>/.mcp.json` and,
for every declared server not already listed in either `enabledMcpjsonServers` or
`disabledMcpjsonServers` of `<worktreeDir>/.claude/settings.local.json`, appends it to
`disabledMcpjsonServers` — least-privilege, default-deny. Servers the operator has already
classified (in either list) are left untouched, and the file is rewritten only when a server is
actually added (idempotent re-runs). This behavior has never been ratified anywhere: not by an ADR,
not by an invariant, not by any shipped doc. `packages/pr-pool/docs/behavior/` — this repo's living
behavior-docs set for the pg-pr/pr-pool system — deliberately excludes it: its own Scope states
"concrete participant implementations (ccpool, beads, prometheus, …) and any deployment-specific
behavior live in a downstream deployment set", and its Floor states the set "names no concrete
tool, transport, tuning constant, or file layout" — `.mcp.json` and `settings.local.json` are
exactly such a file layout. [ADR 0026](0026-pr-pool-behavior-scope-orchestrator-only.md) is the
decision that drew that boundary (pr-pool's generic set is a bare orchestrator; all workflow/domain
and tool-specific behavior is a deployment concern), and ADR 0038's Related Decisions already
flagged the consequence: "ccpool's own behavior-docs set does not exist yet; when it is authored,
clause 6 is the rule its working-directory statement must satisfy." This ADR is the same kind of
record for MCP consent that ADR 0038 is for session cwd ownership — ccpool-internal session-launch
behavior recorded here, in `docs/adr/`, because the set that would otherwise host it structurally
cannot.

### The gap this ADR's bead closes

The default-deny-unclassified behavior is correct and intentional for the fully headless/unattended
pool worker. But when a human interactively `cd`s into one of these ephemeral pool worktrees and
runs `claude` themselves, they get zero prompts (good) but also never see servers they have
**already** approved or rejected elsewhere via a long-lived canonical settings file (e.g. a
symlink-chain-based `settings.local.json` some other long-lived worktree uses) — every unclassified
server gets blanket-disabled regardless of prior real decisions the same human already made. Bead
`pg2-lcmpz` adds a read-only consultation step so an already-made classification is honoured instead
of re-denied.

## Decision

1. **The default-deny-unclassified behavior, as it ships, is ratified.** For every server declared
   in a worktree's `.mcp.json` that is still unclassified after step 2 below, ccpool MUST append it
   to that worktree's `disabledMcpjsonServers` — least-privilege by default. This governs every
   worktree, canonical-decisions consultation configured or not.
2. **`session.Deps` MUST carry an optional, generic canonical-decisions settings path** (empty by
   default — feature off). When configured and non-empty, before applying step 1, ccpool MUST
   attempt to read that path as a file shaped exactly like `settings.local.json` (the same
   `enabledMcpjsonServers` / `disabledMcpjsonServers` arrays) and, for each server declared in the
   worktree's `.mcp.json` that is classified there but **not yet** classified in the worktree's own
   `settings.local.json`, copy that classification (enabled or disabled) into the worktree's file.
   Only servers still unclassified after that consultation are covered by step 1.
3. **The canonical file is consulted read-only and is never written.** ccpool MUST NOT write to the
   canonical-decisions path under any circumstance. This is what keeps this mechanism outside the
   write-through-symlink corruption class referenced by the bead (a canonical settings file some
   deployments make a symlink target for several worktrees) — a step that never writes to that path
   cannot corrupt it.
4. **A missing, unreadable, or unparseable canonical file MUST NOT be a hard error.** ccpool MUST
   log/ignore the failure and fall back to pure default-deny (step 1) for every server, so the
   existing headless-safety property can never regress because of a bad canonical file. An empty
   configured path behaves exactly as before this ADR (no consultation, pure default-deny).
5. **This repo supplies only the generic, empty-by-default mechanism.** Per this repo's own
   "Public Repository — No ZipRecruiter Disclosure" rule, no concrete canonical path, deployment
   name, or symlink-chain layout may be hardcoded here. Which path a deployment points the
   mechanism at (and how that deployment arranges its own long-lived canonical
   `settings.local.json`) is that deployment's own configuration, supplied at runtime — out of
   scope for this record.

Rationale:

- **The headless-safety property must be preserved exactly.** Bead `pg2-80ji`'s rejection of
  `AskUserQuestion` and `enableAllProjectMcpServers` for headless workers stands; this ADR adds a
  read path, never a prompt or a broadened default-enable.
- **Read-only sidesteps the corruption class entirely.** The bug class the bead cites is Claude
  Code writing through a `settings.local.json` symlink. ccpool never writes the canonical path, so
  that failure mode cannot occur by construction — no locking, no atomic-write discipline, and no
  ownership arbitration between ccpool and whatever else may write that file are needed.
- **Interactive humans should not re-litigate a decision they already made.** A human's classification
  recorded once in a long-lived canonical settings file is real signal, not a guess; propagating it
  to a fresh ephemeral pool worktree is strictly more correct than blanket-denying it.
- **The mechanism belongs generically here; the wiring does not.** Keeping the field
  empty-by-default and the path fully caller-supplied is what lets this public flake ship the
  capability while a private deployment (`phillipg-nix-ziprecruiter`) supplies its own
  ZR-specific canonical path and layout without a single ZR literal appearing in this repo.

## Consequences

### Positive

- Ephemeral pool worktrees a human works interactively now respect classifications already made
  elsewhere, closing the exact defect `pg2-lcmpz` was filed against.
- The headless pool worker's behavior is provably unchanged when no canonical path is configured —
  same code path, same result, regression-pinned by the existing test suite.
- Zero new write surface: the canonical file gains no new writer, so no new corruption risk is
  introduced anywhere in the system.
- ccpool's own session-launch behavior now has a durable, discoverable record (this ADR), matching
  the pattern ADR 0038 already established for the sibling `ensureLocked` pre-launch step (cwd).

### Negative

- **A canonical classification is trusted without per-worktree revalidation.** If the canonical
  file records a stale or since-revoked decision, that stale decision now silently propagates to
  every fresh worktree instead of being re-asked. This is accepted because the canonical file is
  human-authored and read-only from ccpool's side — a human who wants to revoke it edits it at its
  own long-lived location, which then takes effect on the next worktree — but it is a real residual
  risk this ADR does not eliminate, only accepts.
- A worktree whose canonical-decisions path is misconfigured (wrong path, wrong shape) fails
  silently into pure default-deny (per clause 4) rather than surfacing the misconfiguration audibly
  to an operator; a future observability pass could add a diagnostic-log line for this case without
  changing the contract.

### Neutral

- Which deployment supplies a concrete canonical path, and how it arranges its own symlink chain of
  long-lived `settings.local.json` files, is entirely out of scope here and is expected to land in
  `phillipg-nix-ziprecruiter`'s own configuration, not in this repo.
- `packages/pr-pool/docs/behavior/` is unchanged by this ADR. Its Scope/Floor were read and are
  the reason this record lives in `docs/adr/` instead.

## Alternatives Considered

### Write the worktree's classification back into the canonical file too (bidirectional sync)

Rejected. It would make ccpool a second writer of a file some deployment's long-lived worktree
already treats as canonical/authoritative, reintroducing exactly the write-through-symlink
corruption class the bead exists to avoid. A worktree's own `settings.local.json` remains the only
file ccpool ever writes.

### Have `PreDisableUnclassified` prompt or otherwise defer to a human when the canonical file lacks a classification

Rejected. It reopens the headless-safety question bead `pg2-80ji` already settled (no
`AskUserQuestion` in this path); an unclassified server, canonical file or not, still resolves to
default-deny.

### Do nothing; keep pure default-deny for interactive humans too

Rejected. This is the exact defect `pg2-lcmpz` was filed to fix — a human re-approving the same MCP
servers on every ephemeral pool worktree despite having already classified them elsewhere.

### Document this in `packages/pr-pool/docs/behavior/`

Rejected. That set's own Scope names concrete participant implementations (ccpool named explicitly)
and deployment-specific behavior as **extent (out)**, and its Floor forbids naming a concrete file
layout — `.mcp.json` / `settings.local.json` violate that floor directly. Forcing this addition in
would require a structural change to that set's own boundary, which [ADR 0026](0026-pr-pool-behavior-scope-orchestrator-only.md)
already deliberately drew the other way. An ADR is the established alternative for ccpool-internal
session-launch behavior (precedent: ADR 0038 for session cwd ownership, over the same
`ensureLocked` pre-launch chain).

## Related Decisions

- [ADR 0038](0038-ccpool-session-cwd-caller-owned.md) — the sibling `ensureLocked` pre-launch step
  (`EnsureTrusted`) documented the same way, for the same reason; its Related Decisions section
  already anticipated that "ccpool's own behavior-docs set does not exist yet."
- [ADR 0026](0026-pr-pool-behavior-scope-orchestrator-only.md) — draws the boundary that keeps
  concrete participant/tool behavior (ccpool included) out of `packages/pr-pool/docs/behavior/`,
  which is why this record lives here instead.
- Bead `pg2-80ji` — established the headless default-deny-unclassified rationale this ADR ratifies
  (clause 1) and does not revisit.
- Bead `pg2-lcmpz` — the decision-and-documentation gap this ADR closes (clauses 2-5).
