# Claude Code hook router: design + implementation plan

**Status**: **Accepted** by the operator, 2026-09-16.

**Resolves**: `tc-c2ijv` ("Re-evaluate ceta + hook tooling: a hook router which can delegate to
other commands and decide on how to generate/merge the response").

**Deciders**: Phillip Green II (operator) — approved 2026-09-16, including the resolutions in
§5.3/§5.4, after this document was designed collaboratively earlier the same day.

**Repos touched**: `phillipg-nix-repo-base` (new nix builder), `phillipgreenii-nix-agent-support`
(new package + HM module + ceta migration).

---

## 1. Context

### 1.1 The problem

Three tools on this machine want to intercept the same Claude Code hook event
(`PreToolUse`, matcher `Bash`): ceta (`claude-extended-tool-approver`, the existing rule-chain
gate), rtk (a rewrite tool, seam exists but currently unwired), and pg-wi-flow's identity
processor (`tc-q25wo`, already released to drain as of this bead's own session — its identity
export rides inside ceta's own input-processor chain, not as an independent hook; see 1.3).

If two or more plugins each register their own `PreToolUse(Bash)` hook in their own
`hooks/hooks.json`, Claude Code runs them **in parallel** and merges the results unsafely.

### 1.2 What was measured (Claude Code 2.1.269, 2026-09-16)

All of the following were independently verified this session (nested throwaway `claude -p`
sessions, and direct inspection of this machine's `~/.claude/plugins/` state) rather than assumed
from memory or docs alone:

- **`updatedInput` collision (upstream #15897, closed not-planned).** When multiple `PreToolUse`
  hooks fire on one event, the hook that **completes last** wins wholesale on `updatedInput`; an
  earlier hook's rewrite is silently discarded. This holds regardless of whether the hooks come
  from separate `hooks.json` array entries or one entry's `hooks` list — it is not fixable by
  reorganizing a single plugin's own manifest.
- **No sequential-hook feature (#21533, closed not-planned).** Claude Code always runs
  same-event hooks in parallel; there is no opt-in to sequential execution.
- **Disabling a plugin does not suppress its hooks (#39307, #58520, both closed not-planned).**
  `enabledPlugins.<key> = false` does not stop that plugin's declared hooks from firing. This
  rules out "install a third-party plugin but disable it, then re-invoke it ourselves" as a
  composition strategy — the disabled plugin's own hook still runs independently, so the
  collision is not removed, only duplicated.
- **No plugin/marketplace lifecycle hook exists.** The full 32-event hook vocabulary
  (`SessionStart`, `PreToolUse`, …) has nothing for install/update/enable/disable/remove, and
  `SessionStart`'s payload carries no plugin-change signal either. Plugins load only at session
  start, or mid-session via `/reload-plugins` (which does **not** reconnect/disconnect MCP
  servers — that still needs a restart).
- **Commands/agents/skills/MCP servers are auto-namespaced per _installed plugin_**
  (`/plugin:command`, `mcp__plugin_<name>_<server>__<tool>`) — but that namespacing operates at
  the _installed-plugin_ granularity. Once multiple sources are merged into **one** plugin
  directory (our router), that protection no longer applies between the merged-in sources; we
  own de-duplication ourselves at merge time.
- **A hook's `command` string can contain a literal, unexpanded `${CLAUDE_PLUGIN_ROOT}`**,
  resolved by Claude Code at _invocation_ time against the plugin that declared it — not at
  install time. A merged/vendored source's hook command therefore needs this placeholder
  rewritten, at merge/build time, to point at wherever that source's files actually land _inside
  the merged tree_.
- **A hook entry's `matcher` field has context-dependent syntax, verified against
  `https://code.claude.com/docs/en/hooks.md` and real installed `hooks.json` files on this
  machine**: a string containing only letters/digits/`_`/`-`/spaces/`,`/`|` is exact-string-or-
  pipe-alternation matching (`"Bash"`, `"Edit|Write"`); anything else is treated as an unanchored
  JavaScript regex (`RegExp.prototype.test()`); absent, `"*"`, or `""` all mean match-everything
  (confirmed empirically: ceta's own `PreToolUse` entry has no `matcher` key at all and applies to
  every tool). **Matching is case-sensitive and the field it matches against is event-specific,
  not always "tool name"**: `PreToolUse`/`PostToolUse`/`PostToolUseFailure`/`PermissionRequest`/
  `PermissionDenied` match against the payload's `tool_name`; `SessionEnd` matches against
  `end_reason` (a session-end payload carries no `tool_name` at all); `SessionStart` matches
  against `session_start_reason`. A real example on this machine confirms the non-tool case:
  `superpowers`'s `hooks.json` has `"matcher": "startup|clear|compact"` on `SessionStart` — that's
  session modes, not tool names.
- **Whether two DIFFERENTLY-matchered hooks on the SAME event, both satisfied by one call, run in
  parallel and hit the #15897 collision is not independently re-confirmed for that exact
  sub-case** — official docs don't state it either way. The general pattern already measured
  (§ above: "all matching hooks run in parallel... regardless of separate array entries vs one
  entry's hooks list") strongly suggests yes, since nothing in the documented architecture
  describes any matcher-specificity-based conflict resolution. This does not change the design
  below either way: registering once per event with a broadest/union matcher and filtering
  internally is strictly safer than relying on Claude Code to arbitrate between differently
  scoped registrations, whether or not that specific case is currently buggy.
- **Claude Code's own directory-source marketplace cache-copy step silently drops any symlink
  pointing outside a plugin's own directory tree** (documented in this repo's own CLAUDE.md,
  precedent bead `pg2-sikj3`: a `/nix/store` symlink was dropped this way; the established fix is
  `home.packages` + bare-name command reference, used today by `ceta` and `pg-pr`). Any generator
  that assembles a merged plugin directory **must emit real copied files**, never symlinks into
  another store path — this is not a nix-purity nicety, it is required for the plugin to survive
  Claude Code's own copy step.
- **Multi-hook merge rules are real and documented, but split across two pages, and this draft
  previously cited the wrong one and got one wrong.** `hooks.md`'s own "Decision control" table
  (verified directly against the raw markdown, not an AI-summarized fetch — a first-pass fetch of
  this page fabricated a merge table and a "deny > retry > allow" ordering that do not exist in
  the source; always cross-check a summarized web fetch against `curl`'d raw text before citing
  it as a documented fact) lists **only which fields each event honors**, not how multiple hooks'
  values combine. The actual combination rules live on the companion page
  `https://code.claude.com/docs/en/hooks-guide.md` (verified 2026-09-16, `curl`'d directly):
  _"For `PreToolUse` permission decisions, the most restrictive answer applies, in the order
  `deny`, `defer`, `ask`, `allow`."_ — a **strict** four-way order, not the two-way
  `deny`/`ask`-or-`defer`/`allow` this draft's §2.5 previously assumed (`defer` is more
  restrictive than `ask`, not tied with it). The same page also documents `updatedInput`
  (`PreToolUse`) as **last-hook-to-finish wins, order non-deterministic** since hooks run in
  parallel, and `additionalContext` as **kept from every hook** (matches `hooks.md`'s own
  line "When several hooks return `additionalContext` for the same event, Claude receives all of
  the values").
- **`retry` (`PermissionDenied`) is its own standalone boolean field, not a value of
  `permissionDecision`, and Claude Code documents no merge rule for it at all.** Verified against
  the raw `hooks.md` decision-control table (row: `PermissionDenied` → key field `retry: true`,
  no `permissionDecision` listed) and the dedicated `PermissionDenied decision control` section:
  `hookSpecificOutput.retry: true` "tells the model it may retry the denied tool call"; it does
  not reverse the denial. Unlike `updatedInput` (documented last-wins) and `permissionDecision`
  (documented most-restrictive-wins), **no page documents what happens when multiple
  `PermissionDenied` hooks set `retry` to different values** — this is a genuine, unresolved gap
  in Claude Code's own documentation, not something this session failed to find. Nothing in this
  repo sets `retry` today (ceta's `handlePermissionDenied` always prints `{}` — see the finding
  folded into §1.3/§2.4 below), so this is about correctly scoping a _future_ delegate's risk, not
  a live bug. Consequence for the router's own design: see §2.4/§2.5 (contract + merge policy) —
  the router is the _sole_ `PermissionDenied` registrant, so it is not trying to replicate an
  unproven Claude-Code behavior here; it is free to pick, and does pick, its own safe policy.
- **`PostToolUse`'s `updatedToolOutput` and `PermissionRequest`'s `decision.updatedInput` are
  the _same collision shape_ as `PreToolUse`'s `updatedInput` — not risk-free.** `hooks.md`
  documents "Interaction with rewrites" for `PostToolUse`: _"Claude Code drops the note if that
  rewrite is rejected or another hook's rewrite replaces it"_ — i.e. a last-wins-style
  overwrite, structurally identical to the `PreToolUse` defect this whole design exists to fix.
  `PermissionRequest`'s `decision.updatedInput` (nested under `decision`, not top-level
  `hookSpecificOutput`) is not explicitly documented for multi-hook merge either way, but has the
  identical rewrite shape. This corrects an assumption floated at the end of the prior session
  (see §1.3): it is _not_ true that nothing outside `PreToolUse` has an "equivalent last-wins
  field" — two of ceta's other four registered events do. Only `SessionEnd` (no decision control
  fields at all — `hooks.md`: "No decision control. Used for side effects like logging or
  cleanup") and `PermissionDenied` (one field, `retry`, undocumented-merge as above) are actually
  risk-free-by-construction today.

### 1.3 What is explicitly _not_ being re-decided here

`tc-c2ijv`'s own filed description names three items as "already decided / in flight — do not
re-open here":

1. ceta's ordered `CETA_INPUT_PROCESSORS` list + payload identity env
   (`CETA_SESSION_ID`/`AGENT_ID`/`AGENT_TYPE`/`CWD`) — `tc-7m85u`, closed.
2. pg-wi-flow's identity processor rides **inside** that ceta-internal chain, not as an
   independent top-level hook — `tc-q25wo`.
3. ceta's input-processor chain runs only on `Approve`/`Ask`, never on `Abstain`/`NoOpinion` —
   ADR `0070`.

This matters for scoping: **ceta's own internal input-processor seam is a separate, already-solved
composition layer**, one level _below_ the router this document designs. The router composes
across independently-registered **plugins** (ceta as a whole is one delegate); it does not
replace or reach inside ceta's own already-decided internal rewrite chain. rtk and pg-wi-flow's
identity processor do not need to become separate router delegates — they already have a working
home inside ceta's chain.

**Honest scoping consequence**: as of this design, **ceta is the only plugin that actually
registers hooks today** — and it registers more than `PreToolUse(Bash)`. Its real
`hooks/hooks.json` (`claude-marketplace/claude-extended-tool-approver/hooks/hooks.json`, read
directly for this revision) declares **five** entries: `PreToolUse` with **no matcher** (fires on
every tool, not only Bash — this is how ADR `0049`/`0051`'s agent-config write-protection carve-out
for `Write`/`Edit`/`MultiEdit` actually gets enforced, per `docs/ARCHITECTURE.md`'s sequence
diagram), plus `PostToolUse`, `PermissionRequest`, `PermissionDenied`, and `SessionEnd`. An earlier
draft of this document scoped the router to `PreToolUse(Bash)` only and left the other four events
— and the unmatched breadth of `PreToolUse` itself — unaddressed; a completeness review caught
that migrating ceta as originally scoped would have silently dropped the write-protection carve-out
with no test catching it. §2.1 below now scopes the router to _all_ events, generating a
registration only where a live delegate needs one, specifically to close that gap.

**Confirmed by re-reading ceta's real handlers** (`packages/claude-extended-tool-approver/cmd/
claude-extended-tool-approver/main.go`, lines 129-311, `nix-agent-support`): of ceta's five
registered events, only `PreToolUse` (`handlePreToolUse`) does real decision work
(`permissionDecision`/`updatedInput`). The other four — `handlePermissionRequest`,
`handlePostToolUse`, `handlePermissionDenied`, `handleSessionEnd` — are **pure side-effect
handlers**: each writes rows into ceta's own `asklog` SQLite store (recording a request,
resolving an approval, registering a background shell, recording a denial, resolving unresolved
rows at session end) and then **unconditionally prints `{}`** — never `permissionDecision`, never
`updatedInput`, never even `additionalContext`. This is exactly the shape §2.4 below names
`observe`: invoked purely for its side effect, response always discarded, never merged. It is
_not_ `annotate` (`annotate` implies setting `additionalContext`; these four handlers never do).

There is still no live cross-_plugin_ collision to fix right now (only ceta registers anything).
This document's value is honestly two different things depending on the event, not one uniform
"prevents data loss" story, and both halves should be stated explicitly when presenting this ADR:

- For `PreToolUse` (real, exercised today) and, per §1.2's correction above, `PostToolUse` and
  `PermissionRequest` (unexercised by ceta today, but carrying the _identical_ last-wins rewrite
  shape the moment either field is ever used), the router removes a real defect class _before_ a
  second decision-making plugin can trigger it.
- For `SessionEnd` (no decision-control surface at all) and `PermissionDenied` (one field,
  `retry`, with no live use and no documented merge rule), the router's value is "one consistent
  registration point for ceta's full surface, and a deliberate, chosen policy for a field Claude
  Code itself leaves undefined" — not "prevents data loss," because there is no evidence of a
  Claude-Code-native loss mode on these two events to prevent.

Independent of the per-event collision story, migrating ceta's own registrations onto a composable
form covering its _full_ existing surface — not a subset — means a future second plugin does not
have to solve this from scratch, and the deliberate, build-time vendoring path for a _third-party_
plugin that wants to participate stands regardless of which events currently carry real collision
risk. It is infrastructure investment plus a faithful lift of ceta's existing five-event surface,
not an active-incident fix nor a scope reduction.

### 1.4 Existing infrastructure this design builds on, not around

`phillipg-nix-repo-base`'s `lib/claude-marketplace.nix` (`mkClaudePlugin`, `mkClaudeMarketplace`,
`mkDirectoryMarketplaceSettings`, documented in `docs/claude-marketplaces.md`, decision record
ADR-0010) already does most of "parse a plugin's manifest and produce a synthetic, version-stamped
plugin directory Claude Code will see" — in pure nix + `jq`, with real `cp -r` (never symlinks),
content-digest version stamping (`<declared>+<digest>`, never a repo git rev), and a proven
consumer-registration half (`phillipgreenii.programs.claude-code.marketplaces.*` in
nix-agent-support's `claude-marketplaces` HM module, which already registers 17 plugins this way,
including `ceta` itself). This design adds one new builder to that same file/family rather than
inventing a parallel packaging mechanism.

---

## 2. Decision

Build **one router plugin** that is the sole registrant, on this machine, for every hook event any
of its delegates needs (§2.1). It dispatches, per event and in explicit priority order, to a
**build-time-generated delegate list** — never a live/dynamic one.

### 2.1 Scope: every event is supported, but a registration is generated only where needed

Revised per operator direction (2026-09-16): "this implementation will need to support all hooks,
but only generate hooks in the hooks.json, if needed." The mechanism (event + matcher + command
triples, grouped and dispatched uniformly — §2.3-§2.5) is uniform across every hook event, since
that is what's required to lift ceta's actual five-event surface faithfully (§1.3) without
inventing a special case per event. But the GENERATOR only ever emits a `hooks.json` entry for an
event that has at least one live delegate; an event nothing has registered for produces no entry
at all. This is not the generic any-event abstraction the operator earlier cautioned against
over-solutioning for — there is no speculative support for events nothing uses yet, no
event-specific special-casing beyond what parsing each event's own matcher-target field requires
(§2.3), and no new configuration surface beyond the same `{name; command; contract; priority;
event; matcher; }` shape applied uniformly.

### 2.2 Registration model

**Router owns every hook registration it covers.** Its `.claude-plugin/plugin.json` /
`hooks/hooks.json` is the only manifest on this machine declaring any hook for an event that has a
router delegate (§2.1) — one entry per such event, never a per-plugin-per-event registration for
anything the router covers. It lives as a new plugin directory in nix-agent-support's existing
`claude-marketplace/` tree (`claude-marketplace/hook-router/`), riding the same
`mkClaudeMarketplace` pipeline every other plugin there already uses.

**Two distinct paths become a delegate, matched to trust level:**

- **Tools we author** (ceta today, across its real five-event surface — §1.3; a future second
  first-party decision-plugin) contribute to a shared HM option,
  `programs.claude-hook-router.delegates` (a list of `{ name; event; matcher; command; contract;
priority; }` submodules — `matcher` optional, absent meaning match-all for that delegate; the
  ordering mechanism this `priority` field denotes is **RESOLVED: the banded convention**, §5.3
  item 3/Phase C1 — the field name and wire shape here are unaffected, only the nix-level
  mechanism that produces the value changes), **instead of** declaring their own hooks for any
  event the router covers. The router's HM module reads the fully-merged option at _HM build
  time_ and renders both its own `hooks.json` entries (one per event with ≥1 delegate) and a
  static, ordered `router-config.json` the runtime binary reads. No runtime plugin-discovery
  mechanism is needed — HM already knows the complete delegate set at build time.
- **Third-party plugins** are never composed live. A maintainer who wants one included runs it
  through a new nix builder, `mkClaudeHookRouterPlugin` (§2.6), which vendors the plugin's source,
  extracts its hook command(s) across whichever events it declares, and adds them to the same
  generated `router-config.json`. This is a deliberate, versioned, pinned inclusion — the same
  trust model as any other vendored dependency in this workspace (gomod2nix lockfiles, flake input
  pins — see Phase A4 for the concrete mechanism) — never something that happens because an
  operator ran `/plugin install` in a live session. This is confirmed correct, not merely
  convenient: §1.2 established there is no lifecycle hook to react to a live install anyway, and
  auto-composing an arbitrary installed plugin's hook logic without review would be a real
  supply-chain risk.

**Consequence for updates**: since a vendored third-party delegate is never an "installed Claude
Code plugin" from Claude Code's own point of view, it never goes through `/plugin update`. Keeping
its pin current is our own responsibility (§4, Phase A/D) — the same shape as bumping a flake
input, on our own schedule.

### 2.3 Registration grouping and per-event matcher filtering

The router registers **one `hooks.json` entry per event that has ≥1 live delegate**, using the
**broadest matcher any of that event's delegates needs** — in practice, absent/match-all whenever
any delegate for that event wants match-all (as ceta's own `PreToolUse` does today). It does
**not** register multiple differently-matchered entries for the same event and rely on Claude Code
to pick the right one — that reintroduces exactly the collision this design exists to remove
(§1.2's bullet on whether differently-matchered same-event hooks still race — not independently
re-confirmed for that exact sub-case, but the general parallel-dispatch pattern strongly suggests
yes). Instead, filtering narrower than the registered matcher happens **inside the router's own
dispatch loop**, using each delegate's _original_ matcher (parsed at generation time straight out
of that delegate's own `hooks.json`, §2.6) evaluated against the **event-appropriate payload
field** — `tool_name` for `PreToolUse`/`PostToolUse`/`PostToolUseFailure`/`PermissionRequest`/
`PermissionDenied`, `end_reason` for `SessionEnd`, `session_start_reason` for `SessionStart`
(§1.2). This filter step is event-aware by construction, not a hardcoded `tool_name` check, since
the matcher-target field genuinely differs by event.

**The router must replicate Claude Code's own matcher semantics exactly** (§1.2): a matcher
containing only letters/digits/`_`/`-`/spaces/`,`/`|` is exact-string-or-pipe-alternation;
anything else is an unanchored regex; absent/`"*"`/`""` is match-all. **Risk flagged for
implementation**: the runtime binary is Go, whose standard `regexp` package is RE2-based and does
not support every JavaScript regex feature (lookahead/lookbehind, backreferences). A delegate's
matcher written assuming JS regex semantics could behave differently under the router's Go-based
filter than it would have under Claude Code's own native dispatch.

**A correctness review of this section's own test plan caught an overclaim worth correcting
here**: a Go unit test asserting "the router's evaluator agrees with Claude Code's documented
behavior" can only encode the _test author's own understanding_ of JS regex semantics — it
regression-locks the matcher strings actually exercised (which, per §5.4, are today all in the
exact/pipe-alternation subset, where RE2 and JS agree completely), but it cannot _prove_ engine
parity for a genuinely regex-shaped matcher, because nothing in this design puts a real matcher
string in front of real Claude Code to compare against (Tier 3, §4.0, never exercises a
regex-shaped matcher either). Given that, the actual mitigation is narrower and more honest than
"test for parity": **the generator (§2.6) should flag or reject any vendored delegate's matcher
that falls outside the exact/pipe-alternation subset** — i.e. anything that would be evaluated as
a regex — as needing explicit manual review before it's trusted, rather than silently accepting it
on the assumption that Go's RE2 and Claude Code's JS regex agree. Only a delegate matcher that
_is_ a genuine regex, reviewed and accepted, needs the compatibility check; the common case (every
matcher in use today) needs only the regression-lock test.

### 2.4 Delegate contract and dispatch

Each delegate declares a `contract` scoped to what its event actually supports. **Four types**
(revised this session — folded in from the last finding of the prior design session, not yet in
an earlier revision of this draft):

- `decide` — may set the event's own primary decision/directive field: `permissionDecision` for
  `PreToolUse`/`PermissionRequest`, or **`retry` for `PermissionDenied`** (correcting an earlier
  revision of this draft, which wrongly listed `PermissionDenied` under `permissionDecision` —
  per §1.2, `PermissionDenied` never supports `permissionDecision` at all; its only key field is
  `retry`).
- `rewrite` — may set `updatedInput` (`PreToolUse`) or `decision.updatedInput`/`updatedPermissions`
  (`PermissionRequest`) or `updatedToolOutput` (`PostToolUse`) — per §1.2's correction, this is
  _not_ `PreToolUse`-only; it applies to any event with a rewrite-shaped field, whether or not a
  live delegate uses it today.
- `annotate` — may set `additionalContext` — supported broadly.
- **`observe`** (new type) — invoked purely for its side effect; its response is always discarded
  and never merged. This is ceta's real shape on four of its five events today (§1.3): each of
  `handlePermissionRequest`/`handlePostToolUse`/`handlePermissionDenied`/`handleSessionEnd`
  unconditionally prints `{}` after writing to its own `asklog` store. It is a real, needed
  fourth type, not a degenerate case of `annotate` — `annotate` implies setting
  `additionalContext`, which these handlers never do.

A delegate may combine `decide+rewrite` (matching ceta's own `PreToolUse` shape today), but
`observe` is exclusive — a delegate declaring `observe` on an event contributes no other field.
`SessionEnd` has no decision-control surface at all (§1.2), so its delegates are `observe`-only by
construction, not merely "effectively" so. The generator (§2.6) should reject a delegate config
whose declared `contract` doesn't match what its `event` supports, rather than silently accepting
a no-op. Dispatch order **within one event's delegate list** is **priority ascending**, ties
broken by `name`.

- **Sequential, never parallel.** Each delegate receives the _current_ cumulative command text
  (the original, or a prior delegate's rewrite, for events where that concept applies) as
  `tool_input.command` on its stdin — the same JSON shape Claude Code would give it directly,
  since a given event's payload is not plugin-specific.
- **All-`observe` short-circuit.** If every delegate registered for a given event declares
  `observe`, the router does not run its merge computation for that event at all: it dispatches
  each observer sequentially (still sequential, for determinism and per-call budget accounting —
  see §2.5's total-budget bullet), discards every response, and returns `{}` unconditionally. This
  is a real simplification, not just vocabulary: today this applies to `PermissionRequest`,
  `PostToolUse`, `PermissionDenied`, and `SessionEnd` (ceta is `observe`-only on all four), leaving
  only `PreToolUse` running the full dispatch/merge loop below.
- **Abstain does not short-circuit.** A delegate returning `{}` (NoOpinion) contributes nothing
  to the merge and the router proceeds to the next delegate. This directly fixes the
  currently-measured defect where ceta's own Abstain (13.2% of calls on monorepod, per the 30-day
  `ceta report` breakdown already on file in `tc-c2ijv`) drops any `updatedInput` wholesale — under
  the router, an Abstain from one delegate no longer erases a later delegate's rewrite, because
  there is no "later" hook racing it; there is only the next step in one sequence.
- **A delegate that errors** (non-zero exit, or stdout that fails to parse as the expected
  `hookSpecificOutput` shape) is treated as Abstain for that call (logged, not fatal to the
  chain) — one misbehaving delegate must not silently veto every other delegate's contribution.
  **Confirmed by the operator, 2026-09-16**, over the alternative (a security-relevant delegate
  like ceta erroring instead warranting fail-closed `deny`) — Abstain is the settled choice, not
  merely this draft's proposal.

### 2.5 Merge policy (the router now owns this; Claude Code no longer arbitrates anything)

- `permissionDecision`: most restrictive across every delegate that expressed one, in the
  **strict** order `deny` > `defer` > `ask` > `allow` (corrected this session — an earlier
  revision of this draft had `ask`/`defer` tied, which is wrong: `defer` is more restrictive than
  `ask`). No delegate expressing one leaves the router with nothing to return for this field, in
  which case the router **must itself abstain** (return `{}`). This matches Claude Code's own
  documented multi-hook semantics for `PreToolUse` — verified against
  `https://code.claude.com/docs/en/hooks-guide.md` (§1.2), not `hooks.md` alone, which documents
  only which fields each event honors, not how they combine.
- `updatedInput` (`PreToolUse`), `decision.updatedInput` (`PermissionRequest`), `updatedToolOutput`
  (`PostToolUse`): whatever the cumulative rewrite chain landed on after the last `rewrite`-capable
  delegate ran (the original value if no delegate rewrote it) — deterministic **priority-order**
  last-wins. This deliberately mirrors Claude Code's own documented (§1.2) but
  **non-deterministic** last-_completes_-wins behavior for these fields, while removing the
  nondeterminism: under the router there is no parallel race, so "last" always means "last in
  priority order," reproducibly.
- `additionalContext`: concatenation of every delegate's `additionalContext`, in dispatch order —
  matches Claude Code's own already-documented "kept from every hook" behavior for this field, so
  delegates keep working unmodified here.
- **`retry` (`PermissionDenied` only): any delegate setting it `true` wins (OR-merge across the
  chain).** Resolves this bead's own resume item 1. Unlike `updatedInput`/`updatedToolOutput`
  (arbitrary data, where a merge would be meaningless and Claude Code itself picks one winner) and
  `permissionDecision` (safety-critical, where "most restrictive" is the conservative default),
  `retry` is a single low-stakes boolean: per `hooks.md`, setting it does not itself reverse or
  loosen the denial, it only tells the model it may attempt the call again — a call that goes
  through the exact same evaluation on retry. There is no real permission escalation to guard
  against by picking the _more_ restrictive (`false`) outcome, so OR-merge — matching the spirit
  of `additionalContext`'s own "combine, don't arbitrate" precedent rather than inventing an
  order-dependent pick — is the safer and more honest choice here. This is **not** an attempt to
  replicate an undocumented Claude Code behavior (§1.2: no page documents `retry`'s multi-hook
  merge at all): the router is the sole `PermissionDenied` registrant, so this is the router's own
  policy, chosen and justified here rather than left as an open question or silently defaulted by
  whatever the implementation happens to do first.
- **Total time budget: 6 seconds for the whole chain** (confirmed by the operator, 2026-09-16),
  split across delegates, not each delegate independently claiming its own full budget. This
  covers ceta's own existing documented worst case (up to ~3s rule chain + up to ~3s for one
  input-processor slot) with zero headroom left for a second delegate — chosen deliberately over
  a wider ceiling, so this number must be revisited, not silently assumed still correct, the day
  a second delegate is actually added (§5.4).

### 2.6 The generator: `mkClaudeHookRouterPlugin`

New builder, living beside `mkClaudePlugin`/`mkClaudeMarketplace` in
`phillipg-nix-repo-base/lib/claude-marketplace.nix` (or a same-directory sibling file, TBD at
implementation time), following that file's existing idiom exactly — pure-nix manifest reads via
`builtins.fromJSON`/`builtins.readFile`, a `runCommand` build script doing real `cp -r` (never
`ln -s`), `jq` for structured merges, and the same `<declared>+<digest>` content-derived version
stamping `stampVersion` already provides.

**Inputs**: a list of source entries, each `{ name; src; includeHooks; includeCommands;
includeAgents; includeSkills; includeMcp; }` (booleans default `false` except where the router's
own own-authored delegates want everything), where a source with `includeHooks = true` may
contribute **multiple** `{ event; matcher; command; contract; priority; }` tuples — one per hook
entry its own `hooks/hooks.json` declares — not just a single fixed `PreToolUse`/`Bash` tuple.

**Per source, in the build script**:

- If `includeCommands`/`includeAgents`/`includeSkills`: `cp -r` that source's resolved file set
  (respecting the already-verified manifest semantics — an explicit `commands`/`agents` field in
  the source's `plugin.json` **replaces** its own default directory scan; `skills` **adds to**
  it — the generator must replicate whichever resolution that source's own manifest specifies,
  not assume directory-scan for everything) into `$out/<surface>/<name>/…`, namespaced by `name`
  to prevent cross-source collisions now that Claude Code's own per-installed-plugin namespacing
  no longer applies (§1.2). **Unverified — flagged for Phase A**: whether Claude Code actually
  preserves that subdirectory nesting as part of a command's or agent's addressable identity,
  or addresses them by bare filename regardless of parent directory (in which case namespacing by
  subdirectory alone does not prevent two sources' same-named command from becoming
  indistinguishable to a user typing `/`). This needs the same empirical-first treatment as
  everything else in §1.2 before Phase A locks in "subdirectory" as the collision fix — the
  fallback, if subdirectories don't disambiguate, is renaming files (`<name>-<original-filename>`)
  rather than nesting them.
- If `includeMcp`: merge that source's `mcpServers` entries into `$out/.mcp.json`, each key
  prefixed `<name>-` to avoid raw key collisions across sources.
- If `includeHooks`: parse that source's `hooks/hooks.json` (or inline `plugin.json` `hooks`) in
  full — **every event it declares, not only `PreToolUse`/`Bash`** — extracting each entry's
  `{ event; matcher; command; }`. Rewrite each command's `${CLAUDE_PLUGIN_ROOT}` occurrences to
  the source's **post-merge** path (`${CLAUDE_PLUGIN_ROOT}/vendored/<name>/…`), and append
  `{ name; event; matcher; command; contract; priority; }` to the generated
  `router-config.json`, grouped by event.

**Per-event registration, after all sources are processed**: for each distinct event appearing
in the accumulated delegate list, compute the union/broadest matcher needed (match-all if any
delegate for that event wants match-all; otherwise the delegates' own matchers, since the router
filters internally regardless — see §2.3) and emit exactly one `hooks.json` entry for it. An event
with zero delegates gets no entry at all (§2.1's "generate only if needed").

**Output**: `$out` is a complete, version-stamped plugin directory
(`.claude-plugin/plugin.json`, a `hooks/hooks.json` with one entry per event that has ≥1 delegate
— all pointing at the router binary by bare name — the merged `commands`/`agents`/`skills`/
`.mcp.json` trees, and `router-config.json`) — indistinguishable in shape from any other plugin
`mkClaudeMarketplace` already knows how to fold into the marketplace tree.

### 2.7 The runtime: a new Go binary

New package (name TBD, e.g. `packages/claude-hook-router`), built with `mkGoApp`/gomod2nix per
this repo's established Go convention (never `vendorHash`/`buildGoModule`), riding `home.packages`
and referenced by **bare command name** in the generated `hooks.json` — the same pattern ceta and
pg-pr already use, specifically because a plugin-relative `bin/` reference is the exact shape that
gets silently dropped by Claude Code's directory-source cache copy (§1.2, `pg2-sikj3`).

Responsibilities: read `router-config.json` (path resolved relative to its own
`${CLAUDE_PLUGIN_ROOT}`, injected by Claude Code at invocation — this one is fine to depend on,
since the router genuinely is the plugin Claude Code is invoking), read the incoming hook JSON
from stdin, read `hook_event_name` from it to select that event's delegate list from
`router-config.json`, apply the per-delegate matcher filter (§2.3) against the event-appropriate
payload field, then run the sequential-dispatch/merge loop from §2.4–2.5 over the surviving
delegates, and emit one `hookSpecificOutput` JSON to stdout. An event with delegates registered
but none surviving the matcher filter for this specific call is a no-op (abstain).

**Per-delegate environment**: the router receives `CLAUDE_PROJECT_DIR` from Claude Code (available
on every event, not plugin-specific) and forwards it unchanged to each delegate it invokes — real
passthrough of a value the router already has, not faking anything. **Correction from an earlier
draft**: `CLAUDE_ENV_FILE` is _not_ available here — verified against
`https://code.claude.com/docs/en/hooks.md`, it is scoped only to `SessionStart`/`CwdChanged`/
`FileChanged`, and a `PreToolUse` (or any of the other events this router covers) hook never
receives it, so there is nothing to forward for that variable on any event this router currently
handles. If the router ever gains a `SessionStart`-family delegate in the future, `CLAUDE_ENV_FILE`
forwarding would need to be added specifically for that case, not assumed to already work. The
router assigns each delegate its own `CLAUDE_PLUGIN_DATA` subdirectory
(`<router's own data dir>/vendored/<name>/`) so vendored delegates' persisted state cannot collide
with each other or with the router's own. `CLAUDE_PLUGIN_OPTION_<KEY>` forwarding is **out of
scope for v1** unless a specific vendored delegate is found to need it (no current delegate does).

---

## 3. Alternatives considered

- **Fold everything into ceta itself (ceta stays "the router").** Rejected: ceta's own scope is a
  rule-chain gate with a specific, already-documented threat model (ADR-0053) and an
  already-decided internal input-processor seam (§1.3). Overloading it with generic multi-plugin
  dispatch conflates two different composition layers (within-ceta rewrite chaining vs.
  across-plugin decision composition) and makes ceta's own threat model harder to reason about.
  ceta becomes delegate #1 of the router instead — its internals are untouched.
- **Live/dynamic third-party composition** (react to `/plugin install`). Rejected: no lifecycle
  hook exists to react to (§1.2), and even if one did, auto-composing an unreviewed plugin's hook
  logic into a permission-deciding chain would be a supply-chain risk we do not want by default.
- **Runtime filesystem discovery of installed plugins' manifests** (parse
  `~/.claude/plugins/installed_plugins.json` at dispatch time instead of build time). Rejected:
  that file is documented as private, undocumented state (no official API), and reading it at
  every hook invocation adds latency and a fragile dependency on an internal format Anthropic
  could change without notice. Build-time composition via HM/nix, which this workspace already
  does for everything else, is both faster and more stable.

---

## 4. Implementation plan

Phased; each phase should land as its own bead/PR, gated on the previous phase's tests passing.
Order matters — B depends on A's output shape being stable; C depends on both.

### 4.0 Testing strategy: three tiers

Not everything in this design can be tested with nix alone, and this section says so explicitly
rather than leaving the boundary implicit — a prior draft of this plan gestured at test coverage
per-phase without stating which TOOL verifies which CLAIM, which is what a test-coverage review
flagged as thin. The system decomposes into three tiers, each verified with the tool actually
suited to it, all still gated at land time:

- **Tier 1 — the generator (§2.6), pure nix, exhaustively testable.** Everything
  `mkClaudeHookRouterPlugin` does is a deterministic, side-effect-free function of its inputs:
  parse manifests, merge/namespace files, extract and rewrite hook commands, compute per-event
  matcher unions. No network, no live Claude Code, no nondeterminism — this is nix's home turf,
  proven out already by `lib/claude-marketplace-tests.nix`. A build-check derivation can assert
  anything about `$out` (file existence, no-symlink-outside-`$out`, exact generated JSON), and a
  `builtins.tryEval`/intentionally-failing-build pattern proves malformed input is _rejected_, not
  silently mishandled. This tier should aim for real exhaustiveness — see Phase A2's expanded case
  list below.
- **Tier 2 — the runtime dispatch/merge logic (§2.4-§2.5, §2.7), Go's own test suite, gated by
  `nix flake check` but not written in nix.** Nix was never the language for testing "does the
  router correctly ignore a hung delegate" — Go is, the same way ceta's entire rule-chain engine
  is tested today with plain `go test`, not nix expressions. **Confirmed, not just analogous**
  (correctness review): ceta's own Go tests are gated in `nix-agent-support/flake.nix` via
  nix-repo-base's `mkGoTest` builder (`lib/go-builders.nix`), run as a nix derivation with
  `-race` on by default — this is the exact shape proposed for the router's own tests, and the new
  package can reuse `mkGoTest` directly rather than inventing a new gating mechanism. The router's
  whole contract is "JSON in on stdin, JSON out on stdout," fully within our control, so every
  scary-sounding runtime case is mechanically constructible with a stub delegate process and Go's
  stdlib (`os/exec`, `context.WithTimeout`) — none of it needs Claude Code running (with the one
  scoped exception §5.4 and Phase B1 note: matcher-evaluation testing regression-locks known-safe
  matchers, it doesn't prove Go-RE2/JS-regex parity for a hypothetical regex one). See Phase B1's
  expanded case list below.
- **Tier 3 — real Claude Code behavior, irreducibly empirical, and deliberately small.** Whether
  Claude Code actually namespaces a subdirectory command, actually drops an external symlink,
  actually fires the generated hook — none of this is nix-sandbox-testable (no network, no way to
  purely-evaluate an external closed binary's behavior), and it doesn't need to be: it's exactly
  the technique this whole design already used to establish every fact it rests on — a scripted
  throwaway `claude -p` session. `claude plugin validate ./result` is the one piece of this tier
  that IS local/no-network and belongs inside a nix check (Phase A3); the rest is a small, named,
  explicitly non-hermetic validation script (Phase B4), run deliberately (once per Claude Code
  version bump or generator change), not folded silently into "tests exist" and not treated as a
  gap because it isn't sandboxed.

### Phase A — nix builder (`phillipg-nix-repo-base`)

- **A1.** `lib/claude-marketplace.nix` (or a new sibling file, e.g. `lib/claude-hook-router.nix`,
  imported from the same factory): implement `mkClaudeHookRouterPlugin` per §2.6. Extend the
  factory's returned attrset (`{ mkClaudePlugin; mkClaudeMarketplace; mkDirectoryMarketplaceSettings;
mkClaudeHookRouterPlugin; }`).
- **A1a. Verify command/agent subdirectory namespacing empirically before locking in the merge
  strategy** (§2.6's flagged gap) — a throwaway `claude -p` session installing a plugin with two
  same-named commands in different subdirectories, confirming whether Claude Code disambiguates by
  path or collides on filename. This determines whether A1 nests files under `<name>/` or renames
  them `<name>-<file>` — get this answer before, not after, writing the merge logic.
- **A2.** Nix tests, following `lib/claude-marketplace-tests.nix`'s existing convention (matching
  its rigor, not thinner — a test-coverage review confirmed that suite is pure-eval `lib.runTests`
  with no build-time or live-Claude-Code check today, so this bar is achievable), with new
  fixtures under `lib/tests/` (sibling to `lib/tests/claude-marketplace-fixture`) covering:
  - single-source passthrough (degenerate case, one delegate);
  - multi-source namespacing (two fixture sources each shipping a same-named command — assert
    both survive, distinctly, per whichever strategy A1a settles on);
  - multi-**event** grouping (a fixture source declaring hooks on 2+ different events — e.g.
    mirroring ceta's real shape: an unmatched `PreToolUse` plus a `SessionEnd` — assert the
    generated `hooks.json` gets one entry per event, and an event no source declares gets none);
  - matcher parsing/union (fixture sources with an exact-string matcher, a pipe-alternation
    matcher, a regex-shaped matcher, and an absent matcher on the same event — assert the
    generated registration matcher is the correct union and each delegate's _original_ matcher
    survives into `router-config.json` unchanged, for the runtime's own filtering to use);
  - **matcher-syntax boundary cases**: a matcher that's only whitespace, a matcher of a single
    `-` or `,` (charset-legal but semantically odd), a matcher that looks plain-charset but is
    meant as a regex (or vice versa) — assert the exact/regex classification rule (§2.3) is
    applied consistently, not guessed differently in different code paths;
  - **matcher union when two delegates on one event have DIFFERENT specific (non-match-all)
    matchers** — this is a real, currently-undecided design choice, not just a test gap: does the
    generator fall back to registering match-all for that event (simplest, always correct, but
    means the router receives and must filter every call even when neither delegate's specific
    matcher would have fired), or does it compute an actual regex union (`(r1)|(r2)`, tighter but
    another place to get regex-dialect compatibility wrong, §2.3's Go-RE2-vs-JS-regex risk)? Pick
    one explicitly in Phase A1 and test that exact behavior — don't leave both looking plausible;
  - **both `hooks.json` forms**: a source declaring hooks via a standalone `hooks/hooks.json`
    AND a source declaring them inline in `.claude-plugin/plugin.json`'s own `hooks` key — assert
    the generator extracts identically from both, since a vendored third-party plugin could use
    either;
  - **one matcher entry with multiple commands** (confirmed possible — Claude Code's own hook
    dispatch already treats a single hooks array with several `{type:"command",...}` entries as
    multiple hooks for the same matcher): assert the generator expands each into its own separate
    delegate in `router-config.json`, not one delegate with a multi-command string;
  - `${CLAUDE_PLUGIN_ROOT}` placeholder rewriting, including the placeholder appearing **more
    than once** in one command string, and a command string containing **no** placeholder at all
    (must pass through unchanged, not error) — not just the single-occurrence case;
  - **a real-copy assertion** (assert no symlink anywhere under `$out` resolves outside `$out`
    itself — the one property §1.2 says is load-bearing for Claude Code compatibility);
  - **negative/adversarial cases** (test-coverage review): a malformed source `plugin.json`
    (invalid JSON / missing required field), `includeHooks = true` with no `hooks/hooks.json`
    present, two sources whose MCP server names collide even after `<name>-` prefixing, a source
    whose `commands` field names a nonexistent directory, two sources sharing the exact same
    `name` (a config error, not a data error — should fail fast at the nix level before any
    file operation runs), **a delegate config whose declared `contract` doesn't match what its
    `event` supports** (§2.4's stated generator obligation had no test in an earlier revision —
    e.g. a `SessionEnd` delegate declaring `rewrite`, which that event cannot support), and **a
    vendored delegate's matcher falling outside the exact/pipe-alternation subset** (a
    regex-shaped matcher — per the correctness note above, the generator should flag/reject this
    for manual review rather than silently trust Go-RE2/JS-regex parity) — each asserting a clean
    build-time error or an explicit flagged-for-review output, not a silently-wrong `$out`.
- **A3.** A `checks.*` derivation that builds the generated plugin and runs
  `claude plugin validate ./result` against it (test-coverage review: `docs/claude-marketplaces.md`
  already documents this as a manual build-inspection step but nothing wires it into automated
  checks today for the _existing_ builders either — this is a place to add rigor beyond precedent,
  not just match it, since this builder's failure mode — the symlink-drop behavior — is exactly
  the kind of thing a pure-eval nix test cannot catch). **Confirmed local/no-network** (correctness
  review, against `plugins-reference.md`: validate checks manifest schema only). **One preflight
  concern the confirmation doesn't cover**: whether the `claude` binary's own process startup
  (telemetry, an update check) stays silent inside a network-less nix sandbox regardless of what
  `plugin validate` itself does — worth an explicit check (e.g. the relevant
  `DISABLE_TELEMETRY`/update-check env var, if one exists) before wiring this into `checks.*`,
  not assumed.
- **A4.** Update `docs/claude-marketplaces.md` with a new "Pattern 3 — merge several plugins'
  surfaces into one (the hook-router case)" section, and explicitly document the `src` input's
  actual fetch mechanism (UX review flagged this as unspecified in the draft — resolve it here:
  the working assumption, consistent with this workspace's existing vendored-dependency trust
  model, is a flake input per vendored third-party plugin, pinned and updated the same way any
  other flake input is bumped — confirm this is the intended mechanism before A1 is implemented,
  since it shapes the builder's `src` parameter type).

### Phase B — router runtime (`phillipgreenii-nix-agent-support`)

- **B1.** New package implementing the dispatch/merge loop (§2.7). Config: reads
  `router-config.json`. Concrete behaviors needing explicit unit-test coverage (the total-budget
  number is now resolved — 6s total, §2.5/§5.4 — so nothing here is blocked on it):
  - Event selection from `hook_event_name`, then per-delegate matcher filtering against the
    event-appropriate field (§2.3) before dispatch.
  - **Matcher evaluation, scoped honestly** (§2.3's correctness-reviewed correction): a table test
    covering every matcher string actually in use today (all exact-string/pipe-alternation/absent
    — where Go's RE2 and Claude Code's JS regex agree completely) is a real regression lock, not a
    parity proof — no test in this plan puts a genuinely regex-shaped matcher in front of real
    Claude Code to compare against, so a claim of "verified engine parity" would be false. The
    actual mitigation is §2.6's generator-side flag/reject for any matcher outside that safe
    subset (tested in A2), not an attempt to prove Go-RE2/JS-regex equivalence here.
  - Sequential dispatch in priority order; cumulative rewrite passthrough between delegates —
    explicitly covering **all three** rewrite-shaped fields this design's corrected §1.2/§1.3
    scoping identifies, not just `PreToolUse`: `updatedInput` (`PreToolUse`), `updatedToolOutput`
    (`PostToolUse`), and `decision.updatedInput` (`PermissionRequest`). §2.5's last-wins rule is
    uniform across all three; the test suite should say so explicitly rather than only naming
    `PreToolUse` the way an earlier revision did.
  - Abstain-continues (not short-circuit).
  - Errored/malformed delegate output treated as Abstain for that delegate, chain continues.
  - Merge policy table: **commit to exhaustive coverage of `permissionDecision` across every
    ordered pair for a 2-delegate chain — 25 cases (`{allow,ask,deny,defer,abstain}²`)**
    (corrected this session: an earlier revision's "16 cases" used a 4-value domain that dropped
    `defer` entirely, silently undoing §2.5's own correction that `defer` is a distinct rank
    between `deny` and `ask`, not tied with `ask` — the exhaustive table must actually cover the
    rank the correction was about) — plus a documented property-based strategy ("most restrictive
    wins, strict order `deny > defer > ask > allow`" as the checked invariant) for chains of 3+
    delegates, rather than leaving "property-test or exhaustive table" as an unresolved choice for
    the implementer (test-coverage review: the draft gestured at this without committing to either
    approach or a case count).
  - Total-budget enforcement across the whole chain (not per-delegate) — including a test that
    actually simulates a delegate exceeding its slice (a stub sleeping past budget), not just an
    assertion on a config value.
  - Total-failure fallback: if the router process itself cannot run any delegate (e.g.
    `router-config.json` unreadable), it must **fail safe** — return `{}` (abstain), never a
    silent deny/hang that blocks every Bash call on this machine. _(Flagged — see §5.4: this
    needs to be a stated requirement, not an implicit assumption.)_
  - **Adversarial delegate behavior** (test-coverage review, corrected by a correctness review's
    findings below): a delegate that hangs past budget — an actual sleep-past-timeout stub via
    `context.WithTimeout` + `os/exec`, asserting the router still returns within its overall
    budget, **and explicitly setting `cmd.WaitDelay`** (Go's own documented mechanism for the case
    where a killed child leaves pipe file descriptors open — e.g. via a grandchild process — which
    can otherwise hang `cmd.Wait()`/a pipe read past context cancellation; a naive
    `StdoutPipe()`-based implementation is exactly the shape that hits this, so this needs calling
    out explicitly rather than assumed away); a delegate writing non-JSON to stdout; a delegate
    writing schema-valid-but-wrong-shape JSON; a delegate writing valid JSON with unexpected
    **extra** fields (must be ignored gracefully, not treated as malformed — a forward-compat
    case, not just a strictness case); a delegate killed mid-write (SIGKILL) **or that writes a
    partial JSON fragment and then hangs** — both block identically at the `io.Read()` level and
    both require the same context-triggered process kill to unblock, differing only in the
    resulting error shape (parse/unexpected-EOF vs. a plain read error); both must map to Abstain,
    worth two assertions in one test scenario rather than two separate mechanisms (an earlier
    revision of this plan overstated them as distinct); a delegate command that doesn't exist /
    isn't executable (the `os/exec` "binary not found" error path — must degrade the same as any
    other errored delegate, not panic or exit the whole router process).
  - **Cross-process data isolation, not a data race** (correctness review: an earlier revision
    mischaracterized this as something `go test -race` would catch — it wouldn't, since `-race`
    instruments goroutines within one instrumented binary and cannot see two separate OS
    processes, and since each delegate's `CLAUDE_PLUGIN_DATA` subdirectory is distinct by
    construction there is no shared-memory race to detect in the first place). The actual test:
    spawn two real, separate router process invocations concurrently (simulating two overlapping
    Bash tool calls in one session) and assert their filesystem writes land in their own
    subdirectories with no cross-contamination — a filesystem-isolation assertion, run as an
    ordinary (non-`-race`) integration-style Go test.
  - **Partial-chain failure semantics — RESOLVED (operator, 2026-09-16): return the MERGE of
    whatever the prior delegates already contributed**, not discard the whole call's result, when
    a delegate mid-chain (not the first, not the last) blows the total budget or errors.
    Consistent with the abstain-continues philosophy elsewhere in this design — a broken delegate
    mid-chain shouldn't erase correctly-completed prior work any more than an Abstain does. Lock
    this in with a test per this bullet's original intent.
  - **Attribution/observability, with a concrete format** (a completeness review of this section
    caught that an earlier revision claimed this was "decided here" without actually deciding
    it): one JSON-lines entry per delegate invocation, written to a rotating log file under the
    router's own `CLAUDE_PLUGIN_DATA` (never to stdout, which is reserved for the final merged
    `hookSpecificOutput`), with at minimum `{ timestamp; hook_event_name; delegate_name;
contract; verdict; duration_ms; }`. UX and completeness reviews both independently flagged
    that without this, nobody can determine after the fact which delegate produced a given merged
    decision — this bullet is what actually closes that gap, and B2 must include a test asserting
    the log is written correctly for a representative multi-delegate call.
- **B2.** Unit tests per the bullets above.
- **B3.** A bats end-to-end test: a small chain of 2-3 stub shell delegates (one `decide`, one
  `rewrite`, one `annotate`) run through the real built binary, asserting the final stdout matches
  the expected merged `hookSpecificOutput` for a handful of representative input scenarios
  (including one where an early delegate abstains and a later one still contributes, and one
  spanning two different events to confirm event-selection routes correctly). **Must include a
  persisted regression scenario for ceta's write-protection shape specifically** (a completeness
  review caught that this was otherwise checked only once, manually, during the C2/C3 migration
  step, with nothing to catch a later regression): a stub delegate registered on an unmatched
  `PreToolUse` (match-all, mirroring ceta's real registration — §1.3) that denies a `Write`/`Edit`
  call, asserted end-to-end through the router the same way the generic decide/rewrite/annotate
  scenarios are — this is what actually stands in for "ADR 0049/0051's carve-out still works" as
  an automated check, not just a migration-day manual verification.
- **B4. Tier 3 (§4.0) — a named, scripted, deliberately non-hermetic validation, not a
  `checks.*` gate.** Install the Phase A-generated plugin into a real Claude Code instance and
  confirm the router hook actually fires end-to-end for at least one real tool call, the same way
  this whole design's factual claims were established (a throwaway `claude -p` session). This
  cannot live inside a sandboxed nix build (no network, no way to purely-evaluate an external
  binary's behavior) and isn't meant to — commit it as a runnable script
  (e.g. `scripts/validate-hook-router-live.sh`) with a documented run cadence (before landing a
  generator change, and after any Claude Code version bump), so it isn't silently skipped just
  because it can't be a `checks.*` entry. Test-coverage review flagged that nothing in the draft
  otherwise exercises real Claude Code at all, despite the entire design resting on
  empirically-measured Claude Code behavior that could be subtly wrong.
- **B5.** Wire the Tier 1 and Tier 2 suites — A2, A3 (`claude plugin validate`, which IS local/
  no-network and belongs here), B2, B3 — into each repo's `checks.*` (both repos have
  `.pre-commit-config.yaml`/`flake.nix` per their own CLAUDE.md land-time gates). **B4 is
  deliberately excluded from this gate** (§4.0, Tier 3) — conflating it with the sandboxed suite
  would either silently skip it (defeating its purpose) or break the sandbox (no network). Test-
  coverage review flagged that "tests exist" and "tests gate `nix flake check`" are different
  claims the draft conflated; this bullet makes both the inclusion and the deliberate exclusion
  explicit.

### Phase C — HM wiring + ceta migration

- **C0. Blocking prerequisite, promoted from §5.3's open question — RESOLVED (operator, 2026-09-16):
  graceful degradation.** ceta's HM module branches on whether the router is enabled: uses the
  router when enabled, falls back to its own direct hook registration otherwise. This keeps a real
  rollback story ("disable the router, fall back to ceta's direct registration") available at all
  times, at the cost of maintaining two registration code paths in ceta's HM module.
- **C1.** New HM module `home/programs/claude-hook-router/default.nix`:
  `programs.claude-hook-router.enable`, `programs.claude-hook-router.delegates` (the shared
  option, §2.2). **Ordering mechanism — RESOLVED (operator, 2026-09-16): adopt the banded
  convention.** This workspace already solved "flat list, multiple contributors, ordering" once
  before — `docs/adr/0020-status-line-parts.md` rejected bare per-entry priority fields in favor
  of a banded `mkOrder`/`mkBefore`/`mkAfter` convention — and this design adopts the same
  convention for `delegates` ordering rather than diverging from precedent. **Implementation note
  for Phase C1**: every `priority` field referenced elsewhere in this document (§2.2, §2.4, §2.6,
  §2.7's `router-config.json` shape, Phase A2's test fixtures, Phase B1's dispatch-order tests)
  denotes the _ordering value_ a delegate contributes — at implementation time, re-read
  ADR-0020's exact mechanism and produce/consume that banded value instead of a bare arbitrary
  integer; the dispatch model itself (ascending order within one event's delegate list, ties
  broken by `name`) is unchanged by which mechanism produces the ordering value. Builds the plugin
  via `mkClaudeHookRouterPlugin`, registers it into the existing marketplace pipeline exactly like
  every other in-repo plugin, and puts the router binary on `home.packages` (co-gated on
  `claude.enable`, matching precedent).
- **C2.** Migrate ceta's own HM module (`home/programs/claude-extended-tool-approver`) to
  contribute to `programs.claude-hook-router.delegates` **instead of** declaring its own hooks —
  covering ceta's **full real five-event surface** (`PreToolUse` with no matcher, `PostToolUse`,
  `PermissionRequest`, `PermissionDenied`, `SessionEnd` — §1.3), not only `PreToolUse(Bash)` as an
  earlier draft of this plan scoped it. This is the change that actually closes the completeness
  review's most severe finding (ADR 0049/0051's write-protection carve-out going dark) — verify
  post-migration that a `Write`/`Edit`/`MultiEdit` call still round-trips through ceta's rule
  chain via the router, as an explicit acceptance check for this bullet, not an assumption.
  Gated on `programs.claude-hook-router.enable` per C0's now-resolved graceful degradation: ceta's
  HM module keeps its own direct registration as the fallback path.
- **C3.** A migration runbook step (UX review): after C2, an explicit command/check the person
  running the migration runs to confirm ceta's hooks still fire correctly under the router —
  closing the "silent foot-gun" gap where forgetting to enable the router could leave ceta
  unprotected with no visible symptom until something slips through.

### Phase D — docs + ADR landing

- **D1.** After operator approval of this design, finalize and commit this document as
  `docs/adr/0071-claude-code-hook-router.md` and add its row to `docs/adr/index.md`.
- **D2.** File the implementation beads for phases A-C (this plan's own bullets, each phase as a
  parent with per-bullet children), linked back to `tc-c2ijv` and to each other in dependency
  order. Also file, as siblings rather than silently folding into an unrelated bead (completeness
  review):
  - a bead to actually wire rtk into ceta's `inputProcessors` chain — rtk today is a bare
    `home.packages` entry with no wiring into that seam at all (confirmed by reading
    `home/programs/rtk/default.nix`), so "rtk's migration path" is currently an assertion in this
    document, not a deliverable, unlike pg-wi-flow's identity processor which already has one
    (`tc-q25wo`). Either file this bead, or amend this document to state rtk migration is
    explicitly out of scope and why, before treating the bead's "migration path for … rtk"
    requirement as satisfied.
  - documentation updates beyond `docs/claude-marketplaces.md` (A4) and this ADR: `ceta`'s own
    `packages/claude-extended-tool-approver/README.md`, `docs/ARCHITECTURE.md` (whose sequence
    diagram currently shows Claude Code invoking ceta's hooks directly — becomes inaccurate post
    C2), and the `inputProcessors` option doc-comment in
    `home/programs/claude-extended-tool-approver/default.nix` (currently states rewriting
    "MUST enter through this list instead of registering a hook of its own" — no longer strictly
    true once the router exists as an alternative registration path for _decision-making_
    delegates, even though it remains true for rewrite-only processors within ceta's own chain).

---

## 5. Consequences

### 5.1 What this fixes

- Removes the `updatedInput`-collision defect class (§1.2, upstream #15897) — and, per this
  session's correction, the _same-shaped_ `PostToolUse`/`updatedToolOutput` and
  `PermissionRequest`/`decision.updatedInput` collisions — _before_ any of them can bite. All
  three are currently latent (only one real plugin registers today, and it doesn't exercise the
  latter two fields), but guaranteed to bite the moment a second decision-making plugin is added
  without this.
- Gives `retry` (`PermissionDenied`) a deliberate, justified merge policy (§2.5: any-true wins)
  where Claude Code itself documents none — closing what would otherwise be a second `#15897`-
  shaped gap the moment any delegate actually starts setting `retry`.
- Fixes the measured Abstain-drops-context defect (13.2% of calls) for any delegate chain that
  exists under the router — though note this defect currently lives _inside_ ceta's own
  already-decided input-processor seam (§1.3), which this router does not touch; the router only
  fixes it for delegates composed _at the router's own layer_.
- Gives a deliberate, reviewed, versioned path for adding a third-party plugin's hook logic
  without trusting Claude Code's own (broken) multi-hook merge.
- **Lifts ceta's full five-event registration faithfully** (§1.3, §2.1, Phase C2) — including the
  unmatched `PreToolUse` that backs ADR 0049/0051's write-protection carve-out — rather than the
  `PreToolUse(Bash)`-only subset an earlier draft scoped, which a completeness review identified
  would have silently dropped that protection with nothing to catch it.
- Provides router-level decision attribution (Phase B1's per-call, per-delegate log) closing a
  debuggability gap both the UX and completeness reviews independently flagged as otherwise
  absent from the design.

### 5.2 What this does not fix / explicitly leaves alone

- ceta's own internal input-processor chain (rtk, pg-wi-flow identity) is unchanged — it remains
  ceta's own already-decided mechanism, one layer below this router.
- No live reaction to `/plugin install`/`update` for vendored third-party delegates — confirmed
  impossible given Claude Code's current hook vocabulary (§1.2), and treated as a feature (forces
  deliberate review) rather than a gap. This does create a new maintenance obligation: keeping a
  vendored third-party delegate's pin current is now our job, not Claude Code's `/plugin update`.
  Phase A4 proposes a flake-input pin per vendored source as the concrete mechanism — a UX review
  flagged the original draft as leaving this unspecified entirely.

### 5.3 Reviewer questions — all four RESOLVED (operator, 2026-09-16)

1. ~~Graceful degradation vs. hard dependency~~ **RESOLVED: graceful degradation.** See Phase C0.
2. ~~`CLAUDE_PLUGIN_DATA` per-delegate isolation now vs. deferred~~ **RESOLVED: keep it now.**
   Cheap to build correctly the first time (a subdirectory-naming convention); retrofitting it
   later, once a second delegate already assumes a shared directory, is the harder direction. No
   design change from §2.7's original text — this confirms it rather than deferring it.
3. ~~Ordering mechanism~~ **RESOLVED: adopt the banded convention** (ADR-0020's
   `mkOrder`/`mkBefore`/`mkAfter`, not a bare priority integer). See Phase C1's implementation
   note.
4. **Command/agent subdirectory namespacing** (§2.6, Phase A1a) — **not an operator decision**;
   remains an empirical question for Phase A1a's throwaway `claude -p` test to answer before A1's
   merge logic is implemented, per §2.6's own text. Listed here only for completeness of what
   this section originally covered.

### 5.4 Numeric/behavioral questions — RESOLVED (operator, 2026-09-16) except one

- ~~The `retry` (`PermissionDenied`) multi-hook merge question~~ **RESOLVED this session** (this
  bead's own resume item 1): Claude Code documents no merge rule for `retry` at all (§1.2); the
  router adopts its own any-true-wins policy (§2.5), reasoned from `retry`'s low-stakes,
  non-escalating semantics. Folded into §2.4 (new `observe` contract type + corrected
  `PermissionDenied` field mapping) and §2.5 (merge policy).
- ~~The router's total per-call time budget~~ **RESOLVED: 6s total**, split across delegates.
  This covers ceta's full existing documented worst case (up to ~3s rule chain + up to ~3s for
  one input-processor slot) with zero headroom left for a second delegate — deliberately chosen
  over a wider ceiling with headroom, so this number should be revisited (not silently assumed
  still correct) the day a second delegate is actually added, per this workspace's
  premise-freshness conventions. Folded into §2.5's total-time-budget bullet.
- ~~The errored-delegate-treated-as-Abstain rule~~ **RESOLVED: keep Abstain**, as originally
  drafted in §2.4 — the operator confirmed this over the fail-closed-`deny` alternative.
- **Go `regexp` (RE2) vs. Claude Code's JS-regex matcher semantics** (§2.3) — **not an operator
  decision to make now**; the draft already commits to a concrete mitigation (the generator
  flags/rejects any vendored matcher outside the exact/pipe-alternation subset for manual review,
  §2.6, tested in Phase A2) rather than leaving the choice open. Left here only as a standing
  implementation-time reminder that no test in this plan can prove Go-RE2/JS-regex parity for a
  genuinely regex-shaped matcher — that mitigation, not a parity test, is the actual answer.
- ~~Partial-chain failure semantics~~ **RESOLVED: return the merge of whatever prior delegates
  already contributed**, not discard the whole call's result. Consistent with the
  abstain-continues philosophy elsewhere in §2.4 — a broken delegate mid-chain shouldn't erase
  correctly-completed prior work any more than an Abstain does. Folded into Phase B1's own
  bullet on this exact question.
- **Fail-safe default on total router failure** (Phase B1): confirmed as a stated requirement
  (return `{}`/abstain if the router itself cannot run any delegate — e.g. an unreadable
  `router-config.json` — never a silent deny/hang blocking every Bash call machine-wide), but not
  yet backed by an implementation, only a plan-level intent. Not a judgment call — this is a
  Phase B1 implementation obligation, not something needing operator sign-off. The one item on
  this list that stays a to-do rather than a resolved question.
