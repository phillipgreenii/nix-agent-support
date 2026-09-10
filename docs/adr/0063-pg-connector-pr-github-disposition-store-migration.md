# pg-connector-pr-github: the pg-pr disposition-store migration path

**Status**: Superseded (2026-09-10)
**Date**: 2026-09-06
**Deciders**: Phillip Green II

**Superseded 2026-09-10** (operator ruling, via `pg2-usjba`): the 2026-09-09 design amendment
(commit `e0186710`) replaced the disposition-store migration mechanism this ADR describes.
Dispositions are no longer migrated under any backend — `pg-desk`'s interpreter re-derives them
from gathered facts on every run (Phase 3 migrated zero real rows in practice), so the `feedback`
and `code_comment_message` tables DROP outright rather than moving. The rest of this ADR's
Context/Decision below is retained as a historical record of the migration path that was
considered and initially accepted, not as the current design.

## Context

`packages/pg-pr/internal/store` holds a `feedback` SQLite table with a `disposition_action` column
(`will-fix` / `wont-fix` / `no-action`, or unset), keyed by `pr_id` (a foreign key into
`pull_request(repo, number)`) and identified per-row by `comment_node_id` (GitHub's own GraphQL
node id) or, when that was never captured, `external_id`. `pg-pr feedback disposition <id>` is its
only writer; `pg-pr feedback list <repo> <pr>`/`feedback show <id>` are its readers, both already
supporting `--json`.

`packages/pg-connector`'s `pg-connector-pr-github` backend (`cmd/pg-connector-pr-github/internal`)
has its own, independent JSON-file store (`store.go`) recording the same kind of fact — a per-PR,
per-comment disposition — under a different shape: keyed by `formatPRID(repo, number)` (a plain
`"<owner>/<repo>#<number>"` string, no foreign key), with dispositions in a
`map[commentID]schema.Disposition` using the wire enum `open | will-fix | wont-fix | no-action`.
`store.go`'s own top-of-file comment already states why this is a fresh design rather than a port:
FK-ing against pg-pr's `pull_request` table would make this backend's module depend on pg-pr's,
which `packages/pg-connector`'s self-contained-module rule (ADR
[0062](0062-pg-connector-tier1-tier2-connector-architecture.md), design
`docs/superpowers/specs/2026-09-03-unified-connector-architecture-design.md` §5.2/§9) forbids.

The design already decided the destination and the atomicity rule (§6.1: the disposition op "lives
under the `pr` capability... because the feedback-disposition store itself moves under the PR
GitHub backend"; §9.1: "the disposition-store migration and the deletion of pg-pr's own `feedback`
command group MUST land in the same cutover step, never split across two — there is no window in
which both binaries hold their own independently-writable copy"). What the design does not state is
the concrete mechanism: which fields map to which, how a comment's identity round-trips across the
two stores' different key shapes, and what actually invokes the import. Finding A19 of the
`2026-09-05-pg-connector-deep-review.md` review (section A, findings 17-20) named this gap: "the
migration of pg-pr's `feedback` table into the new store has no specified path." This ADR is that
path.

Both `pg-pr` and `pg-connector-pr-github` are single-user, operator-run tools today (design §9's
own "no coexistence architecture... Phillip is currently the sole user" framing) — this is a
one-shot maintenance operation run once at the cutover step, not a permanently-maintained bridge.

## Decision

1. **Export via pg-pr's existing, unchanged CLI — no pg-pr code changes, no direct SQLite access
   from pg-connector.** The source of truth for the migration is
   `pg-pr feedback list <repo> <number> --json`, which already emits a JSON array of
   `internal/store.Feedback` rows (no `json` tags on that struct, so its keys are the exported Go
   field names verbatim). This keeps `pg-connector`'s module free of any dependency on
   `packages/pg-pr` or its SQLite schema — the same self-contained-module rule `store.go`'s own
   design decision already rests on.
2. **Field mapping**, implemented as `internal.ImportLegacyDispositions` in
   `cmd/pg-connector-pr-github/internal/migrate.go`:
   - Only rows whose `Kind` is `code-comment-thread` or `pr-comments` carry a disposition this
     store has a slot for (its `Dispositions` map is keyed by a single GitHub comment/thread id;
     `ci-failure`/`review-request`/`jira-link`/`self-review` rows have no such id and are out of
     scope for this backend's store — they are not lost, they simply have no destination here).
   - A row with an empty `DispositionAction` (never dispositioned in pg-pr) is skipped, not
     imported as an explicit `open` write: this store already distinguishes "never written" from
     "explicitly open" (`store.go`'s `PRState` doc comment), and importing every undispositioned
     row as `open` would manufacture history pg-pr never actually recorded.
   - `DispositionAction` maps onto `schema.Disposition` by literal value: `will-fix` ->
     `DispositionWillFix`, `wont-fix` -> `DispositionWontFix`, `no-action` -> `DispositionNoAction`.
   - The comment identity is `CommentNodeID` when present, falling back to `ExternalID` — mirroring
     `internal/github.go`'s own convention of using GitHub's GraphQL node id as `api.Comment.ID`.
   - The target `prID` is `formatPRID(repo, number)` for the same `repo`/`number` the `feedback
list` call was scoped to (the caller already knows this — it is the CLI arguments it just
     passed to `pg-pr feedback list`).
   - An unrecognised `DispositionAction` value is reported (as part of a single joined error) but
     does not abort the rest of the batch — one unexpected row must not block every other
     legitimate disposition in the same export from migrating.
3. **Idempotent by construction.** `SetDisposition` is a plain set/overwrite
   (`store.go`), and the mapping above is a pure function of its input — re-running the same export
   through `ImportLegacyDispositions` twice reaches the same end state, never a duplicate row or a
   conflicting write. This matters because the operational recipe below is expected to be run once
   per repo at that repo's own cutover moment, not globally atomically, and a retry after a partial
   failure must be safe.
4. **Category is explicitly NOT migrated.** Per design §6.1, `category` is "new state this design
   introduces rather than a migration of anything pg-pr's existing SQLite store already has" — pg-pr
   has no equivalent column, so there is nothing to import for it.
5. **Operational recipe, per the design's atomicity rule (§9.1).** For a given `repo`, in the SAME
   cutover step that deletes pg-pr's `feedback` command group for that repo (never split across
   two, per §9.1):
   ```
   pg-pr feedback list <repo> <number> --json
   ```
   for every PR in `<repo>` that has feedback, decoded into `[]internal.LegacyFeedbackItem` and
   passed to `internal.ImportLegacyDispositions(store, formatPRID(repo, number), items)`. This is
   deliberately **not** wired as a new subcommand on `pg-connector-pr-github` itself: that binary's
   own `.nix` derivation and `main.go` doc comment already state it "has no independent CLI
   identity" (a scriptout-protocol-only process), and adding a maintenance-mode CLI branch to it
   would contradict that decision. It is also deliberately not a new pg-pr subcommand: `pg-pr` has
   no reason to know pg-connector's on-disk store shape. Today the mapping function is exercised
   directly by `migrate_test.go`; the actual one-shot invocation at cutover time is a short script
   (or an ad hoc `go run` snippet) built when that cutover step is actually executed — consistent
   with pg-pr's own `migrate`/`migrate-feedback` precedent of being one-shot maintenance operations,
   not permanently-installed connector wire ops (design §9.1's verb->destination table: both have
   "No destination verb").

## Consequences

### Positive

- The migration path is now a concrete, testable function (`migrate_test.go`) rather than an
  unspecified gap — the review's own required acceptance ("the pg-pr/pg-connector disposition-store
  migration path is written down somewhere... and implemented") is satisfied by this ADR plus
  `migrate.go`.
- `pg-connector`'s module stays free of any pg-pr/SQLite dependency; the seam between the two stores
  is exactly the JSON-over-stdout boundary pg-pr's CLI already exposes, not a new one.
- The mapping is idempotent, so a partial migration (interrupted, or a data issue found mid-run) is
  safe to re-run rather than requiring manual cleanup first.

### Negative

- This is exercised as a library function with tests, not yet wired into a runnable one-shot
  binary/script; the actual cutover invocation is deferred to when that cutover step is executed
  (removal-criterion item 1 in design §9.1 — the write-verb groups `pr review`/`pr comment` and
  others — has not landed yet, so the cutover this migration exists for has not happened yet
  either). A future packet building the actual cutover step MUST use `ImportLegacyDispositions`
  rather than re-deriving the mapping.
- The migration only covers the two comment-bearing `Kind` values. If a future design decision
  gives `ci-failure`/`review-request`/`jira-link`/`self-review` feedback a home in some Tier-2
  store, that is a separate mapping this ADR does not cover.

## Alternatives Considered

### Have pg-connector open pg-pr's SQLite file directly

Rejected: this is exactly the FK/module dependency `store.go`'s own design decision (fresh store,
no pg-pr precedent) exists to avoid, and it would require vendoring a SQLite driver into
`pg-connector`'s go.mod purely for a one-shot operation neither module needs at runtime otherwise.

### A permanent dual-write shim during the overlap window

Rejected: design §9 already rules this out repo-wide ("no coexistence architecture... no shim,
dual-write, or routing layer"), and it would not even fit this specific case — the two stores use
incompatible key shapes (`pr_id` foreign key vs. a plain `owner/repo#number` string), so a shim
would need the same mapping this ADR defines anyway, just run continuously instead of once.

### Migrate category alongside disposition

Rejected: pg-pr has no category-equivalent column to source from (see Decision item 4); inventing
one would be answering a question design §6.1 already answered the other way.

## Related Decisions

- Implements design §6.1/§9.1's disposition-store migration decision from
  `docs/superpowers/specs/2026-09-03-unified-connector-architecture-design.md`, resolving finding
  A19 of `2026-09-05-pg-connector-deep-review.md` (bead `pg2-j4puf`).
- Builds on the self-contained-module rule recorded in ADR
  [0062](0062-pg-connector-tier1-tier2-connector-architecture.md).
