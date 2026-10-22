# Investigation: issue change emission, spikes answered by reading code and documents (2026-10-22)

Phase 1 of the issue change emission work (beads and Jira, watched-entity events only, plus a Jira
daemon) owes a set of read-only spikes. This page records the ones that need only code, documents
and a handful of read calls to the external tracker. The spikes that need live data and tools are
recorded in the sibling page `2026-10-22-issue-change-emission-spikes-live-data.md`.

This repository is public, so the page names no organization, tenant, project, issue key, person or
home directory. The repository that owns the `pjira` command is named only by that description and
by module-relative paths (`modules/jira/pkg/pjira/`, `modules/jira/cmd/pjira/`). The same findings
are posted as one comment on the program epic.

Each section below holds three things: the finding, the evidence read (file, symbol and what it
showed), and the default that applies if the spike changes nothing. No code, configuration or
tracker state was changed. The tests were not run (the closed-at check is a judgement from reading
the ladder and its tests).

## Summary

| Spike                         | Finding                                                                                                                                               | Default                                                                          |
| ----------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| Closed-at migration           | Compatible: an additive nullable column on `entity` is the same kind of step as the shipped version 3 column. Rollback of a daemon binary is refused. | Proceed as designed; phase 2 updates the version-pinned tests and adds a fixture |
| Capabilities configuration    | No: the umbrella sends the capabilities op with no configuration; backends answer from compiled-in values                                             | The umbrella adds the query list to the answer                                   |
| Dependant-staleness detector  | Keep the default; the reverse-edge index is available at hydration time                                                                               | Re-hydrate watched direct dependants of a closed watched blocker                 |
| Tracker-versus-store compare  | No evidence to build it                                                                                                                               | Not built                                                                        |
| Router failed-dispatch series | No: the failure counter has a role label and no event-type label                                                                                      | Add the series in the router (later work, not done here)                         |
| Shadow harness location       | No option dominates; a question for the operator                                                                                                      | The decision stays open until the operator rules                                 |
| pjira references              | Five references re-read: all confirmed (two with a one-line overhang)                                                                                 | Keep the design's references                                                     |
| Atlassian text                | Figures match, except two differences: no exemption end date is stated, and `Retry-After` is a floor, not a hint                                      | Budget stays internal and conservative                                           |
| Burst headroom                | Recommended cap 2 requests per second is 2% of the GET and POST burst limit; live headroom unreachable                                                | Cap of 2 requests per second, concurrency 2                                      |
| search --expand               | Yes: comments and the status changelog come back in the listing; the description does not                                                             | Use expand for comments and changelog; keep per-issue calls for the description  |

## Closed-at migration

**Finding.** An additive, nullable `closed_at` column on the `entity` table is compatible with the
GitHub daemon's store: it is the same kind of step the ladder already contains, it changes no
shipped rung, and no query in the GitHub daemon reads the entity table by position. Three
consequences phase 2 must plan for:

1. A binary that predates the new rung REFUSES a store at the newer version: it exits without
   touching the file. Once any daemon binary built with the new rung has opened a store, that store
   cannot be opened by an older binary, so rolling a daemon binary back needs the store moved aside
   (a full refill and loss of the change log). This is a rollback hazard, not an upgrade one.
2. Several tests pin version 3 literally and must be updated by the step that bumps the version (list
   below). Their failure at the bump is by design, not a regression.
3. The fixture generator seeds the entity table with a positional insert of seven values, which
   fails once the table has an eighth column. A new fixture for the new version needs a named-column
   insert.

The rung's position relative to the other design's rung is not decided here; the shared-library
hoist owns that.

**Evidence read.**

- `packages/pg-connector/pkg/connectord/store/schema.go`: `SchemaVersion = 3` and the ladder
  `builtinMigrations` has three rungs (`migrateV1`, `migrateV2`, `migrateV3`). The comment on the
  ladder says a shipped migration MUST NOT change and a new step adds a fixture store under
  `testdata/`. The `entity` table (rung 1) has the columns `kind`, `id`, `version`, `last_access`,
  `keep_alive`, `last_known_mergeable`, `tombstoned_at`: no closed-at timestamp.
- Same file, `schemaV3`: ends with `ALTER TABLE query ADD COLUMN baseline_at INTEGER;`, a nullable
  column added to an existing table by a shipped rung. This is the direct precedent.
- `packages/pg-connector/pkg/connectord/store/tables.go`: `entityCols` is the explicit list
  `kind, id, version, last_access, keep_alive, last_known_mergeable, tombstoned_at`, used by both
  entity selects, and the three entity inserts name their columns. A new column is therefore
  invisible to the existing accessors.
- `packages/pg-connector/pkg/connectord/store/migrate.go`: `applyOne` runs each rung and
  `PRAGMA user_version = N` in one transaction, so a rung either applies fully or leaves the version
  alone. `runMigrations` returns `SchemaNewerError` when the file's version exceeds the ladder.
- `packages/pg-connector/pkg/connectord/store/store.go`: `Open` reads the file header first
  (`readHeader`, the 4-byte `user_version` at offset 60) and, if it is above the binary's ladder,
  calls `refuseNewer`: outcome `OutcomeRefusedNewer`, the file is "never moved and never modified".
  A migration that fails moves the store aside (`OutcomeMovedAside`), which costs a refill.
- `packages/pg-connector/cmd/pg-connector-github/internal/serve/store_test.go`:
  `TestErrorTable_StoreNewerThanTheBinary_RefusesToStartAndNeverMovesTheFile` shows the daemon's
  behaviour on a newer store: serve exit code 5, stderr names the schema version and "downgrade",
  a `store_refused` log line at error level, the file byte-identical afterwards. It pins version 99,
  so it stays green across a bump.
- `packages/pg-connector/cmd/pg-connector-github/internal/serve/serve.go`: the only use of the
  open result in the daemon is `d.opened.Outcome == store.OutcomeMovedAside` (record a moved-aside
  marker); `OutcomeMigrated` only produces a log line in
  `internal/logs/structured.go` (`StoreOpened`). A migrated GitHub store behaves like any other.
- Outside the store package, the only references to the store's version in this module are that
  newer-schema test and logging; no other package hard-codes version 3.

**Tests phase 2 must keep green** (all under `pkg/connectord/store` unless stated):

- `TestSchemaVersionMatchesLadder`, `TestValidateLadderRejectsGapsAndNilApply`.
- `TestFixtureExistsForEveryVersion` and `TestOpenFromEachFixture`: the first needs a new
  `testdata/v4.sqlite3` (generated, not hand-edited, by `TestGenerateFixture` with
  `PG_CONNECTOR_STORE_UPDATE_FIXTURES=1`); the second then opens every fixture, version 1 up, and
  checks the old data survives.
- `TestUpMigrationFromFixtureKeepsData`: climbs from the version 1 fixture through the shipped ladder
  plus a scratch step; it uses `SchemaVersion + 1`, so it follows the bump.
- `TestNewerSchemaRefusedAndFileUntouched`, `TestNewerSchemaInWALIsRefusedNotMovedAside`,
  `TestFailedMigrationMovesStoreAside`: relative to `SchemaVersion`, they follow the bump.
- Tests that pin 3 and must be edited with the bump: `TestOpenEmptyCreatesCurrentSchema` (constant
  `wantVersion = 3` and the per-table column list, which gains `closed_at` for `entity`),
  `TestVersion1And2FixturesMigrateToVersion3KeepingTheirData` (`SchemaVersion != 3` fails) and
  `TestVersion3FixtureIsAPlainOpenAndReopenIsIdempotent` (expects "opened" at 3 and `ToVersion` 3).
- `TestGenerateFixture`: change `INSERT INTO entity VALUES (...)` to a named-column insert before
  generating the next fixture.
- The GitHub daemon's own serve and store tests (`internal/serve/store_test.go`, the summary and
  changes packages that open real stores) as a regression run; none reads the entity table by `SELECT *`.

**Default.** If nothing changes: proceed with the additive column as designed, verify it with the
store package's migration tests plus the GitHub daemon's serve tests when the rung lands, and record
the rollback refusal in the daemon's rollout notes. The rung's position is the hoist's decision.

## Capabilities configuration

**Finding.** No. A backend does not see its configuration on a capabilities call as the umbrella
sends it today, and every backend's capabilities answer is a compiled-in value. Which component can
populate each query's watched value in the capabilities answer:

| Component                                          | Can populate the watched value? | Why                                                                                                                                                                       |
| -------------------------------------------------- | ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| The umbrella                                       | Yes                             | It loads the configuration and holds each backend's block (`Registry.BackendConfig`), including its named queries, so it can add the values to the answer after the call. |
| A daemon-backed backend's client (the Jira daemon) | Yes, if built to                | Its argv carries its own configuration file path (the GitHub client is invoked with `--config <path>`), so its capabilities handler could read its own file.              |
| The beads backend                                  | No                              | It is a stateless script with no configuration of its own on this call; it sees a configuration block only when the umbrella attaches one to an op request.               |

The shape of the answer is left to the sibling task "P1: connector interface doc and the
pg-router-source-pg-desk adapter doc"; see its comment on the program epic.

**Evidence read.**

- `packages/pg-connector/pkg/scriptout/exec.go`, `InvokeCapabilitiesTarget`: builds the request as
  `Request{Op: OpCapabilities}` with no `Args` and no `Config`, whereas `InvokeTarget` copies a
  `config` argument onto `Request.Config` for every other op.
- `packages/pg-connector/pkg/scriptout/envelope.go`: `Request` has a `Config json.RawMessage`
  member, so the wire could carry it; `CapabilitiesResponse` has `ProtocolVersion`, `SchemaVersions`,
  `Ops`, `Vocabulary` and `Version` and nothing for queries.
- `packages/pg-connector/pkg/scriptout/capabilities.go`, `AddCapabilities`: the handler is
  `func(_ context.Context, _ json.RawMessage)`: it ignores both its context (which would carry the
  request configuration) and its arguments, and returns a copy of the compiled-in response with
  `Ops` computed from the dispatch table.
- `packages/pg-connector/cmd/pg-connector-issue-jira/main.go`, `capabilitiesBase`, and
  `cmd/pg-connector-issue-beads/main.go` (same `AddCapabilities` call): static schema versions and
  vocabulary, no configuration.
- `packages/pg-connector/cmd/pg-connector-github/internal/client/instances.go`,
  `Instance.Capabilities`: the daemon client answers "compiled-in", without contacting the daemon.
- `packages/pg-connector/cmd/pg-connector/registry_backend.go`: `Registry.InvokeCapabilities` calls
  `scriptout.InvokeCapabilitiesTarget(ctx, r.Target(name))`; the two umbrella callers
  (`changes_refresher.go`, `config_validate.go`) and `capabilitiesAll` in `fanout.go` use only that.
  `registry.go` `BackendConfig` returns a backend's configuration block, and `config_show.go`
  already prints each backend's named queries (`config show --queries`), so the umbrella already
  reads them.

**Default.** The design's default applies: the backend does not see its configuration, so the
umbrella adds the query list (with each query's watched value) to the capabilities answer.

## Dependant-staleness detector

**Finding.** Keep the default. The reverse-edge index of INV-CHANGES-17 is available at hydration
time: it is built from the store alone, and the change engine holds that store while it hydrates.
The default detector (on a watched blocker's closed row, re-hydrate its watched direct dependants
through the index, within `hydration.max_per_poll`) is implementable as designed.

Points the implementation must respect, found while reading:

- The index is built per read by one full scan of stored entities with a JSON decode of each issue's
  facts, so a poll should build it once, not once per closed blocker.
- It lists a dependant only from the dependant's own stored direct `blocks` edges, so a dependant
  that was never hydrated is not in it; that is acceptable because only watched dependants matter.
- Its `Available` answer concerns the blocker's own stored recursive dependencies and is not needed
  by the detector.
- The poll already passes one budget object through the listed-change phase and the remote sweep, so
  the new phase fits between them and spends the same `hydration.max_per_poll` budget.
- Today a periodic remote age sweep (default maximum age 6 hours, 20 entities per poll) eventually
  catches such a dependant; the default detector only matters once that sweep is removed.

**Evidence read.**

- `packages/pg-desk/internal/dependency/issue_dependents.go`: `NewIssueDependents(r Reader,
isTerminal)` reads `ListEntities()` once and indexes each issue's stored `issue_show.deps` edges
  of type `blocks` (case-insensitive), skipping inactive and caller-defined terminal dependents;
  `DependentsOf` returns the sorted direct dependants; the type comment says the recursive
  `issue_deps` set "is deliberately NOT the edge source".
- Same package, `Reader`: `RequireNewSchema`, `ListEntities`, `ListXrefLinksFrom`,
  `ListXrefLinksTo`. `packages/pg-desk/internal/focus/rank.go` builds the index from the store reader.
- `packages/pg-desk/internal/changes/changes.go`: the `Engine` has `Store *store.Store` and already
  calls `e.Store.ListEntities()` (`activeEntityIDs`); the poll creates `bud := &budget{left:
e.Cfg.HydrationMaxPerPoll()}` and runs `hydrateListed` then `sweep` with it.
- `packages/pg-desk/internal/config/config.go`: defaults `DefaultHydrationMaxPerPoll = 50`,
  `DefaultSweepMaxPerPoll = 20`, `DefaultSweepMaxAge = 6h`.
- `docs/behavior/pg-desk/changes.md`, INV-CHANGES-16 and INV-CHANGES-17: the stored edges and the
  direct-`blocks`-only, unavailable-not-empty rules the index implements.

**Default.** On a watched blocker's closed row, re-hydrate its watched direct dependants through the
reverse-edge index, within `hydration.max_per_poll`.

## Tracker-versus-store comparison

**Finding.** No evidence changes the default: a direct comparison of tracker state with stored state
for watched issues is not built. What the code shows against building it:

- Every watched member already has its stored fingerprint compared with the tracker's on each poll
  (the list diff hands a list fingerprint to hydration), so a separate comparator would repeat that
  work with extra tracker reads.
- A comparator needs one tracker read per watched issue: a process per issue for beads, one request
  per issue for Jira, charged to the budget the Jira daemon is meant to keep small.
- The designed outcome alert (the age of the oldest successful observation of a watched source)
  already bounds how long a watched open issue can differ, using no extra reads.

**Evidence read.** `packages/pg-desk/internal/changes/poll.go`, `hydrateListed` and its `diffListed`
call (hydration receives `listFP`, the list fingerprint, per pending entity);
`packages/pg-desk/internal/changes/listing.go`, `recordListing` (the stored listing status is
overwritten at every call, failures included, which is why the design adds a separate
last-success key); the Jira request budget in the Atlassian-text and burst-headroom sections below.
The size of the watched set per backend is a live-data spike, not read here.

**Default.** Not built.

## Router failed-dispatch series

**Finding.** No. The router does not expose a series of failed dispatches labelled by BOTH event
type and role. The failure counter is labelled by class, role and (for some classes) reason, and the
event type is deliberately left out of its label set. Event-type-and-role labels exist only on
success-side and gate series.

**Evidence read.** All in `packages/pg-router/internal/metrics/metrics.go`:

- `MetricFailures` (`pg_router_failures`, `Int64Counter`): fed by `OnDeclined`,
  `OnDispatchFailure` and `OnHandlerFailure`; each records `class` and `role` (declined adds
  `reason`), and each discards its event-type argument (`OnDispatchFailure(_, listenerID string)`;
  the doc says the type "is not part of the label set").
- Series with both `type` and `role`: `RecordThroughput` (accepted dispatches),
  `RecordQueueWait`, `RecordRun`, `RecordDispatchLatency` (it has an `outcome` label, but its doc
  says only "accepted" is ever recorded and failure outcomes are deferred). `OnGateDrop` carries
  `event_type`, `role` and `gate`, which is not a failed dispatch.
- The dispatch-failure hook itself exists: `eventqueue.Observer.OnDispatchFailure(evtType,
listenerID)` in `packages/pg-router/internal/eventqueue/queue.go` receives both values, so the
  data reaches the emitter and is dropped there. It fires only for a panic recovered from a
  listener's offer; handler-reported errors come through `OnHandlerFailure`, which has the event
  type, the listener and the error.
- `packages/pg-router/cmd/pg-router/run.go`: `activityObserver.OnDispatchFailure` records the event
  type in the activity ring buffer, which is not a metric.

**Default.** Add the series in the router (a failed-dispatch counter by event type and role, fed
from the hooks above), not a pg-desk side table. Until it exists, the alert for a failed decider run
with no later change stays the router's generic failure-rate rule and the gap stays named. Acting on
this is later work and is not done here.

## Shadow harness location

**Finding.** No option dominates, so this is a question for the operator. Options compared:

- **A. Extend `pg-desk-shadow`.** Reuses the sandbox and its start-up self-test, the allowlisted
  child environment and its per-tick verification, the runner, the collector and the report, and the
  runbook's single-run rule.
- **B. A separate harness beside the Jira daemon's package.** Avoids touching a harness the GitHub
  run depends on.

Evidence for and against, from the safety policy:

- The policy is a CLOSED allowlist built around one GitHub daemon and the beads tracker directory:
  `allowed` lists a fixed set of variables; `Policy.Daemon` switches on exactly five
  `PG_CONNECTOR_GITHUB_*` variables, checked by `verifyDaemon`; `BeadsDir` is the one read-only
  tracker directory, verified for `BEADS_DIR`, `PG_CONNECTOR_ISSUE_BEADS_DIR` and every `--beads-dir`
  word in the scratch config. A Jira daemon needs its own variables and a credential path, so A means
  generalising `Daemon` from one daemon to a list.
- The shims classify only `gh` and `bd` (`internal/shim/classify.go`: read allowlists, everything
  else refused). A Jira run needs a read-only classifier for `pjira` (its verbs include create,
  comment and transition, which write).
- The budget guard (`internal/budget`) is a GitHub points guard (a floor on the shared GraphQL
  token's remaining points). Jira exposes a rate, not a quota (see the Atlassian text), so it needs a
  different guard.
- The scratch configuration derivation deletes the Jira block (`scratch/config.go`, "no Jira"), and
  the report model and cost section are GitHub-points shaped.
- The Jira token: `pjira` reads it by default from the environment variable `JIRA_API_TOKEN`
  (`secret.go`), while the allowlist passes no secret; the policy keeps the real home directory only
  so the GitHub login keychain resolves. Either a secret is added to the allowlist or `pjira` is
  pointed at a file or command source: a credential-handling decision inside a safety policy.
- The module has no compile-time dependency on the daemon (`default.nix`, `scratch/daemon.go`: names
  are a contract pinned by a test) and its packages are `internal/`, so B cannot import the policy:
  it would copy it. A second copy of a safety policy can drift from the first.
- The sandbox does not block the network, so for a remote tenant the shim and the budget guard are
  the only protection (runbook safety summary); both are new work in either option.

The question for the operator: extend the shared harness (generalise the daemon policy and add a
`pjira` read shim, a request-rate guard and a credential rule), or build a separate harness and
accept the duplicated safety code? The deciding fact is how the Jira token reaches the child.

**Default.** The decision stays open with the operator; nothing in the Jira daemon's first steps
depends on it before the shadow run.

## pjira references

Re-read on 2026-10-22 in the repository that owns `pjira` (read only, nothing edited, committed or
branched there). Each reference is confirmed, moved or wrong:

| Reference (design)                                                                      | Result                       | What the code showed                                                                                                                                                                                                          |
| --------------------------------------------------------------------------------------- | ---------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `client.go:127`, basic auth with an API token                                           | Confirmed                    | `(*Client).do` sets `Authorization` from `c.basicAuth()`; `basicAuth` (lines 38 to 40) returns `"Basic " + base64(Email + ":" + Token)`.                                                                                      |
| `secret.go:83`, `JIRA_API_TOKEN`                                                        | Confirmed                    | `NewSecretSource`: for source `""` or `env` with no `EnvVar`, the variable name defaults to `JIRA_API_TOKEN`. Other sources: `file`, `command`.                                                                               |
| `config.go:21`, `JIRA_API_TOKEN`                                                        | Confirmed                    | `DefaultConfig` returns `Secret: SecretConfig{Source: "env", EnvVar: "JIRA_API_TOKEN"}`.                                                                                                                                      |
| `eventlog.go:33-37` (this repository, Jira backend), "no figure, header or Retry-After" | Confirmed, one-line overhang | The quota bullet in the package comment of `cmd/pg-connector-issue-jira/internal/eventlog/eventlog.go` starts at line 33 and its last clause ("the wire error_code stays unavailable") ends on line 38. The wording is exact. |
| `backend.go:184-189` (this repository, Jira backend), `updated` not mapped              | Confirmed                    | `cmd/pg-connector-issue-jira/internal/backend.go`: lines 184 to 189 are the `Updated` field and its comment ("deliberately not mapped onto schema.Issue"); `toSchemaIssue` (line 304) maps no description or comments.        |

Also read, relevant to the budget: `pjira` surfaces no rate-limit header. A search of its non-test
sources for `RateLimit`, `Retry-After`, `NearLimit` and `429` found nothing; a non-2xx answer becomes
an error `status <resp.Status>` (for example `pjira: search: status 429 Too Many Requests`), which is
what the backend's `rateLimitMarkers` match.

**Default.** Keep the design's file and line references (with the one-line note above).

## Atlassian text

Read live on 2026-10-22 from the Jira Cloud rate limiting page on the Atlassian developer site (page
footer: last updated 2026-10-22), through a summarising fetch, twice (the second time asking for
verbatim sentences). Figures as read:

| Item                                  | As read                                                                                                                                                                                                                         | Design's quoted figure                      | Difference                                                                 |
| ------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------- | -------------------------------------------------------------------------- |
| Points-based hourly quota enforcement | Begins 2026-03-02                                                                                                                                                                                                               | Enforced from 2026-03-02                    | None                                                                       |
| Apps it applies to                    | "all Forge, Connect, and OAuth 2.0 (3LO) apps"                                                                                                                                                                                  | Forge, Connect and OAuth 2.0 (3LO)          | None                                                                       |
| Global pool                           | 65,000 points per hour, shared across all tenants (default tier)                                                                                                                                                                | 65,000 per hour                             | None                                                                       |
| Per-tenant pools                      | Assigned only after review; the page lists sizes by edition (for example Standard 100,000 plus 10 per user, capped at 500,000)                                                                                                  | Assigned after review                       | More detail on the page; none affects an API-token client                  |
| API-token traffic                     | "API token-based traffic is not affected by this change, and will continue to be governed by existing burst rate limits."                                                                                                       | Not affected; exemption time-bound          | The page states NO end date for the exemption (sentence search found none) |
| Burst limits, per tenant per endpoint | GET 100, POST 100, PUT 50, DELETE 50 requests per second; some endpoints differ (GET issue 150, POST approximate-count 150)                                                                                                     | 100 for GET and POST, 50 for PUT and DELETE | None for the defaults; two custom per-endpoint limits noted                |
| Per-issue write limits                | 20 writes per 2 seconds, 100 per 30 seconds                                                                                                                                                                                     | Same                                        | None                                                                       |
| Headers on every response             | `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-NearLimit` (`true` below 20% capacity)                                                                                                                               | Same                                        | None                                                                       |
| Headers on 429 only                   | `Retry-After`, `X-RateLimit-Reset`, `RateLimit-Reason` (`jira-burst-based`, `jira-quota-tenant-based`, `jira-quota-global-based`, `jira-per-issue-on-write`)                                                                    | Same                                        | None. `Retry-After` may also appear on some transient 5xx answers          |
| Beta headers                          | `Beta-RateLimit-Policy`, `Beta-RateLimit`, `Beta-Retry-After`, informational only; the prefix is to be dropped at enforcement                                                                                                   | "Beta-RateLimit" points headers             | More detail; still irrelevant to API-token traffic                         |
| Meaning of `Retry-After`              | "Respect `Retry-After`: If present, use the `Retry-After` header value as the minimum delay."                                                                                                                                   | "Atlassian says Retry-After is a hint"      | DIFFERENT: the page calls it a minimum delay, not a hint                   |
| Backoff                               | Exponential backoff with jitter (factor 0.7 to 1.3, doubling per 429, capped attempts); quota 429s: pause all requests until the window resets; the example values are inconsistent (2 s base in prose, 5,000 ms in pseudocode) | Double the pause with jitter                | More detail; the design's rule is consistent with it                       |

Implications: the hourly quota still does not apply to the daemon today (API-token traffic), and the
live page, as read, gives no date on which that stops; the design's "time-bound" claim is not
supported by the text read, but a summarising fetch can drop a sentence, so the daemon SHOULD NOT
rely on the exemption. Because `Retry-After` is a floor, the design's pause (default 60 seconds when
absent, doubled on consecutive 429s) should never go below the header's value when it is present.

**Default.** The budget stays internal and conservative (a request-rate cap and a concurrency cap,
pause on 429 for at least `Retry-After`), as the design says; the 429 signal and the daemon's own
counter are the only inputs until `pjira` surfaces headers.

## Burst headroom

**Finding.** Recommended request-rate cap for the Jira daemon: 2 requests per second, concurrency 2.
Its share of the documented burst limit is cap divided by limit:

- GET and POST (the daemon's reads are GET and POST searches): 2 / 100 = 0.02, that is 2%.
- GET of one issue, which has its own limit of 150: 2 / 150 = 0.0133, about 1.3%.
- PUT and DELETE (limit 50 per second; the daemon's writes stay in client mode, so it sends none):
  for reference only, 2 / 50 = 0.04, that is 4%.
- At the cap the daemon sends at most 2 x 3,600 = 7,200 requests per hour, and stays far below the
  20% capacity point (20 requests per second at the 100 limit) at which `X-RateLimit-NearLimit`
  turns true.

The cap is a recommendation, not a measurement: the design fixes no number, and the watched-set size
that sets real demand is a live-data spike. The share is of the DOCUMENTED limit; how much of it the
shared tenant's other traffic already uses is unreachable: `pjira` surfaces no rate-limit header (see
the pjira references), none of the four read calls made for this page saw a 429, and a load test of
the shared tenant is out of bounds.

**Evidence read.** The Atlassian text above (limits); `pjira` `Search` posts to
`/rest/api/3/search/jql` and `GetIssue` gets `/rest/api/3/issue/<key>` (`client.go`), the two
endpoints the daemon's reads use.

**Default.** A cap of 2 requests per second and a concurrency cap of 2, counted by the daemon,
raised only when the live headers or a measured demand justify it.

## search --expand

**Finding.** Yes for comments and changelog; no for the description. `pjira search --expand
changelog,comments` returns both in the listing, so per-issue detail calls can be avoided for them.
The listing still carries no description (it is not in the fixed field list), so a description still
needs a per-issue call.

From four read-only calls through the installed `pjira` (two authentication checks, the first of
whose answers was not captured, and two searches, none in a loop, none against a write verb): the
authentication check answered ok. A one-result search of the operator's own most recently updated
issues, with expansion, returned one item with `comments` (3 entries) and `changelog` (9 entries)
alongside the ordinary fields. A five-result search returned, per item, comments / changelog entries
of 3/9, 5/2, 3/0, 10/3 and 2/1. Each comment carries `id`, `author`, `body` (flattened text) and
`created`; each changelog entry carries `id`, `author`, `field`, `from`, `to` and `at`, and every
changelog entry's field was `status`. Both answers reported `truncated` with a next-page token, as a
limited search should. No error, no 429. Not tested: an issue with a very long comment list or
history (whether the search truncates them is unverified).

**Evidence read.** `modules/jira/pkg/pjira/client.go`: `SearchPage` requests the fixed field list
(summary, status, issuetype, labels, priority, project, created, updated, duedate, reporter,
assignee, parent), adds the `comment` field when `ExpandOpts.Comments` is set (comment: "not a Jira
expand: sending expand=comments returns nothing") and sets `expand=changelog` when
`ExpandOpts.Changelog` is set; the mapping keeps only changelog items whose field is `status`.
`modules/jira/cmd/pjira/main.go`, `newSearchCmd`: `--expand` takes `changelog,comments`. The
backend's own model (`cmd/pg-connector-issue-jira/internal/backend.go`) notes `Changelog` and
`Comments` are populated only by this option.

**Default.** Use `--expand changelog,comments` on the listing for the comment summary and status
history; keep a per-issue call for the description (the detail group).
