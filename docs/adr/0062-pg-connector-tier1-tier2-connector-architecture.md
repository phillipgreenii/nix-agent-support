# pg-connector: Tier-1 umbrella + Tier-2 backend connector architecture

**Status**: Accepted
**Date**: 2026-09-06
**Deciders**: Phillip Green II

## Context

`pg-pr`, `pr-pool`, and `work-activity-tracker` each independently reimplemented overlapping
GitHub/Jira/beads sync and correlation logic. A design pass (recorded at the time in
`docs/superpowers/specs/2026-09-03-unified-connector-architecture-design.md`, landed on `main`)
replaced that duplication with a pluggable connector suite: `pg-connector` (Tier 1) as the
generic, org-agnostic umbrella; one thin binary per (entity type, backend) pair (Tier 2)
implementing it for one external system each; a ZR-specific consumer layer (Tier 3, outside this
repo). `pr-pool` remains the cross-system workflow dispatcher, unchanged in its own core.

Ten code packets landed against that design before this ADR did: the four entity-type capabilities
(pr, issue, ci, scm), their shared schema/provider/wire-protocol packages, and four Tier-2 backends
(`pg-connector-pr-github`, `pg-connector-ci-github-actions`, `pg-connector-scm-git`,
`pg-connector-issue-beads`). None of them was accompanied by an ADR or a `docs/behavior/` set, even
though the design's own acceptance criteria required exactly that set to be authored as
pg-connector's **first** work packet, before any code-producing packet, "so later packets have
real behavior-IDs to cite from day one." That ordering was not honored. This ADR, together with
the `packages/pg-connector/docs/behavior/` set it accompanies, is that gap filled retroactively
(bead `pg2-wajat`).

A design spec under `docs/superpowers/specs/` is not this repo's durable citation target (see this
repo's `CLAUDE.md`, "Architecture Decision Records" → "Citation conventions", rule 3) — the ~254
existing `[design: §N]` comments scattered through `packages/pg-connector`'s source predate that
convention being applied here and are tracked for a rewrite in bead `pg2-hidkm`, which needs a
durable, non-spec target to retarget them at. This ADR and the behavior-docs set are that target.

Independently of the retroactive-documentation gap, three post-landing review passes found real
divergences between the design's stated contract and the shipped code, each fixed and closed before
this ADR was written: version negotiation was specified but never wired (`pg2-p2z7o`); a caller
input error was misreported as backend ill-health because the error taxonomy had no code for it
(`pg2-r9iok`, which added the sixth `invalid_argument` code); and a Tier-2 backend was shelling out
to the Tier-1 umbrella that dispatches it, an authorization the design never actually granted
(`pg2-0vwcc`). This ADR's Decision below records the architecture **as built after those fixes**,
not the design document's first draft.

## Decision

Adopt, as this repo's architecture for connecting agent/human tooling to external
PR/issue/CI/SCM systems, the Tier-1 umbrella + Tier-2 backend model:

1. **One umbrella, N pluggable backends, scoped by capability, never by system.** `pg-connector` is
   the sole user-facing CLI and the sole holder of the shared entity-type schemas (`pr`, `issue`,
   `ci`, `scm`) and the wire protocol. It MUST know nothing about any backend's external system
   (GitHub, Jira, beads, git) — that knowledge lives entirely inside a Tier-2 backend binary,
   reached only through the wire protocol. In design-pattern terms: the umbrella is a **Facade**
   presenting one coherent CLI over N interchangeable backends; each capability's Provider Go
   interface (`pr.Provider`, `issue.Provider`, `ci.Provider`, `scm.Provider`) is a **Strategy** the
   umbrella selects among via its registry (today exactly one strategy is registered per capability,
   except `scm`, whose registry entry is single-valued by design — see invariant `INV-REG-1`); and
   each Tier-2 backend is an **Adapter** translating one external system's own shape into that
   capability's generic wire contract, realized as a **process-boundary adapter** — a separate OS
   process speaking a small JSON protocol, not an in-language object — because a backend's own
   dependencies (a `gh` binary, a `bd` binary, Cloudflare Access credentials) MUST NOT become the
   umbrella's own transitive dependencies.
2. **A capability, never a system, is the unit of interface design.** An interface's name and
   method set MUST correspond to exactly one capability (`pr`/`issue`/`ci`/`scm`, and any future
   entity type) and MUST name no backend/system. A single interface spanning one backend's own
   PR+CI+Issue operations looks unified but actually branches per system internally with no shared
   shape — exactly what this rejects (see "Alternatives Considered" below).
3. **The wire protocol is a small, versioned, JSON-over-stdio envelope with a closed six-value
   error taxonomy** — `not_found`, `unauthenticated`, `unavailable`, `unknown_op`,
   `version_mismatch`, and `invalid_argument` — and two independently-versioned numbers: one global
   `protocolVersion` for the envelope shape itself, and one `schemaVersion` per schema-bearing
   capability, so a breaking change to one capability's schema never forces every unrelated backend
   to redeploy in lockstep.
4. **pg-connector's own CLI exit codes are a layer separate from the wire protocol's plain `0`/`1`,
   and MUST NOT be built from or confused with it.** They split by op shape: a **fan-out** op
   (queries every backend registered for a type/capability) reports `0`/`2`/`3`
   (all-succeeded / degraded-partial / total-failure); a **targeted** op (resolves to exactly one
   backend) reports `0`/`4`/`1` (success / `not_found` — a well-formed negative, not a failure /
   any other error). Every multi-source response carries a `sources[]` row per backend actually
   queried, never collapsed into one pass/fail signal.
5. **A Tier-2 backend MUST resolve a cross-capability data need through its own direct, already-
   declared system access, and MUST NOT execute the `pg-connector` umbrella or a sibling Tier-2
   backend binary to satisfy its own op.** This reverses this repo's own first cut at the CI
   backend's PR→branch lookup, which briefly shipped by shelling out to `pg-connector pr show`
   before `pg2-0vwcc` found and fixed it.
6. **Credential resolution, auth checking, and a backend's own local store are each that backend's
   own concern.** Auth checking is asserted structurally through an optional `AuthChecker`
   sub-interface (a type-check, never a required method), so a backend with nothing to check (the
   local-git `scm` backend, which has no remote credential concept at all) simply does not implement
   it, and is reported as a well-formed "disabled: not applicable" rather than a forced or
   meaningless answer. pg-connector ships no shared credential-resolution library of its own.
7. **Adding a backend is a registry-config change, never an umbrella code change.** A registry
   entry is either a bare binary name on `PATH` or an instance, `{name, command}`, where `command`
   is an argv list whose first word is a bare binary name (no path separator, no whitespace) and
   whose remaining words are arguments passed to that binary. There is no `exec:`-prefix or other
   built-in/external distinction, because nothing is compiled into the umbrella itself. See
   item 11.
8. **A new entity-type capability (`calendar`) was added under this same model, without changing
   the model itself.** `calendar` follows every rule above identically: one capability-scoped
   `calendar.Provider` Go interface (`packages/pg-connector/pkg/provider/calendar`), its own
   independently-versioned `schema.CalendarSchemaVersion`, and (in a later packet of the same
   docket) its own Tier-2 backend and `connector.calendar` registry entry. That later backend
   (`pg-connector-calendar-osx-bridge`) talks to `osx-bridge-api` — a local, already-landed
   (bead `pg2-p9ap3`) shared daemon that solves a macOS TCC process-identity problem, never a
   credential-resolution concern. This does **not** contradict principle 6's rejection of a
   shared credential-resolution library: `osx-bridge-api`'s shared-daemon pattern exists because
   EventKit's own TCC permission grant is scoped to whichever process first requested it — a
   problem with no credential/token shape at all — distinct from resolving a backend's OWN
   external-system credentials (a GitHub token, a Jira session, …), which is the concern
   principle 6 actually rejects sharing.
9. **A new entity-type capability (`alert`) was added under this same model, without changing
   the model itself.** `alert` is a first-class Tier-1 entity (peer of `pr`/`issue`/`ci`/`scm`/
   `thread`/`calendar`): one capability-scoped `alert.Provider` Go interface
   (`packages/pg-connector/pkg/provider/alert`), its own independently-versioned
   `schema.AlertSchemaVersion`, a list-valued `connector.alert` registry entry, and the CLI verbs
   `alert list|show|history`. It follows every rule above identically, with these `alert`-specific
   decisions: (a) **attention is implemented DIRECTLY** — a backend registered for alerts also
   implements `attention.Provider` (merging both dispatch tables in one binary, as
   `pg-connector-calendar-osx-bridge` does) and is registered under the top-level
   `attention.sources` independently of `connector.alert`; the umbrella never derives attention
   items from `alert list`, because that would make it interpret alert fields and add a new
   coupling direction (principle 1); (b) the list is **firing-only** and a named query can only
   narrow it, never widen it; (c) `acknowledged` is an **optional** indicator, absence meaning
   "this source cannot express it", not "unacknowledged"; (d) severity mapping is each backend's
   own internal, closed table and is never defaulted; (e) there is **no umbrella entity-cache
   fallback** for alerts, since a cached firing set would render a stale "all clear" as current —
   unknown is distinguishable from none only via the `sources[]` row; (f) the capability is
   read-only (no acknowledge, silence, or hide). The two Tier-2 backends
   (`pg-connector-alert-grafana`, and a deferred `pg-connector-alert-pagerduty`) are separate
   work; this item records the Tier-1 entity, and the Grafana backend implements it.

10. **A new entity-type capability (`mail`) was added under this same model, without changing
    the model itself.** `mail` is a first-class Tier-1 entity (peer of `pr`/`issue`/`ci`/`scm`/
    `thread`/`calendar`/`alert`): one capability-scoped `mail.Provider` Go interface
    (`packages/pg-connector/pkg/provider/mail`), its own independently-versioned
    `schema.MailSchemaVersion`, and a list-valued `connector.mail` registry entry (never
    single-valued like `scm`). It follows every rule above identically, and, like `calendar`, it is a
    NEW capability rather than a backend of an existing one. Its op set is read (`list`, `show`,
    search), mark read/unread, archive/unarchive, and attachment fetch; it has **no delete operation,
    now or later** (invariant `INV-MAIL-1`, in `packages/pg-connector/docs/behavior/invariants.md`).
    This item does **not** restate item 8's distinction between a shared local daemon and a
    credential-resolution library; it relies on it. The Tier-2 backend (`pg-connector-mail-osx-bridge`)
    talks to the same local shared daemon item 8 describes (now named `pg-osx-bridge-api`, in
    `phillipgreenii-nix-support-apps`), and `mail` records its **own** reason for routing through it:
    not a TCC necessity (Apple Events automation is a different TCC permission category from
    EventKit's, so mail does not need the daemon's process-identity treatment the way `calendar`
    did), but **architectural consistency** (every Tier-2 backend stays uniformly simple and
    stateless, a thin socket client) and **readiness** for future OS integrations that DO need the
    daemon's TCC treatment. The bridge-side mechanism for driving Mail.app was ruled by the operator
    (Phillip, 2026-10-05, recorded as the close reason of bead `pg2-qc5uc.1`), quoted here
    without addition: "Option A -- in-process NSAppleScript/ScriptingBridge inside pg-osx-bridge-api;
    no subprocess, guards unchanged; adds AppleEvents grant for Mail to the bridge; needs new ADR
    superseding 0044 + updated allowlist.golden; backend talks only to the bridge mail service;
    helper options B1/B2 rejected." (The "0044" there is `phillipgreenii-nix-support-apps` ADR 0044,
    which that repo's own bridge work supersedes; this repo does not.)

## Consequences

### Positive

- The ten already-landed code packets (the four entity-type capabilities, their shared
  schema/provider/wire-protocol packages, and the four Tier-2 backends) already conform to this
  model; this ADR gives that architecture a durable record instead of leaving it to live only in
  code comments and a design document this repo's own conventions treat as non-durable.
- Capability-scoped interfaces keep a future fifth entity type or a second interchangeable backend
  for an existing one (e.g. a Forgejo PR backend) a registry-config change, not a rewrite of an
  interface that would otherwise have to grow a new backend-specific branch.
- The three post-landing fixes (`pg2-p2z7o`, `pg2-r9iok`, `pg2-0vwcc`) are now durable invariants
  with citable IDs (`packages/pg-connector/docs/behavior/invariants.md`), closing the risk of the
  same regression landing unnoticed a second time.

### Negative

- The full design's remaining scope — the `attention`/`search` cross-cutting capabilities,
  dashboard/alert conventions, the deferred `Thread`/`Note` entity types, `pg-pr`'s actual
  retirement, and the `df-categorize`/`df-feedback` pr-pool roles — is not yet built and is
  therefore deliberately **not** covered by this ADR's Decision or by the accompanying
  behavior-docs set's extent: only what has landed is recorded as intended behavior today. A future
  packet that builds one of those pieces MUST amend both this ADR's scope and the behavior-docs set
  in the same change, per this repo's own documentation rule (`CLAUDE.md`, "pg-pr / pr-pool
  Development Rules"). (That list is itself already stale with respect to `attention`, `search`,
  and `Thread` — all three have since landed, as an ADDITION to Decision item 1's own capability
  enumeration and to the behavior-docs set, never a removal from this list, since none of the three
  was ever named on it. Fixing that pre-existing drift is out of scope for the `calendar` packet
  that added this note.)
- As of bead `pg2-o2dmu` (the "pg-connector-calendar-osx-bridge: calendar Tier-2 backend" docket,
  decomposing bead `pg2-si5jo`'s design), the `calendar` entity-type capability is likewise added
  to this ADR's Decision (item 8, above) and to the accompanying behavior-docs set's extent — it is
  no longer part of the "not yet built" list in the bullet above.
- As of bead `pg2-wms83` (the `alert` Tier-1 entity, from design bead `pg2-k3lxs`), the `alert`
  entity-type capability is likewise added to this ADR's Decision (item 9, above) and to the
  behavior-docs set's extent. The PagerDuty Tier-2 backend is NOT yet built and remains outside
  the extent. The Grafana Tier-2 backend (`pg-connector-alert-grafana`, bead `pg2-rejc3`) has
  since landed with `list`, `show` and `list_attention`, and its `list_history` (bead `pg2-rwuhs`)
  has since landed too (per-rule enumeration and state-history parsing live in the backend).
- As of bead `pg2-qc5uc` (the "pg-connector-mail-osx-bridge: mail Tier-2 backend" docket,
  decomposing bead `pg2-no8ic`'s design), the `mail` entity-type capability is likewise added to
  this ADR's Decision (item 10, above) and to the accompanying behavior-docs set's extent. Only the
  Tier-1 capability (interface, wire schema, dispatch table) is covered by that first packet; the
  concrete Tier-2 backend, the CLI verb group and the registry entry are separate packets of the
  same docket, and `mail create`/`mail reply` are not built at all.
- Behavior-docs-first ordering, having been skipped for the first ten packets, cannot be
  retroactively un-skipped; this ADR and its behavior-docs set are a backfill, not evidence the
  process was followed from day one.

### Neutral

- This ADR and the behavior-docs set deliberately do not restate the design document's own numbered
  sections as their citation target. The design-spec-citation cleanup tracked in bead `pg2-hidkm` is
  expected to retarget pg-connector's existing `[design: §N]` code comments at this ADR and the
  behavior-docs set's own element IDs instead.

## Alternatives Considered

### Keep per-system interfaces (one interface spanning a backend's own PR+CI+Issue operations)

Rejected: this is exactly the shape a prior ZR-side interface (`INTF-ZR-CODEHOST`) took, and it
looks unified while actually branching per system internally with no shared shape — the opposite of
what capability-scoping buys, and it would make every new backend a change to an
already-multi-purpose interface rather than an implementation of a small, focused one.

### Let a Tier-2 backend call back into the umbrella (or a sibling backend) for cross-capability data

Rejected, and reversed after briefly shipping this way (`pg2-0vwcc`): it creates an undeclared
runtime dependency on `pg-connector` itself being on `PATH` and registered, a multi-process chain
per call, and a duplicated error-code-to-sentinel map, for data a backend can almost always resolve
directly against a system it already holds credentials for (the CI backend already had its own `gh`
gateway and needed only one more direct `gh pr view` call).

### A shared credential-resolution library

Rejected: the three backends already landed resolve credentials three different, legitimate ways
(GitHub's env-then-`gh auth token` chain, a keychain-backed CLI for beads/Jira-style tooling, and a
Cloudflare Access JWT for Captain's Log-style tooling); forcing one shape onto all three would fit
none of them well, and a future backend is free to pick whatever chain fits its own token model.

11. **A registry entry names an instance: a `name` plus an argv `command`; one backend binary MAY be
    registered more than once.** (Amendment, bead `pg2-91y12`, operator rulings of Phillip,
    2026-10-09.) A registered backend's command was never meant to be a single word: it is a
    command, binary plus arguments, that only has to behave correctly when pg-connector invokes it
    with the usual wire request on stdin. Every registration (each `connector.<type>` entry,
    `attention.sources`, `search.sources`, `activity.sources`) therefore accepts either a plain
    string, which means name = binary and command = `[name]` exactly as before, or a mapping
    `{name, command}` with `command` an argv LIST (never a shell string). The name is the
    instance's identity everywhere the umbrella uses one: `sources[].source`, the `--backend` pin,
    the `backends.<name>` config key, and the cache and ledger key; it therefore MUST NOT contain a
    path separator or the two-character cache/ledger key separator (`__`), and a name that appears
    in more than one registration MUST carry the same command in each. The umbrella execs
    `command[0]` with `command[1:]` as arguments, adds nothing after them, and still sends the JSON
    request on stdin; the wire protocol, its schemas and `protocolVersion` are unchanged and a
    backend never learns its registered name. `pg-connector-activity-*` capability-only backends
    remain rejected under `connector.<type>`, now checked against both the name and `command[0]`.
    The motivating case is the beads backend, which serves one tracker per process: it is
    registered twice, as `pg-connector-issue-beads-pg2` and `pg-connector-issue-beads-zr`, each
    running the same binary with `--beads-dir <tracker>` (flag over
    `PG_CONNECTOR_ISSUE_BEADS_DIR` over `BEADS_DIR`; no tracker at all remains a refusal), with
    neither instance primary and no unsuffixed instance. The registry behavior is recorded as
    invariant `INV-REG-4` in `packages/pg-connector/docs/behavior/invariants.md`.

12. **Fan-outs run their backend calls in parallel; the output order does not change.** (Amendment,
    bead `pg2-55k6y`, operator ruling of Phillip, 2026-10-09: "fan-outs MUST run in parallel so
    that no backend being down or slow holds up the others; output order stays deterministic and
    unchanged".) The umbrella's fan-outs used to call their backends one after another, with no
    recorded rationale, so one wedged backend added its whole per-op deadline to every call that
    included it and delayed every backend queued behind it. They now go through one helper
    (`packages/pg-connector/cmd/pg-connector/fanout.go`, `fanOutEach`) that runs the per-backend
    calls concurrently and returns the results in an index-addressed slice, so each fan-out folds
    them front to back and emits its `sources[]` rows and concatenated or grouped results in
    REGISTRATION order whatever the completion order. The invariant is `INV-FANOUT-1` in
    `packages/pg-connector/docs/behavior/invariants.md`. Covered: `pr`/`issue`/`thread`/`mail`/
    `calendar`/`alert`/`ci` lists, `alert history`, `attention list`, `search` (and its
    capabilities probe), `activity list` (and the `activity_kinds` probe), `auth status`,
    `config validate`, and the per-backend work of `pr`/`issue`/`calendar`/`thread` `changes`.
    Design decisions:
    - **Concurrency cap: configurable, default 8.** `state.fanout_concurrency` (a positive
      integer in the shared config file's `state:` block, read like `consumer_prune_after`; absent,
      non-numeric or below 1 means the default) bounds the number of simultaneous backend calls,
      each of which is one subprocess. 8 is above the number of backends any host registers today
      (two beads trackers, jira, pr-github, slack, calendar, mail, alert and the activity
      sources), so by default every backend runs at once, while an unusually large registry still
      cannot fork an unbounded number of processes. `1` restores the old strictly serial behavior
      and is the escape hatch should a backend ever prove unsafe to overlap.
    - **Per-backend deadline unchanged.** The helper adds no deadline; `scriptout` still applies
      its per-op exec deadline to each call, so a hung backend still costs its own deadline, and
      the fan-out costs the slowest backend instead of the sum. Cancelling the caller's context
      stops backends not yet started and cancels those running (their exec is context-bound).
    - **Failure isolation.** A call returning an error, an undecodable answer, or panicking yields
      that backend's own `degraded` row (a recovered panic becomes a per-backend error); it never
      aborts siblings. No goroutine outlives the fan-out. With one backend, or a cap of 1, the
      call runs inline on the caller's goroutine.
    - **Only the calls are concurrent.** Per-backend config is resolved serially before any call
      starts, and each fan-out still assembles its outcome, decodes results, serves the cache
      fallback and writes live entities to the entity cache serially in registration order, so
      the shared outcome and the umbrella's cache writers never see two goroutines. The one
      exception is `changes`, whose per-backend ledger refresh runs inside the backend's own
      goroutine; that is safe because every ledger and cache file is keyed by (type, backend,
      query, instance) and each backend is handled by exactly one goroutine.
    - **Shared-state audit.** (a) Umbrella entity cache and delta ledger: per-key files written by
      temp file plus rename, one writer per key per call, as above. (b) `Registry`: read-only
      after load (`BackendConfig` and `StateValue` are called serially up front; `Invoke` and
      `InvokeCapabilities` only read the command map); `scriptout`'s timeout and exec-factory
      variables are read-only outside tests; the package has no other mutable globals. (c) The
      backends' event log (`events.jsonl`, shared by the two beads instances and by concurrent
      calls generally): each event is one `O_APPEND` write and rotation takes an `flock`, so
      concurrent processes were already safe by design, and a fan-out only makes that overlap
      more likely. (d) `pr-github`'s GraphQL budget (`rate_reserve_points`): the gate reads the
      LIVE remaining points from GitHub on each call and holds no counter shared between calls,
      and a fan-out still sends each backend exactly one call, so total points spent are
      unchanged. The only new exposure is time-of-check slack when two `pr-github` instances
      sharing one token overlap, which can overshoot the reserve by at most one call's cost per
      instance; the reserve exists to be a margin, and `fanout_concurrency: 1` removes the
      overlap if that ever matters. Its per-PR `flock` and posted-comment sidecar guard
      `review submit` only, a targeted op that never fans out. (e) The per-entity single-flight
      lock (`show`'s read-through) is a cross-process `flock` already built for concurrent
      callers and is on a targeted path.
    - **Not a fan-out, not parallelized.** `INV-REG-2`'s try-each resolution of an id-keyed op
      (`DispatchTargeted`, `DispatchTargetedOptional`, `show` with cache fallback,
      `lookupShowCache`) stops at the first answer and includes write ops (`comment`,
      `transition`), so it stays sequential in registration order. A `--backend` pin runs one
      call, as before.

## Related Decisions

- Realizes the Tier-1/Tier-2 split first proposed in
  `docs/superpowers/specs/2026-09-03-unified-connector-architecture-design.md`, filed for
  retroactive documentation as bead `pg2-wajat`.
- Records, as durable invariants, the fixes from `pg2-p2z7o` (version negotiation), `pg2-r9iok`
  (the `invalid_argument` wire code and `not_found` reachability), and `pg2-0vwcc` (the
  composition-boundary rule) — see `packages/pg-connector/docs/behavior/invariants.md`.
- Continues, under a renamed binary, the PR-data-interface/workflow-owner split recorded in
  [0034](0034-pg-pr-prpool-review-ownership-split.md).
- Is the intended retargeting point for the design-spec citation cleanup tracked in bead
  `pg2-hidkm`.
- Extended by bead `pg2-si5jo`'s design (decomposed as docket `pg2-o2dmu`), which added the
  `calendar` entity-type capability (Decision item 8, above) under this same Tier-1/Tier-2 model.
- Extended again by bead `pg2-no8ic`'s design (decomposed as docket `pg2-qc5uc`), which added the
  `mail` entity-type capability (Decision item 10, above) under this same Tier-1/Tier-2 model.
- Amended by bead `pg2-91y12` (operator rulings 2026-10-09), which generalized registry entries
  from bare binary names to `{name, command}` instances (Decision item 11, above) so one backend
  binary can be registered more than once.
- Amended by bead `pg2-55k6y` (operator ruling 2026-10-09), which made the umbrella's fan-outs run
  their backend calls in parallel while keeping registration-order output (Decision item 12,
  above).
- Amended by ADR 0090 (draft, 2026-10-10), which lifts the uniformity statement ("every Tier-2
  backend stays uniformly simple and stateless", Decision item 10): a connector MAY be stateful and
  MAY run one daemon per upstream rate-limit domain, and a backend's own local store (Decision item 6) is the governing rule.
