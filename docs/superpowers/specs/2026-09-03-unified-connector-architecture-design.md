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
  not by system."

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
sentinel errors in `pkg/scriptout` (`ErrNotFound`, `ErrUnauthenticated`, `ErrUnavailable`,
`ErrUnknownOp`, `ErrVersionMismatch`), wrapped as `fmt.Errorf("%w: %s", sentinel, message)` — the
same pattern `vcs.ErrAuthInvalid` already establishes — so callers use `errors.Is` instead of
substring-matching the message.

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
  composing pg-connector's own capability verbs (`pg-connector pr view`, `pg-connector issue
view`, …) rather than talking to backend systems (GitHub, Jira, …) directly.** Allowing direct
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

- `connector.<type>` is flat and type-keyed at the top level; `issue`/`ci`/`pr` are list-valued,
  `scm` is single-valued; no `exec:` prefix exists anywhere in the registry.
- Every envelope carries `protocolVersion` and `schemaVersion` (per §4.3); `error` is `{code,
message}` from the closed enum; every backend answers `capabilities`; `config validate` fans out
  `auth_status` + `capabilities` across every backend.
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
  would catch a regression.
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
  - **`1`** — any other error (`unauthenticated`/`unavailable`/`unknown_op`/`version_mismatch`,
    or a CLI-level failure before a well-formed response was produced at all: bad arguments, an
    unreachable/non-executable backend, a panic).
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
built from identical shared `src`). Each of the six nix outputs (the umbrella plus five backend
binaries — Thread/Note dropped per §2/§10) needs a single-entry `subPackages` list. Known,
already-accepted cost: shared `src` means editing any one backend's `internal/` code bumps the
content-digest-versioned nix rebuild of all six binaries — acceptable given there's no independent
release-cadence requirement.

### 5.3 Captain's Log (existing ZR backend)

Captain's Log's cross-repo access needs a module-level `replace` (Go's `replace` operates at
module granularity) pulling in this module's entirety from ZR's side, mirroring how pg-pr is
consumed today. The actual consumer imports three packages — `pkg/schema` (wire shapes),
`pkg/scriptout` (wire protocol), and `pkg/provider`'s CI-capability Go interface — the three-way
split described in §5.2. The migration: rename the import paths, add a `pg-connector-src` flake
output (mirroring today's `pg-pr-src`), and repoint ZR's `go.mod replace`/`build.nix` at it. This
is a real migration step, bounded and doable, not a mechanical no-op.

**Acceptance criteria**

- Every Tier-2/plugin binary matches `pg-connector-<type>-<backend>`, `<type>` drawn from the
  capability verb (including `attention`/`search`); ZR-specificity is consistently encoded in
  `<backend>`, including a renamed Captain's Log binary.
- One Go module builds all binaries via N `mkGoApp` calls sharing `src`+`gomod2nixToml`; only
  `pkg/schema`+`pkg/provider`+`pkg/scriptout` cross backend boundaries, backstopped by a
  convention check.
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
  together — the mechanical form of "no cross-connector entity store."
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
from pg-pr, one group at a time per the verb→destination table (§9.1, not yet written). No
parallel-running architecture, shim, or dual-write mechanism is planned.

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
- No shim, dual-write, or routing-layer coexistence mechanism is built; the transition is
  build-test-cutover per pg-pr command group, per the verb→destination table.
- pg-connector's own `docs/behavior/` set is authored as its own first work packet, before any
  code-producing packet, so later packets have real behavior-IDs to cite from day one. The four
  existing implicated behavior-docs sets (pg-pr's, pr-pool's, ZR's daily-focus/pr-pool config,
  work-report's) update in the same change as whatever packet touches them, per this repo's
  existing documentation rule.

## 10. Next phase — deferred, not blocking

### 10.1 Captain's Log CI registration

`pg-connector-ci-zr-captains-log` (§5.1) already ships today as a working, PATH-wired binary with
real Cloudflare Access auth and unit tests — nothing needs to be built from scratch. Registering
it into `connector.ci` (and applying the renamed-binary convention) is deferred to a later phase;
the `ci` connector type ships and works today with the GitHub Actions backend alone, and nothing
else in this design depends on Captain's Log being registered. Carried-forward open question for
whenever this phase starts: Captain's Log's ID scheme is assumed to share GitHub's own run/job
IDs, so multi-backend fan-out would work without a separate correlation step — if that assumption
is wrong when this is actually built, it needs the same external-ref correlation pattern used
everywhere else in this design.

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

- `pg-connector-ci-zr-captains-log` keeps working, PATH-wired and unchanged, through the entire
  transition.
- `connector.ci` does not list it until this phase is explicitly started.
- Thread and Note are not implemented, registered, or referenced as live types anywhere in the
  initial build; this section is their only mention.

---

## Appendix A: known gaps and open items (not blocking, but not yet resolved)

These are real, non-trivial gaps identified across this design's review history. None of them
blocks starting implementation of the sections above, but each needs a decision or more work
before the area it touches is actually done.

**Retirement completeness (§9)**

- The full verb→destination table for pg-pr's remaining ~13 command groups (worktree, branch,
  open, review, sync, changes, config, auth, migrate, migrate-feedback, plus the local dashboard)
  does not exist yet as a durable artifact — only pr/issue/ci have stated destinations.
- No coordinated rewrite plan exists for the roughly 133 downstream literal `pg-pr <verb>`
  invocations across agent-support's Claude Code plugin assets (skills, review subagents, slash
  commands, a PreToolUse hook, pinned flake checks, a tldr page, a capabilities-list entry, and a
  cross-plugin reference from another skill) — per §9's now-settled build-test-cutover approach
  (no shim, no dual-write), these need direct one-by-one rewriting to call `pg-connector <verb>`
  instead, sequenced by the verb→destination table once it exists.
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
