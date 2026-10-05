# pg-desk — show, status, sweep, reconcile, doctor, heartbeat, heartbeat-item

## show

`pg-desk show <pr> [--refresh]` prints the store's interpretation for one PR with its `as_of`
time, and — when `sync.mode` is `plan` — that PR's own planned sync writes (kind and content
hash; see [`sync.md`](sync.md)). `--refresh` MUST run the pipeline (see
[`pipeline-run.md`](pipeline-run.md)) for that id first, then print the resulting interpretation.
This replaces `pg-pr pr view`'s enrichment display and `pg-pr sync --pr N` as the manual re-check
(D20) — the bead-writing call sites this docket's design retargets point here.

Exit codes: `0` on success, including when `--refresh` completes a degraded run (degraded is not
a failure — see [`gather.md`](gather.md)); `1` when `<pr>` does not resolve, or the store cannot
be read or written. `show`'s own failure modes never exceed the `run` contract's exit-`1` case
when `--refresh` is given. A config-load failure (needed only to check `sync.mode`) degrades to
omitting the planned-sync-writes section rather than failing `show` outright.

## status

`pg-desk status` prints the store path and schema version, entity and interpretation counts, the
last heartbeat/run/sweep times, degraded rows, sync errors (with how many are retrying, exhausted,
and non-transient — see [`sync.md`](sync.md)'s "Automatic retry"), the age of the stalest applied anchor check
(`oldest_anchor_check_age_seconds`, the same value as `serve`'s
`pg_desk_oldest_anchor_check_age_seconds`, `0` when none), and — when `sync.mode` is
`plan` — planned sync rows by kind (anchor / feedback-cycle / review-request; see
[`sync.md`](sync.md)). It also prints `pg-connector ledger show` for the configured consumer.

It prints a `change_flow` section, human-readable, per entity type and per consumer, matching the
change-flow families `serve` exposes on `/metrics` (see [`serve.md`](serve.md)). Per type it
reports the active entity count, the due backlog (active entities whose `hydrated_at` is older than
the sweep max age, or that were never hydrated), the change-log records by kind and origin, the
hydration and hydration-failure totals, the optimistic-concurrency retries, and the entities with
repeated degraded hydrations. Per consumer it reports the cursor, the lag (the type's highest change
sequence minus the cursor) and `seen_at`, which is the consumer's liveness now that the heartbeat is
retired. For the sweep bound (`active_count / N x poll_interval <= D`), `status` reports only its
inputs (`active_count`, `N`, `D`) and `poll_interval: unknown`, with NO verdict: the poll interval is
the router's timer period, which `pg-desk` does not own, so only `doctor` evaluates the bound (with
`--router-config`). On a store that has not been cut over, the section reads `unmigrated` instead of
failing.

Exit codes: `0` on success; `1` when the store cannot be opened. A config-load failure (needed
only to check `sync.mode`, and to read the sweep bound's inputs) degrades to omitting the planned-sync-rows section rather than failing
`status` outright — status's own exit-code floor stays "1 only when the store cannot be opened."

## sweep

`pg-desk sweep` (bead `pg2-gznpe`) is the operator's bulk backfill command: it re-runs the full
`gather` -> `interpret` -> `store` -> `sync` pipeline (see [`pipeline-run.md`](pipeline-run.md)),
always with `--change sweep`, for EVERY entity currently in the store — the same pipeline call a
single `pg-desk run pr <id>` uses (an absent `--change` already defaults to `sweep`; this command
just automates that call across every tracked entity rather than requiring an operator to
enumerate and re-run each one by hand). This is the only way to force a full recompute of every
entity after a `gather`/`interpret` schema or enrichment change — otherwise an entity only
refreshes on its own next webhook-triggered event, and a change that adds a field (e.g. a new
`Enrichment` field) silently leaves any untouched-since entity stale.

Every entity is attempted even after an earlier one fails (mirrors `run issue`/`run thread`'s own
"attempt every one, join errors" convention) — a handful of stale or since-deleted entities must
not stop the rest of the backfill. Once the pass over every entity completes, `sweep` stamps
`meta.last_sweep` (surfaced as `last_sweep_at` on the `serve` dashboard — see
[`serve.md`](serve.md) — and as `last_sweep` in `status`'s own output above), even when one or
more individual entities failed: the SWEEP itself completed, mirroring `run`'s own
"degraded-but-completed is still success" contract.

`sweep` is distinct from, and does NOT implement, [`sync.md`](sync.md)'s still-out-of-scope
"store-wide sweep that re-verifies every ledger row whose entity has left every gathered query" —
that needs a driver over the `ledger` table (which entities have vanished from every live query);
`sweep` only re-runs the pipeline for entities the `entity` table already knows about.

Exit codes: `0` when every entity's pipeline run succeeds (including a degraded run — see
[`gather.md`](gather.md)); `1` when the config or store cannot be opened, or when one or more
entities' pipeline run failed (naming which).

## reconcile

`pg-desk reconcile` is the event-independent repair pass for PRs that left the open set. Closure
is otherwise driven only by the one-shot `--change removed` event, and `pr-sweep` lists only PRs
matching the open queries, so a single failed closure would leave a merge-request anchor open
forever. `reconcile` reads the store and re-drives, through the same `run pr <id> --change
removed` path (so closure stays ledger-guarded and re-entrant), every PR entity that has either:

- a `kind=anchor` ledger row that is not `closed` (with a non-empty bead id) whose PR a
  `--change removed` re-read reports as merged, closed, or not found; a PR still `open` is left
  alone; or
- a recorded `interpretation.sync_error` whose automatic retry is due (see [`sync.md`](sync.md)'s
  "Automatic retry"), re-driven regardless of PR state (a successful run clears it). A row with
  no recorded retry state yet is due.

A `sync_error` row whose retry is not due — still backing off, exhausted, or non-transient — is
held: `reconcile` MUST NOT re-drive it through either bullet above (its open anchor is not re-read
either, since that re-drive would be one more retry of the same failing sync). This is how a
transient failure heals on a later scheduled pass with no operator action, while a persistent one
stops after the retry bound. `--retry-all` lifts the hold for one run: every recorded `sync_error`
is re-driven now, whatever its retry state — the operator's manual repair once the cause is fixed.

`reconcile` MUST be idempotent: once an anchor is closed and `sync_error` is empty, the entity is
no longer re-driven. Every candidate is attempted even after one fails; failures are joined into
one error. It takes no positional arguments and does not stamp `meta.last_sweep`. `pg-desk`
ships no scheduler: an external scheduler (for example a pg-router timer or launchd job) MUST
invoke `pg-desk reconcile` periodically — without `--retry-all`, so the retry policy holds.

`reconcile` is bounded per run so a large backlog converges across successive scheduled runs
instead of being killed mid-pass. `--budget <duration>` (default `4m`; `0` = unbounded) stops
`reconcile` from starting new candidates once that much wall-clock time has elapsed (the candidate
in flight finishes). Candidates are visited oldest-checked first, using a per-entity
`meta.reconcile.checked.<id>` stamp written after every attempt, so each bounded run resumes where
the previous one stopped. Re-reads are serial (the gatherer is not safe for concurrent calls).

Exit codes: `0` when every candidate succeeds (including none) and also when the budget ran out
with candidates remaining — that case is distinguishable only by the stderr JSON line
`{"event":"reconcile_budget_exhausted","processed":N,"remaining":M}`; `1` when config or store
cannot be opened, or when any candidate's re-drive failed (naming which), even if the budget also
ran out.

## doctor

`pg-desk doctor` checks: the config resolves; `pg-connector` is on `PATH` and its `config
validate` passes; `serve` is reachable; that no row carries a recorded `sync_error` (a gate: any
such row fails `doctor`, and each is listed with its error and its retry indicator — still
retrying, with retries used out of the bound and the next retry time; exhausted; or
non-transient — see [`sync.md`](sync.md)'s "Automatic retry"); and the stranded-cycle report
formerly produced by `pr-pool reconcile`. `pg-connector config validate`'s own
query-name-coverage check (a config-authoring signal comparing a backend's declared query names
against its peers of the same type, bead `pg2-2j5ac.28.1`) is informational-only as of
`pg2-rnnfz` — it does not affect that check's pass/fail verdict, so a query-coverage gap alone
never fails `doctor`.

`doctor` also runs the change-flow checks, printed under a `change_flow:` heading, on a store that
has been cut over to the change-flow schema:

- **Watched queries resolve.** Every configured watched query (`watch.<type>.queries`) is probed
  against `pg-connector` with its read-only listing verb (`pg-connector <type> list --query <q>
--ids-only --output json`), which touches no change ledger and no consumer cursor and fails when
  `pg-connector` does not recognize the query name. A query that does not resolve fails `doctor`,
  naming the type and the query. A partially degraded answer (some backend down) still counts as
  resolved: it says nothing about the query name.
- **Stalled consumers.** A registered consumer whose `seen_at` is older than three times its expected
  period fails `doctor`; it is reported, never pruned. The expected period is the timer period of the
  router `[[query]]` whose command names that type and that consumer (`--consumer <name>`), when
  `--router-config` supplies one; a consumer no router query names is checked against
  `consumer_stale_after` (default 7 days) instead, because no period is known. A consumer that was
  never seen counts as stalled.
- **The sweep sizing bound** (`active_count / N x poll_interval <= D`, see [`changes.md`](changes.md))
  per entity type. `doctor` evaluates it ONLY when `--router-config` supplies the poll interval: the
  period of the router `[[query]]` entries whose command names the type and carries a `--consumer`,
  taking the smallest when several match. Otherwise it prints the bound's inputs (`active_count`,
  `max_per_poll`, `max_age`) with `poll_interval: unknown` and no verdict, exactly as `status`
  does. A violated bound fails `doctor`.
- **Repeated degraded hydrations.** The entities whose hydration degraded or failed on at least two
  consecutive attempts, per type, with their count and the time the run began. A report, not a gate.
- **Router roles** (only with `--router-config`): per type, the decider roles bound to it. A role
  binds to a type when any of its `binds` entries starts with `<type>.` — exact string comparison, no
  wildcard. The list is expected empty for `issue` and `thread` until their deciders exist. A report,
  not a gate.

`--router-config <path>` points `doctor` at the pg-router config, read as a file (no dependency on
pg-router). `doctor` reads only: each `[[query]]`'s `trigger = { kind = "period", every = "..." }`
and command `argv`, and each `[[role]]`'s `name`, `enabled` and `binds`. A config that cannot be read
or parsed fails `doctor` as a `router config` check; the checks that need no router config still run.

On a store that has NOT been cut over (old schema, or none at all), `doctor` does not refuse and does
not crash: it reports that the store is unmigrated, points at `pg-desk migrate --cutover`, skips the
change-flow checks above, and still prints the `sync_error rows` and `stranded cycles` lines it
always prints.

"The config resolves" includes every configured `repos[].beads_dir`: config load MUST fail —
so every command, `serve` startup, and `doctor` exit non-zero — when a `beads_dir` does not
exist, is not a directory, or is not a beads workspace (no `config.yaml` or `metadata.json`).
The error MUST name the repo and the path. This keeps a stale path from surfacing later as a
swallowed per-event bead-write failure.

**Path-move checklist.** Whenever a checkout or tracker directory moves (for example a
primary-checkout relocation), update the deployment's pg-desk `repos[].beads_dir` in the same
change, then run `pg-desk doctor` and confirm the config check passes.

Exit codes: `0` when every check passes; `1` when any check fails (naming which one) — including an
unresolvable watched query, a stalled consumer, a violated sweep bound and an unreadable
`--router-config`, so an alert can hang off the exit code.

## heartbeat / heartbeat-item

`pg-desk heartbeat` stamps `meta.last_heartbeat`. `pg-desk heartbeat-item` prints the single
pr-pool item the `desk-heartbeat` query emits, carrying a timestamp id.

Exit codes: `0` on success; `1` when the store cannot be written (`heartbeat`) or read
(`heartbeat-item`).

## Telemetry and logs

All six commands emit nothing over OpenTelemetry or Prometheus through Phase 10 (D24). `status`'s
counts and `doctor`'s checks are read-side reporting only, not an exported metrics surface — that
surface is `serve`'s minimal `/metrics` (see [`serve.md`](serve.md)). `sweep` is the one exception
to "logs only ordinary CLI report/error text" below: because it calls the SAME per-entity pipeline
`run` does (see [`pipeline-run.md`](pipeline-run.md)), it carries `run`'s own structured-JSON
logging contract too — one line per entity, to stderr, plus the three-stage timeline under
`sweep`'s own `--verbose`. `show`, `status`, `doctor`, `heartbeat`, and `heartbeat-item` carry no
such contract; each of those five logs only ordinary CLI report/error text.

## Out of scope (Phase 9, narrowed by Phase 10)

- `doctor`'s stranded-cycle report is unchanged by Phase 10 — sync minting/reconciling
  agent-signal beads does not, on its own, give `doctor` a report to run; that report's own
  implementation remains this docket's later concern.
- All six commands operate against the single repository this phase supports.
