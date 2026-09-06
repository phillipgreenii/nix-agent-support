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

Four connector types for this initial build, each with its own schema: **PR**, **Issue**
(Jira/beads/GitHub Issues — symmetric, same interface, multiple simultaneously-active instances),
**CI** (a build/run, linked to a PR), and **SCM** (local git state — see §4.7; unlike the other
three, it has no remote entity to sync). Person and Repository are not separate connectors — they
are attributes on the above. **Feedback item** (a review comment thread with its own
open/will-fix/wont-fix/no-action disposition) is not a fifth type either — it is a
categorization-style component (§6.1) working from a PR and its comments.

**Thread (Slack) and Note (Notion) are deliberately dropped from this build**, not merely
deferred in the sense of "not started yet": neither has any prior art in this codebase, a sketched
field set, or an identified consumer — they read as speculative placeholders rounding out "six
types" rather than types earning their place. They are tracked as candidate future connector types
in §10, to be reconsidered only once a real consumer needs one; re-adding a type later costs
nothing to the four shipped here.

No connector mirrors entity state into a shared store (§8 explains why) and no connector widens
its own scope to survey more than what's asked for.

**Acceptance criteria**

- The shared schema package defines exactly four connector types (pr, issue, ci, scm); Person and
  Repository appear only as attribute fields, never as top-level schemas. Thread and Note are not
  implemented in this build (tracked in §10).
- Feedback item is implemented as a categorization-style component off PR + comments, carrying an
  open/will-fix/wont-fix/no-action field — not a separate connector type.

## 3. Interface design principle: capability-scoped, not per-system

Interfaces are scoped by _capability_ (PR ops, Issue ops, CI ops, …), never by _system_. A single
interface spanning one backend's PR+CI+Issue operations (the shape ZR's own INTF-ZR-CODEHOST
ended up with) looks unified but actually branches per system internally with no shared shape —
exactly what this avoids. The Issue capability widens from read-only (`GetIssue`) to read+write
(create/comment/transition) so Issue can be a full connector, not just a mirror.

**Acceptance criteria**

- The issue capability interface exposes create/comment/transition write verbs, not just a read.
- A naming/convention check over `pkg/provider` (§5.2) confirms every exported interface's name
  and method set corresponds to exactly one capability (pr/issue/ci/scm/attention/search) and
  names no backend/system (github/jira/slack/…) — the mechanical form of "scoped by capability,
  not by system." **Implemented, not just claimed:** `naming_convention_test.go` (`pg2-nvm80`).

## 4. Tier 1 — pg-connector

The umbrella binary. It owns the four entity-type schemas, the wire protocol, a config-driven
backend registry, and the only user-facing CLI surface: `pg-connector pr/issue/ci/scm ...`, plus
the two cross-cutting capability verbs `pg-connector attention list` and `pg-connector search
<query>` (§4.4). It knows nothing about GitHub, Jira, beads, or Captain's Log — only the schemas
and the protocol. (`scm`'s git backend is local plumbing, not a remote system, so it's the one
exception to "knows nothing about the backend.") Every backend, including the GitHub default, is
a registered entry with no special-cased built-in — this is what makes adding a future backend a
one-binary, one-config-line change.

### 4.1 Config and registry

The registry is flat and type-keyed at the top level:

```
connector.pr      = [pg-connector-pr-github]
connector.issue   = [pg-connector-issue-jira, pg-connector-issue-beads]
connector.ci      = [pg-connector-ci-github-actions]   # pg-connector-ci-zr-captains-log deferred, §10
connector.scm     = pg-connector-scm-git
```

`issue`, `ci`, and `pr` are list-valued (multi-instance); `scm` takes exactly one value — it has
no analogous multi-backend future the way `pr` does with §9's eventual second, interchangeable PR
backend (e.g. Forgejo), so it stays single-valued rather than being made list-valued speculatively.
`pr` is list-valued now even though today it has exactly one entry: making it list-valued later,
once a second backend actually exists, would be a breaking schema change, so the cost of declaring
it list-valued from the start is paid once, now, for free. Every registry value is a bare binary
name — there is no `exec:`-prefix distinction between a builtin and an external provider, since
nothing is compiled in.

Config format, resolution order ($PG_PR_CONFIG → $XDG_CONFIG_HOME → ~/.config), and YAML carry
over unchanged from pg-pr's existing config machinery.

Each backend's own settings (e.g. Captain's Log's `CAPTAINS_LOG_URL` + cloudflared login) live in
that backend's own environment/config, not in this registry — the registry only names the binary.

**Config schema versioning, migration, and validation (resolved here, not left as "carries over
unchanged" implies).** "Carries over unchanged" above is true of the _format_, but the `connector:`
key itself has no version marker of its own — a different thing from §4.3's `protocolVersion`/
`schemaVersion`, which version the wire _envelope_, not the config _file_. Decision: add a
config-schema version field now, the same reasoning `pg2-681xo` already applied to `schema.PR`'s
freshness field — cheap while the shape is still this simple, expensive once real config files
exist in the wild that a later format change would need to migrate. Concretely: a top-level
`configSchemaVersion` integer key, defaulting to `1` when absent (so every config file written
before this decision, including the operator's own, keeps parsing unchanged — no forced migration
for the one install that exists today), checked by `pg-connector config validate`; an unrecognized
value is an explicit validation error, never silently ignored, matching this design's
document-loudly theme elsewhere (§4.2's structured error codes, §4.5's manifest markers). No
migration _tool_ is designed here — there is exactly one config file in the wild and no external
consumer — only the version _field_, so a future format change has somewhere to branch from
instead of needing a `schema.PR`-style retrofit a second time.

Today's registry loader is validation-light by design intent (bare pass-through, no `exec:`-prefix
parsing to get wrong), but that intent doesn't cover gaps a fresh review already found in the
_code_: unknown keys under `connector:` are silently ignored, an empty list/duplicate binary
name/path-separator-containing entry is not rejected, and `config validate`'s fan-out count is not
computed from the real backend list. These are real, already-filed code defects, not a design gap
— `pg2-qnyzz` tracks tightening `registry.go`'s `AllBackends`/`config validate` path to reject
unknown keys, validate the empty-list/duplicate/path-separator cases, and dedupe a backend
registered under multiple types. This design's own position is that config validation SHOULD be
strict (reject the unrecognized rather than silently accept it), consistent with the
`configSchemaVersion` decision above; `pg2-qnyzz` is where that gets implemented.

**Registry file naming — an explicit "don't fix it," not an oversight.** `registry.go`'s own doc
comment already states the deliberate carry-over: the registry reads the SAME
`~/.config/pg-pr/config.yaml` (a directory literally named `pg-pr`) that pg-pr itself reads,
unrenamed, so a host running both tools shares one file. That's fine while pg-pr still exists
(§9's build-test-cutover transition), but nothing shims, renames, or migrates `~/.config/pg-pr/`
to a `pg-connector`-named path once `packages/pg-pr` is finally deleted, and §9.1's removal
criterion says nothing about it. Decision: leave it, for the same reason the `$PG_PR_CONFIG` env
var above is also deliberately not renamed — there is exactly one operator, one machine, one file;
renaming buys nothing a one-line manual edit couldn't do whenever it's actually inconvenient, and
it would just compete for space in an already-long retirement criterion list for no real benefit.
Recorded here so a future reader doesn't reintroduce this as a surprise gap.

### 4.2 Wire protocol

One-shot exec-per-call scriptout: JSON on stdin (`{"op": "...", "args": {...}}`), JSON on stdout
(`{"result": ...}` on success, `{"error": {...}}` on failure), coarse exit codes. The driver loop
and the `auth_status` convention carry over unchanged from pg-pr's existing `pkg/plugin/scriptout`
— they are already fully generic. The error field itself is not carried over as-is: today it is a
bare string; this design widens it to the structured object described below.

The `{"error": ...}` payload becomes a structured object, `{"code": "...", "message": "..."}`,
with `code` drawn from a closed set: `not_found`, `unauthenticated`, `unavailable`, `unknown_op`,
`version_mismatch`, `invalid_argument` — enough for the fan-out layer to classify
degraded-vs-broken without substring-matching. Exit codes at the wire level stay 0/1;
classification lives in the JSON body, matching scriptout's existing "only stdout JSON is the
contract" convention. On the Go consuming side, the wire boundary translates each `code` into one
of a small set of exported sentinel errors in `pkg/scriptout` (`ErrNotFound`, `ErrUnauthenticated`,
`ErrUnavailable`, `ErrUnknownOp`, `ErrVersionMismatch`, `ErrInvalidArgument`), wrapped as
`fmt.Errorf("%w: %s", sentinel, message)` — the same pattern `vcs.ErrAuthInvalid` already
establishes — so callers use `errors.Is` instead of substring-matching the message.

**`invalid_argument` (resolved, not left open — bead `pg2-r9iok`, 2026-09-06):** this section's
earlier draft scoped the closed set as "at least" five codes, deliberately leaving room to extend
it; that room is exercised here to close a real gap found across four Tier-2 backends
(`pg-connector-pr-github`, `pg-connector-ci-github-actions`, `pg-connector-scm-git`,
`pg-connector-issue-beads`). A caller-input-validation failure — an empty required field, an id
that doesn't even parse into this backend's own id shape — was falling through to
`unavailable`, whose own doc comment defines that code as "this backend cannot currently be
used." That is actively misleading: the backend is fine, the _caller's request_ was malformed,
and a health-reporting/alerting consumer of the fan-out layer's `sources[]` (§4.5) has no way to
tell those two situations apart without substring-matching the free-text `message`, exactly what
this structured taxonomy exists to avoid. `invalid_argument` is the sixth code, reserved
specifically for that case — never for "the specific entity doesn't exist" (that stays
`not_found`, whether the "entity" is a PR, a CI run, or a git ref/branch: a caller supplying a
well-formed but nonexistent id is a `not_found` case exactly like today, not `invalid_argument`).
This changes nothing about §4.5's CLI exit-code scheme: a targeted op still exits `1` for
`invalid_argument`, identically to every other non-`not_found` code — §4.5 already named "bad
arguments" explicitly as one of the CLI-level failures folded into that `1`. Only the wire body's
`error.code` (and the Go sentinel a caller can `errors.Is` against) gains the extra precision.

### 4.3 Versioning and capability discovery

Versioning is split into two independent numbers, because they answer two different questions and
coupling them into one was the actual defect in an earlier draft of this section — see the
reasoning below.

- **`protocolVersion`** (one global integer): versions the _wire envelope itself_ — the
  `op`/`args`/`result`/`error` field structure described in §4.2. It changes only when that
  structure changes, which should be rare; it is genuinely universal, so one number for it is
  correct.
- **`schemaVersion`** (one integer per schema-bearing capability — each of the four entity types
  plus the two cross-cutting capabilities, attention and search): versions that capability's own
  field shape.

Every request/response envelope carries `protocolVersion` and `schemaVersion` — for a normal op
(e.g. `pr show`), `schemaVersion` is unambiguous, since every op belongs to exactly one capability.
The one exception is the `capabilities` op itself, which reports on the backend as a whole and so
needs a version per capability it implements:
`{"protocolVersion": N, "schemaVersions": {"<capability>": N, ...}, "ops": [...], "vocabulary":
{...}}` — one `schemaVersions` entry per schema-bearing capability that backend implements (almost
always exactly one entity type for a normal Tier-2 backend; two entries for a backend that also
implements `attention`/`search` per §4.4, or a standalone attention/search-only plugin's single
entry for its own capability). A mismatch on either `protocolVersion` or a `schemaVersion` surfaces
through the error taxonomy as `code: "version_mismatch"` and, at the fan-out layer, as `status:
"degraded"` — identical treatment to an auth failure or a down backend, no special-casing.

**Why split, not one global number (resolved, not left open):** the earlier single-counter draft
coupled every capability's compatibility together — a breaking change to, say, a future Thread
schema (§10) would force every unrelated backend (PR, CI, SCM) to pick up the new constant and
redeploy just to stay "in version sync," even though nothing about their own shape changed. That
bites hardest at §5.3's Captain's Log: an independently-built, cross-repo, CI-only consumer would
have had to rebuild and redeploy on every unrelated schema break. Splitting protocol-envelope
versioning (rare, truly universal) from per-capability schema versioning (changes often,
independently) removes that false coupling while keeping the wire envelope itself — which really
is one shared thing — under a single number. The alternative of per-capability counters with no
separate envelope version was considered and rejected: the envelope structure changes far less
often than any one capability's fields, so treating it as "just another versioned thing" would be
over-fragmented for no benefit.

`vocabulary`'s shape is defined per entity-type schema (e.g. an issue backend's vocabulary lists
its actual transition/state names) rather than universal — this is the real fix for issue backends
being "symmetric" while Jira/beads/GitHub Issues don't share a state vocabulary.

`pg-connector config validate` fans out both `auth_status` and `capabilities` across every
registered backend, reported through the same outcome-reporting envelope as §4.5.

### 4.4 Cross-cutting capabilities: attention and search

> **Implementation status: DEFERRED, not started (bead `pg2-2j5ac.11`, 2026-09-06).** This
> section has zero landed code today — no subcommands, schemas, registry keys, severity enum,
> merge/tiebreak logic, or dependency-direction check exist anywhere in `packages/pg-connector/`.
> Every other section of this design that has landed code got there one of two ways: as a real
> `plan-decompose` phase (§2's Issue/CI/SCM entity types, docket bead `pg2-oih8h`), or through
> ad hoc gap-discovery against code that ALREADY EXISTS (deep-review findings on landed sections,
> each filed as its own bug/task bead and fixed — e.g. `pg2-p2z7o`, `pg2-r9iok`, `pg2-6hrx6`).
> Neither mechanism fits this section: there is no code here to find gaps in, and this section's
> own scope — two new Go capability interfaces, a wire op each, a merge/tiebreak algorithm, a
> severity enum, a dependency-direction CI check, two new CLI verbs, and a naming convention for
> standalone plugins — is comparable in size to a full implementation phase, not something a
> single design-doc-reconciliation bead can responsibly plan in one pass. `pg2-2j5ac`'s own
> epic-decompose round already reached that same conclusion independently: it sketched this exact
> scope as a dedicated "Phase 1," whose adversarial review returned no findings on the boundary,
> then stopped at the operator-approval gate before any phase or trigger bead was created (see
> `pg2-4g0dd`'s round-1 report, 2026-09-04). That sketch is the real decomposition mechanism for
> this section — it was never a missing plan, only an unresumed one, and inventing a second,
> parallel plan here would fork that effort rather than complete it.
>
> **Revisit trigger:** before §7.1's `df-attention`/`df-search` are built — they are pure clients
> of the two verbs this section defines and are non-functional without them — or before any
> Tier-2 backend attempts to implement `list_attention`/`search`, whichever comes first: resume
> `epic-decompose` against `pg2-2j5ac` (or run `plan-decompose` directly against this section) to
> produce the real phase/packet breakdown, rather than re-deriving scope ad hoc at that point.

Two optional, generic capabilities, defined alongside (not as one of) the four entity-type
schemas: `attention.Source` and `search.Source`, each with one op (`list_attention`/`search`) over
the same wire protocol. Attention is a continuous "does this need my eyes" signal; it is
deliberately disjoint from a daily-planning ritual layered on top of it (§7.3 explains why) — a
query returns everything that currently qualifies, full stop, with no snapshot memory and no
cross-awareness of any consuming ritual.

**Attention is stateless by design, with no acknowledge/hide/unhide mechanism anywhere in this
capability, and that is intentional, not a gap.** Attention's whole job is signaling "this needs
you to do something" — resolving that signal means actually doing the thing (closing the bead,
replying to the thread, merging the PR), and once the underlying condition is genuinely resolved,
the source stops reporting it on the next live query. There is no generic "I've acknowledged this
without resolving it" state worth tracking here: how a source clears itself is source-specific and
happens entirely out-of-band (e.g. `bd`'s own label/claim lifecycle for a beads attention source,
or a Slack reply for a thread-based one) — never a boolean this system stores. An item resurfacing
because the condition wasn't actually resolved is correct behavior.

(pg-pr's separate `pr hide`/`unhide` — a personal "don't show me this in my dashboard" suppression
— is a different thing entirely and was never really a property of the PR itself. Its home in this
design is the **client/dashboard layer**: whatever Tier-3 tool renders PR/attention data tracks
that suppression locally. It is not a pg-connector wire concept and does not touch the PR entity's
own state.)

An attention item is exactly `{type, id, summary}` plus optional `severity` (closed enum `low |
medium | high | critical`, canonical rank `low < medium < high < critical`, defined once in the
shared schema package). A source with no opinion omits `severity`; each source maps its own
internal signal into the enum itself (e.g. a PR backend could expose an existing computed urgency
level directly). This shape is what a single SOURCE's own `list_attention` response carries per
item. Tier 1's aggregated `pg-connector attention list` output is a strict superset of it, not a
violation: the merge step below adds `via: [source, ...]` to a merged item, and the response
envelope as a whole adds `truncated`/`total_before_cap` when a cap applies — neither exists on a
single source's own per-item shape.

**Search result shape** (previously unspecified — this was blocking for the capability). Every
search result carries a small core set of attributes, `{type, id, title, url, source}`, defined
once in the shared schema package, plus:

- Each entity **type** MAY declare its own additional attributes as a fixed part of that type's
  own schema in `pkg/schema` (e.g. the PR type's schema might add `branch`; the issue type's might
  add `status`) — available from any backend implementing that type.
- Each backend **implementation** MAY further declare its own additional attributes beyond its
  type's set, the same way §4.3's `vocabulary` is declared: dynamically, in that backend's own
  `capabilities` response (e.g. a specific Jira instance's custom-field values).
- A search query MAY specify which attributes it wants returned (`{"query": "...", "fields":
[...]}`). There is no type-filter parameter on a search query — every call queries every
  registered `search.sources` entry — so "the queried type(s)" is simply the union of every type
  any of those registered sources can return. `pg-connector search` itself — not each backend —
  validates the requested field list against the union of the core set, those types'
  schema-declared attributes, and each queried backend's own capabilities-declared attributes; a
  field matching none of those produces a warning in the response envelope (never a stderr line,
  per §4.5's convention), not an error.
- Each backend implementation, for its part, silently ignores any requested attribute it doesn't
  itself recognize or support — no error, no warning from the backend. The validation-and-warning
  responsibility sits entirely at the aggregation layer described above, not duplicated in every
  backend.

No score field exists on a search result — most backends expose an ordering, not a comparable
magnitude, and forcing one would produce numbers that look comparable across backends while
meaning nothing of the kind.

Two kinds of implementer, registered identically under either capability:

- A Tier-2 connector backend MAY additionally implement `list_attention`/`search` alongside its
  normal entity-type ops in the same binary (e.g. a PR backend flagging PRs needing action using
  its own domain knowledge).
- A standalone executable with no connector duties at all MAY implement just one of these ops, for
  rules that need knowledge no single connector backend has on its own (e.g. a rule spanning a
  PR's linked issue and its review-comment count). **Such a plugin MUST synthesize its judgment by
  composing pg-connector's own capability verbs (`pg-connector pr show`, `pg-connector issue
show`, …) rather than talking to backend systems (GitHub, Jira, …) directly.** Allowing direct
  backend access here would reintroduce, under a different binary shape, exactly the per-system,
  no-shared-interface coupling §3 rejects — a standalone plugin is still bound by §3's principle
  even though it isn't itself a Tier-2 backend. These are named `pg-connector-attention-<backend>`
  / `pg-connector-search-<backend>` — `attention` and `search` are valid values of the same
  `<type>` token used for every Tier-2 binary (§5.1), since they already have real verbs on the
  Tier-1 umbrella.

**This authorization is scoped to those two implementer kinds only, and does not extend to an
ordinary Tier-2 backend needing data owned by a _different_ capability's backend (resolved and
tightened here, not left to per-packet analogy, after a Tier-2 backend was found composing
`pg-connector pr show` for exactly this reason).** A Tier-2 backend sits on the opposite side of
this boundary from a standalone attention/search plugin: it is itself one of the things
`pg-connector`'s own verbs compose, so a Tier-2 backend that shells out to `pg-connector` (or execs
a sibling backend binary directly) to satisfy its own op is not "using the umbrella's verbs" in the
sense this section authorizes — it is calling back into its own caller, producing an undeclared
runtime dependency on `pg-connector` itself (plus, transitively, on whichever backend the registry
currently resolves for that capability) and a needless multi-process chain per call. §5.2's
compiler-enforced backend isolation (independent `internal/` trees per backend, with only
`pkg/schema`/`pkg/provider`/`pkg/scriptout` shared) already forecloses the alternative of importing
a sibling backend's own concrete implementation in-process — that isolation is deliberate, not an
oversight to route around via a subprocess.

A Tier-2 backend needing data that another capability's backend would otherwise supply MUST
instead resolve it using its own direct, already-declared system access — never by exec'ing
`pg-connector` or any other backend binary. This is usually available because a Tier-2 backend's
system dependency commonly spans more than one capability already: `pg-connector-ci-github-actions`
needs a PR's head branch (data the `pr` capability's `show` op also returns) purely to filter `gh
run list`, and it already holds its own GitHub credentials/`gh` gateway for its own ops — resolving
the branch with one more direct `gh pr view` call, alongside its existing `run list`/`run view`/
`run rerun` calls, is no new dependency and no new trust boundary, just one more read against a
system it already talks to. Where a backend genuinely has no independent path to the data it needs
(the data is only obtainable through another capability's own backend-specific credentials or
store), that is a real design gap — it MUST be raised as an open question here, not papered over
with a subprocess shortcut.

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
  with a `via: [source, ...]` list; for a merged item's own `summary`/`severity` fields, the
  most-severe contributing source's values win, and a tie at the same severity is broken by
  `attention.sources` config order (earliest-configured source wins); (2) sort the overall list by
  `severityRank` descending — missing severity is treated as `medium` for sorting only, never
  reported as `medium`; (3) tiebreak by `via.length` descending (an item flagged by two sources
  outranks one flagged by only one); (4) final tiebreak, `attention.sources` config order, then
  each source's own item order. **No cap is applied by default** — a caller MAY pass a cap
  (`pg-connector attention list --cap N`, or `{"cap": N}` in the wire args) to receive at most N
  ranked items after the merge/sort above; when a cap is applied, the response marks `truncated:
true` + `total_before_cap: N` — a truncation is always a manifest marker, never a silent
  omission.
- **Search** stays grouped by source, never interleaved: each source's group is ordered exactly as
  that backend returned it, groups ordered by `search.sources` registration order.

**Acceptance criteria**

- **Implementation of this section is DEFERRED (see the status note above); the mechanical ACs
  below are the specification for whenever work resumes, not a claim that any of them hold
  today.**
- `connector.<type>` is flat and type-keyed at the top level; `issue`/`ci`/`pr` are list-valued,
  `scm` is single-valued; no `exec:` prefix exists anywhere in the registry.
- Every envelope carries `protocolVersion` and `schemaVersion` (per §4.3); `error` is `{code,
message}` from the closed enum; every backend answers `capabilities`; `config validate` fans out
  `auth_status` + `capabilities` across every backend. **This bullet restates §4.1-§4.3's own
  mechanism, not new scope this section's own DEFERRED status covers — unlike every other bullet
  in this list, it is independently true today, not waiting on this section's attention/search
  work landing:** `protocolVersion`/`schemaVersion` comparison and the `version_mismatch` outcome
  are implemented end-to-end, including `config validate` actually consuming the capabilities
  payload it used to discard (`pg2-p2z7o`); every one of the four shipped backends answers
  `capabilities` via a dispatch-table-derived handler, not a hand-typed op list
  (`pg2-fh2vh`).
- `pg-connector attention list` / `pg-connector search <query>` exist as real Tier-1 verbs.
- A single source's own `list_attention` response carries an item that is exactly `{type, id,
summary}` + optional `severity`; Tier 1's aggregated `attention list` output is a strict
  superset (adds `via`, and `truncated`/`total_before_cap` at the envelope level), never a
  violation of that per-source shape. A search result carries the core `{type, id, title, url,
source}` set plus any type-/implementation-declared extensions the query requested; it carries
  no score field.
- A search query MAY request specific fields; an unrecognized field produces a warning in the
  response envelope (never stderr), never an error; a backend silently omits fields it doesn't
  recognize.
- Attention output is deduped/ranked per the algorithm above, with no cap unless the caller passes
  one; search output is grouped by source, never interleaved.
- Standalone attention/search-only plugins are named `pg-connector-attention-<backend>` /
  `pg-connector-search-<backend>` and compose pg-connector's own verbs internally — never a direct
  backend-system client of their own.
- A Tier-2 backend never execs `pg-connector` or another Tier-2 backend binary to satisfy its own
  op; a cross-capability data need is resolved via that backend's own direct system access instead
  (composition-boundary text above) — a mechanical grep for `exec.Command`/`os/exec` naming
  `pg-connector` or another `pg-connector-<type>-<backend>` binary outside a backend's own tests
  would catch a regression. **Implemented, not just claimed** (and independent of this section's
  own deferred attention/search work — this check already guards the four existing backends
  today): `pg2-nvm80`, a module-wide regression guard for an earlier violation `pg2-0vwcc` fixed.
- A beads attention source, if built, performs no claim/ack/exit mutation.
- Attention aggregation is stateless: `pkg/schema`'s `attention.Source` shape and its
  implementations have zero imports of, or references to, any daily-focus/df-survey package or
  type — enforced by a dependency-direction check. It has no acknowledge/hide/unhide mechanism; an
  item resurfaces exactly as long as its underlying condition is genuinely unresolved.

### 4.5 Outcome reporting and error taxonomy

Every multi-source response carries a top-level `sources` array, one row per source actually
queried, regardless of merge strategy: `{"source": <name>, "status": "succeeded" | "degraded" |
"disabled", "count": N, "reason": <string|null>}`. This applies uniformly to every fan-out in this
design — `attention.sources`, `search.sources`, `connector.issue`, `connector.ci`,
`connector.pr` — and is never collapsed into one pass/fail signal. Outcome rows live in the JSON
body, never as a stderr `WARNING:` line. For a fan-out whose merge stage dedups across sources
(attention's `{type, id}` dedup, §4.4), each source's own `count` is that source's raw, pre-merge
item count exactly as it returned it — independent of how many of those items survive
deduplication in the merged output. A single logical item counted by two sources appears in both
rows' counts; that's expected, since `count` measures per-source health, not final output
cardinality.

**These are pg-connector's own CLI exit codes — a different layer entirely from §4.2's
per-backend wire-level exec exit codes**, which stay a plain 0/1 with classification living in the
JSON body a Tier-2 backend author writes. A Tier-2 backend's own process exit code MUST NOT be
built against the scheme below; that scheme belongs solely to the `pg-connector` binary's own
response to whoever invoked it.

Exit codes distinguish outcomes, not just pass/fail, and split into two schemes depending on
whether the invoked op is a FAN-OUT (queries every registered source of a type/capability:
`attention list`, `search`, or a list-type op against a list-valued connector type) or a TARGETED
op by a specific id, resolving to exactly one backend (e.g. `show`, `categorize`,
`feedback_set`, `transition`):

- **Fan-out ops** — this both fixes the earlier "exit 0 on partial failure" scripting trap
  (automation gating on exit status alone could not previously tell that some backends were down)
  and satisfies this workspace's own convention that a branchable meaning uses a distinct exit
  code rather than overloading 0/1:
  - **`0`** — every queried source succeeded; no degraded/disabled rows in `sources[]`.
  - **`2`** — degraded/partial: at least one source succeeded and at least one did not.
  - **`3`** — total failure: every source failed or was disabled; zero usable results.
- **Targeted ops** — a well-formed negative answer is not a system failure, so it gets its own
  code rather than sharing the fan-out scheme's failure codes:
  - **`0`** — the operation completed and produced a well-formed response (including a
    successful write).
  - **`4`** — `not_found`: the operation completed correctly; the specific entity genuinely
    doesn't exist. A healthy backend giving a definitive negative answer MUST NOT share a code
    with an actual failure.
  - **`1`** — any other error
    (`unauthenticated`/`unavailable`/`unknown_op`/`version_mismatch`/`invalid_argument`, or a
    CLI-level failure before a well-formed response was produced at all: bad arguments, an
    unreachable/non-executable backend, a panic). `invalid_argument` (§4.2) exits `1` here
    identically to every other code in this bucket — it sharpens the wire body's `error.code` for
    a caller-input-validation failure, not the CLI exit-code scheme, which already folded "bad
    arguments" into `1` before that code existed.
- **`1`** is otherwise deliberately reserved and never emitted for a fan-out outcome — it stays
  available for the CLI's own generic/unexpected-failure path, matching the common convention
  that 1 is the default, catch-all failure code so many tools already assume.

An automation author who only checks `$?` for zero-vs-nonzero still gets a correct pass/fail
signal from either scheme; one who wants finer detail (which sources are down, or whether a
targeted miss was a real "not found" versus a break) now can, without parsing `sources[]`/the
error body — though parsing it remains necessary to know _which_ sources or _why_.

Each list-valued type/operation states its own merge strategy explicitly (concat-with-per-source-
outcome, or first-wins) rather than assuming one universal rule — the existing CI fan-out already
uses both (`runs` concatenates, `logs` tries each until one succeeds), and pg-connector's schema
makes that choice explicit per type/op rather than implicit.

**Acceptance criteria**

- Every multi-source response carries a `sources` array with one row per source queried, never
  collapsed; a source's `count` is its own raw pre-merge count, unaffected by later dedup.
- Fan-out exit code is `0` (all succeeded), `2` (degraded/partial), or `3` (total failure).
  Targeted-op exit code is `0` (success) or `4` (`not_found` — a well-formed negative answer, not
  a failure); `1` is never emitted by either scheme for an in-taxonomy outcome and is reserved for
  a generic/unexpected CLI failure.
- No degraded/failure signal is emitted as a stderr `WARNING:` line.
- Every list-valued type/operation states its merge strategy explicitly.
- pg-connector's own CLI exit codes are never confused with, or built from, §4.2's per-backend
  wire-level exec exit codes.

### 4.6 Credentials

pg-connector defines no credential mechanism of its own — consistent with "knows nothing about
the backend." Each Tier-2 backend resolves its own credentials, exactly as today's three existing
backends already do independently and differently (GitHub's env-then-`gh auth token` chain;
Jira's env vars or a keychain-backed CLI; Captain's Log's Cloudflare Access JWT). There is no
shared resolution order to build and no shared library; a future backend picks whatever chain fits
its own token model when it's actually implemented (see §10 for Thread/Note, dropped for this
build but tracked as future candidates). Nix/home-manager secret delivery is likewise each
backend's own concern, decided at that backend's implementation time.

`AuthChecker` (in `pkg/provider`, §5.2) is an **optional** sub-interface, following the same
small-separately-asserted-interface pattern `vcs.Provider` already uses today — a backend asserts
it via a type-check, not by implementing every method of one monolithic interface. `scm`'s git
backend is the concrete case that needs this: it has no remote credentials concept at all (§4.7 is
local plumbing), so it simply doesn't implement `AuthChecker`. `pg-connector auth status`'s
fan-out reports such a backend's row as `disabled` with a reason of "not applicable," rather than
forcing a meaningless answer out of it.

pg-connector's only involvement: fan out the existing `auth_status` op via `pg-connector auth
status`, reusing the outcome-reporting envelope from §4.5 verbatim.

**Acceptance criteria**

- pg-connector ships no shared credential-resolution library or convention of its own.
- `AuthChecker` is an optional sub-interface in `pkg/provider`; `pg-connector-scm-git` does not
  implement it, and `auth status`'s fan-out reports it as `disabled`/not-applicable rather than
  erroring.
- `pg-connector auth status` fans out the existing `auth_status` op across every registered
  backend via the sources envelope.

### 4.7 SCM — local-only, no remote entity

Unlike the other three entity types (pr/issue/ci), which sync remote state, `scm` manages local
git state — worktrees
and cwd→branch resolution — and has no "sync" concept. It is registered, versioned, and dispatched
through the exact same mechanism as every other type (config-driven registry, scriptout protocol,
`schemaVersion`/`capabilities`), just backed by local git commands: `connector.scm =
pg-connector-scm-git`, single-instance.

It is deliberately generic, not PR-aware: `pg-connector scm worktree add <branch-or-ref>` takes a
branch or ref, never a PR number. A caller that wants "check out PR #482 for review" composes two
calls — `pg-connector pr show 482` to resolve the branch, then `pg-connector scm worktree add
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
  are deferred by design, not an oversight). **This AC is self-satisfied by this section's own
  text** — there is no code to check it against, so "done" and "documented" are the same fact
  here.

### 4.9 Security model: exec-time backend resolution

Every registered backend is resolved and invoked the same way, and that mechanism is this design's
whole security posture for Tier-2 dispatch — there is no sandboxing, allowlisting, or signature
check anywhere in the call path beyond it. Stated explicitly, verified against the current code
(`cmd/pg-connector/registry.go`, `pkg/scriptout/exec.go`):

- **Resolution is bare-name, at exec time, via the OS.** A registry entry with no path separator
  (the normal case, e.g. `pg-connector-pr-github`) is resolved by `$PATH` lookup the moment it is
  execed — not validated, canonicalized, or cached at config-load time. A value containing a path
  separator resolves relative to `pg-connector`'s own current working directory at call time, not
  any fixed or config-relative root — Go's `os/exec` contract, not a choice made here.
- **`exec:` is not a prefix pg-connector recognizes.** Unlike pg-pr's own historical `exec:`
  selector, pg-connector performs no prefix parsing on a registry value at all — a string like
  `exec:something` passes through unchanged as a literal binary name
  (`registry_test.go`'s `TestRegistry_NoExecPrefixDistinction` locks this in) and would simply be
  looked up on `$PATH` under that literal, almost certainly nonexistent, name.
- **The full environment is inherited.** `exec.go`'s `runInvoke` never sets `cmd.Env`, so per Go's
  own `os/exec` contract the child process receives the entirety of `pg-connector`'s own
  environment — every credential a Tier-2 backend resolves via env (§4.6's GitHub token chain
  included) is reachable by construction, and so is anything else present that a backend has no
  business seeing.
- **The caller's privileges are the backend's privileges.** A backend execed this way runs under
  whatever process invoked `pg-connector` — in the pr-pool-driven case (§6.1), pr-pool's own
  subagent permissions, already ruled uniform-to-parent with no per-role restriction. There is no
  intermediate boundary anywhere between a config-file entry and a process holding live GitHub
  credentials.

**Accepted risk, not hardened — with justification.** This is a real widening of what a malformed
or malicious registry entry could do (an arbitrary `$PATH`-resolved or cwd-relative binary, with
every currently-in-scope credential, under the caller's own permissions), but this design accepts
it rather than proposing a mitigation, for the same reason §9 gives for skipping a coexistence
architecture: **Phillip is currently the sole author of every config file `pg-connector` reads**,
on machines only he administers. The registry lives in the same single-user
`~/.config/pg-pr/config.yaml` pg-pr's own config already reads unchanged (§4.1) — this is not a
new externally-reachable surface, it is the same "who can edit this machine's own config" boundary
pg-pr's and pr-pool's existing subprocess-exec call sites already draw, continued under a new
binary name. Nothing in this design's stated scope is multi-tenant, network-facing, or
third-party-config-accepting for that boundary to matter against. If that scope ever changes — a
second operator, a shared/remote config, a plugin registry accepting third-party binary names —
this acceptance MUST be revisited; nothing here should be read as "safe in general," only
"accepted for this build's actual, single-operator threat model." `pg2-qnyzz` (registry validation
tightening — rejecting unknown keys, empty/duplicate/malformed entries) narrows the
accidental-misconfiguration case as a side effect, but it is not a security fix and isn't cited
here as one.

**Acceptance criteria**

- This section states the security model explicitly (bare-name PATH/cwd-relative resolution, no
  `exec:` handling, full env inheritance, caller-privilege inheritance) rather than leaving it
  unstated.
- The risk is either mitigated by a concrete hardening design or explicitly accepted with stated
  justification — this section accepts it, scoped to the single-operator threat model described
  above.

### 4.10 Concurrency

pg-connector's own process is one-shot and stateless per invocation, matching its wire protocol's
own exec-per-call model (§4.2) — there is no persistent daemon holding cross-call state, so there
is no pg-connector-level lock or mutex to design. Two concerns this doesn't erase, both pushed to
whoever actually owns the state:

- **A Tier-2 backend's own local store** (feedback disposition, category — §6.1/§8) can be hit by
  two concurrent `pg-connector` invocations (e.g. two `df-feedback` runs racing on the same PR).
  Concurrency safety for that store is that backend's own responsibility, exactly as it already was
  for pg-pr's existing SQLite store — this design adds no new requirement here, it inherits the
  existing one unchanged.
- **A fan-out op** (§4.4/§4.5) execs one process per registered source for a single logical call.
  Whether those execs run sequentially or concurrently is left as an implementation freedom — the
  `sources[]` outcome shape (§4.5) is identical either way, so nothing here depends on the answer.

No cross-backend locking or coordination is needed anywhere in this design, because backends never
share mutable state in the first place (§8's rejected-shared-store decision).

### 4.11 Timeouts and retries

pg-connector's own dispatch path (`Dispatch`/`scriptout.Invoke`) enforces no timeout of its own
beyond whatever `context.Context` its caller supplies — there is no default deadline anywhere in
`pkg/scriptout`. Timeout policy is entirely the caller's concern: for the pr-pool-driven case
(§6.1), that's pr-pool's own executor, out of scope here.

Retries follow the same pattern. pg-connector performs no retry of a failed backend exec on its
own — one exec is one attempt, once, full stop. The only retry mechanism anywhere in this design is
pr-pool's own exit-`9`/`busy` pre-accept decline (§6.1's exit-code analysis), and that belongs to
pr-pool, not to pg-connector or either handler. A Tier-2 backend's own retry behavior against its
upstream system (e.g. backing off a `gh` rate limit) is invisible at the wire protocol level either
way — the wire only ever sees that backend's final answer, never a mid-flight retry in progress.

### 4.12 Observability and correlation

No correlation/trace-id concept exists anywhere in this design's wire envelope (§4.2's envelope
carries only `op`/`args`/`result`/`error` plus the two version fields) — a multi-hop call
(pr-pool → a handler → `pg-connector` → a Tier-2 backend → its upstream system) has no identifier
threading those hops together across process boundaries. This is an accepted gap for this build,
not a considered-and-rejected one: today's fan-outs are exactly one hop deep (pg-connector directly
to a Tier-2 backend), and pr-pool already logs its own role/item identifiers independently on its
side of the boundary, so nothing in the current scope actually needs cross-process correlation
yet. If a future consumer needs to tie a handler's own logs to the specific pg-connector/backend
calls it made, the natural mechanism — an opaque request id, or reusing pr-pool's own event/item id
as an extra field on the request envelope — is not designed here and stays an open question until
that need materializes.

### 4.13 Multi-instance targeted-op resolution

§2 describes Issue as symmetric with "multiple simultaneously-active instances," and §4.1
registers `connector.issue`/`connector.ci`/`connector.pr` as list-valued specifically so a second
backend can be added later — but a _targeted_ op (`show <id>`, `categorize`, `feedback_set`, …)
resolves to exactly one backend, and nothing before this section says how that resolution works
once more than one is actually registered. Today's code (`cmd/pg-connector/dispatch.go`'s
`Dispatch`) simply refuses: it errors out if more than one backend is registered for the type a
targeted op addresses, deferring the question rather than answering it.

**Resolution policy (decided here, not deferred further):** for a targeted op against a
list-valued type with N > 1 registered backends, `Dispatch` tries each registered backend **in
registration order**, stopping at the first one whose answer is not `not_found`. A `not_found`
answer means "try the next backend" (mirroring §4.5's existing first-wins merge-strategy precedent,
already used by the CI capability's own `logs` op — "tries each until one succeeds"); any other
error (`unauthenticated`, `unavailable`, `unknown_op`, `version_mismatch`, `invalid_argument`, or a
CLI-level failure) short-circuits immediately and is returned as-is, **never** swallowed to fall
through to the next backend — a real auth/availability failure on the first-tried backend must
never be silently misreported as a `not_found` just because a second backend also lacks the id. If
every registered backend answers `not_found`, the aggregate targeted-op result is `not_found`. This
keeps id-shape disambiguation entirely out of pg-connector's own logic (a Jira key and a beads id
never collide in practice, so trying both cheaply is safe) rather than requiring a caller to
pre-select a source — consistent with §2's "symmetric, same interface" framing: a caller of
`pg-connector issue show <id>` shouldn't need to know which backend actually holds that id.

**Acceptance criteria**

- A targeted op against a list-valued type with more than one registered backend tries each in
  registration order, stopping at the first non-`not_found` answer; any non-`not_found` error
  short-circuits without trying the remaining backends; an all-`not_found` result is the aggregate
  `not_found` answer.
- This is stated as a resolution policy, not left as the "future concern" the current code comment
  defers it as.

### 4.14 Reentrancy and layering

§4.4 already forbids a Tier-2 backend or a standalone attention/search plugin from execing
`pg-connector` or a sibling Tier-2 binary, scoped to that section's own two capabilities. That rule
generalizes to the whole architecture, not just attention/search: call direction is strictly
downward — Tier 3 → Tier 1 (`pg-connector`) → exactly one Tier-2 backend → that backend's own
upstream system — and nothing calls back up a level (a backend execing `pg-connector`) or sideways
(one backend execing another, or a standalone plugin execing a specific backend binary directly
instead of composing `pg-connector`'s own verbs). §4.4's mechanical grep-based regression guard
(scoped today to attention/search's own composition boundary) is the concrete precedent for
enforcing this; widening it to cover every capability, not just those two, is straightforward but
not itself specified here.

Because the call graph never loops back on itself, there is no true reentrancy anywhere in this
design either — `pg-connector`'s own process is always a leaf exec once invoked; no path in this
architecture ever needs it to shell out to a live copy of itself.

**Acceptance criteria**

- The downward-only call-direction rule is stated as a general architectural principle, not scoped
  to attention/search alone.
- No component in this design ever execs a live copy of its own binary or calls back up a tier.

## 5. Tier 2 — backend implementation binaries

One thin binary per (type, backend) pair, speaking only the scriptout protocol, with no
independent CLI identity a human types directly.

### 5.1 Naming convention

Every Tier-2/plugin binary matches `pg-connector-<type>-<backend>`, where `<type>` is always
exactly the singular capability verb — `pr`, `issue`, `ci`, `scm`, `attention`, or `search` —
drawn directly from the verb, not chosen per binary. (`thread`/`note` would follow the identical
rule if and when they're built — see §10.) This also fixes a plural/singular mismatch that existed
in earlier naming sketches, since the type token is now mechanically derived rather than picked
freely.

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
    schema/       <- public: shared JSON wire shapes only (per-type + attention/search + core search fields)
    provider/     <- public: the per-capability Go interfaces (pr.Provider, issue.Provider, ...),
                       including optional sub-interfaces like AuthChecker (§4.6)
    scriptout/    <- public: the wire protocol (envelope, protocolVersion/schemaVersion,
                       capabilities/auth_status, and the exported sentinel errors mapped
                       from the error taxonomy's closed code set, §4.2)
  cmd/
    pg-connector/                       <- umbrella; imports pkg/schema + pkg/provider + pkg/scriptout only
    pg-connector-pr-github/internal/    <- backend-private; importable by nothing outside this dir
    pg-connector-issue-jira/internal/
    pg-connector-issue-beads/internal/
    pg-connector-ci-github-actions/internal/
    pg-connector-scm-git/internal/
```

`pkg/schema` and `pkg/provider` are separate packages rather than one combined package, restoring
this repo's existing split (today, JSON shapes and the three provider interfaces already live
apart) instead of introducing a new coupling: a combined package would become a mandatory import
for every binary plus any external consumer (§5.3) purely for its Go-interface half even when a
consumer only needs wire shapes, and it would sit in tension with §3's "scoped by capability, not
by system" principle applied at the package level.

Cross-backend isolation is compiler-enforced: Go's `internal/` visibility rule is evaluated per
import-path text, so nesting an independent `internal/` under each backend's `cmd/` directory
creates N independent, hard-enforced visibility boundaries. Only `pkg/schema`+`pkg/provider`+
`pkg/scriptout` are importable by everyone, because that is the interface. One residual gap the
compiler doesn't close: nothing stops a backend from exporting a stray non-internal package another
backend could import — closing that needs one cheap CI/convention check (every backend's own code
lives in `main` or under its own `internal/`), a backstop, not the main enforcement mechanism.

Nix: N `mkGoApp`/`mkGoBinary` calls sharing `src` + `gomod2nixToml`, differing only in
`subPackages`/`pname` — this repo's existing gomod2nix convention, with real precedent for the
multi-binary-per-module shape one hop away (this workspace's `pn`/`pn-workspace-toml-enforce`,
built from identical shared `src`). **Five nix outputs exist today** (verified against
`flake.nix`'s `perSystem.packages`/its `inherit (pkgs) ...` re-export block): the umbrella
`pg-connector` plus four backend binaries — `pg-connector-pr-github`,
`pg-connector-ci-github-actions`, `pg-connector-issue-beads`, `pg-connector-scm-git`. The layout
diagram above's sixth `cmd/` entry, `pg-connector-issue-jira/internal/`, is aspirational: no such
directory exists under `packages/pg-connector/cmd/` and no corresponding nix output exists either,
and no phase or bead in this design's tracked scope currently adds it — it stays a gap until one
does. (Thread/Note stay dropped per §2/§10 regardless, so they were never part of either count.)
Each existing/planned nix output needs a single-entry `subPackages` list. Known, already-accepted
cost: shared `src` means editing any one backend's `internal/` code bumps the
content-digest-versioned nix rebuild of every one of these binaries — acceptable given there's no
independent release-cadence requirement.

**Distribution (not yet designed here).** This section fixes how the binaries are _built_, not
how they reach a machine: there is no home-manager module and no stated co-installation invariant
(nothing here guarantees the umbrella and its registered backends land together). ZR's
`home/ziprecruiter/packages/default.nix` hand-lists all five `pkgs.pg-connector*` binaries today
against an untyped `connector:` YAML key, with its own comment noting no
`phillipgreenii.programs.pg-connector` option exists yet. That gap is tracked from the code side by
bead `pg2-j3w6i` (add the `phillipgreenii.programs.pg-connector` home-manager module, mirroring
`home/programs/pg-pr`'s existing pattern) with `pg2-bvtiq` as its dependent ZR-side consumer switch
(drop the hand-listing once the module exists) — this design doesn't restate that work, only
cross-references it so the gap isn't silently dropped between the two documents.

### 5.3 Captain's Log (existing ZR backend)

**Current state, today.** Captain's Log's cross-repo access needs a module-level `replace` (Go's
`replace` operates at module granularity) pulling in the source of an agent-support module from
ZR's side — today that module is still `packages/pg-pr`, via the existing `pg-pr-src` flake output;
no `pg-connector-src` flake output exists yet. `modules/pg-pr-zr/go.mod` still carries
`replace github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr => ./pg-pr-src`
unchanged. The actual consumer, `modules/pg-pr-zr/cmd/pg-pr-cicd-captains-log/main.go`, imports
three `packages/pg-pr` packages — `pkg/api` (wire types), `pkg/plugin/scriptout` (wire protocol),
and `pkg/provider/cicd` (the CI-provider Go interface) — and uses exactly one type out of
`pkg/api`, `api.CIRun`.

`api.CIRun` and pg-connector's own `schema.CIRun` (§5.2's `pkg/schema`) are **not** drop-in
compatible: per `packages/pg-connector/pkg/schema/ci.go`'s own header comment, `schema.CIRun` adds
a `PRID` field `api.CIRun` has no equivalent for, and deliberately drops `api.CIRun`'s
`Description` field. **No phase or section in this design covers closing that type gap** — §10.1
covers only the binary rename and `connector.ci` registration, and defers both; the
type-translation work this migration additionally needs is named nowhere else.

**The migration, once undertaken:** rename the import paths so the consumer imports pg-connector's
own `pkg/schema` (wire shapes), `pkg/scriptout` (wire protocol), and `pkg/provider`'s CI-capability
Go interface instead — the three-way split described in §5.2 — add a `pg-connector-src` flake
output (mirroring today's `pg-pr-src`), repoint ZR's `go.mod replace`/`build.nix` at it, and adapt
every `api.CIRun` construction/consumption site to `schema.CIRun`'s field set (dropping
`Description`, supplying `PRID`). This is a real migration step, bounded and doable, not a
mechanical no-op — and, per the type-gap paragraph above, not yet scheduled into any phase this
design names.

**Acceptance criteria**

- Every Tier-2/plugin binary matches `pg-connector-<type>-<backend>`, `<type>` drawn from the
  capability verb (including `attention`/`search`); ZR-specificity is consistently encoded in
  `<backend>`, including a renamed Captain's Log binary.
- One Go module builds all binaries via N `mkGoApp` calls sharing `src`+`gomod2nixToml`; only
  `pkg/schema`+`pkg/provider`+`pkg/scriptout` cross backend boundaries, backstopped by a
  convention check.
- A `pg-connector-src` flake output exists and ZR's `go.mod replace`/`build.nix` are repointed at
  it; Captain's Log still builds and runs, translated onto `schema.CIRun`, through the transition.

## 6. Relationship to pr-pool

pr-pool's core (its durable ordered event queue, query-source/agent-runner contracts, domain-
agnostic per its own design) is not modified by anything new in this design. Roles and query-
sources are wired entirely via config, pointing at external backing commands — never compiled in.
pr-pool already supports any listener adhering to its existing Listener/handler contract, LLM-
backed or fully deterministic, without a core change, provided it's a new role of an _existing_
kind (its two current executor kinds are selected by a `role.Type` field) — which covers the two
new deterministic handlers below.

### 6.1 df-categorize and df-feedback — new pr-pool roles

> Rewritten end-to-end (bead `pg2-2j5ac.12`, 2026-09-06) against pr-pool's REAL command-role
> contract. The prior draft of this section claimed pr-pool's command-role exit contract was
> "`0` on success, any nonzero on failure" and that "pr-pool already retries a failed command-role
> call on its own" — both false. See "Exit codes" below for the real, four-signal contract and
> citations. This revision also fixes df-categorize's argv (its `<type>` field could never be
> populated — see the invocation paragraph) and resolves the `pg-connector pr open` rationale
> against Appendix B (see the categorization bullet).

Two new, deterministic ("command"-kind) pr-pool roles, bound by event type, requiring zero pr-pool
core changes beyond registering them in ZR's own config. pr-pool's command-role executor is
argv-only — it renders `Role.Command.Argv` through a Go template against scalar event fields
(`Item.ID`, `Item.Type`, …) and execs the result; it has no stdin-payload path at all, and adding
one would itself be a pr-pool core change, which is exactly what these two roles are designed to
avoid. **Neither handler's `type` argument is templated from an event field — both bake it in as a
literal, per-role argv constant instead**, matching df-feedback's own pattern below (there is no
existing pr-pool config or code precedent for this argv shape today — confirmed by inspection of
`packages/pr-pool`'s executor and ZR's current `modules/zm` config, which has zero command-kind
roles yet, only command-kind query/source blocks and three unrelated `ccpool`/LLM-backed roles).
Once invoked, both handlers turn around and speak pg-connector's own stdin-JSON scriptout protocol
_outbound_ — a completely separate, unrelated boundary from how pr-pool dispatched them, and the
one where JSON-over-stdin is actually pg-connector's convention (§4.2).

**Why `type` cannot be templated.** pr-pool's argv template has exactly one scalar field that
could plausibly carry a connector type, `{{.Item.Type}}`, and it never carries one for either
handler's use case: `packages/pr-pool/internal/item/item.go:9-14` defines `Item.Type` as whatever
the triggering _query_ stamps on it, and every one of ZR's three live query sources
(`modules/zm/default.nix:123,149`, the `feedback-source`/`worker-source`/`review-source` blocks)
maps it from a bd issue's own `issue_type`, filtered to the literal string `"task"` — a
beads-domain concept, structurally unrelated to a pg-connector connector type (`pr`/`issue`/`ci`)
and incapable of ever equaling one. Nor is there another field to fall back on: neither
`discover.DispatchContext` (`packages/pr-pool/internal/discover/discover.go:35-38`, carrying only
`Role` and `Item`) nor the argv-rendering `prompt.Context`
(`packages/pr-pool/internal/prompt/prompt.go:61-66`, carrying only `Item`/`WorktreeDir`/
`SelfLogin`/`RepoRoot`) exposes the _triggering event's own_ binding-level `type` (the field a
role's `Binds` matches on, e.g. `feedback.ready`) to templating at all — that field is discarded
once the dispatch context is derived from the event. So no templated argv field, today, can ever
resolve to a real connector type for either handler.

- **df-categorize** is registered as **one pr-pool role per connector type** —
  `df-categorize-pr`, `df-categorize-issue`, `df-categorize-ci` — each with its own single-type
  `Binds` entry (a role's `Binds` may name several event types and respond to any of them,
  `packages/pr-pool/internal/roles/roles.go:44-56`, but a role has exactly one `Command.Argv`
  shared across all of them, so one shared role bound to all three connector event types could
  never carry a per-event `type` either way) and its own `Command.Argv` with `type` as a literal
  string, e.g. `argv: ["df-categorize", "pr", "{{.Item.ID}}"]`,
  `["df-categorize", "issue", "{{.Item.ID}}"]`, `["df-categorize", "ci", "{{.Item.ID}}"]`. This
  needs zero pr-pool core change (three near-identical role stanzas in ZR's config instead of one)
  and mirrors df-feedback's own argv below, which already hardcodes `pr` as a literal rather than
  templating a type and was never affected by this bug. Each role's backing script, invoked for a
  PR/CI/Issue event as `df-categorize <type> <id>` with `<type>` fixed per role, fetches
  current state via `pg-connector <type> show <id>` (wire request `{"op": "show", "args": {"id":
"<id>"}}`, response `{"result": {...entity fields per that type's schema...}}`), applies its own
  ranking logic (a different algorithm from df-survey's — different situations want different
  rankings, even within ZR) to compute a category, then writes it back via a dedicated `pr`
  capability op: `pg-connector pr categorize <id> --category <category>` (wire request `{"op":
  "categorize", "args": {"id": "<id>", "category": "<category>"}}`, response `{"result": {"id":
  "<id>", "category": "<category>"}}` on success, `{"error": {"code": "...", "message": "..."}}`
  per §4.2's taxonomy on failure). **Categorization is used only for focus/filtering — never for
  anything a human needs to see on github.com itself — so it does not live in a GitHub label.**
  Its confirmed consumer is Grafana dashboards. A CLI focus tool in the shape of a `pg-connector pr
  open`-equivalent is **not** a confirmed consumer: Appendix B records `pg-pr open`'s own
  disposition as still unanswered — whether the operator keeps using it manually with no stated
  replacement, or it drops entirely — so this design does not rely on that tool existing, and the
  category field's rationale rests on the dashboard consumer alone until Appendix B's question is
  resolved. It's a single-valued field in the same per-backend local store as feedback disposition
  (§9), new state this design introduces rather than a migration of anything pg-pr's existing
  SQLite store already has. Because it's a dedicated field rather than a member of a shared label
  namespace, the write is a plain set/overwrite — no add/remove/toggle ambiguity. The backend's own
  `capabilities` response still declares its valid category vocabulary, the same pattern §4.3
  already establishes for issue-transition vocabulary, so df-categorize's algorithm output can be
  checked against accepted values rather than assumed compatible.
- **df-feedback**, invoked as `df-feedback pr <pr-id>` for a PR+comment/review-thread event — `pr`
  here is already the same kind of literal argv constant df-categorize's split now uses, not a
  templated field, which is why df-feedback was never exposed to the `{{.Item.Type}}` bug above —
  fetches the PR's full current state via `pg-connector pr show <pr-id>` — which includes its
  comments/review threads, each with its own id and current disposition — rather than trusting
  whatever specific comment triggered the event (consistent with the live-recompute philosophy in
  §8: pr-pool's argv only carries the PR id, not a comment id, so df-feedback re-evaluates every
  comment on the PR each time it runs, which is also idempotent and safe to re-trigger). For each
  comment/thread it determines needs a disposition, it writes back via `pg-connector pr
feedback-set <pr-id> <comment-id> --disposition <status>` (wire request `{"op":
"feedback_set", "args": {"id": "<pr-id>", "comment_id": "<comment-id>", "disposition":
"<status>"}}`, response `{"result": {"id": "<pr-id>", "comment_id": "<comment-id>",
"disposition": "<status>"}}` on success, `{"error": {...}}` per the taxonomy on failure, e.g.
  `not_found` if the comment id no longer exists). `disposition` is the closed enum already stated
  in §2: `open | will-fix | wont-fix | no-action`. This op lives under the `pr` capability (not as
  a separate top-level verb) because the feedback-disposition store itself moves under the PR
  GitHub backend (§9's own migration disposition for pg-pr's SQLite store) — this is the concrete
  mechanism for the Feedback item gap first named in §2.

**Permissions.** df-categorize and df-feedback run with permissions uniform to their pr-pool
parent — no bespoke restricted allowlist, per this workspace's existing ruling that pr-pool
subagents share the parent's permissions rather than a per-role least-privilege split.

**Exit codes.** Both handlers report back to pr-pool via pr-pool's own existing command-role
contract for a `"command"`-kind role. That contract has four live signals, per pr-pool's own
source of truth (`packages/pr-pool/docs/behavior/interfaces.md:84-100`'s "Coarse outcome, rich
reply", `INV-FAIL-1`, `INV-CONC-1`, realized concretely by
`phillipgreenii-nix-agent-support · packages/pr-pool/docs/decisions · DEC-WIRE-1` and implemented
in `packages/pr-pool/internal/executor/command.go:14-53` /
`packages/pr-pool/internal/orchestrator/listener.go:165-178`) — **not** the "`0` on success, any
nonzero on failure, and pr-pool retries" scheme a prior draft of this section claimed:

- **`0`** — success. Recorded as a completed dispatch; no retry needed.
- **`9`** — `busy`, the one and only pre-accept decline a command role can signal
  (`executor.ErrBusy` -> `eventqueue.DeclineBusy`): pr-pool re-offers the event to this role (or
  another bound one) while it remains unexpired, at `INV-FAIL-2`'s cadence. Neither handler emits
  this — df-categorize and df-feedback have no notion of their own capacity.
- **Every other exit code — `1`, `2`, `3`, `4`, anything** — is treated identically by
  `roleListener.Offer`: `Accepted: true`, `Decline: DeclineNone`. pr-pool does not distinguish "the
  command ran and failed" from "the command could not be invoked at all"; neither is retried. **The
  event is consumed the moment the command exits anything but `9` — pr-pool does not retry a failed
  command-role call on its own**, correcting the prior draft's claim. A handler that wants its own
  failure retried (a fresh event, an alert, a follow-up bead — anything) owns that entirely itself.
- **`2` and `3` are separately reserved, ecosystem-wide, by `DEC-WIRE-1`'s coarse-exit-code
  convention** — `2` for a generic usage error, `3` for the core's own pre-flight failure
  (`exitPrecheck`) — even though `commandRun.run` itself special-cases only `9` today. This matters
  because **pg-connector defines its own, unrelated meaning for exit codes `2` and `3`** (§4.5):
  for a fan-out op, pg-connector exits `2` for "degraded/partial" and `3` for "total failure",
  while `DEC-WIRE-1` reserves those same two integers for "usage error" and "core pre-flight"
  respectively — two unrelated taxonomies sharing the same two codes. Both handlers' calls today
  (`show`, `categorize`, `feedback_set`) are all §4.5 _targeted_ ops, which only ever produce
  pg-connector exit `0`, `1`, or `4` — so no call _currently_ specified in this section can itself
  produce a raw `2` or `3` — but that is an accident of today's op set, not a guarantee, and
  **neither handler may rely on it**: **neither handler may ever exit with the raw exit code a
  `pg-connector` subprocess returned.** Blindly propagating a subprocess's exit code (e.g.
  `os.Exit(cmd.ProcessState.ExitCode())`) is exactly the trap this collision sets — the day either
  handler grows a fan-out-shaped call, a pg-connector `2`/`3` would leak through as the handler's
  own exit code to pr-pool and be misread against `DEC-WIRE-1`'s meaning instead of pg-connector's.
  **Both handlers MUST translate every outcome into pr-pool's own four-signal set above — exit `0`
  for success (including a skipped `not_found`, below) and a plain `1` for every other failure —
  and MUST NOT re-emit whatever exit code the `pg-connector` subprocess itself returned.**
- Each handler makes more than one targeted `pg-connector` call (df-categorize: `show` then
  `categorize`; df-feedback: `show` then one `feedback-set` per comment needing a disposition) — a
  `not_found` (pg-connector exit `4`, §4.5) from ANY of them, including df-feedback's own "comment
  id no longer exists" case, is NOT a failure by pg-connector's own taxonomy, and the handler MUST
  NOT treat it as one: it means "nothing to do for that item," so the handler skips it and
  continues, still exiting `0` to pr-pool overall if nothing else went wrong. Only a genuine
  pg-connector error (`unauthenticated`/`unavailable`/`unknown_op`/`version_mismatch`, or a
  CLI-level failure) causes the handler to exit `1` to pr-pool — translated per the rule above,
  never pg-connector's own raw code.

**Acceptance criteria**

- pr-pool's core requires zero Go changes for these two roles beyond config registration.
- df-categorize is registered as one pr-pool role per connector type (`df-categorize-pr`,
  `df-categorize-issue`, `df-categorize-ci`), each bound to its own connector-specific event type,
  with `type` baked into that role's own argv as a literal — never templated from
  `{{.Item.Type}}`, which pr-pool's live queries always populate with the beads issue-type
  `"task"`, never a connector type (`packages/pr-pool/internal/item/item.go:9-14`,
  `modules/zm/default.nix:123,149`). df-feedback needs no such split — it already hardcodes the
  literal `pr` in its own argv and was never affected by this bug.
- Both handlers are invoked with `id` as a plain positional argv element, plus their `type`
  (df-categorize: per-role literal; df-feedback: the existing hardcoded `pr`), with no
  adapter/shim binary and no stdin-JSON path on the pr-pool-to-handler boundary; both then speak
  pg-connector's own stdin-JSON scriptout protocol outbound to fetch/write state.
- df-categorize writes back via `pg-connector pr categorize <id> --category <category>` into a
  dedicated local-store field (not a GitHub label), checked against a backend-declared category
  vocabulary; df-feedback writes back via `pg-connector pr feedback-set <pr-id> <comment-id>
--disposition <status>` with `disposition` drawn from open/will-fix/wont-fix/no-action. The
  category field's rationale cites only its confirmed consumer (Grafana dashboards); a
  `pg-connector pr open`-equivalent CLI consumer is not assumed, per Appendix B's open question
  about `pg-pr open`'s own disposition.
- Both handlers report to pr-pool using only pr-pool's real four-signal command-role contract
  (`0` success, `9` busy/pre-accept-decline/retried, every other code `Accepted: true`/no retry,
  `2`/`3` reserved ecosystem-wide by `DEC-WIRE-1` and never emitted by either handler): both exit
  `0` on success and a plain `1` on any other failure, never `9` (neither has a capacity concept),
  and never a raw pg-connector exit code passed through unchanged — pg-connector's own fan-out
  codes `2`/`3` (§4.5) collide with `DEC-WIRE-1`'s reservation of those same two codes, so every
  outcome is translated, never re-emitted verbatim.
- Neither handler assumes pr-pool retries a failed command-role call: pr-pool retries only an
  exit-`9` pre-accept decline, so each handler's own failure handling/surfacing (alerting, a
  follow-up bead, a fresh event) is entirely its own responsibility.
- df-feedback re-fetches and re-evaluates the PR's full current comment list on every trigger
  rather than acting on a single embedded comment id.
- Both run with permissions uniform to their pr-pool parent.
- df-categorize's ranking logic is demonstrably distinct from df-survey's.

## 7. Tier 3 — ZR-specific layer

Explicitly not generic, and not bundled into one binary.

### 7.1 On-demand tools

- **df-attention** is a pure TUI client of `pg-connector attention list` — it performs no direct
  exec of any attention source itself; the fan-out, dedup, ranking, and cap all happen inside
  pg-connector (§4.4). Since the wire default is deliberately uncapped (§4.4), df-attention passes
  its own default `--cap 50` unless the user overrides it — the daily-use TUI stays usable out of
  the box while a script calling `pg-connector attention list` directly still gets everything by
  default. It may surface human-labeled beads read-only, per §4.4, with no claim/ack mutation of
  its own.
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

> **Cross-reference (bead `pg2-2j5ac.11`, 2026-09-06): relationship to `phillipg-nix-ziprecruiter`'s
> daily-focus v2 design.** `phillipg-nix-ziprecruiter`'s
> `docs/superpowers/specs/2026-09-02-daily-focus-v2-design.md` (one day older than this design)
> is a separate design that also claims `modules/daily-focus/` — its own §3 Architecture overview
> and §12 Implementation decomposition sketch add `df-survey`, `df-deferred`, `df-wire`, and
> `df-pull` as new siblings there. **Relationship: COMPLEMENTARY, not superseding and not
> conflicting.** That design's `df-survey` queries `gh`/`pjira`/`bd` directly for its own
> planning-synthesis purpose (the morning ritual named in §7.3 above) and is never routed through
> `pg-connector attention list`/`search` — exactly the disjoint-mechanism split §7.3 already
> anticipates. This section's four tools are pure `pg-connector` clients (`df-attention`/
> `df-search`) or pr-pool roles (`df-categorize`/`df-feedback`, §6.1) with no code overlap with
> `df-survey`'s planning logic. The one real coordination point neither design states on its own:
> both add new `df-*`-named siblings to the SAME hand-enumerated nix lists (that design's §12
> names them explicitly — `modules/daily-focus/scripts.nix`'s `callPackage` block/`allScripts`/
> `checks`/aggregate check, plus `default.nix`'s per-script `.script` append). Whichever design's
> packets land second MUST merge into those lists rather than overwrite the first design's
> additions, and MUST pick names that don't collide with the other's existing `df-*` set (no
> collision exists today between `df-survey`/`df-deferred`/`df-wire`/`df-pull` and
> `df-attention`/`df-search`/`df-categorize`/`df-feedback`).

**Acceptance criteria**

- df-attention/df-search are pure clients of the two Tier-1 verbs, performing no direct exec of
  any source.
- df-attention passes its own default `--cap 50` unless the user overrides it; the wire default
  itself stays uncapped.
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

Categorization and feedback disposition (§6.1) satisfy "downstream of events, not computed at
query time" by writing back into a store scoped to the PR GitHub backend — feedback disposition
into the owning system's own migrated store, categorization into a dedicated local field — instead
of building a separate cross-connector index; reading either back later is just a filtered live
query through that backend. Search, attention, and daily-focus all do live fan-out plus in-memory
correlation on demand, on the same principle.

**Acceptance criteria**

- A CI/convention check confirms no package under `packages/pg-connector/` (outside a single
  backend's own `internal/`) defines a persistent store keyed by more than one entity type's IDs
  together — the mechanical form of "no cross-connector entity store." **Implemented, not just
  claimed:** `entity_store_test.go`'s AST scan (`pg2-nvm80`).
- Any reference resolution outside a query's initial scope uses one bounded, batched lookup, never
  unbounded recursion.

## 9. pg-pr retirement

`pg-pr` as a standalone binary retires — there is no dual-interface question, because there will
only be one. `pg-connector pr` is the sole client-facing PR interface going forward, backed
initially by pg-pr's existing GitHub logic carried over rather than rewritten, and eventually by a
second, interchangeable backend (e.g. Forgejo) — exactly Tier 1's symmetric-backend model, made
concrete by §4.1's `connector.pr` already being list-valued from the start.

**No coexistence architecture is needed for the transition period, by explicit operator
decision.** Phillip is currently the sole user of every tool being retired or replaced here, so
there is no other caller whose breakage a shim, dual-write, or routing layer would be protecting
against. pg-connector's PR backend carries over pg-pr's existing GitHub logic unchanged, reading
and writing the same underlying GitHub state pg-pr always has — there is no real drift risk
between the two to guard against during an overlap window. The transition is simply: build and
test `pg-connector`'s replacement for a given pg-pr command group, then remove that command group
from pg-pr, one group at a time per the verb→destination table (§9.1). No parallel-running
architecture, shim, or dual-write mechanism is planned.

### 9.1 What moves where

pg-pr's `vcs.Provider`/`issues.Provider`/`cicd.Provider` implementations become Tier-2 backend
implementations behind pg-connector. Its scriptout exec-plugin protocol generalizes to all four
connector types shipped in this build (§2). Its bespoke beads-upsert code retires in favor of a
real beads `issues.Provider`
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
composed with a `pg-connector pr show` call for any PR→branch resolution.

**Verb → destination table.** Derived by grepping every `rootCmd.AddCommand(...)` registration in
`packages/pg-pr/cmd/pg-pr/*.go` for the full set of top-level command groups, then every `Use:`
registration in `packages/pg-connector/cmd/pg-connector/*.go` for what already ships. `pr`, `issue`,
and `ci` already had stated destinations before this table; the rest did not.

| pg-pr command group                                                                       | Subcommands                                                                                                                                        | Destination                                                                                                                                                                                                                                                                                                                                | Status today                                                                                                                                                     |
| ----------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pr`                                                                                      | `list`, `view`, `files`, `commits`, `create`, `update`, `close`, `ready`, `draft`, `wip on`/`off`, `hide`, `unhide`, `automerge on`/`off`, `merge` | `pg-connector pr <verb>` (same names, PR GitHub backend, **except `view` → `show`** — pg-connector's shipped verb for "fetch one PR's current state" is `show`, not `view`; every other mention of this op in this design uses `pg-connector pr show`, and this table adopts that same convention rather than pg-pr's own `view` spelling) | `show`, `categorize`, `feedback-set` ship; the rest of this list does not yet                                                                                    |
| `worktree`                                                                                | `add`, `remove`, `list`                                                                                                                            | `pg-connector scm worktree <verb>`                                                                                                                                                                                                                                                                                                         | Ships today                                                                                                                                                      |
| `branch`                                                                                  | `detect`                                                                                                                                           | `pg-connector scm branch detect`                                                                                                                                                                                                                                                                                                           | Ships today                                                                                                                                                      |
| `issue`                                                                                   | `show`                                                                                                                                             | `pg-connector issue show`                                                                                                                                                                                                                                                                                                                  | Ships today (pg-connector's `issue` also has `create`/`comment`/`transition`, unused by pg-pr's read-only `issue show`)                                          |
| `ci`                                                                                      | `runs`, `logs`, `rerun-failed`                                                                                                                     | `pg-connector ci list` (renamed from `runs`), `ci logs`, `ci rerun-failed`                                                                                                                                                                                                                                                                 | Ships today, modulo the `runs`→`list` rename                                                                                                                     |
| `auth`                                                                                    | `status`                                                                                                                                           | `pg-connector auth status`                                                                                                                                                                                                                                                                                                                 | Ships today                                                                                                                                                      |
| `config`                                                                                  | `show`, `validate`                                                                                                                                 | `pg-connector config validate` (ships); `config show` (does not yet)                                                                                                                                                                                                                                                                       | Partially ships                                                                                                                                                  |
| `feedback`                                                                                | `list <repo> <pr>`, `show <id>`, `disposition <id>`                                                                                                | `list`/`show` fold into `pg-connector pr show <id>`'s response, which already returns every comment/thread with its own disposition (§6.1); `disposition` maps to the shipped `pg-connector pr feedback-set <pr-id> <comment-id> --disposition <status>`                                                                                   | No new verb needed — only the call sites need rewriting                                                                                                          |
| `review`                                                                                  | `draft`, `post`, `submit`                                                                                                                          | `pg-connector pr review draft`/`post`/`submit` — new write verbs on the `pr` capability, mirroring the shape `categorize`/`feedback-set` already establish                                                                                                                                                                                 | Does not exist yet                                                                                                                                               |
| `comment`                                                                                 | `add`, `resolve`                                                                                                                                   | `pg-connector pr comment add`/`resolve` — same new-verb pattern as `review`                                                                                                                                                                                                                                                                | Does not exist yet                                                                                                                                               |
| `sync` (+ `sync duplicates`)                                                              | —                                                                                                                                                  | No destination verb. `sync`'s beads-upsert projection and its `duplicates` bd-audit subcommand are exactly the "bespoke beads-upsert code" and "pre-existing violation" this section already retires in favor of pr-pool polling the beads connector directly — there is nothing to rewrite one-for-one                                    | Retires without a rewrite target; also runs as the `pg-pr-sync` launchd daemon that serves the local dashboard below, so its shutdown is gated on that open item |
| local dashboard (`internal/dashboard`, served by the `sync` daemon's `/api/v1/dashboard`) | —                                                                                                                                                  | Unresolved — Appendix B: `pg-pr open`'s disposition (its only confirmed consumer) is still unanswered                                                                                                                                                                                                                                      | Blocked on Appendix B                                                                                                                                            |
| `open`                                                                                    | —                                                                                                                                                  | Unresolved — same Appendix B question: continues as a manually-run pg-connector-equivalent with no stated replacement, or drops entirely                                                                                                                                                                                                   | Blocked on Appendix B                                                                                                                                            |
| `changes`                                                                                 | —                                                                                                                                                  | No destination verb. Superseded by pr-pool polling the beads connector directly instead of pg-pr's own bespoke bd-workspace diff/poll logic                                                                                                                                                                                                | Retires without a rewrite target                                                                                                                                 |
| `migrate`                                                                                 | —                                                                                                                                                  | No destination verb. One-shot/idempotent maintenance on pg-pr's own SQLite store; its disposition is entirely the store's own per-table migration disposition (this section's second acceptance criterion), not a connector call                                                                                                           | Retires with the store                                                                                                                                           |
| `migrate-feedback`                                                                        | —                                                                                                                                                  | No destination verb. One-shot cleanup of legacy pre-store feedback beads, already obsolete before this design started                                                                                                                                                                                                                      | Retires without a rewrite target                                                                                                                                 |
| `version`                                                                                 | —                                                                                                                                                  | No destination verb needed — `pg-connector` has its own `version`/`--version`; moot once the binary retires                                                                                                                                                                                                                                | Trivial                                                                                                                                                          |

Real downstream call sites confirming this table's shape were found across
`claude-marketplace/pg-pr/{agents,commands,skills,hooks}`, `flake.nix`'s pinned checks
(`test-pg-pr-marker-hook`, `test-pg-pr-hook-registered`, `test-pg-pr-review-input-assets`,
`test-pg-pr-shared-reference-docs`), the tldr page source (`home/programs/pg-pr/default.nix`), the
capabilities list (`home/capabilities/default.nix:76`), and a cross-plugin reference from
`integrate-branch`'s `pull-request` skill (`pg-pr pr list`/`create`/`merge`/`automerge on`) — the
same asset classes Appendix A names, not independently re-counted to 133 (see Appendix A on why
that recount is a curation exercise, not a cheap grep). Re-deriving the group inventory did surface
one correction to Appendix A's own prior count: `comment` (`pg-pr comment add`/`resolve`) is
registered as its own top-level command (`rootCmd.AddCommand(reviewCmd, commentCmd)` in
`review.go`), not nested under `review` — it was missing from Appendix A's named list of remaining
groups, which is corrected there.

**Migration-window policy: sync obligation, per-table disposition, and end condition.** Cited for
the PROCESS side (execution risk while both trees are live — carried-over dead surface, internal
duplication, guard fragility): `pg2-lh3c4`, which tracks the pr-github backend's own carried-over
surface and the module-wide `gh`-choke-point/stack-mutation guards this section's own removal
criterion (item 5, below) already depends on relocating. What follows is this design's own
decision, which that bead executes against, not a restatement of it.

- **Sync obligation while both trees are live.** For every command group with a stated
  pg-connector destination other than feedback (pr/issue/ci/scm/auth/config), there is no real
  drift risk during the overlap: both binaries read and write the SAME remote system state
  directly — pg-connector's PR backend performs a live GitHub read on every call rather than
  caching (the `AsOf`/`Stale` pair's own doc comment confirms this for the shipped backend,
  Appendix A) — so pg-pr and pg-connector are never holding two independently-writable copies of
  that data. **The one exception is feedback disposition**, which is durable _local_ state that
  changes OWNERSHIP rather than being re-derived from GitHub each call (§6.1: it "moves under the
  PR GitHub backend"). While pg-pr's own `feedback` command group and pg-connector's
  `pr feedback-set`/`pr show` are BOTH reachable, a write through one binary and a read through
  the other would silently diverge. The obligation this creates: **the disposition-store
  migration and the deletion of pg-pr's own `feedback` command group MUST land in the same
  cutover step, never split across two** — there is no window in which both binaries hold their
  own independently-writable copy. This generalizes: for any table whose disposition (below) is
  "migrate under a Tier-2 backend's own store" rather than "drop" or "no store, always re-read
  live," the migration step and the retirement of pg-pr's own reader/writer for that data are the
  same atomic step, by the same reasoning.
- **Per-table SQLite disposition.** The removal criterion (item 3, below) already requires a
  stated disposition for every table pg-pr's SQLite store holds, not just feedback — this decides
  the remaining ones by the same rule rather than leaving them open indefinitely: a table's
  disposition follows the stated destination of the pg-pr command group that owns it (the table
  above). Applied to Appendix A's named tables: **outbox+leases** and **repo_sync_state** back the
  `sync` daemon and `changes` machinery, both of which "retire without a rewrite target" per the
  table above — they DROP with no data carried over, same as the code they back. **user_state** is
  where pg-pr's `hide`/`unhide` state lives; per §4.4, that suppression is a client/dashboard-layer
  concept, never a pg-connector wire concept — so it does not migrate under any Tier-2 backend's
  own store; it moves (if at all) to whatever Tier-3/client tool ends up owning that suppression, a
  question this design doesn't otherwise resolve. **PR rows** and **approver data** are genuinely
  blocked, not decided here: both underpin the local dashboard/`pg-pr open` question Appendix B
  already leaves open, and inventing a disposition for them ahead of that question's own
  resolution would be answering a question this design has explicitly deferred to Appendix B, not
  resolving it.
- **End condition.** Already stated below: the six-point removal criterion is the migration
  window's end condition — it ends exactly when all six are independently true, not on a
  schedule.

**Removal criterion — pg-pr is removed when:** `pg-pr` as a standalone binary MUST NOT be deleted
until every one of the following holds, with no coexistence period required in between (per this
section's own no-shim/no-dual-write decision):

1. Every "Destination" cell in the table above that isn't already "Ships today" has landed and
   passed its own tests in `pg-connector` — including the `pr review`/`pr comment`/`config show`/
   `pr <write-verb>` group of new verbs.
2. Every real call site found for a retired verb — across `claude-marketplace/pg-pr/**`,
   `flake.nix`'s pinned checks, the tldr page, the capabilities list, the cross-plugin references,
   and pr-pool's own hardcoded `pg-pr` sites (Appendix A) — has been rewritten to call
   `pg-connector` instead, and a repo-wide grep for a literal `pg-pr` invocation (excluding this
   design doc, ADRs, and other historical/prose references) returns zero hits.
3. Every table in pg-pr's SQLite store has a stated and executed disposition (migrate under the PR
   GitHub backend, drop, or otherwise) — not just the feedback-disposition table already covered.
4. Appendix B's two open dispositions — `pg-pr open` and the local dashboard it reads — are
   resolved one way or the other (kept with a stated pg-connector-backed replacement, or dropped)
   rather than left open.
5. The cross-backend test guards (`TestGHExecChokePoint`, `TestNoGHStackMutatingArgv`) have been
   relocated out of pr-github's own package per this section's acceptance criteria below, and still
   pass from their new home.
6. `pg-pr`'s own retiring test suites (`pkg/beads`, `internal/beadsbridge`) have an explicit
   disposition recorded — moved, rewritten against the new seam, or accepted as a coverage loss —
   rather than silently deleted with the binary.

Only once all six hold does the `packages/pg-pr` module itself get deleted. This is intentionally a
condition-based criterion rather than a calendar date: the six conditions are independently
checkable at any time, so the dual-maintenance window ends exactly when the work is actually done,
not on a schedule that can slip unnoticed.

### 9.2 What's explicitly out of scope here

The review-orchestrator ecosystem's _trigger_ mechanism is intentionally not redesigned here. The
operator's stated intent is that this analysis logic ends up triggered by a pr-pool role rather
than primarily by a human running a slash command directly — that redesign, and review draft's
exact home once it lands, are deferred to a future design pass. Preserving today's skills/agents/
slash-commands invoked directly is explicitly not a requirement this design needs to satisfy.

### 9.3 Rollout and cutover sequencing

§9.1's table gives the WHAT (destination per command group); this states the HOW/WHEN for the
roughly 133 downstream literal `pg-pr <verb>` invocations Appendix A inventories (skills, review
subagents, slash commands, a PreToolUse hook, pinned flake checks, a tldr page, a
capabilities-list entry, a cross-plugin reference).

**Sequencing rule:** rewrite a command group's call sites only AFTER that group's pg-connector
destination has landed and passed its own tests (removal criterion item 1, above) — never
speculatively ahead of the destination existing, and never batched into one rewrite pass at the
end, which would recreate exactly the kind of big-bang cutover §9's no-shim/no-dual-write decision
exists to avoid. Practically: as each "Status today" cell in §9.1's table flips from "does not
yet" to "ships," the call sites for that verb group become rewritable, and get rewritten in the
same packet or a closely-following one — not deferred to a single terminal "rewrite everything"
phase.

**Verification:** removal criterion item 2, above, already states the mechanical completeness
proof (a repo-wide grep for a literal `pg-pr` invocation, excluding this design doc/ADRs/other
historical prose, returns zero hits). This subsection adds the ordering discipline that gets there
incrementally, not a second completeness check.

**No rollback mechanism is designed**, consistent with §9's no-shim/no-dual-write decision: a
rewritten call site that breaks is fixed forward (revert or patch the one packet that rewrote it),
never rolled back to calling the old `pg-pr` binary — pg-pr's own command group for that verb is
deleted in the same cutover step, not kept alive as a fallback.

### 9.4 Deprecation timeline

This design deliberately states no calendar timeline, only condition-based gates — the removal
criterion above already establishes that ("not a calendar date... independently checkable at any
time"). What this subsection adds is the expected ORDER those conditions become checkable in,
derived from this design's own phase-shaped scope (the same shape sketched, not yet approved, for
`pg2-2j5ac`'s own decomposition, corrected per this bead's own fix to that sketch below): the
cross-cutting attention/search capability plus the mechanical convention checks; a second Issue
backend; the two new pr-pool roles plus the one already-decided SQLite table migration; the
Tier-3 on-demand tools; and last, the pg-pr-retirement PRECONDITIONS (the verb→destination table,
the remaining SQLite table dispositions, and `docs/behavior/` authoring). No step in that order is
itself a cutover step — cutover (the §9.3 rewrite plus deleting `packages/pg-pr`) starts only once
every one of the six removal-criterion conditions above is independently true. This is a
dependency ORDER, not a schedule, for the same reason the removal criterion itself is
condition-based rather than dated.

**Acceptance criteria**

- A full verb→destination table for every pg-pr command group exists before any retirement packet
  is cut (§9.1's table, above).
- An explicit "pg-pr is removed when X" criterion exists and MUST be re-checked before the
  `packages/pg-pr` module is deleted (§9.1's removal criterion, above) — the dual-maintenance
  window has a stated end condition, not just a stated start.
- A rollout/cutover sequencing rule for the downstream call-site rewrite exists (§9.3), and a
  deprecation timeline stated as a dependency order rather than a calendar date exists (§9.4).
- `TestGHExecChokePoint` and `TestNoGHStackMutatingArgv`
  (`packages/pg-connector/cmd/pg-connector-pr-github/internal/github/chokepoint_test.go` and
  `.../stack_readonly_test.go`) MUST be relocated out of the pr-github backend's own test package,
  into a location whose lifecycle is independent of any single backend, **before** any retirement
  packet removes, deletes, or substantially restructures that backend's package. This is a
  prerequisite of retirement completing, not cleanup that can trail it: both tests walk the whole
  `pg-connector` module from inside one backend's package today (via a shared `moduleRoot(t)`
  helper that finds `packages/pg-connector/go.mod`), so deleting or restructuring that package
  first — as pr-github's own surface shrinks while pg-pr's GitHub logic is carried over — would
  silently delete the module's only cross-backend `gh` choke-point guard and its only stack-mutation
  guard.
- pg-pr's local SQLite store gets a stated per-table disposition (migrate / drop / move under the
  PR GitHub backend) before any retirement packet lands.
- No shim, dual-write, or routing-layer coexistence mechanism is built; the transition is
  build-test-cutover per pg-pr command group, per the verb→destination table.
- pg-connector's own `docs/behavior/` set is authored as its own first work packet, before any
  code-producing packet, so later packets have real behavior-IDs to cite from day one. The four
  existing implicated behavior-docs sets (pg-pr's, pr-pool's, ZR's daily-focus/pr-pool config,
  work-report's) update in the same change as whatever packet touches them, per this repo's
  existing documentation rule.

## 10. Next phase — deferred, not blocking

### 10.1 Captain's Log CI registration

`pg-pr-cicd-captains-log` — the binary §5.1 requires be renamed to
`pg-connector-ci-zr-captains-log` to carry the naming convention's ZR marker — already ships
today, under that current pre-rename name, as a working, PATH-wired binary with real Cloudflare
Access auth and unit tests: nothing needs to be built from scratch. What's deferred to a later
phase is both the §5.1 rename itself and registering the (renamed) binary into `connector.ci`; the
`ci` connector type ships and works today with the GitHub Actions backend alone, and nothing else
in this design depends on Captain's Log being renamed or registered before then. Carried-forward
open question for whenever this phase starts: Captain's Log's ID scheme is assumed to share
GitHub's own run/job IDs, so multi-backend fan-out would work without a separate correlation step
— if that assumption is wrong when this is actually built, it needs the same external-ref
correlation pattern used everywhere else in this design.

### 10.2 Thread and Note connector types

Dropped from this build entirely (§2), not merely unscheduled: neither has prior art in this
codebase, a sketched field set, or an identified consumer. Tracked here as candidate future
connector types, to be reconsidered only once a real consumer needs one — a Slack-backed Thread
consumer or a Notion-backed Note consumer, most plausibly surfacing from Tier 3 ZR-specific work.
When and if that happens, they follow every convention already established for the four shipped
types unchanged: `connector.thread`/`connector.note` registry keys, `pg-connector-thread-<backend>`
/`pg-connector-note-<backend>` naming (§5.1), their own per-capability `schemaVersion` (§4.3), and
their own optional `attention.Source`/`search.Source` implementations if applicable (§4.4) —
nothing about re-adding them requires revisiting a decision made for the other four. One thing
that will need fresh work whenever this happens: Slack and Notion are HTTP-only backends with no
local CLI to stub, so their tests will need a double convention (RoundTripper injection or
record/replay) that no existing pattern in either repo currently covers.

**Acceptance criteria**

- `pg-pr-cicd-captains-log` keeps working, PATH-wired and unchanged, under its current name, until
  this phase actually starts; the §5.1 rename to `pg-connector-ci-zr-captains-log` and the
  `connector.ci` registration land together, in that phase, not before.
- `connector.ci` does not list it until this phase is explicitly started.
- Thread and Note are not implemented, registered, or referenced as live types anywhere in the
  initial build; this section is their only mention.

## 11. Testing strategy

This states the overall STRATEGY across the tiers, not just an inventory of what exists — the
inventory alone would just restate Appendix A's own "Wire protocol and testing" gaps.

- **Per-backend unit tests** against a fake `Runner`/`gh`-client with hand-typed JSON fixtures are
  the existing convention, one per Tier-2 backend today. This is the correctness gate for a
  backend's OWN logic — it does not, and is not meant to, prove that logic against what the real
  upstream system actually returns.
- **Module-wide mechanical/convention checks** (§3's naming check, §4.4's dependency-direction
  check, §8's cross-entity-store check, all `pg2-nvm80`; §4.3's version-negotiation check,
  `pg2-p2z7o`) are the architecture gate — they keep this design's own principles (capability
  scoping, layering, no shared store, version skew detection) true as code, not just as prose, and
  catch a regression a unit test wouldn't notice.
- **An e2e/contract suite against the real `gh`/`bd` CLIs** — not hand-typed fixtures — is
  deliberately a separate, credential-gated layer, kept out of the default `nix flake check`
  sandbox so a missing-credential CI environment doesn't block on it. This is the "does this
  actually work against the real world" gate, and it does not exist yet: `pg2-qp50z` (open) tracks
  building it, motivated concretely by a bug (a `ListReviews` id shape mismatch) that every
  existing hand-fixtured unit test missed.
- **Not committed to yet, and not invented here:** a scriptout schema/goldens/conformance suite an
  external implementer could run against any candidate backend, and a test-harness extension for
  the pr-pool↔df-categorize/df-feedback integration seam (§6.1) — both remain real, named gaps in
  Appendix A below, not silently dropped by this section's existence.

---

## Appendix A: known gaps and open items (not blocking, but not yet resolved)

These are real, non-trivial gaps identified across this design's review history. None of them
blocks starting implementation of the sections above, but each needs a decision or more work
before the area it touches is actually done.

**Retirement completeness (§9)**

- The full verb→destination table now exists (§9.1's table), covering every remaining command
  group. Re-deriving it also corrected the group count above: `comment` (`pg-pr comment
add`/`resolve`) is its own top-level command, not nested under `review`, so the accurate
  remaining-group list is worktree, branch, open, review, comment, sync (+ sync duplicates),
  changes, config, auth, migrate, migrate-feedback, plus the local dashboard — 12 groups, not ~13
  minus a hidden extra. Two of those — `open` and the local dashboard it reads — still have no
  destination verb; both are blocked on Appendix B's still-open `pg-pr open` disposition question,
  not on missing analysis here. §9's acceptance criteria now also state an explicit "pg-pr is
  removed when X" removal criterion, closing the second half of this finding.
- A rewrite sequencing policy now exists (§9.3): rewrite a command group's call sites only after
  its pg-connector destination has shipped and passed its own tests, incrementally per group,
  never as one batched pass at the end. What §9.3 does not do is execute that policy — the
  roughly 133 downstream literal `pg-pr <verb>` invocations across agent-support's Claude Code
  plugin assets (skills, review subagents, slash commands, a PreToolUse hook, pinned flake checks,
  a tldr page, a capabilities-list entry, and a cross-plugin reference from another skill) are
  still unrewritten today; the sequencing rule and the verb→destination table (§9.1) together are
  the plan, not a claim that any rewriting has happened.
- pg-pr's local SQLite store (feedback dispositions, PR rows, outbox+leases, user_state,
  repo_sync_state, approver data) now has a stated disposition for four of six tables (§9's new
  "Migration-window policy" block, above): feedback dispositions migrate under the PR GitHub
  backend (unchanged from before); outbox+leases and repo_sync_state drop with the `sync`/
  `changes` machinery they back; user_state's hide/unhide portion moves to the client/dashboard
  layer, never a pg-connector store. **PR rows and approver data remain genuinely unaddressed** —
  both are blocked on Appendix B's still-open `pg-pr open`/local-dashboard question, not decided
  here, since either answer to Appendix B would determine theirs.
- pr-pool's own hardcoded `pg-pr` call sites (two literal `exec.CommandContext` sites, a nix
  `wrapProgram`/`callPackage` reference, and a compiled-in default `AllowedTools` string) are
  already tracked in pr-pool's own gap register but not sequenced into this design.
- A proposed 5-phase implementation-decomposition table exists in `pg2-2j5ac`'s own
  epic-decompose round-1 report (bd comment, 2026-09-04 19:40) — a planning artifact, not part of
  this document, and not yet operator-approved. Three corrections to it (a missing phase for
  §5.3's flake-output/repoint step and behavior-docs/ADR/README coverage; Phase 5 wrongly listing
  no dependencies on Phases 1-4 despite retirement-precondition work needing to know what they
  actually shipped; Phase 1 bundling §4.4's attention/search capability, §3's naming check, and
  §4.8's dashboard convention into one unrelated slice) are recorded as a follow-up comment on
  that same bead, not duplicated into this design doc.

**Data freshness and existing guarantees**

- **Resolved, not left open (bead `pg2-681xo`, 2026-09-06):** `schema.PR` now carries `AsOf`/
  `Stale` fields (`SchemaVersion` bumped 1 -> 2), mirroring pg-pr's own `INV-ASOF-1`/`INV-ASOF-2`
  guarantee that every acted-on read carries its own as-of time and a backend-computed stale flag
  a consumer must not re-derive. The shipped GitHub PR backend always performs a live read and so
  always reports `Stale: false`; the pattern is documented for any future backend that DOES
  cache — that backend is expected to adopt the same pair rather than invent a parallel
  convention. The `ci`/`issue`/`scm` sibling schemas do not carry this pair yet, because none of
  their current backends caches remote data either — there is nothing to represent staleness of
  yet, not an oversight. What remains genuinely open, and is NOT resolved by the field's
  existence: whether a future `pg-connector pr list` (not yet shipped, §9.1) becomes a live
  network call or a backend-local store persists, and, if a store, whether/when it would ever
  report `Stale: true`. The schema mechanism to represent that answer already exists either way —
  this is a live-vs-cache design question for whenever `list` is built, not a schema gap.

**Wire protocol and testing**

- Wire-level exit codes stay 0/1, which doesn't meet this workspace's own code-file convention
  that branchable meanings use ≥2 distinct exit codes.
- scriptout has no schemas/goldens/conformance suite analogous to pr-pool's own (which an
  importing consumer can run against any implementation). Given §4's "zero compiled-in providers"
  decision, nothing structurally prevents a backend's unit tests from all passing against a fake
  shape no real backend implements.
- The pr-pool↔df-categorize/df-feedback integration seam (§6.1) has no test harness extension —
  pr-pool's existing conformance driver only accepts Go-interface participants, not a
  subprocess-pluggable command-role handler.
- df-categorize's ranking has no stated injectable clock parameter, which daily-focus's own
  ranking-test convention requires to avoid flaky golden tests.
- No stated plan for pg-pr's and pr-pool's retiring test suites (pg-pr's `pkg/beads` and
  `internal/beadsbridge`, including its concurrency/locking tests; pr-pool's beads-query tests) —
  whether they move, get rewritten against the new seam, or are accepted as coverage loss.

**Design details left unpinned**

- Whether an existing backend's computed urgency/priority signal (used as the illustrative example
  for `severity` in §4.4) is an actual requirement for that backend, or just an example, is not
  confirmed.

**Explicitly deferred by design, not oversights**

- Dashboard/alert Tier-1 and Tier-2 _contents_ (§4.8) — the two-tier convention is fixed, the
  actual metric/alert lists are a later design pass, by explicit choice.
- The review-orchestrator trigger redesign (pr-pool-role-triggered rather than slash-command-
  triggered) — stated intent, not designed (§9.2).
- Captain's Log's rename (§5.1) and registration into `connector.ci` (§10) — the binary already
  works today under its current, pre-rename name (`pg-pr-cicd-captains-log`); the rename and the
  registration step are both deferred together, not the registration alone.

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
