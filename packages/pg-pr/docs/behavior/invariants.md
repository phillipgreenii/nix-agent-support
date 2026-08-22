# Invariants — pg-pr

Rules this set's implementation MUST hold, following the behavior-docs method
(`phillipgreenii-nix-agent-support · behavior-docs/docs/behavior`). Names are namespaced by topic
(`INV-3`) since this set cites the method. Four invariants below carry a UUID **moved** from the
ZR deployment set rather than freshly minted, because they are the same rule relocated to its
correct owner (ADR 0034) — the UUID preserves identity; only the owning set changed.

## Reads (`INV-READ-*`, `INV-ASOF-*`)

- **`INV-READ-1`** <!-- uuid: fcbe3724-6712-428d-a129-eea663f44ff9 --> — The base machine read seam
  (no augmentation requested) MUST read from the store with **no network call** and **no store
  mutation**. It carries PR facts only — never pg-pr's own configuration state.
- **`INV-ASOF-1`** <!-- uuid: 39888347-cfb9-45fd-b72e-b465e2337d15 --> — Every acted-on read seam
  MUST carry its own as-of time: per item on the machine read seam, or at the payload level on
  the dashboard payload. An item or payload with no usable as-of time MUST be reported **stale**.
  A consumer MUST NOT act on a read flagged stale — the acting decision is the consumer's, but
  the flag itself is pg-pr's obligation to raise correctly.

## Approval (`INV-APPROVAL-*`)

- **`INV-APPROVAL-1`** <!-- uuid: d214af68-dcb2-4eff-8d26-7f129e64d60b --> — **Per approver,
  never collapsed.** pg-pr MUST track approval **per approver**. It MUST NOT collapse the
  approver set into a single approved/not-approved boolean; each approver's own approval MUST
  stay individually addressable, so two approvers approving is distinguishable from one.
- **`INV-APPROVAL-2`** <!-- uuid: 13334fae-beac-4b7a-bd0c-0303810c6c7d --> — **Two independent
  verdict axes.** Every approver's verdict MUST be classified on two independent axes: findings
  (`clean`, `problems`, or `unknown`) and authority (`approved`, `withheld`, `pending`, or
  `absent`). Neither axis MUST be inferred from the other axis's value.
- **`INV-APPROVAL-3`** <!-- uuid: 890a643f-b6dc-4597-93b6-2c0e8a39d03f --> — **Staleness is per
  approval, relative to head.** An approval MUST carry its own staleness relative to the PR's
  current head commit, distinct from — and in addition to — the as-of-time staleness of
  `INV-ASOF-1`. A review the code host reports as dismissed MUST be read as a **stale**
  approval, never as an absent one.
- **`INV-APPROVAL-4`** <!-- uuid: f5ca7207-632d-4513-83c7-d8e7c4560ee4 --> — **A bot approver
  counts.** Once an account is configured as an approver, its verdict MUST count toward
  approval on equal terms whether that account is a human or a bot; pg-pr MUST NOT discount or
  withhold a bot approver's authority on account of it being a bot.
- **`INV-APPROVAL-5`** <!-- uuid: 7fae2413-1287-4811-89ac-a9bcc1789e47 --> — **An unmatched
  verdict MUST be observable.** A verdict body pg-pr cannot classify under any configured
  grammar MUST surface as an observable signal. It MUST NOT be silently read as `absent`
  findings or authority — an unrecognized verdict and a genuine absence of approval MUST remain
  distinguishable to whoever is watching for it.

## Merge-request records (`INV-MR-*`)

- **`INV-MR-1`** <!-- uuid: bacb5392-230f-4cf7-a10b-6821ddb0f085 --> — pg-pr MUST be the **sole
  creator** of merge-request tracking records. At most **one** record exists per
  `(repository, PR number)`; a re-sync MUST update the existing record rather than create a
  second one, and a **closed** record MUST NOT be reopened by sync.

## Sync (`INV-SYNC-*`)

- **`INV-SYNC-1`** <!-- uuid: 6178529e-79c0-4ec6-84b7-ebdb9f9c5ed0 --> — Change detection MUST be
  a pure comparison that **mutates nothing**. Only a **worker** writes; a worker is the **sole
  authority** that closes or removes a tracked PR.
- **`INV-SYNC-2`** <!-- uuid: 998c3de9-7c8b-4269-8c6c-049266c23329 --> — A sync pass that could
  not confirm completeness for some subset (a transient error, a truncated page) MUST NOT
  conclude that subset's PRs are gone. It MUST carry forward the prior known state for the
  unconfirmed subset rather than mass-closing it.

## The write surface (`INV-WRITE-*`, `INV-REVIEW-*`, `INV-ATTR-*`)

- **`INV-WRITE-1`** <!-- uuid: f7360062-33d4-4ca5-8a45-6f8af19e6987 --> — An inline comment or
  review MUST anchor to the exact commit it was produced against, so a commit landing after it
  was written does not invalidate it.
- **`INV-REVIEW-1`** <!-- uuid: ea7a8bd0-e947-43db-9d98-e76c511cfa6e --> — **Never auto-submit.**
  pg-pr's write surface MUST create a pending review — review content held in the code host's
  own **pending** state; submitting it with a verdict is a separate, explicit act pg-pr MUST NOT
  perform on its own. On a code host with no concept of a pending review, review content MUST be
  held for the operator to post rather than published live.
- **`INV-REVIEW-2`** <!-- uuid: c8e0f2dd-e254-4c50-a1d0-a493c14ca57e --> — **No review of
  drafts.** pg-pr MUST NOT post review content against a PR its author still marks draft;
  reviewing begins only once the PR is marked ready.
- **`INV-REVIEW-3`** <!-- uuid: 1a763847-8b40-4500-ba7d-e982207b3503 --> — **A re-review
  supersedes; at most one pending draft.** A fresh draft on a PR that already carries a pending
  draft MUST **supersede** it rather than stack alongside it. Whether a pending draft already
  exists MUST be resolved by a fail-closed pending check: an undetermined result MUST block the
  post, never assume none exists.
- **`INV-ATTR-1`** <!-- uuid: 50c826f0-4852-4c4e-945e-573c39d51c7b --> — **Bot attribution.**
  Anything pg-pr posts to the code host under a shared or human account MUST carry an
  attribution mark: a human-visible marker distinguishing it as agent-generated, plus a
  machine-detectable invisible marker used to recognize pg-pr's own prior posts (dedup and
  identity).
