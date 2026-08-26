# Unpushed Landing Debt (full contract)

U-1..U-4 and U-6 MOVED here in full out of the core rules file (tc-ql0o Stage D, 2026-08-26):
session close-out is exactly the observable moment this debt is assessed and (selectively)
reported, so this skill is a sufficient home for the full text. **U-5** — the bare prohibition
against pushing/applying/updating on the agent's own initiative — has no session-close trigger of
its own (it applies the instant an agent considers ANY such command, not only at wrap-up).

U-5 alone remains unconditionally in the core `pgii-agent-rules.md`.

> A local ff-merge makes work LANDED, not PUBLISHED, and the debt REGENERATES on every land — so it
> is computable state that no record can hold, and a standing bead for it is a defect (`pg2-5subz`
> nearly orphaned 11 unrelated commits; its replacement `pg2-dawg2` pushed 12, closed correctly, and
> the debt was back within a day). Unpushed commits are NOT in themselves a problem, so the
> OBLIGATION IS TO LOOK WHEN IT MATTERS — not to narrate the count at every session end.

- **U-1** Unpushed landing debt MUST be treated as DERIVED STATE and re-derived from git at the
  moment it matters. No bead, label, comment, handoff doc, or earlier reading is its handle: a
  reading is valid only for the instant it was taken (the `beads-lifecycle` skill's F-1) and MUST
  NOT be cached across a later land, a peer session, or a hand-off.
- **U-2** An agent MUST NOT create, maintain, or "restore" a standing push bead, a
  `push-carryover` bead, or a handoff-doc section whose PURPOSE is to remember that locally landed
  commits are unpushed. Such a record duplicates computable state, goes stale at the very next
  land, and can be closed while the condition it names is still true — that IS this defect. A bead
  for ONE push a person has already authorized as a discrete task is NOT this; making a bead the
  standing accounting for the aggregate debt IS.
- **U-3** In a `pn` workspace the probe is `pn workspace doctor`, run from anywhere inside the
  workspace — it already computes `origin/<branch>` vs local for EVERY repo, so an agent MUST reuse
  it rather than write another per-repo `git rev-list --count origin/main..main` loop. The debt is
  a `branch-synced` finding carrying `ahead N` with `N > 0`; `behind M` alone is NOT (that is
  un-pulled remote work), and a repo with no debt emits no section at all. The agent MUST read the
  `branch-synced` findings specifically — the trailer's error count also includes other checks.
  This workspace-wide AGGREGATE is NOT the `beads-lifecycle` skill's F-3 `pushed?` probe, which
  answers whether ONE named commit is on a remote; a single `pushed?` reading MUST NOT be
  generalized to "that repo is pushed".
- **U-4** The probe MUST be run READ-ONLY. An agent MUST NOT pass `--fix`: its `branch-synced` plan
  is `git merge --ff-only origin/<branch>` executed in the CANONICAL clone, which cannot publish an
  ahead-only divergence (so it does not clear the debt) and mutates the canonical clone, which the
  core rules' R-3 forbids.
- **U-6** REPORTING IS NOT MANDATORY, AND WHEN DUE IT MUST BE AT MOST ONE LINE. A count of
  unpublished commits is not a problem, so an agent MUST NOT give unpushed state its own heading,
  quote probe output verbatim, attribute commits to sessions, or spell out the remediation
  sequence, and MUST NOT run the U-3 probe merely to have something to report. A session that
  landed locally and is not blocked by that fact reports NOTHING about it. The one case that earns
  a line is a CONSEQUENCE for the work in hand: unpublished state BLOCKS it — e.g. a consumer flake
  pins these repos as `github:` inputs, so the change cannot take effect on apply until they are
  pushed and relocked. Then name the blockage and the repos in ONE line and stop; the operator asks
  for the probe output or the remediation path if they want it.

(**U-5**, unmoved: discharging the debt is OUTWARD-FACING and operator-authorized. An agent MUST
NOT `git push`, `pn workspace push`, `pn workspace update`, or `pn workspace apply`, and MUST NOT
invoke `/pn-workspace-sync` or `/pn-workspace-update`, on its own initiative to clear it. REPORTING
is in scope; PUBLISHING is not. Trimming the reporting duty above — U-6 — does NOT relax this
restraint. See the core `pgii-agent-rules.md`'s Unpushed Landing Debt section for its authoritative
text.)
