# CETA: unconditional/verb-scoped trust for first-party workflow tools

**Status**: Accepted (resolves `pg2-23z9w` in part)
**Date**: 2026-09-21
**Deciders**: Phillip Green II

## Context

`pg2-23z9w` enumerates the commands our own plugins prescribe that CETA cannot decide, so every
invocation falls through to the auto-mode LLM classifier (an abstain) or, in a `dontAsk` dispatched
session, is denied outright. Measured 2026-09-15..21 (`claude-extended-tool-approver report
--since 2026-09-15 --misses-only --group-by command`), three of those miss classes are this
machine's own first-party workflow tools, prescribed verbatim by our plugins:

- **30 misses** — `cd $CC && wtdone $FB --cc $CC ...`. `wtdone` is agent-support's own guarded
  worktree-teardown wrapper, prescribed by `integrate-branch:ff-merge-to-main` and
  `session-wrapup:wrap-up-session`.
- **126 misses** — `session-mode set-status <status> [--handoff-bead <id>] || true`, prescribed by
  `session-wrapup:wrap-up-session` / `pb:drain-beads` to record a session's mode.
- **6 misses** — `pg-router status` and `ccpool status`, operational status queries used across
  drain and pg-router-review sessions.

None of these had a home in machine `rules.json` before this decision. Extension point 2 in
`packages/claude-extended-tool-approver/docs/ARCHITECTURE.md` ("adding or changing an approved/
blocked command") already covers exactly this shape of gap — a flat basename in `approvedTools`,
or a `(tool, verb)` pair in `verbScopedApprovals`, both consumed by the config-driven `build-tools`
rule (ADR 0033) — so `pg2-23z9w`'s part 1 item 3 question ("does this need a new plugin-conditional
extension mechanism?") is answered **no** for this class of miss: the existing per-machine
`rules.json` mechanism is sufficient, and no new mechanism was designed or built. (Whether the
remaining classes in the bead's table — the `SubagentHandback` tool and the `kill -0` idiom — need
one is left open; see the bead for the current state of that question.)

`build-tools`'s `verbScoped` matcher is keyed purely on executable basename (see
`internal/rules/buildtools/buildtools.go`'s `Evaluate`), not on any notion of "is this a build
tool" — the module name is historical. `cue vet` and `jar xf` are already base-generic examples of
the same mechanism applied to a non-build tool, so routing `session-mode`/`pg-router`/`ccpool`
through it is consistent with how the rule already behaves, not a repurposing of it.

## Decision

1. **`wtdone` is added to `machines/phillipg-mbp-02/default.nix`'s `buildtools.approvedTools`** —
   unconditional, argument-blind trust for that one basename, the same posture ADR 0040 already
   establishes for the flat `approvedCommands` list and that this repo's `buildtools` config
   already grants `prove`/`yath`. This is safe specifically because `wtdone`'s own flag surface is
   closed by construction: only `-h`/`--help` and `--cc <dir>` are recognized, any other `-*`
   token makes it exit immediately, and its internal liveness/dirty-worktree/unmerged-branch guards
   have no force override anywhere in its argument space (no `-D`, no `worktree remove --force`).
   There is no argument shape that escalates what an invocation of it can do — the property ADR
   0040 requires before granting argument-blind trust.

2. **`session-mode set-status`, `pg-router status`, and `ccpool status` are added as
   `buildtools.verbScopedApprovals` entries** (`{tool, verb}` pairs), each approving _only_ that
   one first subcommand — deliberately **not** a blanket `approvedTools` entry for any of the three
   tools, because each has other subcommands with real side effects (`session-mode` has mode
   transitions, `pg-router`/`ccpool` have dispatch/cancel operations) that this decision does not
   vet and must not silently approve as a side effect of approving `status`/`set-status`. Each is
   safe to approve at the verb level because:
   - `session-mode set-status` writes a small local status record only; it never mutates a repo or
     touches anything remote, and callers already invoke it best-effort (`|| true`).
   - `pg-router status` and `ccpool status` are read-only operational queries — they report pool/
     session state and mutate nothing.

3. **No plugin-conditional / per-plugin `rules.json` fragment mechanism was built for this
   decision.** All three approvals land in the single flat per-machine `rules.json` that
   `configrules.Load` already reads, exactly as `prove`/`yath`/the existing script approvals do.
   Building fragment-based, enabled-plugin-scoped config (contemplated but not committed to by
   `pg2-23z9w`'s part 1 item 3) remains a separate, undecided question for whichever miss classes
   genuinely need it — none of the three covered by this ADR do.

## Consequences

- The 30 `wtdone` misses and the 132 `session-mode set-status` / `pg-router status` / `ccpool
status` misses (126 + 6) should no longer abstain or deny once this machine's `rules.json` is
  regenerated from `machines/phillipg-mbp-02/default.nix` and applied.
- A future subcommand added to `session-mode`, `pg-router`, or `ccpool` is **not** silently trusted
  by this decision — it must clear the LLM classifier (or get its own explicit
  `verbScopedApprovals` entry) exactly as before, which is the intended narrow blast radius.
- If `wtdone` ever grows a flag that changes its guarantees (a force/override flag, an argument
  that widens what path it can act on), this ADR's safety argument for the unconditional
  `approvedTools` entry no longer holds and the entry must be revisited — it was granted on the
  strength of the CURRENT flag surface, not as a standing guarantee about the tool's future shape.
- Rejected: leaving these three classes as abstains pending a future plugin-conditional mechanism.
  The commands are already unconditionally prescribed by first-party skills every session runs;
  deferring them compounds the exact miss volume `pg2-23z9w` exists to shrink for no offsetting
  benefit, since extension point 2 already covers the shape of the gap.
- Rejected: a blanket `approvedTools` entry for `session-mode`, `pg-router`, or `ccpool` (approving
  the whole tool rather than one verb each). Each tool has other subcommands with real,
  unreviewed side effects; only the verb actually prescribed by a plugin is approved.
