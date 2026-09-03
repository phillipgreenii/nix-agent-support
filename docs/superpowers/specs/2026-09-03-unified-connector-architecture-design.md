# Unified pluggable connector architecture: pg-connector + ZR df-\* layer

## 1. Purpose and scope

`pg-pr`, `pr-pool`, and `work-activity-tracker` each independently reimplement overlapping
GitHub/Jira/beads sync and correlation logic. This design replaces that duplication with a
pluggable connector suite: no tool talks to an external system directly. `pg-connector` (Tier 1)
is the generic, org-agnostic umbrella; per-backend plugins (Tier 2) implement it for one system
each; a small set of ZR-specific tools (Tier 3) consume it for ZipRecruiter's own workflows.
`pr-pool` remains the cross-system workflow dispatcher, unchanged in its core.

Each major section below ends with its own acceptance criteria. Known gaps, deferred decisions,
and loose threads that don't block starting implementation are collected in the appendix.

## 2. Entity model

Six connector types, each with its own schema: **PR**, **Issue** (Jira/beads/GitHub Issues —
symmetric, same interface, multiple simultaneously-active instances), **Thread** (Slack),
**Note** (Notion), **CI** (a build/run, linked to a PR), and **SCM** (local git state — see §4.7;
unlike the other five, it has no remote entity to sync). Person and Repository are not separate
connectors — they are attributes on the above. **Feedback item** (a review comment thread with
its own open/will-fix/wont-fix/no-action disposition) is not a seventh type either — it is a
categorization-style component (§6.1) working from a PR and its comments.

No connector mirrors entity state into a shared store (§8 explains why) and no connector widens
its own scope to survey more than what's asked for.

**Acceptance criteria**

- The shared schema package defines exactly six connector types (pr, issue, ci, thread, note,
  scm); Person and Repository appear only as attribute fields, never as top-level schemas.
- Feedback item is implemented as a categorization-style component off PR + comments, carrying an
  open/will-fix/wont-fix/no-action field — not a seventh connector type.

## 3. Interface design principle: capability-scoped, not per-system

Interfaces are scoped by _capability_ (PR ops, Issue ops, CI ops, …), never by _system_. A single
interface spanning one backend's PR+CI+Issue operations (the shape ZR's own INTF-ZR-CODEHOST
ended up with) looks unified but actually branches per system internally with no shared shape —
exactly what this avoids. The Issue capability widens from read-only (`GetIssue`) to read+write
(create/comment/transition) so Issue can be a full connector, not just a mirror.

**Acceptance criteria**

- The issue capability interface exposes create/comment/transition write verbs, not just a read.
- No interface is scoped by system — every interface is scoped by capability only.

## 4. Tier 1 — pg-connector

The umbrella binary. It owns the six entity-type schemas, the wire protocol, a config-driven
backend registry, and the only user-facing CLI surface: `pg-connector pr/issue/ci/thread/note/scm
...`, plus the two cross-cutting capability verbs `pg-connector attention list` and
`pg-connector search <query>` (§4.4). It knows nothing about GitHub, Jira, Slack, Notion, beads,
or Captain's Log — only the schemas and the protocol. (`scm`'s git backend is local plumbing, not
a remote system, so it's the one exception to "knows nothing about the backend.") Every backend,
including the GitHub default, is a registered entry with no special-cased built-in — this is what
makes adding a future backend a one-binary, one-config-line change.

### 4.1 Config and registry

The registry is flat and type-keyed at the top level:

```
connector.pr      = pg-connector-pr-github
connector.issue   = [pg-connector-issue-jira, pg-connector-issue-beads]
connector.ci      = [pg-connector-ci-github-actions]   # pg-connector-ci-zr-captains-log deferred, §10
connector.thread  = pg-connector-thread-slack
connector.note    = pg-connector-note-notion
connector.scm     = pg-connector-scm-git
```

`issue` and `ci` are list-valued (multi-instance); `pr`/`thread`/`note`/`scm` take exactly one
value. Every registry value is a bare binary name — there is no `exec:`-prefix distinction between
a builtin and an external provider, since nothing is compiled in.

Config format, resolution order ($PG_PR_CONFIG → $XDG_CONFIG_HOME → ~/.config), and YAML carry
over unchanged from pg-pr's existing config machinery.

Each backend's own settings (e.g. Captain's Log's `CAPTAINS_LOG_URL` + cloudflared login) live in
that backend's own environment/config, not in this registry — the registry only names the binary.

### 4.2 Wire protocol

One-shot exec-per-call scriptout: JSON on stdin (`{"op": "...", "args": {...}}`), JSON on stdout
(`{"result": ...}` on success, `{"error": {...}}` on failure), coarse exit codes. The driver loop
and the `auth_status` convention carry over unchanged from pg-pr's existing `pkg/plugin/scriptout`
— they are already fully generic. The error field itself is not carried over as-is: today it is a
bare string; this design widens it to the structured object described below.

The `{"error": ...}` payload becomes a structured object, `{"code": "...", "message": "..."}`,
with `code` drawn from a closed set (at least `not_found`, `unauthenticated`, `unavailable`,
`unknown_op`, `version_mismatch`) — enough for the fan-out layer to classify degraded-vs-broken
without substring-matching. Exit codes at the wire level stay 0/1; classification lives in the
JSON body, matching scriptout's existing "only stdout JSON is the contract" convention. On the Go
consuming side, the wire boundary translates each `code` into one of a small set of exported
sentinel errors in `pkg/schema` (`ErrNotFound`, `ErrUnauthenticated`, `ErrUnavailable`,
`ErrUnknownOp`, `ErrVersionMismatch`), wrapped as `fmt.Errorf("%w: %s", sentinel, message)` — the
same pattern `vcs.ErrAuthInvalid` already establishes — so callers use `errors.Is` instead of
substring-matching the message.

### 4.3 Versioning and capability discovery

Every request/response envelope carries `schemaVersion` (an integer, bumped only on a
backwards-incompatible change — this exists to detect a mismatch, not to support multiple
versions at once). A mismatch surfaces through the error taxonomy as `code: "version_mismatch"`
and, at the fan-out layer, as `status: "degraded"` — identical treatment to an auth failure or a
down backend, no special-casing.

Every backend answers a universal `capabilities` op: `{"schemaVersion": N, "ops": [...],
"vocabulary": {...}}`. `vocabulary`'s shape is defined per entity-type schema (e.g. an issue
backend's vocabulary lists its actual transition/state names) rather than universal — this is the
real fix for issue backends being "symmetric" while Jira/beads/GitHub Issues don't share a state
vocabulary.

`pg-connector config validate` fans out both `auth_status` and `capabilities` across every
registered backend, reported through the same outcome-reporting envelope as §4.5.

### 4.4 Cross-cutting capabilities: attention and search

Two optional, generic capabilities, defined alongside (not as one of) the six entity-type
schemas: `attention.Source` and `search.Source`, each with one op (`list_attention`/`search`) over
the same wire protocol. Attention is a continuous "does this need my eyes" signal; it is
deliberately disjoint from a daily-planning ritual layered on top of it (§7.3 explains why) — a
query returns everything that currently qualifies, full stop, with no snapshot memory and no
cross-awareness of any consuming ritual.

An attention item is exactly `{type, id, summary}` plus optional `severity` (closed enum `low |
medium | high | critical`, canonical rank `low < medium < high < critical`, defined once in the
shared schema package). A source with no opinion omits `severity`; each source maps its own
internal signal into the enum itself (e.g. a PR backend could expose an existing computed urgency
level directly). A search result carries no score field — most backends expose an ordering, not a
comparable magnitude, and forcing one would produce numbers that look comparable across backends
while meaning nothing of the kind.

Two kinds of implementer, registered identically under either capability:

- A Tier-2 connector backend MAY additionally implement `list_attention`/`search` alongside its
  normal entity-type ops in the same binary (e.g. a PR backend flagging PRs needing action using
  its own domain knowledge).
- A standalone executable with no connector duties at all MAY implement just one of these ops, for
  rules that need knowledge no single connector backend has on its own (e.g. a rule spanning a
  PR's linked issue and its review-comment count). These are named `pg-connector-attention-<backend>`
  / `pg-connector-search-<backend>` — `attention` and `search` are valid values of the same
  `<type>` token used for every Tier-2 binary (§5.1), since they already have real verbs on the
  Tier-1 umbrella.

Registration is two always-list-valued keys, independent of `connector.<type>`:

```
attention.sources = [pg-connector-pr-github, pg-connector-attention-zr-stale-review, ...]
search.sources    = [pg-connector-pr-github, pg-connector-issue-jira, ...]
```

A beads-backed attention source may surface beads carrying the workspace's `human` label as
attention items — read-only visibility only, performing no claim/ack/exit mutation of its own.
That label's real lifecycle (claim, exit, audit trail) stays entirely owned by the existing
unblock-human-beads tooling; a beads attention source is not a second front-end competing with it.

**Merge and sort semantics differ between the two capabilities, because only one has a comparable
cross-source signal:**

- **Attention** interleaves: (1) dedup by `{type, id}`, collapsing multi-source hits into one item
  with a `via: [source, ...]` list; (2) sort by `severityRank` descending — missing severity is
  treated as `medium` for sorting only, never reported as `medium`; (3) tiebreak by `via.length`
  descending (an item flagged by two sources outranks one flagged by only one); (4) final tiebreak,
  `attention.sources` config order, then each source's own item order. A cap truncates after
  ranking, marked with `truncated: true` + `total_before_cap: N` in the envelope — a truncation is
  always a manifest marker, never a silent omission.
- **Search** stays grouped by source, never interleaved: each source's group is ordered exactly as
  that backend returned it, groups ordered by `search.sources` registration order.

**Acceptance criteria**

- `connector.<type>` is flat and type-keyed at the top level; `issue`/`ci` are list-valued, the
  rest single-valued; no `exec:` prefix exists anywhere in the registry.
- Every envelope carries `schemaVersion`; `error` is `{code, message}` from the closed enum; every
  backend answers `capabilities`; `config validate` fans out `auth_status` + `capabilities` across
  every backend.
- `pg-connector attention list` / `pg-connector search <query>` exist as real Tier-1 verbs.
- An attention item is exactly `{type, id, summary}` + optional `severity`; search results carry
  no score field.
- Attention output is deduped/ranked/capped exactly per the algorithm above; search output is
  grouped by source, never interleaved.
- Standalone attention/search-only plugins are named `pg-connector-attention-<backend>` /
  `pg-connector-search-<backend>`.
- A beads attention source, if built, performs no claim/ack/exit mutation.
- Attention aggregation is stateless and has no coupling to any daily-planning ritual built on top
  of it.

### 4.5 Outcome reporting and error taxonomy

Every multi-source response carries a top-level `sources` array, one row per source actually
queried, regardless of merge strategy: `{"source": <name>, "status": "succeeded" | "degraded" |
"disabled", "count": N, "reason": <string|null>}`. This applies uniformly to every fan-out in this
design — `attention.sources`, `search.sources`, `connector.issue`, `connector.ci` — and is never
collapsed into one pass/fail signal. Outcome rows live in the JSON body, never as a stderr
`WARNING:` line. Exit code stays coarse: 0 whenever at least one source succeeded, 1 only when
every source failed/disabled with zero usable results.

Each list-valued type/operation states its own merge strategy explicitly (concat-with-per-source-
outcome, or first-wins) rather than assuming one universal rule — the existing CI fan-out already
uses both (`runs` concatenates, `logs` tries each until one succeeds), and pg-connector's schema
makes that choice explicit per type/op rather than implicit.

**Acceptance criteria**

- Every multi-source response carries a `sources` array with one row per source queried, never
  collapsed; exit 0 if ≥1 source succeeded, 1 only if all failed/disabled with zero results.
- No degraded/failure signal is emitted as a stderr `WARNING:` line.
- Every list-valued type/operation states its merge strategy explicitly.

### 4.6 Credentials

pg-connector defines no credential mechanism of its own — consistent with "knows nothing about
the backend." Each Tier-2 backend resolves its own credentials, exactly as today's three existing
backends already do independently and differently (GitHub's env-then-`gh auth token` chain;
Jira's env vars or a keychain-backed CLI; Captain's Log's Cloudflare Access JWT). There is no
shared resolution order to build and no shared library; a future Slack or Notion backend picks
whatever chain fits its own token model when it's actually implemented. Nix/home-manager secret
delivery is likewise each backend's own concern, decided at that backend's implementation time.

pg-connector's only involvement: fan out the existing `auth_status` op via `pg-connector auth
status`, reusing the outcome-reporting envelope from §4.5 verbatim.

**Acceptance criteria**

- pg-connector ships no shared credential-resolution library or convention of its own.
- `pg-connector auth status` fans out the existing `auth_status` op across every registered
  backend via the sources envelope.

### 4.7 SCM — a sixth connector type, local-only, no remote entity

Unlike the five entity types, which sync remote state, `scm` manages local git state — worktrees
and cwd→branch resolution — and has no "sync" concept. It is registered, versioned, and dispatched
through the exact same mechanism as every other type (config-driven registry, scriptout protocol,
`schemaVersion`/`capabilities`), just backed by local git commands: `connector.scm =
pg-connector-scm-git`, single-instance.

It is deliberately generic, not PR-aware: `pg-connector scm worktree add <branch-or-ref>` takes a
branch or ref, never a PR number. A caller that wants "check out PR #482 for review" composes two
calls — `pg-connector pr view 482` to resolve the branch, then `pg-connector scm worktree add
<branch>` — rather than one command secretly doing both. `pg-connector scm worktree remove/list`
and `pg-connector scm branch detect` (cwd → `{repo, branch}`) round out the type. This is a
different mechanism from the harness's own session-scoped worktree-isolation tools — those key on
nothing PR-specific; this type is keyed on an arbitrary branch/ref for review/inspection purposes.

**Acceptance criteria**

- `connector.scm` registry key exists, single-instance.
- `pg-connector scm worktree add` takes a branch/ref, never a PR number.
- `pg-connector scm worktree remove/list` and `pg-connector scm branch detect` exist.

### 4.8 Dashboards and alerts

Every connector gets a two-tier convention, mirroring the standard-vs-backend-specific split used
elsewhere: a Tier-1 standard dashboard/alert layer, derived free from data every backend already
must expose (the `sources` outcome rows, `auth_status`, `capabilities`/`schemaVersion`, the
attention severity ranking), so a backend gets the baseline for free just by implementing ops it
already has to implement; and an opt-in Tier-2 backend-specific detail panel/alert condition
beyond that baseline (e.g. today's pg-pr dashboard becomes its GitHub backend's own detail panel,
carried over rather than rebuilt).

The specific metrics and alert conditions in both tiers are deliberately deferred to a later
design pass — this section fixes the convention (two-tier; standard is free; backend-specific is
opt-in), not the contents.

**Acceptance criteria**

- The two-tier convention is documented; no functional AC beyond the convention existing (contents
  are deferred by design, not an oversight).

## 5. Tier 2 — backend implementation binaries

One thin binary per (type, backend) pair, speaking only the scriptout protocol, with no
independent CLI identity a human types directly.

### 5.1 Naming convention

Every Tier-2/plugin binary matches `pg-connector-<type>-<backend>`, where `<type>` is always
exactly the singular capability verb — `pr`, `issue`, `ci`, `thread`, `note`, `scm`, `attention`,
or `search` — drawn directly from the verb, not chosen per binary. This also fixes a
plural/singular mismatch that existed in earlier naming sketches, since the type token is now
mechanically derived rather than picked freely.

ZR-specificity is encoded consistently inside the `<backend>` slot for every such binary, generic
or not, so no binary's org-specificity is a guess from its name — including
`pg-connector-ci-zr-captains-log`, renamed from its ZR-specific predecessor to carry that marker.

The umbrella's own name, `pg-connector`, was chosen over `pg`/`pg-sync`/`pg-gateway`/`pg-bridge`/
`pg-relay` — a bare `pg` would collide with this workspace's existing personal-tool `pg-` prefix
convention.

### 5.2 Package/module layout

One Go module, `packages/pg-connector/`, produces all binaries — matching this repo's existing
one-module-per-`packages/<name>/` convention rather than one module per backend. Nothing here
needs an independent release cadence per backend, so the multi-module coordination tax isn't worth
paying.

```
packages/pg-connector/
  pkg/
    schema/       <- public: shared JSON shapes AND the per-capability Go interfaces
    scriptout/    <- public: the wire protocol (envelope, schemaVersion, capabilities/auth_status)
  cmd/
    pg-connector/                       <- umbrella; imports pkg/schema + pkg/scriptout only
    pg-connector-pr-github/internal/    <- backend-private; importable by nothing outside this dir
    pg-connector-issue-jira/internal/
    pg-connector-issue-beads/internal/
    pg-connector-ci-github-actions/internal/
    pg-connector-thread-slack/internal/
    pg-connector-note-notion/internal/
    pg-connector-scm-git/internal/
```

Cross-backend isolation is compiler-enforced: Go's `internal/` visibility rule is evaluated per
import-path text, so nesting an independent `internal/` under each backend's `cmd/` directory
creates N independent, hard-enforced visibility boundaries. Only `pkg/schema`+`pkg/scriptout` are
importable by everyone, because that is the interface. One residual gap the compiler doesn't
close: nothing stops a backend from exporting a stray non-internal package another backend could
import — closing that needs one cheap CI/convention check (every backend's own code lives in
`main` or under its own `internal/`), a backstop, not the main enforcement mechanism.

Nix: N `mkGoApp`/`mkGoBinary` calls sharing `src` + `gomod2nixToml`, differing only in
`subPackages`/`pname` — this repo's existing gomod2nix convention, with real precedent for the
multi-binary-per-module shape one hop away (this workspace's `pn`/`pn-workspace-toml-enforce`,
built from identical shared `src`). Each of the eight nix outputs needs a single-entry
`subPackages` list. Known, already-accepted cost: shared `src` means editing any one backend's
`internal/` code bumps the content-digest-versioned nix rebuild of all eight binaries — acceptable
given there's no independent release-cadence requirement.

### 5.3 Captain's Log (existing ZR backend)

Captain's Log's cross-repo access needs a module-level `replace` (Go's `replace` operates at
module granularity) pulling in this module's entirety from ZR's side, mirroring how pg-pr is
consumed today. The actual consumer imports three packages — wire shapes, wire protocol, and the
CI-capability's Go interface — which is why `pkg/schema` explicitly includes the provider Go
interfaces, not just JSON shapes; a version that moved only the wire protocol would leave the
interface with no home and break this consumer. The migration: rename the import paths, add a
`pg-connector-src` flake output (mirroring today's `pg-pr-src`), and repoint ZR's `go.mod replace`

- `build.nix` at it. This is a real migration step, bounded and doable, not a mechanical no-op.

**Acceptance criteria**

- Every Tier-2/plugin binary matches `pg-connector-<type>-<backend>`, `<type>` drawn from the
  capability verb (including `attention`/`search`); ZR-specificity is consistently encoded in
  `<backend>`, including a renamed Captain's Log binary.
- One Go module builds all binaries via N `mkGoApp` calls sharing `src`+`gomod2nixToml`; only
  `pkg/schema`+`pkg/scriptout` cross backend boundaries, backstopped by a convention check.
- A `pg-connector-src` flake output exists and ZR's `go.mod replace`/`build.nix` are repointed at
  it; Captain's Log still builds and runs unchanged through the transition.

## 6. Relationship to pr-pool

pr-pool's core (its durable ordered event queue, query-source/agent-runner contracts, domain-
agnostic per its own design) is not modified by anything new in this design. Roles and query-
sources are wired entirely via config, pointing at external backing commands — never compiled in.
pr-pool already supports any listener adhering to its existing Listener/handler contract, LLM-
backed or fully deterministic, without a core change, provided it's a new role of an _existing_
kind (its two current executor kinds are selected by a `role.Type` field) — which covers the two
new deterministic handlers below.

### 6.1 df-categorize and df-feedback — new pr-pool roles

Two new, deterministic ("command"-kind) pr-pool roles, bound by event type, requiring zero pr-pool
core changes beyond registering them in ZR's own config:

- **df-categorize** reads one event for a PR/CI/Issue event, applies its own ranking logic (a
  different algorithm from df-survey's — different situations want different rankings, even
  within ZR), and writes the result back via `pg-connector pr update <id> --label <category>`.
- **df-feedback** is bound to PR+comment/review-thread events and writes back into the feedback-
  disposition store, which moves under the PR GitHub backend along with the rest of pg-pr's
  retiring surface (this is the concrete mechanism for the Feedback item gap in §2).

**The dispatch adapter.** pr-pool's current command-role executor renders argv via a Go template
against scalar fields only (`Item.ID`, `Item.Type`, …) — it has no way to marshal the full nested
event metadata as JSON, and an externally-pushed event doesn't populate that metadata in a usable
shape anyway. Rather than fight that limitation, the handlers' stdin-JSON interface is scoped to
exactly what's reachable today: `{"type": "<entity type>", "id": "<entity id>"}`. Both handlers
then fetch current state themselves via `pg-connector <type> show <id>` rather than trusting a
snapshot riding along in the event — consistent with how every other live-query piece of this
design already works, and more correct than acting on a possibly-stale embedded snapshot. A small
external adapter translates between pr-pool's current dispatch shape and this minimal interface,
living entirely outside pr-pool's core; it retires cleanly whenever pr-pool's dispatch layer
eventually carries richer payloads natively.

**Permissions.** df-categorize and df-feedback run with permissions uniform to their pr-pool
parent — no bespoke restricted allowlist, per this workspace's existing ruling that pr-pool
subagents share the parent's permissions rather than a per-role least-privilege split.

**Acceptance criteria**

- pr-pool's core requires zero Go changes for these two roles beyond config registration.
- The adapter's stdin-JSON interface is exactly `{type, id}`; both handlers re-fetch current state
  via `show` rather than trusting event payload data.
- Both run with permissions uniform to their pr-pool parent.
- df-categorize's ranking logic is demonstrably distinct from df-survey's.

## 7. Tier 3 — ZR-specific layer

Explicitly not generic, and not bundled into one binary.

### 7.1 On-demand tools

- **df-attention** is a pure TUI client of `pg-connector attention list` — it performs no direct
  exec of any attention source itself; the fan-out, dedup, ranking, and cap all happen inside
  pg-connector (§4.4). It may surface human-labeled beads read-only, per §4.4, with no claim/ack
  mutation of its own.
- **df-search** is a pure CLI/TUI client of `pg-connector search <query>`, same relationship, same
  no-direct-exec rule. Unlike df-attention, its merge is grouped by source, never interleaved.

### 7.2 Event-reactive tools

df-categorize and df-feedback (§6.1) live here conceptually, though their registration lives in
pr-pool's own config, not in this module.

### 7.3 Relationship to daily-focus's morning ritual

Attention (§4.4) is a continuous "does this need my eyes" signal. Daily-focus's own morning
ritual (df-survey) is a periodic planning _synthesis_ that factors attention in alongside other
dimensions — in-progress status, approaching deadlines — to produce a plan for the day. The two
operate at different levels and are deliberately disjoint: attention has no memory of what the
morning ritual already surfaced, and the ritual is not attention's only consumer. If a future
dashboard or TUI wants to correlate "what's attention-worthy right now" against "what today's plan
already covered," that correlation is a client-layer concern built on top of both — never logic
inside either mechanism itself.

### 7.4 Where this lives

df-categorize, df-feedback, df-attention, and df-search ship as new siblings in the existing
`modules/daily-focus/` module in phillipg-nix-ziprecruiter, using its established nix packaging
and bats-testing pattern. Standalone `pg-connector-attention-<backend>`/`pg-connector-search-
<backend>` plugins do **not** live there — despite being ZR-specific, they have no connector
duties of their own and belong alongside the other ZR-specific Tier-2 backends instead.

**Acceptance criteria**

- df-attention/df-search are pure clients of the two Tier-1 verbs, performing no direct exec of
  any source.
- The four Tier-3 tools ship in `modules/daily-focus/` using its existing packaging/test pattern;
  standalone attention/search-only plugins do not.

## 8. Rejected alternative: canonical/shared store

Considered and rejected: a shared store mirroring entity state across connectors. Walked through
concretely (a "give me in-progress work across connectors, resolve refs, recurse" scenario) and
concluded recursion is mostly avoidable — broad-scoped per-connector queries already carry their
own visible ref fields (a bead's external ref, a PR branch name containing a ticket id), so
correlation happens via one bulk query per connector, joined in memory, the same pattern daily-
focus's own survey tool already proves works with zero persistence. True multi-hop transitive
closure is really only a beads dependency-chain thing, and beads's own dependency graph already
provides that natively. The only unavoidable persistence: a reference pointing outside the initial
broad query's scope needs one bounded, batched targeted lookup — never unbounded recursion.

Categorization (§6.1) satisfies "downstream of events, not computed at query time" by writing back
into the owning system (a label, a field) instead of building a separate index — reading it back
later is just a filtered live query. Search, attention, and daily-focus all do live fan-out plus
in-memory correlation on demand, on the same principle.

**Acceptance criteria**

- No cross-connector entity store exists anywhere in the implementation, verified by its absence.
- Any reference resolution outside a query's initial scope uses one bounded, batched lookup, never
  unbounded recursion.

## 9. pg-pr retirement

`pg-pr` as a standalone binary retires — there is no dual-interface question, because there will
only be one. `pg-connector pr` is the sole client-facing PR interface going forward, backed
initially by pg-pr's existing GitHub logic carried over rather than rewritten, and eventually by a
second, interchangeable backend (e.g. Forgejo) — exactly Tier 1's symmetric-backend model.

### 9.1 What moves where

pg-pr's `vcs.Provider`/`issues.Provider`/`cicd.Provider` implementations become Tier-2 backend
implementations behind pg-connector. Its scriptout exec-plugin protocol generalizes to all six
connector types. Its bespoke beads-upsert code retires in favor of a real beads `issues.Provider`
implementation plus the df-categorize role. pr-pool's own beads-specific query code (a documented,
pre-existing violation of its own domain-agnostic design) retires in favor of polling the beads
connector like every other source — this is a small, pre-existing pr-pool core change (its
compiled-in default query set is constructed directly in Go, not through config), not something
this design's own new pieces introduce.

pg-pr's worktree/branch-detect commands resolve cleanly into the new `scm` connector type (§4.7) —
they were never genuinely homeless, they just needed their own type. Wherever the existing review-
orchestrator ecosystem (draft-review orchestrator → worktree add → parallel review subagents →
review draft → worktree remove) currently calls `pg-pr <verb>` for PR data, that becomes
`pg-connector pr <verb>` — same content, new binary, no logic change. Wherever it calls
`pg-pr worktree`/`branch detect`, that becomes `pg-connector scm worktree`/`branch detect`,
composed with a `pg-connector pr view` call for any PR→branch resolution.

A full verb-to-destination table covering pg-pr's remaining command groups (`worktree`, `branch`,
`open`, `review`, `sync`, `changes`, `config`, `auth`, `migrate`, `migrate-feedback`, and the
local dashboard) is required before any retirement packet is cut — see the appendix; it does not
exist yet.

### 9.2 What's explicitly out of scope here

The review-orchestrator ecosystem's _trigger_ mechanism is intentionally not redesigned here. The
operator's stated intent is that this analysis logic ends up triggered by a pr-pool role rather
than primarily by a human running a slash command directly — that redesign, and review draft's
exact home once it lands, are deferred to a future design pass. Preserving today's skills/agents/
slash-commands invoked directly is explicitly not a requirement this design needs to satisfy.

**Acceptance criteria**

- A full verb→destination table for every pg-pr command group exists before any retirement packet
  is cut (see appendix — not yet done).
- pg-pr's local SQLite store gets a stated per-table disposition (migrate / drop / move under the
  PR GitHub backend) before any retirement packet lands.
- pg-connector's own `docs/behavior/` set is authored as its own first work packet, before any
  code-producing packet, so later packets have real behavior-IDs to cite from day one. The three
  existing implicated behavior-docs sets (pg-pr's, pr-pool's, ZR's daily-focus/pr-pool config,
  work-report's) update in the same change as whatever packet touches them, per this repo's
  existing documentation rule.

## 10. Next phase — deferred, not blocking

`pg-connector-ci-zr-captains-log` (§5.1) already ships today as a working, PATH-wired binary with
real Cloudflare Access auth and unit tests — nothing needs to be built from scratch. Registering
it into `connector.ci` (and applying the renamed-binary convention) is deferred to a later phase;
the `ci` connector type ships and works today with the GitHub Actions backend alone, and nothing
else in this design depends on Captain's Log being registered. Carried-forward open question for
whenever this phase starts: Captain's Log's ID scheme is assumed to share GitHub's own run/job
IDs, so multi-backend fan-out would work without a separate correlation step — if that assumption
is wrong when this is actually built, it needs the same external-ref correlation pattern used
everywhere else in this design.

**Acceptance criteria**

- `pg-connector-ci-zr-captains-log` keeps working, PATH-wired and unchanged, through the entire
  transition.
- `connector.ci` does not list it until this phase is explicitly started.

---

## Appendix A: known gaps and open items (not blocking, but not yet resolved)

These are real, non-trivial gaps identified across this design's review history. None of them
blocks starting implementation of the sections above, but each needs a decision or more work
before the area it touches is actually done.

**Retirement completeness (§9)**

- The full verb→destination table for pg-pr's remaining ~13 command groups (worktree, branch,
  open, review, sync, changes, config, auth, migrate, migrate-feedback, plus the local dashboard)
  does not exist yet as a durable artifact — only pr/issue/ci have stated destinations.
- No coordinated rewrite/shim plan exists for the roughly 133 downstream literal `pg-pr <verb>`
  invocations across agent-support's Claude Code plugin assets (skills, review subagents, slash
  commands, a PreToolUse hook, pinned flake checks, a tldr page, a capabilities-list entry, and a
  cross-plugin reference from another skill).
- pg-pr's local SQLite store (feedback dispositions, PR rows, outbox+leases, user_state,
  repo_sync_state, approver data) has a migration disposition for exactly one table (feedback
  disposition); the rest are unaddressed.
- pr-pool's own hardcoded `pg-pr` call sites (two literal `exec.CommandContext` sites, a nix
  `wrapProgram`/`callPackage` reference, and a compiled-in default `AllowedTools` string) are
  already tracked in pr-pool's own gap register but not sequenced into this design.

**Data freshness and existing guarantees**

- pg-pr today guarantees every fact carries its own as-of time and stale flag, and that a consumer
  must not re-derive its own staleness policy. This design doesn't yet say whether
  `pg-connector pr list` becomes a live network call (a latency change from today's read path) or
  a backend-local store persists — and if the latter, how staleness is represented in the schema.
- pg-pr's existing `pr hide`/`unhide` acknowledge/snooze mechanism has no stated home. df-attention
  as designed doesn't consult it, so hiding a PR today wouldn't stop an attention source from
  re-surfacing it tomorrow.

**Wire protocol and testing**

- Wire-level exit codes stay 0/1, which doesn't meet this workspace's own code-file convention
  that branchable meanings use ≥2 distinct exit codes.
- scriptout has no schemas/goldens/conformance suite analogous to pr-pool's own (which an
  importing consumer can run against any implementation). Given §4's "zero compiled-in providers"
  decision, nothing structurally prevents a backend's unit tests from all passing against a fake
  shape no real backend implements.
- Slack and Notion backends need a new double convention for tests (RoundTripper injection or
  record/replay) — no existing pattern in either repo covers an HTTP-only backend with no local
  CLI to stub.
- The pr-pool↔adapter↔df-categorize integration seam (§6.1) has no test harness extension —
  pr-pool's existing conformance driver only accepts Go-interface participants, not a subprocess-
  pluggable handler.
- df-categorize's ranking has no stated injectable clock parameter, which daily-focus's own
  ranking-test convention requires to avoid flaky golden tests.
- No stated plan for pg-pr's and pr-pool's retiring test suites (pg-pr's `pkg/beads` and
  `internal/beadsbridge`, including its concurrency/locking tests; pr-pool's beads-query tests) —
  whether they move, get rewritten against the new seam, or are accepted as coverage loss.

**Design details left unpinned**

- Whether an existing backend's computed urgency/priority signal (used as the illustrative example
  for `severity` in §4.4) is an actual requirement for that backend, or just an example, is not
  confirmed.

**Found by a six-dimension review pass (correctness/completeness/UX/test-coverage/Go-practices/
architecture) against the committed spec, 2026-09-03 — genuinely new, not already covered above**

- **Search has no specified result shape at all (§4.4).** Attention's item shape is fully pinned;
  search only ever gets a negative property ("no score field") — no field list (title/url/
  snippet/source-id/etc.). Neither df-search nor any search backend implementer has anything
  concrete to build against. Blocking for that capability specifically.
- **Thread and Note have no field-level schema content anywhere, and no identified consumer
  (§2, §5.2, §9.1).** PR/Issue/CI can be inferred from pg-pr's existing provider shapes; Thread
  (Slack) and Note (Notion) have no prior art in this codebase, no sketch of a field set, and
  no current tool that needs either — they read as speculative placeholders rounding out "six
  types" rather than types earning their place. Worth deciding whether to specify them properly
  or drop them until a real consumer exists.
- **df-feedback's write-back operation has no stated verb, unlike its stated-symmetric partner
  df-categorize (§6.1).** df-categorize writes via a named call
  (`pg-connector pr update <id> --label <category>`); df-feedback is only said to write "into the
  feedback-disposition store," with no op name or args shape.
- **The attention merge algorithm (§4.4) is underspecified for the golden test its own AC
  implies:** no rule for which source's `summary`/`severity` wins when two sources disagree on
  the same `{type,id}`; no stated cap value or config key; the final tiebreak (each source's own
  item order) doesn't say whose order applies once an item is merged from multiple sources.
- **Standalone attention/search-only plugins may reintroduce the multi-capability, no-shared-
  shape interface §3 explicitly rejects, just relocated (§3, §4.4).** §4.4's own motivating case
  — a rule spanning a PR's linked issue and its review-comment count — needs two capabilities'
  data at once. Undecided whether such a plugin gets that by composing pg-connector's own verbs
  (capability-scoped, consistent with §3) or by talking to backend systems directly to synthesize
  the judgment (exactly the per-system interface §3 rejects, under a different binary shape).
- **PR's single-valued registry cardinality (§4.1) is in tension with §9's "eventually a second,
  interchangeable backend" promise.** `issue`/`ci` are explicitly list-valued for exactly this
  kind of multi-backend case; `pr` is not. If "interchangeable" ever means a concurrent/migratory
  dual-backend period (e.g. a GitHub→Forgejo cutover), the registry schema as specified can't
  express it.
- **The retirement transition period (§9) has no coexistence architecture, only a checklist.**
  Given the unresolved items already listed under "Retirement completeness" above, pg-pr and
  pg-connector will necessarily run in parallel for an extended period — nothing describes how
  (a shim, a routing layer, dual-write, or ad hoc caller-by-caller migration).
- **The pr-pool↔pg-connector dispatch adapter (§6.1) has no assigned owner** — no repo, package,
  or binary name is stated for it, unlike every other component in the design.
- **`pkg/schema` holding both wire shapes and every capability's Go interface (§5.2) is a
  coupling regression from this repo's own existing split** (today, JSON shapes and the three
  provider interfaces live in separate packages) and sits in tension with §3's own "scoped by
  capability, not by system" principle applied at the package level — it becomes a mandatory
  import for every one of 8 binaries plus any external consumer (e.g. Captain's Log).
- **A single global `schemaVersion` (§4.3) couples all capabilities' compatibility together** —
  a breaking change to, say, the Thread schema bumps the same integer an unrelated CI-only
  consumer checks, which bites hardest exactly where §5.3 introduces an independently-built
  cross-repo consumer.
- **No optional-sub-interface story for partial backend support (§3, §5.2).** `vcs.Provider`
  already has a pattern for this (small, separately-asserted optional interfaces like
  `AuthChecker`); undecided whether Slack/Notion backends get the same escape hatch for verbs
  they can't fully support.
- **Several stated acceptance criteria are aspirational rather than checkable** — e.g. §8's "no
  cross-connector entity store... verified by its absence," §3's "no interface is scoped by
  system," and §4.4's "no coupling to any daily-planning ritual" all restate the section's thesis
  without naming an operational condition a test could actually assert.
- **pg-connector's needed conformance harness is new construction, not a port of pr-pool's
  existing one** — pr-pool's conformance suite tests in-process Go structs against an interface;
  pg-connector's Tier-2 backends are out-of-process binaries speaking only JSON over stdin/stdout,
  which is a materially different, unbuilt harness shape.
- **Exit code 0 on partial fan-out failure (§4.5) is a scripting trap** — a cron job or script
  gating on exit status alone cannot tell that some backends are down; it must always parse the
  `sources[]` body, and nothing in the design flags this for automation authors.
- **Stateless attention with no `hide`/`unhide` equivalent (§4.4, §7.3) means a triaged item
  resurfaces indefinitely** — the single biggest daily-friction gap this review round
  surfaced, compounding the existing "pr hide has no stated home" gap above.

**Explicitly deferred by design, not oversights**

- Dashboard/alert Tier-1 and Tier-2 _contents_ (§4.8) — the two-tier convention is fixed, the
  actual metric/alert lists are a later design pass, by explicit choice.
- The review-orchestrator trigger redesign (pr-pool-role-triggered rather than slash-command-
  triggered) — stated intent, not designed (§9.2).
- Captain's Log's registration into `connector.ci` (§10) — the binary already works; only the
  registration step is deferred.

## Appendix B: loose threads carried from prior design sessions

- `pg-pr open`'s disposition is still unanswered — whether the operator uses it manually with no
  stated replacement, or it drops entirely.
- Two pre-existing bugs, found but deliberately not fixed during this design's research, with no
  bead filed for either: a review-team skill double-adds/removes a worktree around the
  orchestrator's own add/remove; a work-bead skill uses a second, untracked worktree convention
  keyed on its own environment variable rather than the `scm` type.
- A separate bead (`pg2-uesze`) compares pg-pr's bead-splitting tool against the plan-decompose
  skill — filed, open, unrelated to this design's critical path.
- `work-report`'s design (tracked separately, still pending its own operator sign-off) very likely
  replaces this design's original work-activity-tracker section outright; that reconciliation is
  not done. Until `work-report` lands, no packet should be cut for work-activity-tracker
  specifically — everything else in this design is independent of that gate.
