# Glossary — pg-pr

Vocabulary for pg-pr's PR-data interface. Review **workflow** — who reviews what, and when — is a
deployment concern and is not defined here (`## Scope`).

## The read seams

- **PR fact** — a piece of information about a PR that pg-pr itself observed from the code host:
  identity, state, ownership, branch, head commit, reviewer roster. Never configuration state,
  never another product's internals.
- **As-of time** — the instant a PR fact was last confirmed against the code host. Carried
  alongside the fact itself on every acted-on read.
- **Stale** — an as-of time judged too old to act on. A read seam MUST flag staleness rather than
  let a consumer assume freshness it cannot vouch for.
- **Machine read seam** — the network-free, side-effect-free listing a program reads facts from.
- **Dashboard payload** — the human-facing read seam; carries its own as-of time at the payload
  level rather than per item.

## Approval

- **Approval** — one approver's current standing on a PR at a given head: a verdict, classified
  by findings and authority, always attributed to that one approver and never collapsed across
  approvers into a single yes/no signal (`INV-APPROVAL-1`).
- **Approver** — an actor, human or bot, whose verdict counts toward approval. Not every account
  that posts review content is an approver; which accounts count is per-deployment
  configuration, never a pg-pr behavior (`INV-APPROVAL-4`).
- **Verdict** — an approver's judgment on a PR's current head, classified on two independent
  axes: findings and authority (`INV-APPROVAL-2`).
- **Findings** — the verdict axis answering "did this approver find problems with the PR":
  `clean`, `problems`, or `unknown`.
- **Authority** — the verdict axis answering "does this approver's verdict currently stand as an
  approval": `approved`, `withheld`, `pending`, or `absent`.

## Approval gate

- **Approval gate** — a PR fact: the aggregate readiness signal reported by a configured gate
  mechanism, tracked as its own axis — distinct from CI health and distinct from any single
  approver's verdict (`Approval`) — and never folded into either (`INV-GATE-1`).
- **Gate state** — the approval gate's classification: `satisfied` (every configured requirement
  currently holds), `partially-satisfied` (some but not all hold), `unsatisfied` (none hold), or
  `unknown` (the signal could not be classified). An absent, unmatched, or unparseable signal is a
  gate state of `unknown`, never `satisfied` (`INV-GATE-2`).

## Sync

- **Fingerprint** — a cheap signature of a PR's observable state, used to detect a change without
  re-fetching everything.
- **Detector** — the read-only comparison that finds what changed. It mutates nothing.
- **Worker** — the only tier that writes: applies a detected change to the store, and is the sole
  authority that closes or removes a tracked PR.
- **Partial data** — a sync pass that could not complete for some subset (a transient error,
  pagination truncation). MUST NOT be treated as "confirmed gone" — see `INV-SYNC-2`.
- **Qualifying reason** — the fact that puts a not-mine PR into pg-pr's retrieved "to review"
  set: team-authored, review-requested, reviewed-by-me, assigned-to-me, or carrying a configured
  watch label. Recomputed from current facts on every rebuild; never a persisted "seen" flag
  (`INV-SYNC-1`).

## Merge-request records

- **Merge-request record** — the one tracking record standing for a PR, identified by
  `(repository, PR number)`. pg-pr is its **sole creator**; a re-sync updates the existing record
  and never creates a second one for the same identity.
- **Upsert** — create if absent, otherwise update; never create a duplicate for an identity
  already tracked.

## The write surface

- **Draft PR** — a state the PR's own author sets on the code host, independent of pg-pr: while a
  PR is a draft PR, ordinary review does not begin (`INV-REVIEW-2`). A PR fact pg-pr reads, never
  something pg-pr decides on its own initiative. Distinct from **Draft** below — an unrelated,
  pre-existing sense of the same word in this glossary, meaning review content pg-pr holds
  locally before posting: a PR can be a draft PR with no such content staged, and content can be
  staged against a PR that is not a draft PR.
- **Draft** — review content staged but not yet posted live.
- **Pending review** — a review posted to the code host but not yet submitted with a verdict; the
  code host's own "not yet final" state for a review.
- **WIP** — the reviewing operator's own suppression on one of their own PRs: while set, that
  operator does not yet want the PR reviewed, even though it is a draft PR the operator would
  otherwise be allowed to review (`INV-REVIEW-2`). WIP has no bearing on whether anyone else's PR
  is reviewable, and no bearing once the PR is no longer a draft PR.
- **Head-anchored** — a comment or review anchored to the exact commit it was produced against,
  so a later commit landing does not invalidate it.
- **Supersede** — a fresh draft replaces an existing pending draft on the same PR rather than
  stacking beside it; at most one agent-authored pending draft exists per PR at a time.
- **Fail-closed pending check** — when pg-pr cannot determine whether a pending draft already
  exists, it MUST treat that as "assume one exists" and refuse to post another, never the
  opposite.
- **Attribution mark** — the visible and invisible marks pg-pr stamps on everything it posts under
  a shared or human account, so the content is distinguishable as agent-generated
  (`INV-ATTR-1`).

## Visibility

- **User-hidden** — a display-only suppression the operator sets on one specific PR they no
  longer want to see in day-to-day surfaces. A user-hidden PR is tracked, synced, and ingested
  exactly as any other PR — user-hidden governs visibility alone, never ingestion. Distinct from
  the pre-existing, unrelated mechanism (outside this glossary's vocabulary) that drops a
  team-authored draft PR from view: that mechanism is host-derived and not the operator's own
  decision, where user-hidden is always an explicit, per-PR act by the operator.

## PR dependency

- **PR dependency** — the fact that one PR's landing is blocked on another PR landing first,
  identified from PR facts pg-pr already reads: the code host's own native stacking relation
  where present, falling back to a base-branch chain (one PR's base branch naming another open
  PR's own branch) where it is not. A PR dependency is always **read-only** — pg-pr identifies
  and reports it, and never creates, links, or unstacks anything on the code host to express or
  resolve it (`INV-DEP-1`).
- **Stacked PR** — a PR that carries a PR dependency: it is either a downstream PR waiting on an
  upstream PR, or the upstream PR something else is waiting on.
- **Upstream PR** — in a PR dependency, the PR that must land first. Ranks higher than the
  downstream PR waiting on it.
- **Downstream PR** — in a PR dependency, the PR that is waiting on its upstream PR to land
  first. Ranks lower than its upstream PR rather than being suppressed from view; if its upstream
  PR merges, the downstream PR becomes unblocked rather than re-pointing to the upstream PR's own
  upstream.

## Excluded (named so the boundary is explicit, `## Scope`)

The **legacy in-daemon review workflow** — draft-review lifecycle, an automated review consumer
and its own agent spawn, re-review-on-head-advance, retry/dead-letter policy, a credential
pre-fetch gate, a result sidecar, and the kill switch gating all of it — is a **deployment
workflow concern**, not a pg-pr behavior, and is named here only to be excluded.
