# Daily focus, store-first: phase 15 of the pg-desk/connector program

- **Date**: 2026-09-23 (revised 2026-10-06 against the landed entity change flow)
- **Status**: Draft — the ranking model, candidate set, epic slot rule and rank placement (section 6,
  D-F11) were ruled by the operator on 2026-10-05, and the minting path (D-F12 to D-F16) on
  2026-10-06; the rest of the document is pending operator review and is NOT approved
- **Bead**: `pg2-2j5ac.27` (this design's own tracking bead; phase 15's decompose-trigger is
  `blocked-by` it)
- **Depends on**: the entity change flow (`docs/superpowers/specs/2026-09-29-entity-change-flow-design.md`,
  ADR 0077) and its cutover release. This document targets the **post-cutover** world: pg-desk's
  `ledger` table, `sync.mode`, `internal/sync`, `pg-desk run` and heartbeat are removed by that
  cutover and MUST NOT be built on. The issue-entity pipeline that an earlier revision treated as an
  unlanded external prerequisite (`pg2-2j5ac.46`, closed) has landed (section 4.1).
- **Amends**: `phillipgreenii-nix-agent-support`'s
  `docs/superpowers/specs/2026-09-09-pg-desk-and-connector-discovery-design.md` (D19, D26, phase
  15's row in its phase table — "designed in its own document", checkpoint "defined by that
  document"; this is that document) and the private machine flake's
  `2026-09-02-daily-focus-v2-design.md` (decides what of v2 survives — answer: `df-attention`/
  `df-search`, untouched; everything else in v2's `df-survey`/`df-deferred`/`df-wire`/`df-pull`/
  `df-resolve-focus`/`df-find-pr-bead`/`df-split-blockers` family retires; the map is section 3's retirement map and the list is section 10). It also asks
  for one amendment to the entity change flow design and ADR 0077 (D-F14).

## 1. Purpose and scope

Phase 15 retires daily-focus's beads-as-primary-store architecture (v2, fully implemented and
live in the private machine flake today) in favor of the store-first pattern the rest of the
pg-desk/connector program already uses: everything surveyed lands uncapped in `pg-desk`'s SQLite
store; focus ranking is a compute-only, read-time view over `entity` rows; a decider mints beads
only where an agent actually needs one; "pulling more work" is selecting more from the store, not a
separate mechanism (D19, D26).

This document is scoped to daily-focus's own retirement (`df-survey`, `df-deferred`, `df-wire`,
`df-verify-wiring`, `df-pull`, `df-resolve-focus`, `df-find-pr-bead`, `df-split-blockers`,
`df-close-focus`, and the three command files that orchestrate them). It does not touch `df-attention`/`df-search`/`df-categorize`/`df-feedback`
(already retired into `pg-desk` in earlier phases per D10, or — for attention/search — deliberately
unrelated per the v2 doc's own §0 cross-reference) or any PR-side pg-desk behavior.

## 2. Decisions ledger

Operator rulings from the 2026-09-23 design session that produced this document, D-F11 from the
2026-10-05 ranking session (which supersedes D-F7), and D-F12 to D-F14 from the 2026-10-06 revision
session that reconciled this document with the landed entity change flow, and D-F15 and D-F16 from the
review that followed it. D-F7 was made in the
operator's absence (a 10-minute `AskUserQuestion` timeout) on the session's best judgment, per
precedent already set twice earlier in the same session; it is superseded, kept for provenance.

| #     | Decision                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| ----- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| D-F1  | Daily-focus's PR candidate gathering reuses pg-desk's existing `pr-mine`/`pr-team` watch queries unchanged. Issue-type candidates (Jira issues, epics, bd tasks) are persisted by the same pull-through that serves PRs: pg-desk lists each name in `watch.issue.queries` with `pg-connector issue list --fingerprints`, hydrates added or changed entities through `issue show`, and persists them (`docs/behavior/pg-desk/changes.md`; the generic issue-entity pipeline of closed bead `pg2-2j5ac.46`, landed). This document therefore adds NO new gather code; it adds three named queries to `watch.issue.queries` (section 4.2). An earlier revision of this document treated issue-entity gather as an unlanded external prerequisite; that framing is retired.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| D-F2  | `pg-connector-pr-github`'s `mine` query stays scoped to the single repository the deployment configures (one repo), narrowing daily-focus's PR survey from v2's cross-repo author search. Recorded loss, same pattern as the design of record's D15. The repository itself is deployment configuration and is not named here.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| D-F3  | `df-deferred`'s description-marker-section mechanism retires entirely. "Deferred" is derived: candidates the store already holds that are not `focus_selection`-selected for the period. No description grammar, no lost-update hazard, no size cost.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| D-F4  | The per-day "focus bead" (one bead with wired blocking deps) retires. "Today's focus" becomes a `pg-desk` view/query (`focus_selection`/`focus_period`, §5), generalizing to week/sprint via a `period_type` column rather than a new bead type per level (not built this phase — schema-ready only). Beads stay reserved for actual agent-workable signals (D8), never for human plan-tracking.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| D-F5  | Gate replies and pull selections reference candidates by their own `(entity_type, entity_id)` — the same key already exists for a PR/Jira/bead item — never a derived positional handle. `focus show`/`focus select`/`focus pull` always recompute live from current `entity`/`interpretation` rows; nothing is cached or frozen between a `show` and the `select --apply`/`pull` that follows it, so a priority or due-date change is never hidden from the operator.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| D-F6  | `focus_selection` and `focus_period` (§5) use an internal surrogate primary key (`id INTEGER PRIMARY KEY AUTOINCREMENT`) with a `UNIQUE` constraint carrying the natural key, diverging deliberately from every existing pg-desk table, which all use a composite natural-column primary key with no surrogate. `focus_selection` references `focus_period` by its surrogate `id` (a real foreign key, not a repeated `period_type`/`period_key` pair) and references `entity` by its own composite key (`repo, entity_type, entity_id`) — `entity` already is the table that establishes an `(entity_type, entity_id)` pair is valid, so `focus_selection` gets that validation from a real foreign key rather than untyped text columns.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| D-F7  | **SUPERSEDED 2026-10-05 by D-F11 (candidate set and epic slot rule).** Original text, kept for provenance: epic candidacy narrows to "owned by me AND has an open/in_progress child" — the "OR a recently-closed child" half of today's rule is dropped as a recorded loss, because expressing it would need a per-epic follow-up query the static named-query model (§4) cannot do. **Made in the operator's absence.**                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| D-F8  | **DONE** (bead `pg2-t9zzg`, closed). `schema.Issue` gained an `Owner` field (bd's `owner` key — the responsible human), distinct from `Assignee`, which carries bd's claim/actor identity, not ownership; and pg-desk's issue ownership classifier reads it through the configured self-owner identity. "Assigned or owned by the operator" (D-F11) therefore reuses `interpretation.ownership` for issues; nothing new is built for it here.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| D-F9  | There is no separate `focus split` verb. `pg-desk focus show` resolves each already-selected row's associated bead and current status unconditionally, folded into its existing per-item output, from the entity's view: the linked work item appears in the view's `links[]` with its state, labels, metadata and assignee. The link exists because the minted bead's own metadata names the source entity (D-F13), which the generic source-entity extractor recognizes; no tracker call is made at `show` time. The watch queries MUST cover the minted beads (a watched bead is one some `watch.issue.queries` name lists; section 4.2), with one nuance recorded in section 4.2: a CLOSED focus bead leaves the open-beads query and reaches `links[]` only through the change flow's removal-confirmation read. `close.md`'s survey step becomes a `focus show` call, not a dedicated verb.                                                                                                                                                                                                                                                                                                                                                                                            |
| D-F10 | `focus close`'s per-bead progress note is appended by `close.md` itself via `pg-connector issue comment` (→ `bd comment`), not by pg-desk and not via today's `bd update --append-notes` (→ bd's separate NOTES field), followed by `pg-desk issue refresh <bead>` so the store sees the write (change-flow S10: external writes go straight through pg-connector by the actor, then refresh). pg-desk MUST NOT execute tracker write verbs (G5). Comments are the better mechanism for this content: bd captures `created_at`/`author` on each comment natively, where NOTES is one unstructured, unbounded-growth text field the caller must manually date-tag (exactly what `df-close-focus.sh`'s `[daily-focus <date>] <progress>` prefix exists to work around). The `[daily-focus <date>]` tag's PURPOSE splits in two — recording that a bead was part of a day's focus (now redundant; `focus_selection` already durably and queryably records this) vs. carrying forward what actually happened for whoever reads the bead next (not redundant; pg-desk's store holds no narrative text). Only the first purpose retires.                                                                                                                                                           |
| D-F11 | **Ranking model, ruled by the operator 2026-10-05** (focused ranking session; supersedes D-F7 and the v2 ranking ported unchanged). Candidate set: everything non-done ASSIGNED to the operator (authored or assigned PRs, assigned Jira issues, beads assigned to or owned by the operator), with no started/ownership filter that hides an item. Slot rule: an epic and its children never use more than one slot; a child takes the slot and the epic is listed only when it is incomplete with no open child. Started: bead in_progress, Jira In Progress category, any open assigned PR. Rank: strict lexicographic tiers, no weights: overdue first (started first, then most overdue), then started, then not started; inside the started and not-started tiers the keys are a due date inside the 7-day horizon, then unblocks, then priority, then age. Placement: the rank is a read-time pure computation inside pg-desk, recomputed on every `focus show/select/pull`; a router-triggered idempotent decider only mints beads for the selected items (this replaces the provisional 2026-10-02 answer that a decider computes the rank). The head-to-head evidence is in section 6.                                                                                              |
| D-F12 | **Selection reaches the minting decider as an annotation (operator, 2026-10-06).** `focus select` and `focus pull` write the `focus_selection` row AND an annotation `focus_selected=<period_key>` on each selected entity (`pg-desk <type> annotate`, origin `pg-desk`). The annotation emits `annotation_changed`, which every decider already subscribes to, so no new change source and no change to the change-flow change-kind catalogue is needed. The table remains the period history and the foreign-key-guarded record; the annotation is the single carrier the decider reads. A struck selection sets the annotation to `none` (section 5).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| D-F13 | **A focus bead is one bead per source entity (operator, 2026-10-06).** The decider's work-item kind is `focus-item`, with dedup key `<entity_type>:<entity_id>:focus-item` and NO per-day suffix, consistent with D-F4 (no per-day bead). The change-flow same-context rule applies, with the context being the source entity: a closed focus bead is never recreated, a bead the decider itself withdrew on a strike is reopened on a reselect, and a bead a worker closed is left closed (section 8); a second bead is never minted. The bead's metadata carries neutral `source_type` and `source_id` fields naming the source entity, which a NEW generic source-entity metadata extractor turns into a link of a distinct relation (`focus`) (change-flow S25: "derived whenever an entity's own data names the other"). The extractor carries no focus vocabulary, so it passes the G5 guard, and it MUST NOT reuse the `repo`+`pr_number` fields: the existing issue extractor reads only those and `parent`, and a reused `repo`+`pr_number` would yield a `work` link that the PR decider treats as that PR's own work item. A strike withdraws the bead and a reselect reopens a bead the decider withdrew (section 8). An epic needs no bead; its entity id already is a bead id. |
| D-F14 | **Amend the change-flow design and ADR 0077 (operator, 2026-10-06).** The change-flow design's decoration-versus-decision classification (section 2; its closing sentence reads "Cross-entity judgment that chooses an outcome (for example daily-focus ranking) is a decision, not a decoration"), together with S2 and G5, puts daily-focus ranking in a decider. The 2026-10-05 ruling (D-F11) places the rank in pg-desk as a read-time view. The amendment records: a rank that is a read-only, side-effect-free view over stored facts — it writes nothing and mints no work — MAY live in pg-desk; only MINTING work from a selection is a decision, and it lives in the decider (D-F12, D-F13). Without it, a change-flow conformance review would reject `internal/focus` in pg-desk. Recorded as ADR 0085, which extends ADR 0081's read-time rule (attention) to this rank, with a pointer in ADR 0077 and in the change-flow design's section 2; all three land in the same commit series as this document.                                                                                                                                                                                                                                                                      |
| D-F15 | **The selection is a reserved view member (operator, 2026-10-06, later in the review session).** The `focus_selected` annotation is surfaced to deciders as a dedicated `annotations.focus_selected` member of `show`'s composite view (and of pg-decider's view reader), beside `ready_to_land`; the decider namespace, documented as written by deciders, is not reused, and no generic other-keys map is added.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| D-F16 | **A strike withdraws the focus bead (operator, 2026-10-06, later in the review session).** When an item is struck after its focus bead exists, the decider closes the bead if it is open and unclaimed, with the withdrawn marker, and only comments if a worker holds it. A reselect reopens a bead the decider withdrew. This reverses the earlier draft's "the decider never closes on a strike".                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |

## 3. Architecture overview

```mermaid
flowchart LR
    subgraph watch["pg-desk pull-through (watch queries)"]
        W1["pr: pr-mine / pr-team (existing)"]
        W2["issue: assigned-to-me, Jira-assigned,\nopen-beads bulk (section 4.2)"]
    end
    subgraph store["pg-desk store"]
        E["entity + interpretation + links"]
        FS["focus_selection (NEW)"]
        FP["focus_period (NEW)"]
        AN["annotation focus_selected"]
    end
    subgraph decide["pg-router and decider"]
        R["pg-router: annotation_changed routed"]
        D["focus decider (NEW rule set on issue/pr)"]
    end
    W1 --> E
    W2 --> E
    E -->|"focus rank: read-time view,\nno caching, writes nothing"| SHOW["pg-desk focus show\n(links[] resolve bead+status\nfor selected rows)"]
    FS -.->|"which rows are selected"| SHOW
    SHOW -->|"operator gate reply:\nok / -key / +key / cap=N"| SELECT["pg-desk focus select --apply"]
    SELECT --> FS
    SELECT -->|"annotate focus_selected=period"| AN
    AN --> R --> D
    D -->|"pg-connector issue create,\ndedup key issue:id:focus-item"| BEAD["bead (work item)"]
    BEAD -->|"derived link, then issue refresh"| E
    SHOW -->|"resolved status only"| CLOSE["pg-desk focus close"]
    CLOSE --> FP
```

### Retirement map

| Component                   | Fate                                 | Replaced by                                                                        |
| --------------------------- | ------------------------------------ | ---------------------------------------------------------------------------------- |
| `df-survey` (shallow)       | Retires                              | Live read of `entity`/`interpretation` rows (§6)                                   |
| `df-survey` (gate-apply)    | Retires                              | `pg-desk focus select --apply` (§7.2)                                              |
| `df-survey` (deep)          | Retires                              | Not needed — `focus show`/`select` are always live, no separate deep pass          |
| `df-deferred`               | Retires                              | `focus_selection` absence = deferred (D-F3)                                        |
| `df-wire`                   | Retires (logic moves into a decider) | The focus decider's `focus-item` rule (§8, D-F12, D-F13)                           |
| `df-pull`                   | Retires                              | `pg-desk focus pull` (§7.3)                                                        |
| `df-resolve-focus`          | Retires                              | `pg-desk focus show`/existence check via `period_key`                              |
| `df-find-pr-bead`           | Retires                              | The entity's `links[]` plus the decider's dedup-key lookup (change-flow work-item) |
| `df-split-blockers`         | Retires                              | `pg-desk focus show`'s resolved bead+status for selected rows (§7.1, D-F9)         |
| `df-verify-wiring`          | Retires                              | Not needed — nothing is wired (§8)                                                 |
| `df-close-focus`            | Retires                              | `pg-desk focus close` (§7.4) plus `close.md`'s own per-bead comments (D-F10)       |
| `df-attention`, `df-search` | Unchanged                            | n/a — unrelated (pure `pg-connector` clients already)                              |

## 4. Gather

### 4.1 Issue entities are already gathered (landed prerequisite, `pg2-2j5ac.46`)

An earlier revision of this document found, by reading the code, that pg-desk gathered only PRs, and
therefore scoped issue-entity gather out to an external prerequisite design session,
`pg2-2j5ac.46`. That session has closed and its design has landed (the generic entity pipeline
design, `docs/superpowers/specs/2026-09-23-pg-desk-generic-entity-pipeline-design.md`): gather,
interpret and persist take an entity-type registry covering `pr` and `issue`, issue ownership is
classified against a configured self-owner identity, and the entity change flow's pull-through
(`pg-desk <type> changes`) is the single path that discovers, hydrates and persists watched
entities of every type (`docs/behavior/pg-desk/changes.md`). This document therefore builds on that
pipeline and specifies no gather code of its own.

Two cautions carry over:

- The old `pg-desk run`/`sync` paths are removed by the change-flow cutover release but still
  present in code until it ships. This document targets the post-cutover world and MUST NOT call
  or extend them.
- Persisted entities are never deleted; a closed or unwatched entity stays as an inactive row.
  Every candidate query in section 6 MUST therefore filter on `entity.active`.

### 4.2 Watch queries

pg-router already runs one `desk-<type>-changes` query per watched type (change-flow S22); what a
type watches is pg-desk configuration: `watch.issue.queries` is a list of named `pg-connector issue
list` queries, each listed with `--fingerprints` and hydrated when added or changed. Daily-focus
adds no pg-router feed, no feed schedule and no `issue.changed` routing of its own. The deployment's
`watch.issue.queries` MUST include three named queries (the names here are illustrative; the real
names are deployment configuration):

- **An assigned-to-me Jira query**: Jira issues assigned to the operator (today's `issue-jira-mine`
  query). Together with the bead query below it realizes D-F11's candidate set for issues.
- **An assigned-or-owned bead query**: beads whose assignee or owner is the operator (the owner
  field exists, D-F8).
- **An open-beads bulk query**: every `open`/`in_progress` bead, with no type filter. Its purpose
  is narrower than the first two: landing every child issue's `parent` field, and every epic itself,
  in the store so the focus-rank step (§6) can answer "does this epic have an open child" with a
  pure in-store join, no per-epic query. It is the same "one bulk bead query, deduped by id" trick
  `df-survey` uses today, running continuously.

PR candidate gathering (`pr-mine`/`pr-team`) needs no change — D-F1. Volume is bounded by the
mechanism rather than guessed at: a poll diffs list fingerprints and hydrates only added or changed
entities, capped by `hydration.max_per_poll` (default 50), with the age sweep re-hydrating the rest
in rolling batches (`docs/behavior/pg-desk/changes.md`; change-flow 8.4, 8.5). `doctor` reports
when the sweep falls behind.

**Beads minted by the focus decider MUST be covered by a watch query** (the open-beads bulk query
does), because the entity's link to its minted bead is resolved from that bead's own entity row
(D-F9, D-F13). A focus bead that closes (a worker finished it, or the decider withdrew it) leaves
the open-beads query, so its entity row is no longer refreshed by a poll; its final state reaches
the source entity's `links[]` through the removal-confirmation read described in
`docs/behavior/pg-desk/changes.md`, and the decider's own `pg-desk issue refresh <bead>` after a
close or reopen (section 8) keeps it current without waiting for that read.

## 5. Store schema

New tables, alongside pg-desk's `entity`/`interpretation`/`xref`/`annotation` (the change flow's v2
schema adds `version`, `hydrated_at`, `active` and `list_fp` to `entity`, and drops `ledger`; none of
that touches the foreign-key target below, because `entity`'s primary key `(repo, entity_type,
entity_id)` is unchanged):

```sql
CREATE TABLE focus_period (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    period_type   TEXT NOT NULL,      -- 'day' this phase; schema allows 'week'/'sprint' later
    period_key    TEXT NOT NULL,      -- e.g. '2026-09-23' for period_type='day'
    closed_at     TEXT,
    close_note    TEXT,
    UNIQUE (period_type, period_key)
);

CREATE TABLE focus_selection (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    focus_period_id INTEGER NOT NULL,
    repo            TEXT NOT NULL,    -- same repo constant pg-desk already uses for every
                                       -- entity it gathers (§8's "single configured repo" in
                                       -- the parent design), not a per-issue attribute
    entity_type     TEXT NOT NULL,    -- 'pr' | 'issue' (Jira or bd; disambiguated by entity_id shape)
    entity_id       TEXT NOT NULL,
    selected_at     TEXT NOT NULL,
    UNIQUE (focus_period_id, repo, entity_type, entity_id),
    FOREIGN KEY (focus_period_id) REFERENCES focus_period (id),
    FOREIGN KEY (repo, entity_type, entity_id) REFERENCES entity (repo, entity_type, entity_id)
);
```

Both tables use an internal surrogate primary key with the natural key expressed as a `UNIQUE`
constraint (D-F6) — a deliberate divergence from every other pg-desk table, called out here so a
later reader does not mistake it for drift. `focus_selection` references `focus_period` by its
surrogate id rather than repeating `period_type`/`period_key`, and — per the operator's own
question during design, "do we have a table which would ensure `entity_type`/`entity_id` is
valid" — it also carries a real foreign key into pg-desk's existing `entity` table, so a
selection can never name an entity pg-desk never actually gathered. Both foreign keys are
genuinely enforced: pg-desk's store already opens every connection with `_pragma=foreign_keys(ON)`
(`internal/store/store.go`), so this isn't a documentation-only intent.

Consequence: `focus_period` is no longer written only by `focus close` (§7.4). Every verb that
writes a selection (`focus select --apply`, `focus pull`) must first get-or-create the
`focus_period` row for `(period_type, period_key)` — `closed_at`/`close_note` left `NULL` on
creation — so it has an `id` to insert `focus_selection` rows against. `focus close` becomes the
verb that sets `closed_at`/`close_note` on a row that, in practice, already exists by the time a
day is closed.

**Where the tables are created.** Schema changes go through pg-desk's migration ladder or the
change-flow cutover block, never an ad-hoc `CREATE TABLE`. Because nothing of this design is in
production yet, the two tables join the change flow's v2 cutover block (one transaction, one
operator window). If the cutover ships first, they become a version-3 migration step instead; the
phase 15 decomposition decides which at that time.

**The selection annotation (D-F12).** In addition to its rows, every selection writes
`focus_selected=<period_key>` on the selected entity (`pg-desk <type> annotate`, origin
`pg-desk`). The key is a reserved annotation, like `ready_to_land` (operator ruling, 2026-10-06:
a dedicated member, not the `decider` namespace, which is documented as written BY deciders). The
view's `annotations` set is closed today (`docs/behavior/pg-desk/show.md`; pg-decider's
`internal/view` `Annotations`), so a stored annotation is never visible to a decider unless the view
contract names it: the decomposition MUST add `annotations.focus_selected` to `show`'s composite view
(the stored value, or `null` when unset) and to pg-decider's view reader (section 12 item (g)). That
is what a decider reads from the entity's view; the two writes happen in `focus select`/`focus pull`'s single flow, the
table first, and a re-run is idempotent (a row that exists and an annotation that already holds the
value are left alone: the verb reads the current value first, so a re-run appends no redundant
change record), so a failure between them is repaired by re-running. A struck selection sets
`focus_selected=none`, because `annotate` today has no generic removal form (only paired verbs for
specific keys, such as `suppress`/`unsuppress`); a generic removal form on `annotate` is the
cleaner mechanism and is a small pg-desk item for the phase 15 decomposition to size, with the
decider treating `none` and an absent key identically until it exists.

The owner field this design needs on issues already exists (D-F8); nothing is added to
`schema.Issue` here.

## 6. Interpret: the focus-rank step

Unlike every other pg-desk interpret step (ownership, enrichment, urgency, category — each a pure
function of ONE entity's own gathered facts), focus-rank is a **sweep**: it operates over the
whole candidate set at once, computed fresh on every read (`show`/`select`/`pull`), never cached.
It reads `entity` rows exactly the way every other interpret step does — this step itself is pure
computation over facts, unchanged in kind from the rest of pg-desk's interpret layer. It reads
issue-type `entity` rows that the change flow's pull-through already persists (§4.1), and only
rows with `active = 1`.

**Placement (ruled 2026-10-05, D-F11; amended into the change flow, D-F14).** The rank is a
read-time, side-effect-free computation inside pg-desk. The router-triggered idempotent decider does
NOT compute it; the decider only mints beads for the items the operator actually selects (D-F12,
D-F13). This replaces the provisional 2026-10-02 answer that a focus decider computes the rank: the
rank depends on time (overdue, the 7-day horizon) and on cross-entity facts (the epic slot rule),
and a rank written on an entity change goes stale for both, the same reason the attention evaluator
is read-time (pg2-m482k, 2026-10-05). The change flow's decoration-versus-decision rule names
daily-focus ranking as a decision; D-F14 amends it so that a read-only rank view that writes nothing
and mints no work lives in pg-desk, while minting stays in the decider.

**Candidate set** (D-F11): every non-done item ASSIGNED to the operator, with no "started" or
ownership filter that hides an assigned item:

- PRs the operator authored or was assigned (surfaced by `pr-mine` or `pr-team`),
- Jira issues assigned to the operator (surfaced by the assigned-to-me Jira watch query, §4.2), and
- beads whose assignee or owner resolves to the configured operator identity (D-F8:
  `interpretation.ownership` for issues, which already reads both).

**Slot rule** (D-F11): an epic and its children MUST NOT consume more than one slot between them.
A child item takes the slot; the epic is NOT listed separately while it has any open child. An epic
that is incomplete and has NO open child work is a candidate in its own right, because the operator
must move it along (create children, split it, and so on). The rule is applied BEFORE the cap line
is counted. The old D-F7 narrowing ("has an open child") is superseded; the closed-child signal is
no longer needed, so the sketched `pg-connector-issue-beads` capability is not required.

**Started** (D-F11): a bead with state `in_progress`; a Jira issue in the In Progress status
category; any open PR assigned to the operator, whether authored or review-assigned. Consequence to
keep visible: every open assigned PR is started, so PRs rank above non-overdue unstarted beads and
Jira issues.

**Rank** (D-F11): strict lexicographic tiers, no weights, no arithmetic. The tiers, in order:

1. **Overdue** (due date before `--date`). Within it: started items first, then the most overdue,
   then unblocks (descending), then priority, then age.
2. **Started** (not overdue).
3. **Not started** (not overdue).

Inside tiers 2 and 3 the keys, in order, are:

1. Due date within a **7-day horizon from `--date`**: a nearer date sorts ahead of a farther one,
   and any date inside the horizon sorts ahead of none. A due date outside the horizon is
   deliberately equivalent to no due date for this key. Source: Jira `duedate` or bd's due date;
   correlated items inherit the earliest across the group.
2. Unblocks (descending): the count of open items this one blocks. For PRs it is the number of
   open dependents the dependency resolver reports for the PR (`dependency.Resolver.DependentsOf`;
   stack and external edges, the latter recorded with `pg-desk pr link add ... --relation
depends_on`). For beads and Jira issues it needs issue-dependency hydration (`SetReadIssueDeps`
   in pg-desk's issue gatherer), which exists in code but is not switched on in production and has
   no configuration key yet; until it is, the unblocks key is `0` for issues. Turning it on is a
   small item the phase 15 decomposition MUST schedule ahead of the rank's acceptance test.
3. Priority: bead P0-P4 directly, Jira priority via the same data-driven mapping table (unmapped
   values, including `Needs Priority`, sort after P4). **A PR with no priority of its own inherits
   the highest priority among its correlated items, else P2** (v2 doc section 4.4); this
   inheritance rule is part of the port, not an incidental detail.
4. Age (descending), then kind+key tiebreak.

Evidence (the operator's head-to-head rulings, 2026-10-05; each pair decided the key order above):

| Pair (first listed wins)                                                | Fixes                                    |
| ----------------------------------------------------------------------- | ---------------------------------------- |
| started P2 over unstarted P1, same deadline                             | started outranks priority                |
| started, blocks nothing over unstarted unblocker                        | started outranks unblocking              |
| started P1 with no deadline over unstarted P3 due tomorrow              | started outranks a non-overdue deadline  |
| overdue unstarted P1 over started P3 with no deadline                   | overdue outranks started                 |
| unstarted P3 due in 3 days over unstarted P1 with no deadline           | deadline outranks priority               |
| started P3 due tomorrow over started P1 with no deadline                | deadline outranks priority, when started |
| unstarted P2 unblocking 3 over unstarted P1 blocking none               | unblocking outranks priority             |
| unstarted, due in 3 days over unstarted, no deadline, unblocks 3 others | deadline outranks unblocking             |

**Cap line**: after the slot rule, the first `cap` (default 6, `--cap N` override) ranked items are
`in_plan: true` for _display_ only. Nothing is written by `focus rank` itself; `focus_selection` is
written only by `select`/`pull` (section 7).

## 7. The `focus` verb family

All four verbs take `--period day` (the only implemented value this phase; `week`/`sprint` are
schema-ready, not wired) and operate on `period_key` = a date, defaulting to today.

**Exit codes.** The `focus` verbs adopt pg-desk's established scheme (change flow 9.12): `0` ok,
`2` partial, `3` total failure, plus a new `6` for "period closed". Usage errors (a malformed reply,
an unrecognized token, malformed stdin) are reported as exit `3` with the problem named and nothing
applied, never as `2`, so each number has exactly one meaning in the binary. The per-verb
descriptions below use that scheme.

### 7.1 `pg-desk focus show [--date YYYY-MM-DD] [--cap N]`

Computes the candidate set and rank fresh (§6), cross-references existing `focus_selection` rows
for the period so already-selected items display as selected, and prints the ranked table —
same shape as today's gate table (§4.2/§4.5 of the v2 doc: category status/truncation, per-item
signals, PR `status_phrase`). Exit `0` all categories ok; `2` partial (a category failed or was
truncated — shown, never silently omitted, per the v2 doc's own §4.8 invariant); `3` total
failure, no candidates computable.

This is also the mitigation for losing "browse today's plan via plain `bd show`" (there is no
more focus bead to `bd show`, D-F4): `focus show` with no pending gate reply is exactly that
browse — and, longer-term, pg-desk's existing `serve` dashboard (parent design §7.7) is the
natural place to surface `focus_selection` alongside the PR panels it already renders, though
that wiring is not designed here.

**Resolved status for already-selected rows (D-F9, replaces `df-split-blockers`/`focus split`).**
For every row already in `focus_selection` for the period, `show` additionally resolves it to a
bead and reports that bead's current status, unconditionally — no separate verb, no flag, and no
tracker call at `show` time: the entity's view carries `links[]`, and the minted bead is one of
them because its own metadata names the source entity (D-F13, a derived link). The linked item's
state, labels, metadata and assignee are part of the link, and an epic's status is already in its
own `entity` row (the entity id already is the bead id). This holds only for beads some watch query
covers (§4.2). `close.md`'s survey step (today's `df-split-blockers`) becomes a plain `focus show`
call, grouping the resolved rows into whatever buckets it needs (its own per-item "real progress"
judgment, §9, already operates at a finer grain than closed/carried-over anyway). A selected item
whose bead has not been minted yet (the decider has not run) is shown as selected with "bead
pending".

### 7.2 `pg-desk focus select --date YYYY-MM-DD --apply [--merge <keepKey>=<absorbedKey>,...]` (reply on stdin)

1. Recompute the candidate table live (identical to `show`) and **print it** — this is what the
   reply gets applied against, always current, never more than one round trip stale (D-F5).
2. Parse the reply: `ok`/empty (accept as shown), `-<key>` (strike — this is also v2's "drop": a
   struck item is simply absent from `focus_selection`, so under this design there is no separate
   drop-vs-strike distinction to preserve), `+<key>` (force-pull an existing candidate, or — if
   `<key>` matches no candidate — attempt a hand-add: resolve it as a real lookup; a key the store has never gathered has no `entity` row for the foreign key, so pg-desk first hydrates it with a read (`pg-desk <type> refresh`, a read, never a tracker write), and if that fails the hand-add is a usage error; a hand-added entity outside every watch query stays selected but is not refreshed by polls, and `show` marks it "unwatched" because D-F9's resolved status is unavailable for it), `cap=N`
   (re-cap). **Parsing is atomic and happens entirely before any application**, matching v2's own
   gate-apply pipeline (v2 doc §4.6: "parse ALL tokens ... NOTHING applied" on any problem): a
   conflicting pair for one key (`-X +X`), an unrecognized token, or a hand-add that resolves to a
   nonexistent or closed id are ALL usage errors — exit `3` naming the problem, nothing applied,
   the caller re-prompts. There is no partial-application case at the reply-parsing stage.
3. Separately, `--merge <keepKey>=<absorbedKey>` (repeatable, comma-separated) is an LLM-supplied
   flag, not part of the operator's typed reply — this is v2's residual-fuzzy-correlation judgment
   (v2 doc §5 step 4, §9.1): two candidates the mechanical v2 doc §4.3 correlation missed but that
   represent the same real work. `absorbedKey` is dropped from consideration entirely (as if
   struck) and never gets its own `focus_selection` row or annotation. Instead `select` records an
   EXTERNAL link between the two entities (`pg-desk <type> link add`, change-flow S25 — external
   links are for exactly what no entity's own data names), so the absorbed item stays visible as
   related work under the kept item and no bead is minted for it.
4. Recompute `in_plan` from scratch: non-struck, non-absorbed items ranked, top `cap` in-plan;
   force-pulled/hand-added items in-plan additionally (each raises the effective cap by one,
   matching the v2 doc's own rule).
5. Replace `focus_selection` rows for this period wholesale with the final in-plan set (rewrite,
   never accumulate — the same "rewrite, never append" discipline the retired `df-deferred` used).
   Get-or-create the `focus_period` row first if absent (§5). Then write the
   `focus_selected=<period_key>` annotation on every selected entity, and `focus_selected=none` on
   every entity struck since the previous run for this period (D-F12, §5).
6. **Nothing is minted here.** `select` writes selection state only. The annotation is what a
   router-triggered focus decider reacts to (§8); `select` does not wait for it, and a bead appears
   after the decider's next run. This is the G5 boundary: pg-desk MUST NOT contain the `focus-item`
   work kind or dedup-key logic, or execute a tracker write verb.
7. Print the echo table, naming the resolved entity for every applied token explicitly (e.g.
   `-OWNER/REPO#123 → struck`), so a token that resolved to something other than intended is visible
   immediately rather than silently wrong.

Exit `0` applied; `3` a usage error (a parse-stage problem — nothing applied, per step 2) or a
`focus_selection` write that failed outright; `2` partial — `focus_selection` committed (step 5's rows)
but the annotation write failed for some, not all, of the entities; the echo names which, and
re-running repairs it.

**Dependency note**: this verb family depends on the entity change flow and its cutover release
(§4.1), and its minting half depends on the focus decider (§8). The 2026-09-23 note that
reconciled the tracking bead's "depends on phase 11 (`sync.mode: apply`)" against the parent
design's phase 13 is moot: `sync.mode` is removed by the change flow, and the plan/apply gate for
minting is the decider's own (`pg-decider plan|apply`). Whoever decomposes phase 15 sets its
real ordering against the cutover and the decider rather than either old phase number.

**UX cost, named rather than left implicit**: typing a full `OWNER/REPO#NUMBER` or Jira/bd key in
a reply is more keystrokes than v2's short `p3`/`j5`/`e2` handles. §13 records why the handle
mechanism was rejected (it required freezing exactly the data the operator wants live); this is
the real, day-to-day price of that trade, not a cost-free simplification.

### 7.3 `pg-desk focus pull [--date YYYY-MM-DD] [k | key...] [--dry-run]`

Same live-recompute-then-print discipline as `select`, but strictly additive: no strike, no
re-cap, no recompute-from-scratch. Bare invocation previews (recompute, print, change nothing —
also "what's left today?"). A count `k` selects the top-`k` not-yet-selected candidates by current
rank; named keys select those specifically (already selected, or no longer a candidate — e.g.
merged/closed since — reported and skipped). Selected items union into `focus_selection` (get-or-
create the `focus_period` row first, same as `select`) and receive the same `focus_selected`
annotation (D-F12), which the focus decider acts on. No re-survey, no cap — "pull more" is
deliberately uncapped, same as today.

Exit `0` done (including "nothing left to pull"); `3` usage or a store write failure; `2` partial
(named items skipped/stale, or nothing named resolves); `6` the period is closed (`focus_period`
has `closed_at` set) — terminal, matching today's df-pull's "pulling into a closed day is always
wrong."

`--dry-run` runs the same resolution and reports exactly what would be selected and annotated,
writing nothing to `focus_selection` or the annotation table, **and skipping the `focus_period`
get-or-create too** — a `--dry-run` call must not leave behind even an empty `focus_period` row as
a side effect of what's supposed to be a no-op preview. Worth having natively (not left to the
caller simply not invoking the verb) because a selection leads to a minted bead, a real, visible,
not-cheaply-undone action.

### 7.4 `pg-desk focus close --date YYYY-MM-DD [--dry-run]` (day summary on stdin)

Writes `closed_at`/`close_note` onto the `focus_period` row (creating it if absent) — the
`close_note` is where today's day-summary-onto-the-focus-bead text goes, since there is no more
focus bead to carry it. `focus close` reads **one JSON object on stdin**: `{"summary": "<day summary
text>"}`. The summary is free-form text that can contain quotes and newlines — the same reason
`select --apply`'s reply is read from stdin rather than argv (v2 doc §4.6). `summary` becomes
`close_note`.

**Per-bead progress notes are NOT written by pg-desk** (D-F10, G5: pg-desk MUST NOT execute tracker
write verbs, and pg-desk's own composition rule limits what it may exec). This does not drop
`df-close-focus`'s real, currently-used behavior of carrying context onto individual beads across
sessions: `close.md` (LLM command prose) appends each progress note itself with `pg-connector issue
comment`, one per resolved bead from `focus show`'s output (D-F9), then runs `pg-desk issue refresh
<bead>` so the store sees the write, and only then calls `focus close`. It fails fast on the first
append failure, same as today's script (no rollback of notes already appended), and does not call
`focus close` if any note failed.

`--dry-run` reports the resolved `close_note`, writing nothing and — same as `pull`'s `--dry-run`
above — not creating a `focus_period` row if one doesn't already exist.

Exit `0` closed; `3` usage (malformed stdin) or a store write failure; `6` already closed
(idempotent no-op, reported).

## 8. Minting: the focus decider

There is no sync pass, and nothing mints from `select` or `pull`. What replaces `df-wire` is a
decider rule (change-flow section 7, G5), registered in `pg-decider` and routed by pg-router:

- **Trigger.** `focus select`/`pull` write `focus_selected=<period_key>` on the selected entity
  (D-F12). The annotation emits `annotation_changed`, which every decider subscribes to, so the
  decider runs on the next routed item for that entity. The routed kind is a hint only: the decider
  re-derives everything from the entity's current view (G6), so duplicates and reordering are
  harmless.
- **Rule.** The rule runs under two entity types, `issue` and `pr` (the entity's own type decides
  the source), and for a `pr` entity it runs inside the PR decider's hidden and suppressed
  precedence: a hidden or suppressed PR gets no focus bead. For an entity whose view shows
  `annotations.focus_selected` set to a real period key, and which has no linked `focus-item` bead,
  create one through `pg-connector issue create` with dedup key `<entity_type>:<entity_id>:focus-item`
  (D-F13) and metadata naming the source entity, using v2's type map (Jira `Bug` → `bug`, else →
  `task`) and priority map. An epic needs no bead (its entity id is a bead id).
  - **Strike withdraws (operator, 2026-10-06).** If the view shows `none` or an absent key and a
    `focus-item` bead exists, the decider withdraws it: an open, unclaimed bead is closed with a
    reason naming the strike and carrying the withdrawn marker (metadata `focus_withdrawn=true`); a
    claimed or `in_progress` bead is left alone and the decider records a comment saying the item was
    struck, so a worker's claim is never pulled out from under it. An absent key with no bead does
    nothing (the rule reports "not matched", from the closed five-reason vocabulary of
    `plan-and-apply.md`).
  - **Same-context row.** The context is the source entity. A bead that is open: nothing to do. A
    bead the decider itself withdrew (`focus_withdrawn=true`) and the operator reselected: reopen it,
    never mint a second. A bead closed any other way (a worker finished it): never recreated and
    never reopened, and the rule reports the skip reason "already handled" rather than staying
    silent.
- **Plan and apply.** The decider ships in `plan` mode first. `pg-decider plan` and `pg-decider
apply` are two commands, so there is no mode flag to switch: the router role that runs the
  decider is changed from running `plan` to running `apply`, the same way every other decider is
  promoted. There is no `sync.mode`.
- **Where the code lives.** The rule lives in `pg-decider`, which is where work-kind and dedup-key
  literals are allowed; pg-desk's `internal/focus` (rank, selection store) carries none, and a
  guard test pins that (G5).
- **After the write.** The decider's apply step runs `pg-desk issue refresh <bead>` (change-flow
  S10) so the new bead's entity row, and therefore the derived link, is visible without waiting for
  the next poll. The audit trail is a comment on the bead, as for every decider.

What's gone compared to today's `df-wire`: no `wire_targets` resolution, no epic-vs-task
blocking-edge distinction, no bulk `bd dep add`, no `df-verify-wiring`. There is nothing to wire to
(no per-day focus bead, D-F4), so the class of defect that produced today's retro finding 4
(undocumented epic-vs-task wiring constraint) cannot recur — not handled, structurally absent.

## 9. Consumer rewrites (the private machine flake)

| Consumer                          | Today                                                                                                                              | Rewritten to                                                                                                                                                                                                                                                              |
| --------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `create.md` duplicate guard       | `df-resolve-focus <date> --status all`                                                                                             | `pg-desk focus show --date X`; nonempty selection ⇒ same use-as-is/amend operator decision                                                                                                                                                                                |
| `create.md` gate                  | `df-survey` shallow → table → reply → `--apply-gate`                                                                               | `pg-desk focus show` → table → reply → `pg-desk focus select --apply`                                                                                                                                                                                                     |
| `create.md` deep/mint/wire        | `df-survey --deep` → LLM drop/merge → `df-wire`                                                                                    | `-<key>` covers "drop"; `--merge <key>=<key>` on `focus select` (§7.2 step 3) covers "merge"; minting is the focus decider's, triggered by the selection annotation (§8), not a step of `create.md`                                                                       |
| `pull.md`                         | invokes `df-pull` verbatim, including its exit-`5` multi-candidate branch                                                          | invokes `pg-desk focus pull` verbatim — **not** the same exit codes: `focus pull` has no exit `5`, for the same reason `close.md` resolve (below) loses its multi-candidate case. `pull.md`'s own exit-5 handling prose is dead and should be removed, not left in place. |
| `close.md` resolve                | `df-resolve-focus <date>`, exit-5 multi-candidate handling                                                                         | `pg-desk focus show --date X` existence check — no multi-candidate case exists (`period_key` is the date, exactly; the ambiguity class retires with it)                                                                                                                   |
| `close.md` survey                 | `df-split-blockers <focus-id>`                                                                                                     | `pg-desk focus show --date X` — resolved bead+status for each selected row (D-F9), grouped by `close.md` itself                                                                                                                                                           |
| `close.md` per-bead notes + close | `df-close-focus`: appends a progress note to every touched bead, then the day summary onto the focus bead, then `bd close --force` | `close.md` appends each per-bead note with `pg-connector issue comment`, then `pg-desk issue refresh <bead>` (D-F10); then `pg-desk focus close --date X` with the day summary on stdin (§7.4); no bead is closed                                                         |

`close.md` steps 3-5 (summarize, judge real progress, Jira comment gate) are unaffected — they
consume the resolved item list either way (grouping it into closed/carried-over, or whatever
grouping it needs, is now `close.md`'s own job rather than a dedicated verb's output shape) and
still produce the same summary/per-bead-notes content that step 6 consumes; step 6 is now two
parts, the per-bead comments (done by `close.md`) and `focus close` (the day summary on stdin).

## 10. Retirement, testing, and validation

**Retires** (script, nix wiring in `scripts.nix`/`default.nix`, and bats tests, together):
`df-survey`, `df-deferred`, `df-wire`, `df-verify-wiring` (a step inside `df-wire`, but its own
top-level script/nix-module/bats-test entry — easy to miss if this list is read as exhaustive
without checking the daily-focus module directory directly), `df-pull`, `df-resolve-focus`,
`df-find-pr-bead`, `df-split-blockers`, `df-close-focus` (its day-summary half folds into `focus
close`, §7.4; its per-bead notes move into `close.md`). Unchanged: `df-attention`, `df-search`.

**Testing moves with the logic**, into a new `internal/focus` package plus `cmd/pg-desk/focus*.go`
(rank and the selection store) and a new `focus-item` rule in `pg-decider` (minting) — a genuine
rewrite of `df-survey`'s bats-tested ranking/gate-apply suites, not a file move. The issue-entity
pipeline (§4.1) has its own landed test suite and is not re-tested here. Named test targets for
THIS document's own scope: ranking (the tier order, the 7-day due horizon, the started and overdue
definitions, the epic slot rule, and the priority-inheritance rule, §6); the unblocks key for PRs
against the dependency resolver; the cap/select/pull recompute-from-scratch and additive-only
semantics respectively; `show`'s resolved-status join through `links[]` (D-F9) for all three
candidate types, including the "bead pending" state; the `--merge` flag recording an external link
(§7.2 step 3); the selection annotation write, its idempotent re-run and the `none` strike (§5,
D-F12); `--dry-run` on both `pull` and `close`, including that it does not create a `focus_period`
row; and, for the decider, `plan` writes nothing while `apply` creates exactly one bead, the dedup
key is stable across days, a closed bead is not recreated, an epic gets no bead, a strike closes an unclaimed bead with the withdrawn marker and only comments on a claimed one, a reselect reopens a withdrawn bead but never a worker-closed one (reporting "already handled"), and an absent key with no bead does nothing. A guard test pins that pg-desk contains no `focus-item` literal and execs
no tracker write verb (G5). What remains bash/bats in the private machine flake: nothing of
substance for daily-focus's own logic — `create.md`/`pull.md`/`close.md` become LLM command prose
invoking `pg-desk` and `pg-connector`, same as they already invoke `pg-connector` today.

**New query and role validation.** `nix flake check` passing proves a config evaluates, not that it
produces real rows or effects when actually triggered (a lesson already recorded in this
repository's `CLAUDE.md` from a live incident). The phase 15 checkpoint below requires exercising
the new watch queries and the new decider role live, not just a clean flake check.

## 11. Phase 15 checkpoint (operator-run by hand, D16)

- The entity change flow cutover has shipped, so pg-desk no longer carries `ledger`, `sync.mode`
  or `pg-desk run`, and issue entities hydrate through `pg-desk issue changes` (§4.1). This
  precedes everything below; none of it is meaningful on the pre-cutover code.
- `pg-desk focus show --date <today>` produces the same candidate set `df-survey` would have,
  modulo the recorded narrowing of D-F2's PR repo scope, and the deliberate widening of D-F11's
  candidate set (everything assigned to the operator, with the epic slot rule) over `df-survey`'s.
- The three watch queries of §4.2 are each run live at least once (`pg-desk issue changes`, or the
  router's `run-query` equivalent) with a confirmed non-trivial outcome — real `entity` rows
  landing for an epic and a plain bd task, not just a clean `nix flake check`.
- The focus decider's role is exercised live in `plan` mode against a real selection and reports
  the bead it would create, then in `apply` mode creates exactly one, whose derived link appears in
  the source entity's `links[]` after `issue refresh`.
- A full `select --apply` → `pull` → `show` (resolved status) → per-bead comments → `close` cycle
  runs end-to-end against the live tracker for one real day.
- `close.md`'s rewritten resolve/survey/close steps produce output its unchanged steps 3-5 accept
  without modification.

## 12. Open items for operator review

- **The change-flow cutover is a hard dependency** (§4.1): phase 15's decomposition and its
  checkpoint order against that release and the focus decider, not against the retired phase
  numbers (§7.2's dependency note).
- **D-F14 is a spec amendment that MUST land with this document**: the change-flow design's
  decoration-versus-decision text and ADR 0077 are amended in the same landing, so the two are not
  in conflict at any commit.
- **Small pg-desk and pg-decider items this design needs, for the decomposition to size**: (a) a
  generic removal form on `pg-desk <type> annotate` (§5; `none` is the stopgap); (b) switching on
  issue-dependency hydration, with a configuration key, so the unblocks key works for beads and
  Jira (§6); (c) the generic source-entity metadata extractor with a distinct `focus` relation (D-F13); (d) the `focus-item` work kind in pg-decider: amend `docs/behavior/pg-decider/work-items.md` (it says five kinds, and `TestKindsAreTheFiveExactStrings` pins them) with the kind, the bead shape, the dedup key and the context row, register an `issue` rule set (pg-decider exits 1 for `issue` today, and `pr` is the only always-registered type), and add the README rules row (§8); (e) where the two focus tables are created, the cutover block or a version-3 step (§5);
  (f) the exit codes of the `focus` verbs, now settled on pg-desk's scheme (§7); (g) the `annotations.focus_selected` member in `show`'s composite view and pg-decider's view reader (§5; the view's annotation set is closed today); (h) the decider's withdraw and reopen of focus beads on a strike or reselect (§8), including the `focus_withdrawn` metadata marker.
- **The rest of the document is not yet reviewed by the operator.** Only section 6, D-F11 and the
  2026-10-06 rulings D-F12 to D-F14 are ruled; approval of the whole document, recorded in this
  header, is still required before landing.
- **Public-repo scrub: done in this revision.** The draft no longer names the employer's private
  repositories, tickets or machine flake. Re-scan the whole file case-insensitively for the
  employer name, its abbreviation, private repository names, PR and ticket numbers before it
  lands, and again after any later edit.
- Whether `week`/`sprint` period types are worth building now or genuinely deferred (the schema
  is ready either way; no verb currently implements them).

## 13. Rejected alternatives

- **A per-day focus bead with wired dependencies** (today's model) — rejected; see D-F4. Beads
  stay reserved for agent-workable signals per D8, and a per-period bead-wiring convention does
  not generalize to week/sprint without new mechanism per level.
- **Positional handles + a persisted candidate snapshot** (an earlier draft of this design) —
  rejected; froze exactly the information (rank, signals) the operator needs live, to solve a
  narrower identity-stability problem that a direct `(entity_type, entity_id)` reference solves
  without freezing anything (D-F5).
- **Composite natural-key primary keys for the new tables**, matching every existing pg-desk
  table — rejected for `focus_selection`/`focus_period` specifically, per operator preference
  (D-F6).
- **A dedicated `pg-connector-issue-beads` capability for exact epic candidacy** (owner +
  active-or-recently-closed-child, in one call) — no longer needed: D-F7's narrowing was superseded
  by D-F11 (2026-10-05), whose slot rule ("an epic is listed only when it has no open child")
  needs no closed-child signal, so no new backend code is required for epic candidacy.
- **A separate `focus split` verb** (an earlier draft of this design, matching `df-split-blockers`
  1:1) — rejected; the bead-resolution it performed is real and needed, but bucketing it into
  `closed`/`carried-over` was a shape carried over from the retiring script rather than something
  any consumer actually required. Folded into `focus show` instead (D-F9), through the entity's
  `links[]`.
- **A daily-focus-specific issue-ingest path, bypassing the shared gather layer** — rejected, and
  now moot: the shared pipeline (§4.1) serves issue entities, so a bypass would have left two code
  paths writing the same table for the same reason.
- **A new pg-connector notes-append capability**, to preserve `df-close-focus.sh`'s exact
  `bd update --append-notes` mechanism — rejected; `Comment` already exists, tested, on both
  issue backends today, and is arguably the better mechanism for this content anyway (D-F10).
- **Minting directly from `focus select`/`pull` through a pg-desk `ledger` and a `sync.mode`
  gate** (this document's own earlier design) — rejected: the entity change flow removes both, and
  G5 keeps work-kind and dedup-key logic and tracker writes out of pg-desk (D-F12, D-F13, §8).
- **A new `focus_changed` local change source**, or **annotations only with no `focus_selection`
  table** — rejected in favor of the annotation plus the table (D-F12): the first amends the change
  flow's change-kind catalogue for no benefit the existing `annotation_changed` does not give, and
  the second loses period history and the foreign-key-guarded `closed_at` join.
- **A per-day suffix on the focus bead's dedup key** — rejected (D-F13): it would mint a bead per
  item per day and contradict D-F4.
- **Computing the rank in a decider** (honoring the change flow's decision rule literally) —
  rejected by the 2026-10-05 ruling (D-F11) and reconciled by the D-F14 amendment: the rank
  depends on time and cross-entity facts, so a rank written on a change goes stale.
