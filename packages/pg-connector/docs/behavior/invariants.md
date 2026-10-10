# Invariants — pg-connector

The rules pg-connector's umbrella follows, plus the one rule binding on every Tier-2 backend
(`INV-COMP-1`). See the [glossary](glossary.md), [interfaces](interfaces.md), [actors](actors.md),
and [journeys](journeys.md). IDs are **topical and stable**; numbering gaps are legal, and each
rule uses RFC 2119 language (`MUST`/`SHOULD`/`MAY`). The ID convention and the invariant/goal
distinction come from the behavior-docs method
(`phillipgreenii-nix-agent-support · behavior-docs/docs/behavior`).

## Capability scoping

- **`INV-CAP-1`** <!-- uuid: 40812675-88b2-40e8-9471-2381106587c3 --> — An interface's name and
  method set MUST correspond to exactly one capability (`pr`, `issue`, `ci`, `scm`, the
  cross-cutting `attention`/`search`, or any future entity type or cross-cutting capability) and
  MUST name no backend/system (GitHub, Jira, beads, git, …). The umbrella itself
  MUST know nothing about any backend's external system — that knowledge lives entirely behind
  `INTF-WIRE`, inside the backend. A single interface spanning more than one capability's own
  operations for one system (e.g. one interface mixing PR, CI, and Issue ops for "GitHub") is a
  violation of this rule even if every method it exposes is individually well-formed, because it
  makes adding a **system** and adding a **capability** the same, conflated change.

## The wire protocol

- **`INV-WIRE-1`** <!-- uuid: f7f35071-38eb-4941-a3af-2615734422dd --> — Every request/response
  pair, except the `capabilities` op, MUST use the envelope: request `{op, args}`; response
  `{protocolVersion, schemaVersion, result}` on success, or
  `{protocolVersion, schemaVersion, error: {code, message}}` on failure. A response carrying
  **neither** `result` nor `error` is a **protocol violation, never a success** — a backend
  answering "nothing to report" MUST send `"result": null` explicitly rather than omit `result`.
  Exit codes at this wire layer stay a plain `0` (well-formed envelope, `result` set) / `1`
  (anything else); classification of what went wrong lives entirely in the JSON `error.code`
  (`INV-ERR-1`), never in a wider wire-level exit-code scheme.
- **`INV-WIRE-2`** <!-- uuid: db5f232e-084d-4efd-ab8c-7a6de399a643 --> — The `capabilities` op is
  the one exception to `INV-WIRE-1`'s envelope: on success its response is the bespoke top-level
  shape `{protocolVersion, schemaVersions, ops, vocabulary}`, never nested inside `result`. On
  **failure** it still uses the ordinary `{protocolVersion, schemaVersion, error}` envelope — so a
  caller decoding a `capabilities` response MUST check for the ordinary error envelope's `error`
  field **before** attempting to decode the bespoke success shape; skipping that check risks
  silently decoding a failure as a zero-value success.
- **`INV-WIRE-3`** <!-- uuid: 9c1e5f47-2b8a-4d6e-b3f9-7a4c1d8e6f52 --> — Every request MAY carry a
  `config` member alongside `op`/`args`: the umbrella copies a registered backend's own registered
  config block VERBATIM into every request it sends that backend (bead pg2-2j5ac.28.1). The
  umbrella MUST NOT validate `config`'s contents — it is opaque at the wire-envelope layer, since
  `INTF-WIRE` is capability-agnostic by design; only a capability's own dispatch table (or a value
  common to more than one capability, like the `list` op's own `queries` key) ever interprets it.
  `config` MUST NOT be logged with a secret exposed — see `INV-STATE-1`. The umbrella copying
  `config` into every request does not oblige a backend to read policy from it: a daemon-backed
  backend (see the [glossary](glossary.md)) MAY take its policy from its own rendered config file
  instead (`INV-STATE-1`, ADR 0090), and for such a backend the copied `config` is not the source
  of its policy.

## Statelessness and daemon-backed backends

> ADR 0090 lifts the earlier ruling that every Tier-2 backend is stateless and that the umbrella
> alone owns the cache, the change decision and the cursors. It lifts a RESTRICTION: a backend
> MAY be stateful and MAY run a daemon, and none is required to. Every backend that is not
> daemon-backed stays exactly as the rules below describe.

- **`INV-STATE-1`** <!-- uuid: 4d8b2e91-7a3c-4f6d-9e1b-8c5a2d7f3b64 --> — A Tier-2 backend that is
  not daemon-backed MUST resolve any per-call, per-backend policy it needs (e.g. the `list` op's
  own named-query definitions, or `pg-connector-pr-github`'s own GraphQL rate-limit reserve) from
  the REQUEST's own opaque `config` member (`INV-WIRE-3`) alone — NEVER from a file, environment
  variable, or other local state it reads or resolves itself for that purpose. The umbrella is the
  sole place such a backend's own config is authored and read from disk; a backend that instead
  resolved its own config file would make the SAME backend binary behave differently depending on
  which host/process invoked it, defeating the umbrella's own single point of configuration. A
  **daemon-backed backend** MAY read its per-call policy from its own rendered config file instead
  (ADR 0090): the daemon and its client both start from one config file, rendered from the same
  option that authors the registry entry, so the binary still behaves identically whichever
  host or process invokes it. Such a backend MUST take everything it needs (the origin's host, the
  paths of its tools, its state directory, its socket path, its queries and its settings) from
  that rendered config file and MUST NOT take any of them from the environment; environment
  overrides exist only for tests and a shadow-comparison harness, so a daemon started by hand,
  with an empty environment and the root as working directory, behaves exactly as it does under
  its supervisor. This does not forbid a backend from resolving its OWN credentials or connection
  details independently (e.g. `pg-connector-pr-github`'s `gh`-mediated token,
  `pg-connector-issue-jira`'s `pjira` config file) — `INV-STATE-1` is scoped to PER-CALL POLICY the
  umbrella itself can vary per backend via `config`, not to a backend's own ambient
  identity/connection resolution. The umbrella's own ledger (`INV-LEDGER-FRESH-1`) is its own
  state, not a backend's, and is unaffected by this rule.
- A backend's own `config.queries` block (the `list` op's own named-query convention) MUST NOT
  define any built-in query name of its own; every name a caller can legitimately supply comes
  from that backend's own registered config, resolved centrally by that capability's dispatch
  table before ever reaching the backend's own `List` implementation (`INV-ERR-3`). This holds for
  a daemon-backed backend too: its queries come from its rendered config, not from a built-in.

## Caching

- **`INV-CACHE-1`** <!-- uuid: 892af663-9a46-4229-93d3-ac43d73f73c6 --> — For a backend that does
  not own its cache and changes, the umbrella-owned entity cache (`cmd/pg-connector/cache.go`,
  phase 14) is default-on per type, with an explicit opt-out per type (a `state:` key) and per
  backend (a `capabilities` `vocabulary` flag); an opted-out `(type, backend)` pair still keeps its
  ledger (the ledger of `INV-LEDGER-FRESH-1` is unaffected by a cache opt-out). The cache MUST NOT
  hold any content a backend did not already report to this umbrella through an ordinary op
  response — it is a copy of what was already returned, not an independent source of entity data.
  Caching this umbrella's own copy of what a backend already returned does NOT weaken `D3`
  (statelessness, `INV-STATE-1`): that backend itself gains no store; only the umbrella does. A
  capabilities-call failure while checking a backend's own per-backend opt-out MUST fail OPEN
  (caching stays enabled for that backend) rather than closed — an already-unavailable backend must
  not be made doubly unavailable by a second failed call. A backend that owns its cache and changes
  (a **daemon-backed backend** that declares `cache_opt_out` and `owns_changes`, ADR 0090) is
  outside this rule: `INV-CACHE-9` states what the umbrella does for it.

## Cache policy: detail level, read-through, refresher

> Written by bead `pg2-cw6b3.2` (spec
> `docs/superpowers/specs/2026-10-05-pg-desk-attention-evaluator-and-connector-refresh-cache-design.md`,
> section 6, "Direction 2: connector membership and refresh cache"). These rules extend
> `INV-CACHE-1`; they apply to the `pr` and `issue` types only, and only to backends that do not own
> their cache and changes (ADR 0090; `INV-CACHE-9` covers the others). The umbrella owns the policy,
> so such a backend stays free of any store of its own (`INV-STATE-1`) and a type adopts the policy
> by configuration.

- **`INV-CACHE-2`** <!-- uuid: 07a8d39c-758b-4fd7-a74b-051f9dc9868e --> — Every cache entry MUST
  record a level, `summary` (what `list` returns) or `detail` (what `show` returns). A `show`
  read-through (`INV-CACHE-4`) and the `show` stale fallback MUST be satisfied only by a `detail`
  entry, so a list summary is never served to a caller that asked for the full entity. A write of a
  `summary` MUST NOT replace or downgrade the content of a live `detail` entry for the same id; a
  write of a `detail` MUST replace any entry. An entry persisted before levels existed MUST be read
  as `summary`.
- **`INV-CACHE-3`** <!-- uuid: 001bf296-3510-4932-a944-fb3fef29ec8c --> — The `list` stale
  fallback MUST be scoped to the requested query: it MUST serve only cached entities whose ids are
  live members of that query's ledger index, and MUST serve nothing for a query whose ledger index
  holds no live member. It MUST NOT serve every cached entity of a backend regardless of the query.
- **`INV-CACHE-4`** <!-- uuid: 192b1938-cc33-4174-a375-076345b55aa4 --> — `pr show` and
  `issue show` MUST be served from a live `detail` cache entry whose age is within `read_ttl`
  without calling the backend, unless the caller passed `--fresh`. A read older than `read_ttl`
  (or with no entry) MUST fetch from the origin under a single-flight lock keyed by the entity, so
  concurrent readers of one entity collapse to one origin call: a reader that waited for the lock
  MUST re-check the cache and serve what the lock holder just wrote instead of calling the origin
  again. A failure to take the lock MUST fail open (the reader calls the origin itself) and MUST NOT
  turn a read into a failure. `read_ttl` defaults to 120 seconds; a configured value of `0` or
  `off` disables the read-through. An `unavailable` answer from the origin MUST still fall back to a
  stale `detail` entry marked `stale` (`INV-CACHE-1`). A type or backend opted out of the cache
  (`INV-CACHE-1`) MUST be neither read-through nor written.
- **`INV-CACHE-5`** <!-- uuid: e96e5e10-0fb8-4ff4-b301-aac0b73da791 --> — A `show` answered by the
  umbrella MUST carry two additive fields beside the entity: `served_from`, `origin` when the
  answer was fetched from the origin by this call or `cache` when it came from a cache entry
  (including the stale fallback), and `age_seconds`, the whole seconds since the entity's own
  `as_of` (`0` for an origin answer). `--fresh` MUST bypass `read_ttl` and fetch from the origin;
  it MUST NOT disable the stale fallback. A cache-served answer MUST NOT advance any freshness
  stamp (`INV-LEDGER-FRESH-2`).
- **`INV-CACHE-6`** <!-- uuid: d3116653-4bd4-4aaf-9e6e-a865c3c7aa64 --> — `changes` MUST NOT be
  served from `read_ttl`: it is the thing that detects change, so it always asks the origin
  (unless `--cached`). When `refresh_after` is configured for the type (the refresher mode, off by
  default), `changes` MUST read membership with the ids-only `list`, MUST fetch a full entity only
  for a member that is new to the ledger or whose `detail` entry is missing or older than
  `refresh_after`, and MUST classify changes only among the entities it fetched, so a member whose
  entry is young reports no change until it ages. Every entity it fetched MUST be written to the
  cache as `detail`, only after the response has been written and flushed. A refresh pass in which
  any fetch failed is not a whole-query answer: it MUST record `last_error` with the code
  `truncated` and MUST NOT record `refreshed_at` (`INV-LEDGER-FRESH-3`), though the changes it did
  classify are still reported. Outside the refresher mode, `changes` MUST write each entity it
  listed to the cache as `summary`, after the response is flushed.
- **`INV-CACHE-7`** <!-- uuid: bb80b573-8500-45f0-b65e-ea1f39e4abe3 --> — In the refresher mode, an
  id that is in the ledger index but absent from a complete membership answer MUST be confirmed by
  one read of the entity before it is reported `removed`. If the read returns the entity (for
  example a PR that merged or closed), the `removed` row MUST carry that confirmed content. If the
  read answers `not_found`, the removal is confirmed and the row carries the last cached content.
  If the read fails any other way, the removal MUST be withheld: the id stays live in the ledger
  and the next call confirms it again, and the pass counts as incomplete (`INV-CACHE-6`). A
  truncated membership answer reports no removal at all (unchanged).
- **`INV-CACHE-8`** <!-- uuid: eab5289c-7cb4-437c-abf4-42b2ee6b9889 --> — The cache policy above
  MUST NOT change the `capabilities`-flag and `state:` opt-outs, the fail-open rule, the
  `unavailable` stale fallback, or the existing `0`/`2`/`3`/`4` exit codes of `show`, `list` and
  `changes`. A consumer that has just detected a change and needs the current entity MUST pass
  `--fresh` to `show`, because a `detail` entry younger than `read_ttl` is otherwise served. This
  rule is scoped like the cache policy it constrains: it binds the umbrella-owned cache, so it
  applies to backends that do not own their cache and changes (`INV-CACHE-9`). For a backend that
  does, `--fresh` is forwarded to the backend as `fresh: true` and it is the backend that decides
  how current its answer is.
- **`INV-CACHE-9`** <!-- uuid: c9435bbd-8d45-4613-961d-84e719d85ccc --> — Toward a backend that
  owns its cache and changes (ADR 0090), the umbrella MUST NOT keep a cache or a change decision of
  its own, because the backend holds both. For a backend that declares `owns_changes`, the umbrella
  MUST forward `changes` and `changes_ack` to the backend, MUST acknowledge only what it has already
  flushed to its caller, and MUST fail closed (an `unavailable` answer) when the backend cannot
  answer, never falling back to a ledger diff of repeated `list` calls. For a backend that declares
  `cache_opt_out`, the umbrella MUST pass the backend's own annotations (`served_from`, `stale`,
  `age_seconds` and the field-group freshness) through unchanged, and MUST NOT write cache entries
  or cache tombstones for it. It MUST still stamp the ledger for such a backend
  (`INV-LEDGER-FRESH-5`). A backend that declares neither keeps every rule of this section
  (`INV-CACHE-1`).

## Ledger freshness

> Written by bead `pg2-ll4dw.1` (spec
> `docs/superpowers/specs/2026-10-05-pg-desk-attention-evaluator-and-connector-refresh-cache-design.md`,
> section 5, the `INV-FRESH-*` freshness contract this set's half realizes). Freshness is the time
> of the last SUCCESSFUL origin fetch; the delta ledger is where the umbrella records it, per
> `(type, backend, query)` key, so a consumer can read data age locally without a network call.
> For a backend that owns its cache and changes the backend, not the umbrella, observes the origin
> fetch, so the same ledger is stamped from what the backend reports (`INV-LEDGER-FRESH-5`, ADR 0090).

- **`INV-LEDGER-FRESH-1`** <!-- uuid: d6ace83f-a679-4ea8-9cf6-05d637d209f7 --> — After each
  completed origin fetch for a ledger key, the umbrella MUST record that fetch's outcome on that
  key: on a whole-query answer it MUST record `refreshed_at`, the time of that fetch. A key with no
  recorded success has no `refreshed_at` (unknown, never a default time). For a backend that owns
  its changes the origin fetch is the backend's, and `INV-LEDGER-FRESH-5` says where the umbrella
  takes the outcome from.
- **`INV-LEDGER-FRESH-2`** <!-- uuid: 872fc219-5a0a-4d44-9fd3-03466805a948 --> — An answer served
  from the ledger or the entity cache without asking the origin (a `--cached` read, a cache
  fallback) MUST NOT record `refreshed_at`, and MUST NOT record a failure. Otherwise a cache would
  hide staleness behind a young timestamp.
- **`INV-LEDGER-FRESH-3`** <!-- uuid: 74bc6e39-0f55-443d-88bc-c9b31c3889f3 --> — A fetch counts as
  a success only when the origin answered for the whole query. A truncated or partial answer MUST
  NOT record `refreshed_at`; it MUST record `last_error` with the code `truncated`. A failed fetch
  MUST record `last_error` as `{at, code}`, where `code` is the wire error-taxonomy code of the
  failure, and MUST leave the previous `refreshed_at` unchanged. A backend that does not implement
  the query's `list` op at all (`unknown_op`) or does not define the query name
  (`query_not_recognized`) is not a failed source, and neither records anything. A failed fetch
  MUST NOT change the entity index, version counter, cursor, or any consumer position, and MUST NOT
  persist a `--reset` made for that call. `last_error` is retained after a later success, so a
  reader tells "failing since" from "recovered" by comparing `last_error.at` with `refreshed_at`.
  For a backend that owns its changes, "the origin answered for the whole query" is the backend's
  own report of a whole-query answer (`INV-LEDGER-FRESH-5`).
- **`INV-LEDGER-FRESH-4`** <!-- uuid: 5c1c53f6-58f1-41cb-b4eb-2c92da23a8b1 --> — `ledger show`
  MUST report `refreshed_at` and `last_error` for every matching key, as JSON `null` when absent
  (never omitted), so a consumer can distinguish "no success recorded" from a missing field. A
  ledger written before these fields existed reads as no recorded success.
- **`INV-LEDGER-FRESH-5`** <!-- uuid: 9d4453e8-0447-48ef-b017-5cdfb10fc0d5 --> — For a backend that
  owns its changes (ADR 0090), the ledger keeps its `(type, backend, query)` keys, so no row of a
  consumer's freshness view becomes a ghost. The umbrella MUST stamp `refreshed_at` and `last_error`
  on each key from the `sources_freshness[]` the backend returns on every forwarded `changes`
  answer, one entry per configured query: the time of the backend's last WHOLE-query origin answer
  and its last error. The umbrella MUST also stamp the consumer's `last_seen` on every forwarded
  call for that consumer, since the consumer's abandoned-row rule reads it. A cache answer still
  MUST NOT advance `refreshed_at` (`INV-LEDGER-FRESH-2`): the stamp reflects when the backend last
  heard from the origin, never when the umbrella last answered a caller. The backend's own `status`
  exposes the same values.

## Versioning

- **`INV-VER-1`** <!-- uuid: a47b92f1-34ec-451e-93ca-0e41e2f2081d --> — Every wire response MUST
  carry two independent version numbers: `protocolVersion`, one global integer for the envelope
  shape itself, and `schemaVersion`, one integer for whichever schema-bearing capability the
  invoked op belongs to. The two MUST NOT be coupled into one counter — a breaking change to one
  capability's own schema MUST NOT force every other, unrelated backend to redeploy just to stay
  "in version sync." A response whose `protocolVersion` does not match what the umbrella itself
  expects MUST be reported as `version_mismatch` rather than trusted, even if its `result`
  otherwise looks well-formed — a mismatched envelope version means the response's shape cannot
  actually be verified against what this build expects. `config validate`, whose per-backend row
  combines an `auth_status` check with a `capabilities` check, additionally MUST compare each
  capability a backend's `capabilities` response self-declares a `schemaVersion` for against this
  build's own current expectation for that capability, reporting `version_mismatch` on any
  disagreement over a capability **both sides know about**; a capability key the backend declares
  that this build does not recognize (e.g. a future capability this build has no opinion on) MUST
  NOT be treated as a mismatch by omission.

  A capability's own schema version moves only with that capability's own wire shape: for example
  the `issue` capability's additive `status_category` field moved `issue` from schema version 7 to
  8 (bead `pg2-mj0jv`) and left every other capability's version, and `protocolVersion`, untouched. The `created_at` field then moved it from 9 to 10 (bead `pg2-2j5ac.44.2`).

  ```mermaid
  flowchart TD
      recv["backend response received"] --> pv{"protocolVersion matches?"}
      pv -->|no| vm1["version_mismatch (INV-VER-1) - do not trust result"]
      pv -->|yes| iscaps{"was this a capabilities call?"}
      iscaps -->|no| ok["proceed - schemaVersion is this op's own capability's, already correct by construction"]
      iscaps -->|yes| cmp["compare each self-declared schemaVersions[cap] this build recognizes"]
      cmp --> known{"a recognized capability disagrees?"}
      known -->|yes| vm2["version_mismatch, naming that capability"]
      known -->|no| ok2["no mismatch - unrecognized capability keys are skipped, not flagged"]
  ```

## Error taxonomy

- **`INV-ERR-1`** <!-- uuid: 18167463-248b-4264-b08e-2dce05a601b9 --> — A wire-level failure's
  `error.code` MUST be one of exactly seven values: `not_found`, `unauthenticated`, `unavailable`,
  `unknown_op`, `version_mismatch`, `invalid_argument`, `query_not_recognized` (the seventh member,
  added for the `list` op's own named-query resolution — see `INV-ERR-3`). Each MUST map 1:1 to a
  Go sentinel error a caller can match with `errors.Is` rather than substring-matching `message`. A
  handler error that is not wrapped in one of these seven sentinels MUST be reported as
  `unavailable` — the taxonomy's closest fit for "something went wrong and this backend cannot
  currently be used" — rather than invent an eighth code or leave `error.code` unset.
- **`INV-ERR-2`** <!-- uuid: b2e4a6ea-a6e7-4de5-b317-1e2c29bbd83d --> — `not_found`,
  `invalid_argument`, and `unavailable` MUST NOT be conflated, because each answers a different
  question about who or what is at fault:
  - **`not_found`** — the request was well-formed and named a specific entity that genuinely does
    not exist. A **valid negative answer**, never a failure.
  - **`invalid_argument`** — the **caller's own** request was malformed (an empty required field,
    an id that does not even parse into this backend's own id shape). The backend itself is
    healthy; only the request was bad.
  - **`unavailable`** — the backend itself cannot currently be used (down, unreachable, or any
    other backend-health problem).

  A backend reporting `unavailable` for what is actually a caller-input problem, or for what is
  actually a genuine "no such entity," misreports its own health and denies a health-reporting
  consumer of the ability to tell the two apart without parsing free-text `message`.

  ```mermaid
  flowchart TD
      err["backend op handler hits an error"] --> parse{"did the request even parse into this backend's own id/argument shape?"}
      parse -->|no| ia["invalid_argument - caller's fault, backend is healthy"]
      parse -->|yes| exists{"does the well-formed request name an entity that exists?"}
      exists -->|"no - genuinely doesn't exist"| nf["not_found - a valid negative answer, not a failure"]
      exists -->|"backend itself cannot answer right now"| un["unavailable - backend's own health problem"]
  ```

- **`INV-ERR-3`** <!-- uuid: 7b4f2a91-6c3d-4e8a-9f10-2d5e8a3c6b17 --> — `query_not_recognized`
  answers a question `INV-ERR-2`'s three-way split does not: the `list` op's own caller-facing
  query NAME (resolved against the backend's own `config.queries` block — see the `INTF-WIRE` op
  catalog's `list` row and `INV-STATE-1`) is not one this backend's own config defines. The request
  is well-formed and the backend is healthy — distinct from `not_found` (which answers "does a
  specific ENTITY exist," never "is this NAME even defined") and from `invalid_argument` (a
  malformed request SHAPE, not an unrecognized caller-facing name). A backend MUST NOT treat an
  unrecognized query name as a usage error, a crash, or a silently empty result — it MUST answer
  `query_not_recognized` — and MUST NOT ship any built-in query name of its own (`INV-STATE-1`), so
  every name a caller can legitimately supply comes from that backend's own registered config.

  The umbrella's own `list` fan-out (`INTF-CLI`) treats one backend answering
  `query_not_recognized` as "not applicable to this backend": skip it, report it in `sources[]` as
  `disabled`, and exclude it from degraded-outcome accounting (`INV-EXIT-2`'s same rule, applied
  here) — a fan-out where at least one OTHER backend recognizes the name is still exit-0 success.
  Only when EVERY registered backend of the type answers `query_not_recognized` does the umbrella
  fail the whole call, as its own CLI-level `invalid_argument` failure (never one of the fan-out
  scheme's `0`/`2`/`3` values) — a query name nothing recognizes is a config-authoring/caller
  mistake, not a partial-outage classification.

## Registry

- **`INV-REG-1`** <!-- uuid: 0b8b254c-3fcf-46ce-bbe7-1c9b1c03be0d --> — The `connector.<type>`
  registry MUST be flat and type-keyed at the top level. `issue`, `ci`, `pr`, `calendar`, and `mail` MUST
  be list-valued (zero or more simultaneously-registered backends); `scm` MUST be single-valued
  (exactly zero or one). Every registry entry MUST be either a bare backend binary name or an
  instance `{name, command}` as stated in `INV-REG-4` — there MUST be no `exec:`-prefix or other
  built-in/external distinction, because nothing is compiled into the umbrella itself
  (`GOAL-MIN-1`). (`thread` is also list-valued in practice but, as of this
  writing, is missing from this enumeration — a pre-existing gap this rule's own text has not yet
  been updated to close.)
- **`INV-REG-2`** <!-- uuid: 6ea815c2-293c-40ca-93d7-2d8cc1b73b93 --> — A **targeted** op MUST
  resolve to exactly one registered backend for its capability. When a capability's registry
  entry names zero backends, a targeted op against that capability MUST fail as a CLI-level error
  before any wire call is attempted.

  A capability's registry entry naming more than one backend resolves differently depending on
  whether the op is **id-keyed** (`show`, `files`, `commits`, `review submit`, `review pending`, `comment`, `transition`, `update`,
  `close`, `deps`, `children` (an optional op: a backend answering `unknown_op` is skipped like a `not_found` one), `get_logs`, `rerun_failed`, `mail`'s `show`, `mark_read`, `mark_unread`, `archive`,
  `unarchive` and `fetch_attachment`, every `scm` targeted verb) or an **id-less write**
  (`issue create` today, the only member) — a split fixed
  by bead pg2-2j5ac.17.2's own operator ruling, deliberately narrow (see this rule's final
  paragraph):
  - **An id-keyed op, no `--backend` given** — the umbrella tries each registered backend in
    registration order, stopping at the first answer that is not `not_found` (the multi-instance
    resolution policy bead pg2-2j5ac.17.2 implements); it MUST NOT otherwise silently pick one of
    several registered backends without exhausting this policy. Any non-`not_found` error
    short-circuits immediately without trying the remaining backends; if every registered backend
    answers `not_found`, that is the aggregate result.
  - **An id-less write, no `--backend` given** — MUST hard-fail as a CLI-level error at N > 1,
    exactly as it did before the multi-instance policy existed; the try-each policy above does
    NOT extend to this case (bead pg2-2j5ac.17.2's own scope ruling: "the narrower question of
    what `create` SHOULD eventually do at N > 1 with no `--backend` given is explicitly out of
    scope," left for a separate bead).
  - **`--backend <binary>` given (either kind), amended by bead pg2-2j5ac.28.1** — every Tier-1
    verb (targeted, id-less, or `list`) accepts this flag; when present, the umbrella resolves
    DIRECTLY to that one named backend — validated against the capability's own registration, a
    CLI-level error if it names a backend not registered for that capability — skipping either
    resolution above entirely. On an id-less write with no meaningful fan-out (e.g. `issue
create`), `--backend` is how an operator resolves an otherwise-ambiguous N > 1 registration
    explicitly, rather than the umbrella guessing or hard-failing. On `list`, `--backend` pins the
    fan-out to exactly that one backend instead of querying every registered backend of the type.

  Selecting among multiple simultaneously-registered same-capability backends for an id-keyed op
  with NO `--backend` given and a first-tried non-`not_found` answer that is itself unhealthy
  (i.e., ranking/failover beyond "first non-`not_found` wins") is a future concern this set does
  not yet resolve.

- **`INV-REG-3`** <!-- uuid: 5adff190-3848-4fdb-b02b-16f4c8f55591 --> — `attention.sources`,
  `search.sources` and `activity.sources` MUST be flat, top-level, always-list-valued registry keys, independent of
  `connector.<type>` (never nested under it, never sharing its own zero/single/list-valued
  distinction per type). A backend name MAY be registered under one of these AND under a
  `connector.<type>` entry at once, with no cross-check between the two — the same binary
  answering more than one capability, extending `INV-REG-1`'s multi-capability-backend allowance
  to these three keys. None of the keys participates in `auth status`'s or `config validate`'s own
  fan-out — all resolve their backend set from `AllBackends` (`connector.<type>` only) — so a
  backend registered ONLY under `attention.sources`/`search.sources`/`activity.sources` reports its own health
  solely through `attention list`'s/`search`'s/`activity list`'s own `sources[]` rows, never through `auth
status`/`config validate`.
- **`INV-REG-4`** <!-- uuid: a9092e28-e3ad-4c3a-8d7c-9def36d2d111 --> — A registry entry, under any `connector.<type>` key or
  under `attention.sources`, `search.sources` or `activity.sources`, MUST be either a plain string
  (name and binary are the same word, command is `[name]`) or a mapping with the keys `name` and `command` and optionally `previous_names`. `command` MUST be a non-empty list of strings whose first word is a bare binary
  name (non-empty, no path separator, no whitespace); it MUST NOT be a shell string. A name MUST be
  non-empty, MUST NOT contain a path separator, and MUST NOT contain `__`. Within one list a name
  MUST appear at most once. A name registered under more than one key MUST have the same command
  under each. The umbrella MUST exec `command[0]` with `command[1:]` as its arguments and nothing
  after them, and MUST NOT interpret, expand or reorder them; the wire request on stdin is
  unchanged. The name is the identity used for `sources[]` rows, the `--backend` pin,
  `backends.<name>` and the cache and ledger keys. A `pg-connector-activity-*` capability-only
  backend MUST NOT appear under `connector.<type>` whether the prefix is on the name or on
  `command[0]`.
  `previous_names`, when present, MUST be a list of names that obey the name rules above, none equal
  to the entry's own name, none repeated, and none a currently registered backend; a name registered
  under more than one key MUST carry the same `previous_names` under each. Because the name is part
  of the ledger key, renaming a backend orphans the ledger filed under its old name (and re-emits
  every live entry as `added` on the first `changes` run) unless the old name is declared in
  `previous_names`: when an entry's own ledger file is absent, the umbrella MUST adopt, for the first
  previous name that has one, the ledger of the same type, query and instance discriminator by
  copying it under the ledger lock, MUST NOT overwrite an existing ledger, and MUST leave the old
  file in place. The cache is not adopted; it re-warms.

## CLI outcome reporting and exit codes

- **`INV-EXIT-1`** <!-- uuid: e804ff39-e941-4a3f-b754-d16a114eea9d --> — pg-connector's own CLI
  exit code MUST be computed from exactly one of two schemes, chosen by the invoked op's shape,
  and this scheme MUST NOT be built from, or confused with, `INTF-WIRE`'s plain `0`/`1`
  (`INV-WIRE-1`):
  - **Fan-out** (queries every backend registered for a type/capability — `ci list`,
    `auth status`, `config validate`, `attention list`, `search`): `0` every queried backend
    succeeded (no degraded/failed row); `2` degraded/partial (at least one backend succeeded and
    at least one did not); `3` total failure (every backend failed, including the case of zero
    backends registered — a misconfigured host has nothing to report as success).
  - **Targeted** (resolves to exactly one backend — `show`, `files`, `commits`,
    `review submit`, `review pending`,
    `create`, `comment`, `transition`, `get_logs`, `rerun-failed`, `worktree add`/`remove`/`list`,
    `branch detect`): `0` the operation completed and produced a well-formed response (including
    a successful write); `4` `not_found` — a well-formed negative answer, MUST NOT share a code
    with an actual failure; `1` any other error (`unauthenticated`/`unavailable`/`unknown_op`/
    `version_mismatch`/`invalid_argument`, or a CLI-level failure before any well-formed response
    was produced at all — no backend registered, an ambiguous multi-backend registration
    (`INV-REG-2`), a bad flag).

  A targeted op that reports a well-formed outcome in its `result` body, including one that did
  not do what was asked (for example `review submit`'s `no_change`, where nothing needed to be
  posted), exits `0`: the outcome is distinguished by the body's `status`, never by the exit code,
  and a caller MUST read it.

  `1` is otherwise reserved and MUST NOT be emitted by the fan-out scheme for an in-taxonomy
  outcome — it stays available as the CLI's own generic/unexpected-failure path.

  ```mermaid
  flowchart TD
      call["operator invokes a verb"] --> shape{"fan-out or targeted?"}
      shape -->|fan-out| fo{"how many backends succeeded / degraded?"}
      fo -->|"all succeeded"| e0a["exit 0"]
      fo -->|"some succeeded, some degraded"| e2["exit 2 (degraded/partial)"]
      fo -->|"none succeeded (incl. zero registered)"| e3["exit 3 (total failure)"]
      shape -->|targeted| tg{"outcome?"}
      tg -->|success| e0b["exit 0"]
      tg -->|"not_found"| e4["exit 4 (well-formed negative)"]
      tg -->|"any other error, or a CLI-level failure before a response existed"| e1["exit 1"]
  ```

- **`INV-EXIT-2`** <!-- uuid: b7b485a1-15d4-4426-80ed-69cad23a0c0d --> — A `sources[]` row of
  status `disabled` (a backend correctly answering "not applicable" to an op it doesn't implement
  — e.g. `auth_status` when its provider implements no `AuthChecker`) MUST count as healthy for
  the fan-out exit code (`INV-EXIT-1`), never as a failure. A backend that is legitimately
  no-credential, or that legitimately doesn't implement an optional op, MUST NOT make a
  fully-correct, fully-configured host read as a standing partial outage forever.
- **`INV-OUT-1`** <!-- uuid: 4ada7880-6ff9-4e91-a3ac-96494cc38d6c --> — Every fan-out response
  MUST carry a `sources[]` array with exactly one row per backend actually queried —
  `{source, status, count, reason}`, `status` one of `succeeded`/`degraded`/`disabled` — and MUST
  NOT collapse that per-backend detail into one pass/fail signal. A degraded or disabled outcome
  MUST live in this JSON body, never as a stderr `WARNING:` line. `count` MUST be that backend's
  own raw, pre-merge item count, unaffected by any later cross-backend deduplication a fan-out's
  own merge stage performs.
- **`INV-FANOUT-1`** <!-- uuid: 995aa786-40bc-444a-a31d-eba8fe3d8c27 --> — A fan-out MUST issue its per-backend calls concurrently,
  so that a backend being down or slow delays only its own row: the fan-out's wall clock is that
  of its slowest backend, never the sum of every backend's deadline (operator ruling, Phillip,
  2026-10-09, bead pg2-55k6y). The concurrency is bounded by `state.fanout_concurrency` (a
  positive integer; default 8; `1` runs the calls strictly one after another). The response MUST
  stay deterministic and byte-identical to a serial run's: `sources[]` rows and every
  concatenated or grouped result are emitted in REGISTRATION order regardless of completion
  order. Each backend call keeps its own per-op deadline; a backend that fails, answers
  undecodably, or panics MUST yield its own `degraded` row and MUST NOT abort, delay or reorder
  its siblings. The concurrency applies to the per-backend work only: assembling the response
  and the entity-cache writes of `list` stay serial, in registration order. The one umbrella
  state written from a backend's own goroutine is `changes`' per-backend delta ledger (its
  refresh, save and freshness stamp), and each backend owns its own ledger and cache key
  (`INV-REG-4`), so no umbrella state is shared between concurrent calls. A
  single-backend call and a `--backend` pin run exactly as before. `INV-REG-2`'s try-each
  resolution of an id-keyed op is NOT a fan-out: it stays sequential, in registration order,
  because it stops at the first answer and a write op MUST NOT be sent to every backend.

## Cross-cutting capability aggregation

- **`INV-ATTN-1`** <!-- uuid: 5e51d13b-8f9e-4a7e-a882-462fdb16af7a --> — `attention list`'s
  aggregation MUST dedup every queried source's raw `list_attention` items by `{type, id}` into
  one merged item carrying a `via` list of every source that reported it — it MUST NOT simply
  concatenate them (unlike `ci list`'s own "runs concatenate" fan-out). A dedup group's own
  `summary`/`severity` MUST come from its most-severe contributing source (an absent/invalid
  severity ranks as `medium` for this comparison only — it MUST NOT be written back as `medium`
  on the wire); a tie at the same rank MUST be broken by `attention.sources` config order (the
  earliest-configured source wins). The merged list MUST then be sorted by severity rank
  descending, tiebroken by `via` length descending, tiebroken by the winning contributor's own
  config order ascending (and, within that, by its own original item order). No cap applies by
  default; an explicit `--cap N` truncates the already-merged/sorted list and MUST set
  `truncated`/`total_before_cap` only when the cap actually cuts items.
- **`INV-ATTN-URL-1`** <!-- uuid: 31ed884e-8c30-4792-9638-612b94096377 --> — An attention
  item's optional `url` (attention schema version 2) MUST name the item's OWN page — the alert in
  its source system, the PR, the issue — and nothing else: never a related entity's page
  (cross-entity links are not an attention concern). A source with no page for an item MUST omit
  `url` entirely (never an empty string). `url` MUST NOT be defaulted or synthesized by any
  caller, including `attention list`'s merge layer, which passes it through unread; a dedup
  group's `url` is the winning contributor's own (the same winner `INV-ATTN-1` selects for
  `summary`/`severity`). The `via`/`truncated`/`total_before_cap` aggregation fields remain the
  only fields the merge layer adds; `url` (like `group`, `INV-ATTN-GROUP-1`) is a per-source descriptive field and not one of them.
- **`INV-ATTN-GROUP-1`** <!-- uuid: 823c2aff-c8fc-45e6-b41a-a95c4423ed17 --> — An attention item's optional `group` (attention
  schema version 3) is the item's work-context group, an object `{key, label}`: `key` identifies
  the group and, when the group is anchored on an entity, is itself a `<type>:<id>` ref
  (`issue:ABC-1`, `pr:<owner>/<repo>#<n>`); `label` is the group's display name. A source with no
  grouping MUST omit `group` entirely (never `null`, never an empty object). `group` is
  descriptive only: it MUST NOT be defaulted or synthesized by any caller, including `attention
list`'s merge layer, which passes it through unread. A dedup group's `group` is the winning
  contributor's own (the same winner `INV-ATTN-1` selects for `summary`/`severity`/`url`): when
  that winner carries none, the merged item carries none, and a losing contributor's `group` MUST
  NOT be borrowed or combined. Adding `group` is additive: a version-2 consumer ignores the field,
  and a version-3 consumer tolerates its absence. A consumer MUST NOT infer a group's size or
  completeness from an `attention list` result, because `--cap N` truncates the item list
  (`INV-ATTN-1`), not groups. The human (`--output human`) rendering of `attention list` is
  unaffected by `group`.
  > **Entity attention is not a connector concern.** Whether a pull request, an issue or a thread
  > needs the operator is decided by `pg-desk`'s evaluator, not by the entity backends. The former
  > "CI failing on my PR" item (`type` `pr-ci`) is now the `pg-desk` rule `pr.own-ci-failing` (see
  > `docs/behavior/pg-desk/attention.md`), and the PR, Jira and beads backends no longer report entity
  > attention: each answers `list_attention` with `unknown_op`, which `attention list` treats as "not
  > applicable" for that source rather than a failure. `INV-ATTN-1` and `INV-ATTN-URL-1` continue to
  > govern every item that does reach the feed. One exception, for the beads backend only
  > (`INV-ATTN-BEADS-1`): it answers a label-driven `list_attention` again, by operator ruling.
- **`INV-ATTN-BEADS-1`** <!-- uuid: 20764bea-8ef7-4101-96d0-27a137652204 --> — The beads backend (`pg-connector-issue-beads`)
  MUST answer `list_attention` as a label-driven to-do list, and MUST NOT evaluate any other
  attention rule (no due-date, deadline or review rule: those stay `pg-desk`'s). It reports the
  beads of the tracker instance it serves that carry ANY label of the `attention_labels` list in
  its own `backends.<name>` config block, set per instance; there is no built-in default label set.
  When `attention_labels` is empty or missing it MUST answer `unavailable` naming
  `attention_labels`, before any `bd` call, and MUST NOT return an unscoped or empty-as-success
  result. A bead qualifies in stored status `open`, `in_progress` or `blocked` (a label is an
  explicit request for attention, so `blocked` still counts); `deferred` (the operator's own "not
  until later"), `closed`, `pinned` and `hooked` MUST NOT be reported. Each item is an
  `AttentionItem` with `type` `issue`, `id` the bead id, no `url` (`INV-ATTN-URL-1`: bd has no
  page), `severity` mapped from bd priority (P0 `critical`, P1 `high`, P2 `medium`, P3 and P4
  `low`), and a `summary` of the form `<title> [P<n>; <matched labels>]`. The tracker is not a
  field of the item: bead ids are unique per tracker prefix, and `via` names the instance. Items
  are ordered by bd priority ascending, then id. This reverses decision D3 of `pg2-m482k` for the
  beads backend only (operator ruling, Phillip, 2026-10-09, bead `pg2-wyeq4`, option B:
  "reintroduce list_attention in the beads backend, driven by a configurable attention_labels list;
  accept reversing D3 for beads"); the PR and Jira backends still answer `unknown_op`, and the
  retired `INV-ATTN-CI-1` stays removed (see ADR 0081, Amendment 2026-10-09).
- **`INV-SEARCH-BEADS-1`** <!-- uuid: 8e459d29-474d-4e8a-b19e-be1d944c9c2a --> — The beads backend MUST answer `search` by
  wrapping `bd search` over the tracker instance it serves: the query is matched by bd (bead titles
  and ids, every status including `closed`, so a filed-or-fixed check cannot silently answer no)
  and each hit becomes a `SearchResult` of `type` `issue` with no `url`. The query text MUST reach
  bd as the value of `--query`, never as a flag. An empty query is `invalid_argument`. The backend
  declares the attribute names it fills under `capabilities.vocabulary.search_attributes`
  (`status`, `priority`, `labels`, `issue_type`, `assignee`, `owner`, `tracker`) so the umbrella's
  `--fields` check recognizes them, fills only the requested ones, and silently ignores any other
  requested name. The umbrella's `--since`/`--before` bound a hit's `updated_at` (since inclusive,
  before exclusive); a hit without a parseable `updated_at` is dropped from a bounded search.
- **`INV-SEARCH-1`** <!-- uuid: a9fdaa89-5b51-4d5f-8a2b-3d72a36a0326 --> — `search`'s aggregation
  MUST NOT merge or dedup across sources at all — unlike `attention list`'s `INV-ATTN-1`, each
  queried source's own results stay grouped under that source, in that source's own returned
  order, and groups themselves MUST be ordered by `search.sources` registration order, never
  interleaved. `search` takes no type-filter argument: "the queried type(s)" is simply the union
  of every type any registered source can return.

## Auth

- **`INV-AUTH-1`** <!-- uuid: 04bd52c1-e78b-44e3-87a9-c205f771f25b --> — A capability's own
  Provider interface MUST NOT require an auth-check method. A backend's concrete provider MAY
  implement the optional `AuthChecker` sub-interface (asserted by a type-check, never folded into
  the Provider interface itself); when it does, that capability's dispatch table gains an
  `auth_status` entry. When it does not, `auth_status`/`capabilities`-driven fan-outs (`auth
status`, `config validate`) MUST report that backend's row as `disabled` with a reason of "not
  applicable," recognized generically via the wire-level `unknown_op` code — never forcing a
  meaningless answer out of a backend with nothing to check.

## Composition boundary

- **`INV-COMP-1`** <!-- uuid: a2751d7e-938e-4ddb-ab99-aabe42ffd91e --> — A Tier-2 backend's own
  op handler MUST resolve a data need belonging to a **different** capability through its own
  direct, already-declared system access. It MUST NOT execute the `pg-connector` umbrella, or any
  other Tier-2 backend binary, to satisfy its own op. This rule binds `ACTOR-BACKEND`, not the
  umbrella — it is the one invariant in this set that constrains a backend's own behavior rather
  than the umbrella's. (One violation of this rule shipped and was found and fixed by hand before
  this set was authored; no automated regression guard exists yet — see the
  [README](README.md)'s realization-gap register.)

## Presentation

- **`INV-OUT-2`** <!-- uuid: b3c99196-b0ea-4bd5-84d2-31631b02cb4f --> — pg-connector's own CLI
  presentation mode (`--output json|human`) MUST default to `json` — the same stable,
  machine-readable envelope every existing script already parses — and MUST NOT be chosen by
  auto-detection (a TTY check or similar): the same invocation MUST always produce the same
  shape, regardless of what plumbing happens to sit between it and its caller. `--output` MUST be
  validated **before any backend is dispatched**, so an invalid value is caught with zero side
  effects rather than after a targeted write op has already run against a live backend. The
  output-mode choice MUST NOT alter `INTF-WIRE`'s own wire protocol in any way — it is a
  CLI-presentation concern layered entirely on top of an already-decoded, already-typed result.

## Alert capability

- **`INV-ALERT-1`** <!-- uuid: 2f06ab7b-1c8f-49cd-8ce8-915fc04ca9e4 --> — `alert` list results (`list`, and attention derived
  from them) MUST contain only actively-firing alerts. A named query narrows within the firing
  set and MUST NOT be able to make resolved, silenced, or inhibited alerts appear.
- **`INV-ALERT-2`** <!-- uuid: 0b779bda-7152-4c9d-86d2-39d3aa0f8849 --> — `Alert.acknowledged` is OPTIONAL and a pointer: absent
  means "this source cannot express it", NOT "unacknowledged". A consumer MUST NOT read absence
  as false.
- **`INV-ALERT-3`** <!-- uuid: 27864503-e22a-4236-96c0-a98c334120e5 --> — `Alert.id` MUST be stable across polls for the same
  firing instance (not derived from a start time or any value that changes while firing) and MUST
  be namespaced by provider, so ids are unique across sources (`{type, id}` is the attention
  merge key, `INV-ATTN-1`). Any future hide/ignore keys on `(alert, Alert.id)`.
- **`INV-ALERT-4`** <!-- uuid: 3d05165a-5cd8-424a-a653-b7ba5f405b1f --> — A backend maps its own source severity onto the
  attention `Severity` enum internally, in a closed table; the umbrella applies no mapping. An
  absent or unrecognized source severity is omitted, and MUST NOT be defaulted anywhere.
- **`INV-ALERT-5`** <!-- uuid: 23834005-df0f-4004-9660-2248f4120f0b --> — `alert` MUST NOT fall back to the umbrella entity cache
  (a cached firing set would render a stale "all clear" as current), so `stale` is always false.
  "Unknown" MUST be distinguishable from "none" via the `sources[]` row (`degraded` versus
  `succeeded` with `count: 0`, or fan-out exit `3`), never from an empty `entities`/`items` array.
- **`INV-ALERT-6`** <!-- uuid: 60fc604d-34bd-4739-9598-530d53a247a1 --> — The `alert` capability is read-only: no acknowledge,
  silence, or hide operation exists on `alert.Provider` or in the `alert` wire catalog.
- **`INV-ALERT-7`** <!-- uuid: 06f06803-001c-475c-aed8-7724c7a2ff58 --> — A backend registered for alerts answers `list_attention`
  directly, from its own data, and MUST NOT derive it by calling the umbrella or a sibling backend
  (`INV-COMP-1`). The umbrella MUST NOT derive attention items from `alert list`; the two
  registrations (`connector.alert`, `attention.sources`) are independent.
- **`INV-ALERT-8`** <!-- uuid: 711a14a6-cad8-4c1b-9256-689261e1270f --> — An acknowledged alert is still an active alert and MUST
  stay in a backend's `list_attention` result: acknowledgement MUST NOT remove it. The backend MUST
  emit its `severity` one level lower than the same alert unacknowledged (`critical` to `high`,
  `high` to `medium`, `medium` to `low`, `low` stays `low`), so unacknowledged alerts of the same
  source severity are considered first. Lowering applies only when `acknowledged` is present and
  true (`INV-ALERT-2`) and only to a present `severity` (`INV-ALERT-4`: never synthesized). The
  umbrella applies no alert-specific logic (`INV-ALERT-7`); its existing severity-rank sort
  provides the ordering.

## Calendar identity matching

- **`INV-CAL-1`** <!-- uuid: cbbe6491-8676-42fb-a491-418f3f36f6a9 --> — A `calendar` backend
  matching an event's attendee identity against a configured `important_people` list MUST do so
  case-INSENSITIVELY, and MUST disambiguate an email-shaped entry from a bare username/display-name
  entry: an entry containing `@` matches only against an attendee's own `email` field (compared
  case-insensitively, with NO domain normalization — `alice@Example.com` and `alice@example.com`
  MUST be treated as the same address, but `alice@example.com` and `alice@sub.example.com` MUST
  NOT); a non-email entry matches only against an attendee's own `name` field (compared
  case-insensitively, exact string match — no fuzzy or substring matching). Every `calendar`
  backend implements this matching independently (ADR 0062 principle 6 forbids a shared
  credential/logic library across backends), so this invariant — not each backend's own private
  copy — is what keeps `pg-connector-calendar-osx-bridge` and a future `pg-connector-mail-osx-bridge`
  from silently drifting apart, even though the `important_people` config VALUES themselves are
  shared between them via nix. (How a `mail` backend splits a sender string into the name and
  email these rules compare against is fixed by `INV-MAIL-2`.)

  A backend MUST treat "organizer matching" as attendee-list matching only:
  `schema.CalendarEvent` (ported from `calendarapi.Event` [landed: `pg2-p9ap3`]) carries no
  separate `Organizer` field — only an `Attendees` list with no is-organizer marker at all — so
  there is no way to test "is this important person the organizer specifically" until
  `pg-osx-bridge-api`'s own wire shape gains one. A backend MUST NOT paper over this: it is a real,
  verified constraint of the underlying system, not an oversight in this invariant.

## Calendar attention ramp

> Operator ruling (Phillip, 2026-10-02, bead `pg2-pf1rb`): "lower attention for dailying meetings,
> higher attentino for 15 minutes before and highest for the duration of the meeting." Realized by
> `pg-connector-calendar-osx-bridge`'s `list_attention`; the rules below bind every `calendar`
> backend that reports calendar events as attention items. A `calendar` backend whose attention
> items are not calendar events is outside them: `pg-connector-calendar-task-focus` reports the
> focus service's tasks and cycles (`INV-CONN-7` in `docs/behavior/pg-task-focus`).

- **`INV-CAL-2`** <!-- uuid: a75011e5-d5f9-4ef1-b501-33231f795c9a --> — A `calendar` backend MUST
  report each event in its `list_attention` result at a `severity` determined by where `now` falls
  relative to the event, in three time tiers: an event that has not started and starts MORE than
  the lead time from now MUST be `low`; an event that has not started and starts WITHIN the lead
  time (`0 < start - now <= lead`, so exactly the lead time is inside) MUST be `high`; an event in
  progress (`start <= now < end`) MUST be `critical` for its whole duration and MUST drop out of
  the result once it ends (`end <= now`). The result MUST be a function of the backend's clock at
  call time alone — no state is kept between calls (`INV-STATE-1`).
- **`INV-CAL-3`** <!-- uuid: b6943fd0-1c4c-4d18-8dee-67ccebc55e9f --> — An all-day event has no
  meaningful start instant and MUST NOT be ramped: a backend MUST report it at `low` for as long
  as it overlaps the attention window and MUST NOT raise it by any other input (`INV-CAL-4`). It MUST
  NOT be reported `critical` for its day. Chosen over omitting it because an all-day event (a
  holiday, an out-of-office marker, a deadline) is still information a consumer MAY want, and a
  consumer can already filter on `low`; omitting would lose it silently.
- **`INV-CAL-4`** <!-- uuid: fcd393b5-d8d9-44aa-8c3c-dbb5b9445f3f --> — The attention level of a
  calendar event MUST be driven by time ONLY (`INV-CAL-2`, `INV-CAL-3`). The event's calendar
  `priority`, a match against `important_people` (`INV-CAL-1`), and a tentative RSVP MUST NOT
  affect it: the same event MUST yield the same `severity` whatever those three are. A tentative
  event is still reported (`INV-CAL-6` excludes only declined events) and sits at the same time
  tier as an accepted one. As a consequence `important_people` is currently UNUSED for attention:
  no `calendar` backend consumes the match, and the config key and `INV-CAL-1` matching are kept
  only as the contract for a future consumer.

  > Operator ruling (Phillip, 2026-10-03, recorded in bead `pg2-pf1rb`, CAL-RAMP-1): "for now, the
  > attention for calendar is only driven by time. for any iten on my calendar, which i have not
  > declined (so tenantivve and approved) will show for the day and bump up 15 minutes before and
  > through the meeting afterwhich it drops off. in-progress meetings must come back" — CAL-RAMP-1:
  > attention MUST be driven by time ONLY; calendar priority, `important_people` and tentative
  > status MUST NOT affect its level (for now). Implemented by bead `pg2-ib03r`, which SUPERSEDES
  > the earlier text of this invariant (a one-level, capped shift of the time tier by those three
  > modifiers, which `pg2-pf1rb`'s first landing, `86f026db`, had wrongly kept; removed).

- **`INV-CAL-5`** <!-- uuid: 23fb614d-c60f-40b3-9428-adb228d6a6d7 --> — The lead time and the
  look-ahead window MUST be per-backend config keys, `attention_lead_time` (default 15 minutes) and
  `attention_window` (default 24 hours), each a Go `time.ParseDuration` string. An event is a
  candidate only if it has not ended and starts before `now + attention_window` (an
  in-progress event is a candidate however long ago it started). A malformed duration, a negative lead time, or a non-positive window MUST
  answer `invalid_argument`. A zero lead time disables the `high` tier. Because the underlying
  range query is not relied on to return an event that started before `now`, a backend MUST widen
  its events query start into the past and filter locally, so an in-progress event is reported
  whether or not the range predicate returns overlapping events.
- **`INV-CAL-6`** <!-- uuid: 4180c936-1c67-412c-98bd-45ba8019f298 --> — A `calendar` backend MUST
  NOT report an event the user has declined (self RSVP `declined`, compared case-insensitively) in
  its `list_attention` result, in any tier. Declined events remain visible to the non-attention
  ops (`list`, `list_events`), which report the calendar, not what needs attention.

## Mail capability

> The `mail` capability's Tier-1 contract: `pkg/provider/mail`, `pkg/schema/mail.go`, and
> `ADR 0062` Decision item 10. The rules below bind every `mail` backend.

- **`INV-MAIL-1`** <!-- uuid: 789aae82-9baf-46cd-b3ba-c0ab2193fad9 --> — A `mail` backend MUST NOT expose any delete operation
  (permanently remove, move to trash, expunge, purge, or any equivalent), and the `mail.Provider`
  interface MUST NOT gain one: its op set is exactly `list`, `show`, `search_messages`, `mark_read`,
  `mark_unread`, `archive`, `unarchive`, and `fetch_attachment` (`INTF-WIRE`'s op catalog). `archive`
  is NOT deletion: the archived message MUST remain retrievable (`show`, `unarchive`). A removal
  path under any other name is a violation. Reason: the operator's firm requirement, "no deleting
  will occur", applies to the whole stack — the Tier-1 interface, every Tier-2 backend, and the
  bridge behind it — so that no bug, misuse, or prompt-injected caller can destroy mail through
  pg-connector. A test asserts both the interface's method set and the dispatch table's registered
  ops.
- **`INV-MAIL-2`** <!-- uuid: fdf56447-c6bd-4c30-902d-249fe696e677 --> — A message's sender is carried on the wire as ONE
  `"Name <email>"` string (`schema.MailMessage.Sender`), not as separate name and email fields. A
  `mail` backend matching a sender against a configured `important_people` list MUST first split
  that string into a name and an email, then apply `INV-CAL-1`'s rules to the result unchanged
  (case-insensitive; an entry containing `@` compares to the email only, with no domain
  normalization; any other entry compares to the name only, by exact match). The split: after
  trimming surrounding whitespace, if the string ends with `>` and contains a `<`, the email is
  the text between the LAST `<` and the final `>` (trimmed) and the name is the text before that
  `<` (trimmed, with one pair of enclosing double quotes removed when present); otherwise, if the
  trimmed string contains `@`, the whole string is the email and the name is empty; otherwise the
  whole string is the name and the email is empty. An empty name or email MUST match no entry. The
  parse is fixed here so that every `mail` backend and the `calendar` backend agree on identity
  matching (the same drift-prevention purpose as `INV-CAL-1`).
- **`INV-MAIL-3`** <!-- uuid: 5940c8e2-9b77-42f5-bf5c-bdf26a8796c2 --> — A `mail` backend MUST NOT derive any attention `severity`
  from a message's `mailbox_priority` or from an `important_people` match: mail attention, if a
  backend answers `list_attention` for mail at all, is time-driven only, exactly as `INV-CAL-4`
  rules for `calendar`, and a `mail` backend is NOT required to be registered under
  `attention.sources` (operator rulings, Phillip, 2026-10-05, beads `pg2-qc5uc.1` and
  `pg2-qc5uc.2`: attention for email is expected to be decided outside pg-connector, in `pg-desk`,
  because it needs information pg-connector does not have; the operator will re-evaluate
  `important_people` and mailbox tiers later). `mailbox_priority` is an OPTIONAL static tag on the
  wire and MAY be unset; an unset value is the expected steady state until tiers are decided, not
  a defect.
- **`INV-MAIL-4`** <!-- uuid: e17e00a3-01fa-4366-85cb-a18b9ec09aae --> — The `mail` backend's
  `list_attention` MUST answer an EMPTY list, however many messages are unread, flagged, or from an
  `important_people` sender, and however its mailboxes are tiered. Reason: `INV-MAIL-3` makes mail
  attention time-driven only, and an email carries no time-driven signal (unlike a calendar event's
  start and end); the operator has ruled that attention for email is decided in `pg-desk`, which has
  the context pg-connector lacks. The backend still answers the op (rather than `unknown_op`) so a
  deployment that does register it under `attention.sources` gets an empty, successful result
  instead of a failure. Mailbox selection, the optional `mailbox_priority` tag, and `important_people`
  stay available to `list` and `search_messages` and MUST NOT leak into attention. A mail backend
  that later derives attention items MUST do so under a new invariant that supersedes this one.
  - **Mailbox scope and duplicates (mail backend, freedom boundary made explicit).** `list` and
    `search_messages` with no `mailbox` query each CONFIGURED mailbox in order, because the bridge's
    own "no mailbox" does not mean "the configured set". A configured mailbox the bridge reports
    `not_found` for is skipped and MUST NOT fail the call; an explicitly named mailbox the bridge
    reports `not_found` for is passed through as `not_found`. One Message-ID filed under several
    configured mailboxes is merged to one entry, the earliest-configured mailbox winning.

## Agent session attention

> Operator ruling (Phillip, 2026-10-02, bead `pg2-m482k`): "any pg-connector listing which returns
> the same thing as an alert, then we should remove that." Realized for `agentsession` by bead
> `pg2-psftz` in `pg-connector-agentsession-pa-monitor`'s `list_attention`; the rules below bind
> every `agentsession` backend that answers `list_attention`.

- **`INV-AGS-1`** <!-- uuid: 75fc5422-35c3-47bb-870d-1a9528ad2a20 --> — An `agentsession` backend
  MUST report, in its `list_attention` result, one item of `type` `agentsession` per session that
  needs a person: a session whose status is `blocked` MUST be reported (severity `high` for the
  blocker reasons `human_input` and `human_authn`; `medium` for `usage_limit` and for any other
  blocker reason, including `error`), and a session flagged long-idle MUST be reported at `low`. A
  session that is neither blocked nor long-idle MUST NOT be reported. The item `id` is the session
  id, and the item carries no `url` (`INV-ATTN-URL-1`: an agent session has no page).
- **`INV-AGS-2`** <!-- uuid: 3c1f7e0a-9d52-4b8e-a6f4-2e8b5d90c417 --> — An `agentsession` backend
  MUST NOT report the account-level 5-hour block or 7-day week usage-cap being hit as an attention
  item (no `agentsession-usage-limit` type), however the cap state reads. Those facts are already
  raised by Grafana alerts (`pa-monitor-5h-usage-limit-hit`, `pa-monitor-weekly-usage-limit-hit`),
  which reach the same consumer through the `alert` backend's `list_attention` (`INV-ALERT-7`), so
  a second item would show the same fact twice. This is the general rule the operator ruling above
  states: a listing that returns the same thing as an alert is removed. It does not apply to a
  `usage_limit`-blocked SESSION, which is a per-session fact no alert covers (`INV-AGS-1`), and no
  alert covers long-idle sessions either.

## Attention content and connector observability

> Operator ruling (Phillip, 2026-10-02, bead `pg2-m482k`): "attention should be based on
> interesting things about the data. ie, meeting is soon, unread chat, reviewable pr which
> requires my approval", and "any pg-connector listing which returns the same thing as an alert,
> then we should remove that"; connectors "generate [their] own logs/events and register the logs
> and alerts into otel separately", with the common part living in the nix configuration, not in
> pg-connector. Written into this set by bead `pg2-5l0x4.3` (spec
> `docs/superpowers/specs/2026-10-05-pg-desk-attention-evaluator-and-connector-refresh-cache-design.md`,
> section 11), so the rules survive the bead that first recorded them.

- **`INV-ATTN-CONTENT-1`** <!-- uuid: 98d97b59-eec2-41b9-806b-229b5139c42d --> — An attention item MUST be about something
  interesting in the data the source reads (a meeting starting soon, an unread chat, a pull
  request awaiting the operator's review), and MUST NOT be about the health of the tool or the
  connector that produced it (an authentication failure, an outage, an exhausted quota). Tool
  health is reported through the connector's own observability (`INV-CONOBS-1`), not through
  `list_attention`.
- **`INV-CONOBS-1`** <!-- uuid: c2201575-aee2-475a-bcb9-db6bb4f7d67c --> — Each backend MUST be treated as an ordinary custom
  tool: it owns its own log/event path, emits rotating logs, emits metrics where useful, and
  declares its own alert rules.
- **`INV-CONOBS-2`** <!-- uuid: 46d6a22d-7f2a-4782-9f53-8f829861e09e --> — A backend for an external service MUST track the
  service's availability and the health of its authentication, and its quota where the service
  exposes one. This is an assumed capability of every such connector; retrofitting every existing
  connector is NOT required now (see the realization-gap register in the [README](README.md)).
- **`INV-CONOBS-3`** <!-- uuid: e7e93fe0-0bad-4c8c-8ca2-17594f77d49e --> — Registering a connector's observability (its log
  source, its metrics target, its alert rule files) MUST be done per connector in the nix
  configuration, separately from pg-connector's own configuration. What is shared across
  connectors is the nix observability options, NOT pg-connector: the umbrella MUST NOT be bound to
  any observability stack.
- **`INV-CONOBS-4`** <!-- uuid: 667c798d-6b1c-43f4-ad95-fddba5888626 --> — An attention listing that duplicates an alert MUST be
  removed: when an alert already raises a fact, a source's `list_attention` MUST NOT report the
  same fact again. The `alert` backend's own `list_attention` (`INV-ALERT-7`) is the one path by
  which an alert reaches attention. `INV-AGS-2` is this rule applied to agent-session usage caps.

## Review head handling

> `review_submit` puts a finished review into the acting identity's pending review. A review is
> written against one commit of the PR (its **saved-at head**, see the glossary) while the PR
> keeps its own **live head**; the two can differ because the PR moved while the review was being
> written. These rules say which saved-at heads are accepted, where the content is anchored, and
> what follows from saving at a head that is no longer live.

- **`INV-REVHEAD-1`** <!-- uuid: f7a85793-e163-4b9a-87ac-a494cd2b0f54 --> — `review_submit`'s
  `head_sha` MUST be a full 40-character sha, compared case-insensitively; an abbreviated sha MUST
  be `invalid_argument` and MUST NOT be expanded. A `head_sha` equal to the PR's live head MUST
  behave exactly as a request for the live head always did. A `head_sha` that is not the live head
  MUST be accepted only when it is one of the PR's own commits; a sha that is not MUST be
  `invalid_argument`, its message MUST name both the requested sha and the live head, and nothing
  MUST be written. When the PR's commit list could not be read in full and the sha is not in the
  part that was read, the answer MUST be `unavailable` (fail closed), never `invalid_argument`:
  the sha may be among the commits not read.
- **`INV-REVHEAD-2`** <!-- uuid: 17bba3a6-c9ba-4ab7-ad75-6bf5674d6215 --> — A `review_submit`
  whose `head_sha` is not the live head MUST save its content at `head_sha` and MUST NOT anchor
  anything at the live head:
  - each new-point comment's `line` MUST be resolved in the diff the PR showed at `head_sha`
    (`RIGHT` against new-file numbering over context and added lines, `LEFT` against old-file
    numbering over context and removed lines), and the comment MUST be recorded against
    `head_sha` even when the pending review it joins was started at another commit;
  - a comment whose line is not in that diff, or whose file has no diff to read (too large,
    binary, rename without content change), MUST fail by itself as `anchor_rejected` through the
    ordinary per-comment failure reporting, and MUST NOT abort the run or the comments that can be
    anchored; a reply (`thread_id`) is unaffected, because it joins an existing thread;
  - when there is no pending review it MUST be created at `head_sha`, and when a body is written
    its section MUST be the one for `head_sha`, followed by a plain line that states the saved-at
    head and the live head at the time of saving, so an operator submitting the review can tell a
    mix of comments anchored at different commits apart.
- **`INV-REVHEAD-3`** <!-- uuid: 1e9fd060-0f51-4ec2-8cbf-ffbc339d3045 --> — The consequences of
  saving at a head that is no longer live:
  - the result's `head_sha` MUST be the saved-at head, `head_moved` MUST say whether it differs
    from the live head, and `live_head_sha` MUST be the live head read during the run;
    `status` keeps its three values and their meaning (`posted`, `append`, `no_change`);
  - a review so saved MUST count as stale for the live head in `review_pending` (it holds no
    comment at the live head and no section for it), so the live head is still seen as needing its
    own review;
  - a comment's identity for replay MUST be computed from
    the line the request carried, so replaying the identical request is idempotent
    (`already_present`). The converse is a KNOWN LIMIT: a later review at the live head that
    restates the same finding at the shifted line is a different comment and is written again; the
    mitigation is to show the later reviewer the comments already on the PR, not backend logic;
  - a request that saves at an earlier head MUST carry at most 20 comments (more is
    `invalid_argument`, nothing written), a lower bound than the live-head cap, because it needs
    extra reads inside the same 30s exec timeout that bounds a whole call.

## Goal

- **`GOAL-MIN-1`** <!-- uuid: 5cc7f9a5-54a9-4bb9-93a6-09179abc65e8 --> — Keep the umbrella
  **minimal**: anything specific to a backend or an external system belongs behind `INTF-WIRE`,
  realized inside a Tier-2 backend, never in the umbrella. Adding a backend is therefore
  configuration (one `connector.<type>` registry line) and MUST NOT require an umbrella code
  change. `scm`'s own backend manages **local** git state rather than a remote system, which is
  why it is the one capability this set names as "local plumbing" rather than "a remote system
  the umbrella is ignorant of" — but this is a property of what `scm`'s backend happens to talk
  to, not a special case coded into the umbrella: the umbrella dispatches to the `scm` backend
  through the identical `INTF-WIRE` contract as any other capability, and knows nothing about git
  either.
