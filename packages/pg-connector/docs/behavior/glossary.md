# Glossary — pg-connector

Vocabulary for pg-connector's own umbrella and wire protocol. A concrete backend's own external
system (GitHub, beads, local git, …) defines its own terms, out of this set's scope.

## Tiers and roles

- **Tier 1 — umbrella** — `pg-connector` itself: the sole user-facing CLI, the sole holder of the
  shared entity-type schemas and the wire protocol, and the registry that resolves a capability to
  its backend(s). A **Facade** over N pluggable backends.
- **Tier 2 — backend** — one thin binary per (entity type, external system) pair, speaking only the
  wire protocol, with no independent CLI identity a human types directly. An **Adapter**
  translating one external system into a capability's generic wire contract, realized as a
  **process-boundary adapter** (a separate OS process, not an in-language object).
- **Tier 3 — consumer layer** — tooling built on top of pg-connector's own verbs (a TUI, a pr-pool
  role, …); out of this set's scope (`## Scope`).
- **Backend implementer** — the role of building a Tier-2 backend against a capability's Provider
  interface and the wire protocol; realized by `ACTOR-OP` acting in that capacity, mirroring the
  method's own convention of not minting a second actor purely for a build-time role. For the two
  cross-cutting capabilities (`attention`, `search`) this role has a second shape: a **standalone
  plugin** — a binary implementing nothing but `attention.Provider`/`search.Provider`, composing
  `pg-connector`'s own other verbs instead of talking to an external system directly (see
  "Cross-cutting capabilities" below).

## Capabilities and entity types

- **Capability** — the general term: a single-purpose Go `Provider` interface plus its own
  wire-op catalog. An interface's name and method set MUST correspond to exactly one capability
  and MUST name no backend/system (`INV-CAP-1`). Six exist in this set's extent: the four
  **entity types** below, plus the two cross-cutting capabilities `attention`/`search` (see
  "Cross-cutting capabilities"), which are capabilities but NOT entity types — neither is tied to
  one kind of external record.
- **Entity type** — a capability tied to one kind of external record: one of `pr`, `issue`, `ci`,
  `scm` in this set's extent. Every entity type is also a capability; `attention`/`search` are the
  two capabilities that are not entity types.
- **`pr`** — a pull/merge request: identity and review/feedback state. Carries no
  category/disposition write fields of its own — bead pg2-2j5ac.28.7 retired the `categorize`/
  `feedback_set` ops (statelessness, `D3`); category/disposition are re-derived by `pg-desk`'s
  interpreter rather than persisted by any backend.
- **`issue`** — a tracked issue (Jira/beads/GitHub Issues, …): identity, state, and read+write ops
  (show, create, comment, transition).
- **`ci`** — a build/run linked to a PR: identity, status/conclusion, and read+write ops (list
  runs, get logs, rerun failed).
- **`scm`** — local git state (worktrees, cwd→branch resolution); unlike the other three, it syncs
  no remote entity.

## Cross-cutting capabilities

- **Attention** — a cross-cutting, fan-out-only capability (no targeted form at all): "everything
  that currently qualifies for attention," aggregated across every backend registered under
  `attention.sources`. Unlike `pr`/`issue`/`ci`/`scm` it is not tied to one entity type — an
  attention item's own `type` field names whatever kind of thing a source reports (a PR, an
  issue, a CI run, …) — and it MAY be implemented either by a capability's own Tier-2 backend
  alongside its normal ops, or by a dedicated standalone plugin implementing nothing else.
- **Attention item** — the attention capability's shared wire shape: `{type, id, summary}` plus
  an optional `severity`. Deliberately stateless — it MUST NOT gain any acknowledge/hide/unhide
  field or method; resolving the underlying condition is how an item stops appearing, entirely
  out-of-band from this capability.
- **Severity** — the attention capability's closed four-value enum (`low` | `medium` | `high` |
  `critical`), each with a canonical rank used only by `attention list`'s own merge/sort
  (`INV-ATTN-1`) — never to default a source's own unopinionated (omitted) severity.
- **Search** — the second cross-cutting, fan-out-only capability: "every result a query matches,"
  reported per-source and never merged across sources (unlike attention's own dedup). Same two
  implementer shapes as attention — a capability's own Tier-2 backend, or a dedicated standalone
  plugin (`INV-SEARCH-1`).
- **Search result** — the search capability's shared wire shape: the core `{type, id, title, url,
source}` plus an optional `attributes` map carrying whatever type-declared or backend-declared
  extension attribute a query's `fields` list requested and the returning backend chose to
  populate. Carries no score/rank/relevance field under any name.
- **`attention.sources` / `search.sources`** — the two top-level, always-list-valued registry
  keys backing these capabilities, siblings of — never nested under — `connector.<type>`
  (`INV-REG-3`). A backend name MAY be registered under one of these AND under a
  `connector.<type>` entry at once (the same binary answering more than one capability); its
  health under one registration is reported independently of the other, and neither key
  participates in `auth status`'s or `config validate`'s own fan-out (`INV-REG-3`).

## Registry

- **Registry** — the `connector.<type>` configuration the operator authors, resolved by the
  umbrella into the backend binary name(s) registered for each capability (`INV-REG-1`).
- **List-valued entry** — a capability whose registry entry names zero or more backends (`pr`,
  `issue`, `ci` today); a **fan-out** op queries every one of them.
- **Single-valued entry** — a capability whose registry entry names exactly one backend (`scm`
  today, by design — it has no analogous multi-backend future).
- **Targeted-op resolution** — resolving a targeted op to exactly one registered backend for its
  capability: the capability's registry entry names exactly one backend, or (with more than one)
  the umbrella resolves directly to the one the operator names via `--backend`, or — for an
  **id-keyed** op only (`show`, `files`, …) — tries each registered backend in registration
  order, stopping at the first non-`not_found` answer. An **id-less write** (`issue create`
  today) has no try-each fallback: with more than one registered backend and no `--backend`, it
  hard-fails as a CLI-level error (`INV-REG-2`).
- **`--backend <binary>`** — the flag every `pr`/`issue`/`ci`/`scm` Tier-1 verb accepts (`attention
list`/`search` accept no such flag — see "Cross-cutting capabilities" above) to resolve directly
  to one named, already-registered backend: on an id-keyed targeted op it skips the try-each
  policy above, on `list` it pins the fan-out to that one backend, and on an id-less write with no
  meaningful fan-out (`issue create`) it resolves an otherwise-ambiguous multi-backend
  registration explicitly (`INV-REG-2`).

## The wire protocol

- **Wire protocol** — the one-shot, exec-per-call, JSON-on-stdin/JSON-on-stdout contract every
  Tier-2 backend speaks to the umbrella (`INTF-WIRE`).
- **Envelope** — the request/response shape every op except `capabilities` uses: request
  `{op, args}`; response `{protocolVersion, schemaVersion, result}` on success or
  `{protocolVersion, schemaVersion, error: {code, message}}` on failure. Exactly one of `result`
  or `error` is present; a response carrying neither is a protocol violation, not a success
  (`INV-WIRE-1`).
- **Op** — one named operation a backend answers (e.g. `show`, `files`, `list_runs`,
  `worktree_add`). A per-capability op catalog is enumerated in `interfaces.md`.
- **`protocolVersion`** — one global integer versioning the envelope shape itself, independent of
  any capability's own schema (`INV-VER-1`).
- **`schemaVersion`** — one integer per schema-bearing capability, versioning that capability's own
  field shape independently of every other capability's (`INV-VER-1`).
- **`capabilities` op** — the capability-discovery op; its response is a bespoke top-level shape
  (`{protocolVersion, schemaVersions, ops, vocabulary}`), not the ordinary envelope, except when it
  fails, which still uses the ordinary error envelope (`INV-WIRE-2`).
- **`auth_status` op** — the optional auth-preflight op a backend answers only if its concrete
  provider implements `AuthChecker` (`INV-AUTH-1`).
- **`vocabulary`** — a per-entity-type, per-backend map of a backend's own accepted values for a
  field this set does not pin to one cross-backend enum (e.g. an issue backend's own transition
  target-state names), declared in that backend's own `capabilities` response.

## Error taxonomy

- **Error taxonomy** — the closed seven-value set a wire-level failure's `error.code` MUST be
  drawn from: `not_found`, `unauthenticated`, `unavailable`, `unknown_op`, `version_mismatch`,
  `invalid_argument`, `query_not_recognized` (`INV-ERR-1`).
- **Sentinel error** — the Go-side counterpart of each taxonomy code (`ErrNotFound`, …), so a
  caller uses `errors.Is` rather than substring-matching a message.
- **`not_found`** — a well-formed request named a specific entity that genuinely doesn't exist; a
  valid negative answer, never a failure (`INV-ERR-2`).
- **`invalid_argument`** — the caller's own request was malformed (an empty required field, an id
  that doesn't even parse into this backend's own id shape); the backend itself is healthy
  (`INV-ERR-2`).
- **`unavailable`** — this backend cannot currently be used; also the default fallback code for an
  error a handler returned without wrapping any of the seven sentinels (`INV-ERR-1`).
- **`query_not_recognized`** — the `list` op's caller-facing query NAME is not one the backend's
  own `config.queries` block defines; the request is well-formed and the backend is healthy
  (`INV-ERR-3`).

## The `list` op and named queries

- **`list`** — the `pr`/`issue`-only op resolving a caller-facing query NAME against the backend's
  own `config` block, returning every matching entity (`entities`), the complete current id set
  (`present_ids`), an always-`null` `cursor`, and a `truncated` flag.
- **Named query** — a `config.queries.<name>` entry: a caller-facing name mapped to one or more
  backend-native query expressions (GitHub search syntax, JQL, a bd argument vector, …), resolved
  centrally by the capability's own dispatch table before the backend's `List` is ever called.
- **`config` block** — the opaque `backends.<binary>` registry entry (`INV-WIRE-3`) the umbrella
  copies verbatim into every wire request sent to that binary; never validated by the umbrella,
  interpreted only by the backend (or a value shared across capabilities, like `queries`).
- **Statelessness** — a backend MUST resolve per-call policy (named queries, a rate-limit reserve,
  …) from the request's own `config` member alone, never from a local file/env it reads itself
  for that purpose (`INV-STATE-1`).

## Outcome reporting and CLI exit codes

- **pg-connector's own CLI exit code** — the exit code the `pg-connector` process itself returns to
  its own caller; a layer separate from, and never built from, the wire protocol's plain `0`/`1`
  (`INV-EXIT-1`).
- **Targeted op** — an op that resolves to exactly one registered backend by id (e.g. `show`,
  `files`, `get_logs`); uses the `0`/`4`/`1` exit-code scheme.
- **Fan-out op** — an op that queries every backend registered for a type/capability at once (e.g.
  `ci list`, `auth status`, `config validate`); uses the `0`/`2`/`3` exit-code scheme, and its
  response carries a `sources[]` outcome row per backend queried (`INV-OUT-1`).
- **`sources[]` row** — one fan-out response row, `{source, status, count, reason}`; `status` is
  `succeeded`, `degraded`, or `disabled` (a well-formed "not applicable," never a failure).
  `count` is that backend's own raw, pre-merge item count.

## Auth and composition

- **`AuthChecker`** — the optional Go sub-interface (`CheckAuth`) a backend's concrete provider MAY
  implement, asserted by a type-check rather than required by the capability's own Provider
  interface (`INV-AUTH-1`).
- **Composition boundary** — the rule that a Tier-2 backend MUST resolve a cross-capability data
  need through its own direct system access, and MUST NOT execute the `pg-connector` umbrella or a
  sibling Tier-2 backend binary to satisfy its own op (`INV-COMP-1`).

## Presentation

- **Output mode** — pg-connector's own CLI presentation mode, `json` (default; the stable
  machine-readable envelope) or `human` (a readable rendering of the same already-decoded result),
  selected by the explicit `--output` flag and validated before any backend is dispatched
  (`INV-OUT-2`). Distinct from, and never a substitute for, the wire protocol's own JSON shape.
