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
  method's own convention of not minting a second actor purely for a build-time role.

## Capabilities and entity types

- **Capability** (also **entity type**) — one of `pr`, `issue`, `ci`, `scm` in this set's extent.
  An interface's name and method set MUST correspond to exactly one capability and MUST name no
  backend/system (`INV-CAP-1`).
- **`pr`** — a pull/merge request: identity, review/feedback state, and two dedicated write fields
  (`category`, and each comment/review-thread entry's `disposition`).
- **`issue`** — a tracked issue (Jira/beads/GitHub Issues, …): identity, state, and read+write ops
  (show, create, comment, transition).
- **`ci`** — a build/run linked to a PR: identity, status/conclusion, and read+write ops (list
  runs, get logs, rerun failed).
- **`scm`** — local git state (worktrees, cwd→branch resolution); unlike the other three, it syncs
  no remote entity.
- **Category** — a `pr`-only, single-valued, backend-declared-vocabulary field set via the
  dedicated `categorize` op; never a GitHub label.
- **Disposition** — the closed enum (`open` | `will-fix` | `wont-fix` | `no-action`) a `pr`
  comment/review-thread entry's current review-feedback state is drawn from, and the value the
  dedicated `feedback_set` op writes.

## Registry

- **Registry** — the `connector.<type>` configuration the operator authors, resolved by the
  umbrella into the backend binary name(s) registered for each capability (`INV-REG-1`).
- **List-valued entry** — a capability whose registry entry names zero or more backends (`pr`,
  `issue`, `ci` today); a **fan-out** op queries every one of them.
- **Single-valued entry** — a capability whose registry entry names exactly one backend (`scm`
  today, by design — it has no analogous multi-backend future).
- **Targeted-op resolution** — resolving a targeted op to exactly one registered backend for its
  capability; today this requires the capability's registry entry to name exactly one backend
  (`INV-REG-2`).

## The wire protocol

- **Wire protocol** — the one-shot, exec-per-call, JSON-on-stdin/JSON-on-stdout contract every
  Tier-2 backend speaks to the umbrella (`INTF-WIRE`).
- **Envelope** — the request/response shape every op except `capabilities` uses: request
  `{op, args}`; response `{protocolVersion, schemaVersion, result}` on success or
  `{protocolVersion, schemaVersion, error: {code, message}}` on failure. Exactly one of `result`
  or `error` is present; a response carrying neither is a protocol violation, not a success
  (`INV-WIRE-1`).
- **Op** — one named operation a backend answers (e.g. `show`, `categorize`, `list_runs`,
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

- **Error taxonomy** — the closed six-value set a wire-level failure's `error.code` MUST be drawn
  from: `not_found`, `unauthenticated`, `unavailable`, `unknown_op`, `version_mismatch`,
  `invalid_argument` (`INV-ERR-1`).
- **Sentinel error** — the Go-side counterpart of each taxonomy code (`ErrNotFound`, …), so a
  caller uses `errors.Is` rather than substring-matching a message.
- **`not_found`** — a well-formed request named a specific entity that genuinely doesn't exist; a
  valid negative answer, never a failure (`INV-ERR-2`).
- **`invalid_argument`** — the caller's own request was malformed (an empty required field, an id
  that doesn't even parse into this backend's own id shape); the backend itself is healthy
  (`INV-ERR-2`).
- **`unavailable`** — this backend cannot currently be used; also the default fallback code for an
  error a handler returned without wrapping any of the six sentinels (`INV-ERR-1`).

## Outcome reporting and CLI exit codes

- **pg-connector's own CLI exit code** — the exit code the `pg-connector` process itself returns to
  its own caller; a layer separate from, and never built from, the wire protocol's plain `0`/`1`
  (`INV-EXIT-1`).
- **Targeted op** — an op that resolves to exactly one registered backend by id (e.g. `show`,
  `categorize`, `get_logs`); uses the `0`/`4`/`1` exit-code scheme.
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
