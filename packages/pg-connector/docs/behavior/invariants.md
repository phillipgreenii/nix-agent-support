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
  `config` MUST NOT be logged with a secret exposed — see `INV-STATE-1`.

## Statelessness

- **`INV-STATE-1`** <!-- uuid: 4d8b2e91-7a3c-4f6d-9e1b-8c5a2d7f3b64 --> — A Tier-2 backend MUST
  resolve any per-call, per-backend policy it needs (e.g. the `list` op's own named-query
  definitions, or `pg-connector-pr-github`'s own GraphQL rate-limit reserve) from the REQUEST's own
  opaque `config` member (`INV-WIRE-3`) alone — NEVER from a file, environment variable, or other
  local state it reads or resolves itself for that purpose. The umbrella is the sole place a
  backend's own config is authored and read from disk; a backend that instead resolved its own
  config file would make the SAME backend binary behave differently depending on which host/process
  invoked it, defeating the umbrella's own single point of configuration. This does not forbid a
  backend from resolving its OWN credentials or connection details independently (e.g.
  `pg-connector-pr-github`'s `gh`-mediated token, `pg-connector-issue-jira`'s `pjira` config file) —
  `INV-STATE-1` is scoped to PER-CALL POLICY the umbrella itself can vary per backend via `config`,
  not to a backend's own ambient identity/connection resolution.
- A backend's own `config.queries` block (the `list` op's own named-query convention) MUST NOT
  define any built-in query name of its own; every name a caller can legitimately supply comes
  from that backend's own registered config, resolved centrally by that capability's dispatch
  table before ever reaching the backend's own `List` implementation (`INV-ERR-3`).

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
  registry MUST be flat and type-keyed at the top level. `issue`, `ci`, and `pr` MUST be
  list-valued (zero or more simultaneously-registered backends); `scm` MUST be single-valued
  (exactly zero or one). Every registry value MUST be a bare backend binary name — there MUST be
  no `exec:`-prefix or other built-in/external distinction, because nothing is compiled into the
  umbrella itself (`GOAL-MIN-1`).
- **`INV-REG-2`** <!-- uuid: 6ea815c2-293c-40ca-93d7-2d8cc1b73b93 --> — A **targeted** op MUST
  resolve to exactly one registered backend for its capability. When a capability's registry
  entry names zero backends, a targeted op against that capability MUST fail as a CLI-level error
  before any wire call is attempted.

  A capability's registry entry naming more than one backend resolves differently depending on
  whether the op is **id-keyed** (`show`, `categorize`, `feedback_set`, `files`, `commits`,
  `comment`, `transition`, `update`, `close`, `deps`, `get_logs`, `rerun_failed`, every `scm`
  targeted verb) or an **id-less write** (`issue create` today, the only member) — a split fixed
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

- **`INV-REG-3`** <!-- uuid: 5adff190-3848-4fdb-b02b-16f4c8f55591 --> — `attention.sources` and
  `search.sources` MUST be flat, top-level, always-list-valued registry keys, independent of
  `connector.<type>` (never nested under it, never sharing its own zero/single/list-valued
  distinction per type). A backend name MAY be registered under one of these AND under a
  `connector.<type>` entry at once, with no cross-check between the two — the same binary
  answering more than one capability, extending `INV-REG-1`'s multi-capability-backend allowance
  to these two keys. Neither key participates in `auth status`'s or `config validate`'s own
  fan-out — both resolve their backend set from `AllBackends` (`connector.<type>` only) — so a
  backend registered ONLY under `attention.sources`/`search.sources` reports its own health
  solely through `attention list`'s/`search`'s own `sources[]` rows, never through `auth
status`/`config validate`.

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
  - **Targeted** (resolves to exactly one backend — `show`, `categorize`, `feedback-set`,
    `create`, `comment`, `transition`, `get_logs`, `rerun-failed`, `worktree add`/`remove`/`list`,
    `branch detect`): `0` the operation completed and produced a well-formed response (including
    a successful write); `4` `not_found` — a well-formed negative answer, MUST NOT share a code
    with an actual failure; `1` any other error (`unauthenticated`/`unavailable`/`unknown_op`/
    `version_mismatch`/`invalid_argument`, or a CLI-level failure before any well-formed response
    was produced at all — no backend registered, an ambiguous multi-backend registration
    (`INV-REG-2`), a bad flag).

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
