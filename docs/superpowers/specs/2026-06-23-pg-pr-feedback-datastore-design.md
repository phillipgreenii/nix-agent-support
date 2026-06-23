# pg-pr Feedback Datastore — Design

**Status**: Draft
**Date**: 2026-06-23
**Deciders**: Phillip Green (phillipg), Claude

## Context

Today pg-pr stores **everything** in beads: a `merge-request` bead per PR, a
`processing-cycle` bead per round of work, and a `feedback` bead per upstream
event (comment thread, CI failure, review thread, …), deduped by fingerprint.
The reply pipeline lives in bead metadata too (`reply_draft` → posted →
`response_id`).

This overloads beads. A `feedback` bead with `status=hooked` is **a record, not
actionable work** — but it still shows up in `bd ready`/`bd list` alongside real
tasks. The intent of beads is that `bd ready` yields something an agent can pick
up and _do_; raw PR feedback doesn't fit that contract.

We want to:

1. **Move feedback storage out of beads** into a pg-pr-owned datastore, so beads
   hold only genuinely actionable work (the PR bead and the "process feedback"
   bead).
2. **Loosen the beads coupling** — pg-pr emits domain events; the beads
   integration becomes one in-process event handler, relocated out of the sync
   engine into its own module.
3. Keep the **existing bead workflow** otherwise intact: feedback is still
   processed into agent-created "work beads," pushes happen, and the cycle
   repeats until merge. The only workflow change is that the feedback-processing
   instructions pull feedback from pg-pr (and mark each item) instead of reading
   `feedback` beads.

## Goals

- A pg-pr-owned **SQLite** datastore is the system of record for PR identity and
  PR feedback (plus disposition + reply state).
- **Library-first, daemon-optional**: the same store/operations are used whether
  pg-pr runs as a one-shot CLI command or as the long-running daemon. No
  operation requires the daemon.
- An **in-process event system** with a **transactional outbox** so handlers
  never fire for a transaction that rolled back.
- The **beads** bead-creation code is relocated into a `beads` event-handler
  module; it no longer creates `feedback` beads.
- An **agent CLI surface** to list feedback and record dispositions, and a
  **reply path** that posts to upstream threads (replacing today's bead-metadata
  reply pipeline).
- A **config-driven agent registry** for identifying bots/agents and the policy
  for responding to them — no third-party bot identities hardcoded in pg-pr
  source.

## Non-Goals (explicitly deferred to later beads)

- Draft-review generation (agents reviewing the diff to produce a review).
- PR enrichment (kind / languages / size / urgency used to pick reviewer
  agents).
- The mine-vs-teammate review split and teammate **attention signals** (need to
  review / approved by others / re-review after post-approval changes).
- A full **`revision` table** (timeline of every observed head SHA with per-
  revision CI summary and "did I review this SHA"). This earns its place with
  the review/attention workflow; for now `head_sha` + `subject_sha` +
  `is_outdated` suffice.

These deferrals are deliberate: this spec is the **storage move**, not a
workflow redesign.

## Architecture

Library-first; the daemon is a scheduler around the same operations the CLI
exposes.

```
┌─ cmd/pg-pr ─────────────┐     ┌─ internal/sync (daemon) ─┐
│  feedback / pr /        │     │  scheduler: loop {       │
│  (existing subcommands) │     │    poll → reconcile →    │
│                         │     │    flush } on a timer    │
└───────────┬─────────────┘     └────────────┬─────────────┘
            │  (both import; neither privileged)
            ▼                                 ▼
        ┌─────────────────── internal/store ───────────────────┐
        │  SQLite (WAL): pull_request, feedback,                │
        │  code_comment_message, outbox                         │
        │  + all read/write ops; transactional outbox enqueue   │
        └───────┬───────────────────────────────┬──────────────┘
                │ dispatch (post-commit)          │ upstream writes
                ▼                                 ▼
        ┌─ handlers (in-process []Handler) ─┐  ┌─ providers/vcs ─┐
        │  beads handler (PR + process-     │  │  post reply,    │
        │  feedback beads, cascade close)   │  │  resolve/        │
        │  reply-poster handler             │──│  minimize thread │
        │  (… future handlers)              │  └─────────────────┘
        └───────────────────────────────────┘
```

- **`internal/store`** owns the SQLite schema and every read/write operation.
  Both the CLI and the daemon import it. SQLite runs in **WAL** mode with a
  `busy_timeout`; write volume is low, so a concurrent ad-hoc CLI invocation and
  a running daemon serialize harmlessly.
- **Driver:** `modernc.org/sqlite` (pure-Go) — pg-pr is currently cgo-free, and a
  cgo driver (`mattn/go-sqlite3`) would break clean cross-compile and complicate
  the gomod2nix build the package CLAUDE.md mandates. Adding the dep requires
  `go mod tidy && gomod2nix generate` + committing `gomod2nix.toml` (phase-1 task).
- **Migrations** are versioned via the `user_version` pragma and run under
  `BEGIN EXCLUSIVE`. A daemon that opens a DB whose `user_version` is newer than
  its binary refuses to write (logs + exits for restart) rather than writing
  against a schema it doesn't understand — guards the "newer CLI migrates while
  older daemon holds the DB" skew.
- The **daemon does nothing the CLI can't** — it runs `poll → reconcile →
flush` on an interval, exactly the operations a one-shot command runs.

### Event system (in-process, not persisted)

- A registered `[]Handler` where `Handler` is roughly
  `func(ctx, Event) error`.
- `dispatch(event)` loops the handlers, **each isolated**: a panic or error in
  one is caught, logged, and the next still runs. No handler can block the
  others.
- Events are **not** a durable log — there is no event_cursor and no replay. The
  only persistence is the outbox row that carries an event across the commit
  boundary.

### Transactional outbox (the rollback fix)

The reason for the outbox: if we dispatched events inline inside a transaction
that later rolled back, handlers would have acted on state that doesn't exist.
So:

1. Begin txn → apply state changes → **insert outbox row(s)** describing the
   event(s) → **commit**.
2. The CLI (or daemon loop) calls the **outbox runner** (`flush`).
3. The runner pulls each `pending` row, dispatches its event to **all current
   handlers**, emits any handler errors, then marks the row `complete`
   **regardless of handler success/failure**.

Properties:

- A rolled-back txn never created the outbox row → no dispatch.
- A crash between commit and dispatch leaves the row `pending` → it's picked up
  on the next run (minimal crash-durability; **not** per-handler retry).
- **Inline auto-flush**: mutating CLI commands run the outbox runner before
  returning (a `--no-flush` escape hatch exists). The daemon flushes on its
  timer. There is one flush code path.

Event types (minimum needed by the handlers): `pr.opened`, `pr.updated`,
`pr.closed`, `pr.merged`, `feedback.created`, `feedback.disposed`,
`feedback.resolved`.

### beads handler module

Today's bead-creation code is scattered in `internal/sync`. It moves into a
`beads` **event-handler module**, registered as one handler:

- `pr.opened` → create the PR bead (the existing `merge-request` bead) and the
  **process-feedback** bead (today's `processing-cycle` bead, titled
  `process-feedback: …`).
- `feedback.created` → ensure an open process-feedback bead exists for the PR.
- `pr.closed` / `pr.merged` → close the PR bead and cascade-close its
  descendants (unchanged from today).
- It **no longer creates `feedback` beads** — those are now `feedback` rows.

The process-feedback bead still spawns the **same work beads** as today — created
by the **agent**, not pg-pr. The only workflow change is the feedback-processing
instructions: pull feedback from pg-pr (`pg-pr feedback …`) and mark each item,
instead of reading `feedback` beads.

## Data model (SQLite)

### `pull_request` (authoritative)

Decision A: the DB row is authoritative for PR state; the beads handler
_projects_ the PR bead from events.

| column                     | notes                                            |
| -------------------------- | ------------------------------------------------ |
| `id`                       | integer PK                                       |
| `repo`                     | `owner/name`                                     |
| `number`                   | PR number; `UNIQUE(repo, number)`                |
| `ownership`                | `mine` \| `team`                                 |
| `author`                   | GitHub login                                     |
| `state`                    | `open` \| `draft` \| `closed` \| `merged`        |
| `branch`, `base`           | head / base branch                               |
| `url`                      | PR URL                                           |
| `head_sha`                 | current head commit — load-bearing for staleness |
| `last_synced_at`           | RFC3339                                          |
| `created_at`, `updated_at` |                                                  |

### `feedback` (single table, `kind` discriminator)

One table for all five kinds. Common lifecycle (`ingested → dispositioned →
replied → resolved`) with a small type-specific tail. **CHECK constraints keyed
on `kind`** enforce required type-specific fields.

**Common columns**

| column                                    | notes                                                                                            |
| ----------------------------------------- | ------------------------------------------------------------------------------------------------ |
| `id`                                      | integer PK — single id space (outbox/events reference this)                                      |
| `pr_id`                                   | FK → `pull_request.id`                                                                           |
| `kind`                                    | `code-comment-thread` \| `pr-comments` \| `ci-failure` \| `review-request` \| `jira-link`        |
| `external_id`                             | upstream id (thread node id / issue-comment rest id / CI run id / requested reviewer / Jira key) |
| `fingerprint`                             | dedup; `UNIQUE(pr_id, fingerprint)`; **policy is per-kind** (below)                              |
| `status`                                  | `new` \| `presented` \| `dispositioned` \| `replied` \| `resolved` \| `superseded`               |
| `title`, `body`                           |                                                                                                  |
| `subject_sha`                             | revision the item pertains to; **nullable** (see "Revision & staleness")                         |
| `is_outdated`                             | bool; **code-comment-thread only** (GitHub `isOutdated`)                                         |
| `is_minimized`                            | bool; any comment kind (GitHub `isMinimized`)                                                    |
| `minimized_reason`                        | e.g. `OUTDATED` / `RESOLVED` (GitHub `minimizedReason`)                                          |
| `created_at`, `updated_at`, `resolved_at` |                                                                                                  |

**Author classification** (on `feedback` and on `code_comment_message`)

| column         | notes                                                                                                               |
| -------------- | ------------------------------------------------------------------------------------------------------------------- |
| `author_login` | raw GitHub login                                                                                                    |
| `author_kind`  | `human` \| `agent` — the clear human/agent distinction                                                              |
| `agent_name`   | nullable; when `agent`: `claude-review` \| `coderabbit` \| `copilot` \| `sonar` \| `pg-pr` \| `other` (from config) |
| `is_ours`      | bool; **marker-detected**, NOT login-based (see "is_ours")                                                          |
| `author_role`  | human relationship: `self` \| `team_member` \| `org_member` \| `external` (meaningful when `author_kind=human`)     |

**Disposition + reply** (repliable/actionable kinds)

| column               | notes                                                                                               |
| -------------------- | --------------------------------------------------------------------------------------------------- |
| `disposition_action` | `will-fix` \| `wont-fix` \| `no-action` (null until dispositioned)                                  |
| `disposition_note`   | reasoning                                                                                           |
| `reply_body`         | text queued to post upstream                                                                        |
| `response_id`        | upstream id after posting — idempotency marker                                                      |
| `severity`           | nullable: `critical` \| `warning` \| `suggestion` (parsed from known bot markers)                   |
| `managed_upstream`   | bool; the source manages its own state (e.g. `github-actions[bot]`) — do not reply/minimize/resolve |

**Type-specific tail (nullable; enforced by `CHECK` on `kind`)**

| kind                  | columns                                                                |
| --------------------- | ---------------------------------------------------------------------- |
| `code-comment-thread` | `file` (NOT NULL for this kind), `line` (nullable), `thread_resolved`  |
| `pr-comments`         | `comment_node_id`, (content in `body`)                                 |
| `ci-failure`          | `run_id`, `check_name`, `conclusion`, `related`, `retry_count`, `link` |
| `review-request`      | requested reviewer in `external_id`                                    |
| `jira-link`           | Jira key in `external_id`, URL in `body`                               |

If the nullable tail ever feels sprawling, the rarest fields can move to a JSON
`attrs` column — but `file`/`line` and CI's `related`/`retry_count` stay real
columns because they're filtered/updated.

### `code_comment_message` (thread context)

The only genuine one-to-many: the messages of a `code-comment-thread`. This is
the conversation context surfaced to agents. `pr-comments` are flat (content in
`feedback.body`); ci/review-request/jira have no messages.

| column                                                                | notes                                                            |
| --------------------------------------------------------------------- | ---------------------------------------------------------------- |
| `id`                                                                  | integer PK                                                       |
| `feedback_id`                                                         | FK → `feedback.id` (kind must be `code-comment-thread`)          |
| `external_id`                                                         | comment `databaseId`/node id; `UNIQUE(feedback_id, external_id)` |
| `author_login`, `author_kind`, `agent_name`, `is_ours`, `author_role` | same author classification                                       |
| `body`                                                                |                                                                  |
| `posted_at`                                                           |                                                                  |

### `outbox`

| column                       | notes                   |
| ---------------------------- | ----------------------- |
| `id`                         | integer PK              |
| `type`                       | event type              |
| `payload`                    | JSON                    |
| `status`                     | `pending` \| `complete` |
| `created_at`, `completed_at` |                         |

## Revision & staleness

Feedback attaches to a _point in the PR's history_, and the head moves. An
outdated item carries less weight than an active one.

- **`pull_request.head_sha`** — current head.
- **`feedback.subject_sha`** — the revision the item pertains to. **Availability
  is per-kind:**
  - `code-comment-thread`: authoritative (review comment `original_commit_id` /
    current `commit.oid`).
  - `ci-failure`: authoritative (the run's head SHA).
  - `pr-comments`: **not anchored** to a SHA by GitHub — record a best-effort
    `first_seen_head_sha` proxy; treat as informational.
  - `review-request` / `jira-link`: n/a (null).
  - Note: after a force-push GitHub may GC old commits, so `subject_sha` can
    point at an unreachable commit. It is for **comparison/weighting, not `git
checkout`**.
- **Staleness is sourced per-kind** — do **not** derive code-thread staleness
  from our own SHA equality (a rebase rewrites every SHA and would falsely mark
  live threads stale):
  - `code-comment-thread`: trust GitHub's `is_outdated` (already rebase-aware —
    GitHub re-anchors threads to the new diff).
  - `ci-failure`: `active` ⟺ `subject_sha == pull_request.head_sha`; older rows
    → `superseded`.
  - `pr-comments`: never auto-outdate; weight signal is `is_minimized` /
    `minimized_reason` (see below).

### Two kinds of "outdated"

GitHub has two distinct mechanisms and we track both:

1. **Automatic, diff-driven `is_outdated`** — `PullRequestReviewThread.isOutdated`;
   **code threads only**; set when the anchored hunk changes.
2. **Manual/programmatic "minimized"** — `minimizeComment(classifier: OUTDATED)`
   sets `is_minimized=true`, `minimized_reason="OUTDATED"` on **any** comment
   (including top-level PR comments). This is a human/bot action, not GitHub
   auto-outdating. It's frequently a _tool's_ marker that the comment was handled
   (e.g. silver-bullet minimizes addressed comments as `OUTDATED`).

So a top-level PR comment showing "outdated" is `is_minimized`, not `is_outdated`.

### Fingerprint policy is per-kind

| kind                  | fingerprint                                                 | revision behavior                                                                |
| --------------------- | ----------------------------------------------------------- | -------------------------------------------------------------------------------- |
| `code-comment-thread` | revision-_stable_ (thread node id / path + normalized body) | one persistent row across force-pushes; `is_outdated` tracks staleness           |
| `pr-comments`         | revision-stable (comment id)                                | rarely stale; `subject_sha` informational                                        |
| `ci-failure`          | revision-_scoped_ (`check_name` + `subject_sha`)            | **distinct row per (check, revision)** = build-failure history; active = on head |
| `review-request`      | per requested reviewer                                      | n/a                                                                              |
| `jira-link`           | per Jira key                                                | n/a                                                                              |

CI history is tracked via per-`(check, sha)` rows (no new child table). If we
later want per-run history within a single SHA (retries/reruns), add a `ci_run`
child symmetric with `code_comment_message`.

## `is_ours` — distinguishing our output from the human's

**Trap:** pg-pr posts under the _user's_ GitHub login (it uses the user's `gh`
credentials), so `author_login == self` can be either the user typing or pg-pr
posting on their behalf — and `__typename` is `User` for both. Login cannot tell
them apart.

**Fix:** `is_ours` is **marker-detected**, not identity-detected.

- `is_ours = true` ⟺ the body carries pg-pr's marker (`internal/marker`,
  ideally an invisible HTML comment like `<!-- pg-pr -->`).
- **Hard ingestion invariant:** everything pg-pr or an agent posts is stamped
  with the marker. An unmarked comment from `self` is treated as the **human's
  own feedback**.

**This is net-new work and must be sequenced carefully.** Today:

- The only marker is the emoji glyph `🤖` (`internal/marker`), and it's applied
  **only in `cmd/pg-pr/review.go`** — the reply pipeline (`processReplyDrafts` /
  `ReplyToThread`) posts **unmarked** bodies. So pg-pr's own replies currently
  violate the invariant.
- Ingestion does **not** filter self/our comments at all (`commentEvent` ingests
  every comment) — the `is_ours` skip is brand-new behavior.

Therefore the plan must:

1. Move the marker to an invisible HTML comment (`<!-- pg-pr -->`) in
   `internal/marker` and **stamp it on every posted body** (replies,
   resolve-comments, agent posts), not just `review.go`.
2. **Sequence marker-stamping before (or atomic with) the ingest `is_ours` skip**
   — otherwise the cutover ingests our own replies as "human feedback."
3. **Transition window:** ingestion recognizes **both** the legacy `🤖` glyph and
   the new HTML marker, so pg-pr comments posted before the switch aren't
   re-ingested as human feedback after it.

Resulting classification (the self-authored-note case is first-class):

| Author                               | `author_kind` | `author_role` | `is_ours` | Treatment                                                                 |
| ------------------------------------ | ------------- | ------------- | --------- | ------------------------------------------------------------------------- |
| CodeRabbit / Copilot / claude-review | `agent`       | —             | false     | ingested as (agent) feedback                                              |
| **You, manual note on your own PR**  | `human`       | `self`        | **false** | **ingested as must-address feedback**                                     |
| pg-pr / your agent posting as you    | `agent`       | —             | **true**  | **skipped on ingest** (loop guard); recorded as our reply (`response_id`) |
| Teammate                             | `human`       | `team_member` | false     | ingested as feedback                                                      |

So your manual review notes flow in as genuine feedback; the processing agent
addresses them, replies _with the marker_, and that reply is recognized as ours
and never bounces back.

**Two-layer detection** (matches the idempotency pattern):

- **Primary (durable):** the marker on the body — survives a DB rebuild.
- **Secondary (fast):** when we post, record the resulting comment/`response_id`
  locally.

Edge case (noted, accepted): if the user edits one of our comments and strips
the marker, the local id-set still catches it; only a stripped marker **and** a
lost DB would risk a re-ingest.

## Ingestion classifier

When the sync engine reads upstream surfaces, it classifies each item before
upserting a `feedback` row:

- **`author_kind`** from GraphQL `author { __typename }`: `Bot`/`Mannequin` →
  `agent`; `User` → `human`.
- **`agent_name`** and **`managed_upstream`** from the **config-driven agent
  registry** (below), matching login + optional body-marker regex.
- **`is_ours`** from pg-pr's marker (source constant) — see above.
- **`severity`** parsed from a per-agent marker/regex when configured (e.g.
  `**critical**:` prefix on claude-inline comments).
- **`subject_sha`** / **`is_outdated`** / **`is_minimized`** / `minimized_reason`
  per the rules above.
- **`fingerprint`** per the per-kind policy.

## Config-driven agent registry

The set of bots (CodeRabbit, Copilot, claude-review, …) and how to respond to
them is **configuration, not source** — `phillipgreenii-nix-agent-support` is a
standalone package and must not hardcode org-specific bot identities. The only
hardcoded marker is pg-pr's _own_ (its identity; the "no other choice"
exception).

This **extends the existing `agents:` config block** and `internal/agentregistry`
(which already classifies agent-vs-human and matches approval regexes for the
dashboard). Today `agentregistry.Entry` is `{Login, ApprovalRegex}`; we extend
it:

```yaml
# config.yaml (generated by phillipg-nix-ziprecruiter → pg-pr-zr module)
agents:
  - login: "coderabbitai[bot]"
    agent_name: coderabbit
    # identification (optional body marker when login is ambiguous, e.g.
    # github-actions[bot] hosts multiple tools):
    body_marker: ""
    approval_regex: "..." # existing dashboard use
    policy:
      ingest: true
      managed_upstream: false
      reply: true
      resolve: true
      minimize: true
      default_severity: warning
  - login: "github-actions[bot]"
    agent_name: claude-review
    body_marker: "<!-- claude-pr-review -->"
    policy:
      managed_upstream: true # manages its own state — don't reply/minimize
      ingest: true
      default_severity: warning
```

- **Identification:** `login` (glob) + optional `body_marker` regex → `agent_name`.
- **Response policy:** `ingest`, `managed_upstream`, `reply`, `resolve`,
  `minimize`, `default_severity` (extensible).
- **Where it lives / how generated:** ZR-specific entries are authored in the
  `phillipg-nix-ziprecruiter` `pg-pr-zr` module and generated into the config
  location pg-pr already loads (`$PG_PR_CONFIG` / `$XDG_CONFIG_HOME/pg-pr/config.yaml`
  / `~/.config/pg-pr/config.yaml`).
- **Source fallback (standalone):** an item whose author is a `Bot`/`[bot]` login
  with no registry match is `author_kind=agent`, `agent_name=other`, with a
  conservative default policy (ingest=true, advisory severity). pg-pr works
  without any `agents:` config; the registry only sharpens behavior.

## Agent & human interfaces (CLI)

- `pg-pr feedback list <pr> [--json] [--active] [--kind=…]` — list feedback for
  a PR; `--active` filters out `is_outdated`/`is_minimized`/`superseded`. Surfaces
  `author_kind`/`agent_name` so the processing agent (and the human reviewer) can
  weight human vs agent.
- `pg-pr feedback show <id> [--json]` — one item **plus its thread and every
  message** (`code_comment_message`) — full conversation context.
- `pg-pr feedback disposition <id> --action=will-fix|wont-fix|no-action
--note="…" [--reply="…"]` — record the disposition; optionally queue a reply.
  Writes the DB row + enqueues the outbox event; inline flush dispatches it.

The feedback-processing instructions (a separate prompt/bead, lightly updated)
change from "read `feedback` beads" to "`pg-pr feedback list` → mark each item."

## Upstream side-effects (reply path)

Replaces today's bead-metadata reply pipeline (`reply_draft` → `response_id`).

- A queued `reply_body` is posted by the **reply-poster handler** (or the
  daemon's flush), targeting the **thread** (`external_id`) for thread kinds, or
  the comment for `pr-comments`.
- **Reply-before-resolve** (silver-bullet pattern): never resolve/minimize a
  thread silently — post a reply with reasoning, _then_ resolve. Best-effort:
  log and continue on failure.
- **Disposition → reply phrasing** contract (adopted from silver-bullet):
  `will-fix` → "Fixed in `<sha>` — …", `wont-fix` → "Not acting — …",
  `no-action` → "Noted — …".
- **Marker stamping** is mandatory on every posted body (so `is_ours` works) —
  see "Marker" below; this is net-new on the reply path.
- **Idempotency:** `response_id` set after posting; `managed_upstream` sources
  are skipped (never reply/minimize/resolve).
- **Durability is the reconcile loop, not outbox replay.** A queued reply that
  fails to post is _not_ lost: each reconcile re-scans `feedback` rows where
  `reply_body` is set and `response_id` is unset and re-attempts the post (this
  is exactly today's `ListFeedbackPendingReply` pattern, `pkg/beads/feedback.go`).
  The outbox is fire-once best-effort _dispatch_; the durable delivery guarantee
  for replies lives in the reconcile re-scan, with `response_id` as the
  idempotency marker.

## Reusable patterns from silver-bullet

`/Volumes/ziprecruiter/pristine/.claude/commands/git/silver-bullet*` is a mature
single-PR review-fix-ship loop. We borrow:

- **GraphQL/REST shapes — but this is net-new provider work, not a verbatim
  lift.** The silver-bullet bash is the reference for the _shapes_, but the Go
  provider does **not** fetch most of what this design needs today: `enrich.go`'s
  `reviewThreads` block selects only `id`/`isResolved`/comment fields — **no
  `isOutdated`, no `isMinimized`/`minimizedReason`, no per-thread
  `originalCommit.oid`** (it fetches only the last commit's `oid`). And the Go
  provider has `resolveReviewThread` but **no `minimizeComment` mutation /
  `ReportedContentClassifiers`**. So a dedicated **provider sub-phase** (ahead of
  ingestion) must extend `enrichedPRsQuery` with `isOutdated` +
  `isMinimized`/`minimizedReason` + per-thread `originalCommit { oid }`, and add a
  `minimizeComment` mutation. `resolveReviewThread` and the issue-vs-inline
  endpoint split already exist and are reused as-is.
- **Source/severity classification** at ingestion (`<!-- claude-inline -->`
  markers, `**<severity>**:` prefix, bot-vs-human-vs-self) → our `agent_name` +
  `severity`.
- **Revision-stable fingerprint** for comments (match across force-pushes by
  path + description, not the mutable comment id).
- **CI `related` / `retry_count`** columns (auto-retry an unrelated flaky check
  once before treating it as feedback).
- **`managed_upstream`** handling (`github-actions[bot]` manages its own state).
- **Two-layer idempotency** (durable marker + local record).
- **Reply-before-resolve** and the disposition→phrasing table.

We do **not** reuse the bash scripts; pg-pr's provider layer is the equivalent.

## Migration

From bead-stored feedback to the SQLite store:

1. **Schema + store first** (no behavior change): create the DB, migrations,
   and `internal/store`.
2. **Backfill + cutover:** ingestion writes `feedback` rows; the beads handler
   stops creating `feedback` beads. Existing open `feedback` beads are
   **backfilled** into `feedback` rows (by fingerprint + parent PR) so nothing
   in flight is lost, then closed with reason `migrated-to-store`. The backfill
   must **remap the kind taxonomy**: today's `FeedbackKind` values
   `comment-thread` → `pr-comments` and `review-thread` → `code-comment-thread`
   (`ci-failure`/`review-request`/`jira-link` are unchanged). Verify the reply
   pipeline's kind-gating (currently keyed on `comment-thread`/`review-thread`)
   is translated to the new `pr-comments`/`code-comment-thread` set.
   See "Marker" for the legacy-`🤖`/new-marker transition window that must run
   during this cutover.
3. The existing `merge-request` bead and `processing-cycle` bead map to the PR
   bead and process-feedback bead — **unchanged**; the beads handler keeps
   creating them.
4. The reply pipeline's `reply_draft`/`response_id` bead metadata migrates to
   `feedback.reply_body`/`response_id`.
5. **Column-cap relief:** the bd `description` TEXT cap (~64 KB) that forced
   truncation of CodeRabbit summaries no longer applies; SQLite TEXT holds the
   full body.

A migration is reversible by re-exporting `feedback` rows to beads if needed
(not expected).

## Error handling & concurrency

- **SQLite WAL + `busy_timeout`**: concurrent ad-hoc CLI + daemon serialize;
  low write volume makes contention a non-issue.
- **Outbox crash-recovery**: a `pending` row left by a crash between commit and
  dispatch is picked up next run.
- **Handler isolation**: a failing handler is logged; siblings still run; the
  outbox row completes regardless (no per-handler retry by design).
- **At-least-once → every handler MUST be idempotent.** A crash after a handler
  acts but before the outbox row is marked `complete` re-dispatches the event.
  The reply-poster is idempotent via `response_id`. The beads handler must reuse
  the existing upserts: `EnsureMergeRequest` (idempotent) and
  `FindOpenProcessingCycle` — and the relocated handler **must preserve
  `FindOpenProcessingCycle`'s error-propagation** (it must not swallow dep-query
  errors as "no open cycle," which is the documented root cause of the
  duplicate-cycle bug — 48 cycles for 27 PRs).
- **Best-effort upstream**: reply/resolve/minimize failures are logged and
  retried on the next reconcile (the row stays un-`replied`/`response_id` unset).

## Testing strategy

- **Store unit tests** against a temp SQLite file (per-test DB); exercise
  upsert/dedup (per-kind fingerprint), staleness reconcile, CHECK constraints.
- **Outbox tests**: rollback → no dispatch; commit → dispatch; crash (no flush)
  → pending → picked up; handler error → row still completes, siblings still run.
- **Classifier tests**: `author_kind`/`agent_name`/`is_ours` (marker vs self
  login), `is_outdated` vs `is_minimized`, severity parsing — table-driven.
- **beads handler tests**: reuse the existing injectable `Runner` (drive `bd`
  wrappers without a real workspace); integration against a disposable `bd init
--reinit-local --prefix=tN` workspace, matching the current
  `pkg/beads` test approach.
- **VCS**: a fake provider (existing pattern in `internal/sync`) feeds upstream
  surfaces; assert the resulting `feedback` rows + outbox events.

## Phasing

1. **Store + migrations + driver** (`internal/store`): add `modernc.org/sqlite`
   (`go mod tidy && gomod2nix generate`, commit `gomod2nix.toml`); schema for
   `pull_request`, `feedback`, `code_comment_message`, `outbox`; versioned
   migrations (`user_version`, `BEGIN EXCLUSIVE`, version-skew guard). No behavior
   change.
2. **Event system + transactional outbox** + inline flush; relocate the beads
   bead-creation code into the `beads` handler module (preserving
   `EnsureMergeRequest`/`FindOpenProcessingCycle` idempotency + error-propagation).
3. **Provider extensions** (net-new VCS work, from review B1): extend
   `enrichedPRsQuery` with `isOutdated`, `isMinimized`/`minimizedReason`,
   per-thread `originalCommit { oid }`; add a `minimizeComment` mutation.
4. **Marker**: switch `internal/marker` to the invisible HTML marker and stamp it
   on **every** posted body (reply path included); ingestion recognizes both the
   legacy `🤖` and the new marker (transition window). Must land **before** the
   ingest `is_ours` skip.
5. **Ingestion → store**: sync writes `feedback` rows (classifier incl.
   `is_ours` skip, per-kind fingerprint, staleness); stop creating `feedback`
   beads; backfill + kind-remap existing ones.
6. **Reply path**: move `reply_body`/`response_id` to the store; reply-poster
   handler; reply-before-resolve; reconcile re-scan for durable delivery.
7. **Agent CLI**: `pg-pr feedback list/show/disposition`; update the
   feedback-processing instructions to pull from pg-pr.
8. **Config-driven agent registry**: extend `agentregistry.Entry` + the `agents:`
   block; generate ZR entries from the `pg-pr-zr` module; source fallback.

## Design-review resolutions (2026-06-23)

Folded in from the subagent design review:

- **SQLite driver** = `modernc.org/sqlite` (pure-Go, preserves no-cgo +
  gomod2nix). [§Architecture, §Phasing 1]
- **Provider fields are net-new**, not a verbatim lift — dedicated provider
  sub-phase. [§Reusable patterns, §Phasing 3]
- **Marker** is currently emoji-only and reply-path-unmarked; switch to HTML
  marker, stamp on all posts, transition window matches both, sequenced **before**
  the `is_ours` ingest skip. [§is_ours, §Phasing 4]
- **Reply durability** = reconcile re-scan of `feedback` rows, not outbox replay;
  outbox is fire-once. [§Upstream side-effects]
- **At-least-once outbox** → all handlers idempotent; preserve
  `FindOpenProcessingCycle` error-propagation (duplicate-cycle bug). [§Error
  handling]
- **Kind remap** in backfill (`comment-thread`→`pr-comments`,
  `review-thread`→`code-comment-thread`). [§Migration]
- **Migrations** versioned + `BEGIN EXCLUSIVE` + daemon version-skew guard.
  [§Architecture]

## Open questions

- Exact `Handler` signature and registration point (CLI vs daemon wiring) — the
  design's central "one dispatch path" claim depends on it; settle in the plan.
- Whether the `ci_run` child table is needed now (per-run history within a SHA)
  or deferred until requested.
- Whether `pr-comments` ever need a logical "thread" grouping for bot reply
  chains (CodeRabbit), or stay flat — flat for now.
- **id space per kind:** use upstream **node ids** uniformly (GraphQL + REST both
  expose `node_id`), not REST `databaseId`, for `external_id` /
  `code_comment_message.external_id`.
- **`subject_sha` GraphQL cost:** adding per-thread `originalCommit { oid }` to
  the enriched query — confirm it stays within the node budget or needs a REST
  fallback.
- **`Mannequin` author type** is fetched but unhandled today; classifier should
  map it (→ `agent`).

## Related Decisions

- See also: `phillipgreenii-nix-agent-support` docs/superpowers/specs/2026-05-19-pg-pr-design.md
  (original pg-pr design).
- See also: `phillipgreenii-nix-agent-support` docs/superpowers/specs/2026-05-27-pg-pr-team-pr-readonly-design.md
  (team-PR read-only handling).
- See also: `phillipgreenii-nix-agent-support` docs/superpowers/specs/2026-06-09-fingerprint-driven-sync-design.md
  (fingerprint-driven daemon sync).
