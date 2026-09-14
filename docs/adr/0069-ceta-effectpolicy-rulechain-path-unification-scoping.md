# CETA: scoping HOW to unify `internal/effectpolicy`'s path resolver with production's RuleChain

**Status**: Proposed
**Date**: 2026-09-14
**Deciders**: autonomous design pass (bead `tc-uxknt`); no operator was in the loop for this call —
this ADR scopes options and recommends a direction, it does not itself decide which option ships.
The WHETHER question (should this unification happen at all) is already decided — see "Related
Decisions" — this document exists only to answer the HOW/WHEN question the operator left open.

> This ADR does not implement anything. It is the scoping/design artifact `tc-uxknt` asked for:
> read the actual current code on both sides, lay out what unifying them could mean, recommend an
> approach, and sketch phases suitable as input to a later `plan-decompose`/`epic-decompose` pass.
> No source file under `internal/effectpolicy/`, `internal/rules/*`, `internal/engine`, or
> `internal/setup` is touched by this ADR.

## Context

### Two independent, already-diverging encodings of "what access does this path have"

**Production** (this repo's primary branch, what actually runs as the Claude Code PreToolUse
hook): path access is computed by `internal/patheval.PathEvaluator`, a single hardcoded
zone-classification ladder (`classify()`, `internal/patheval/evaluator.go:351-433`) that walks
~9 built-in zones in a fixed `if`-chain (project root, `WORKSPACE_ROOT`, `/tmp`, sandbox
`allowWrite`, `/nix`, `~/.claude`, `~/go/pkg`, Gradle home, XDG data-home subpaths, then
env-configured extra roots) and returns one of four ordinal values (`PathReject` / `PathUnknown`
/ `PathReadOnly` / `PathReadWrite`). Secret-path recognition is a **separate**, unrelated
mechanism: `internal/secretpath.Classify`, a purely lexical scan (directory-component and
basename matching) with no notion of zones at all. There is **no deletability concept in
production whatsoever** — `internal/rules/pathsafety/pathsafety.go:241-307` treats the `Delete`
tool identically to `Write`/`Edit`/`MultiEdit`, gating all four on the same `access.CanWrite()`
predicate. `internal/deletable` (Kind/Resolve, the "third access class" ADR 0068 generalizes) does
not exist on this branch at all — it is spike-only.

Production has **no single path-resolution call site**. `internal/setup/factory.go`'s `RuleChain`
(the one ordered, first-match-wins list of ~25 `hookio.RuleModule`s — see that file's own
extensive doc comment on why it is deliberately the single source of truth for ordering) hands
every path-consuming rule module its own reference to the _same_ `*patheval.PathEvaluator`
instance, and each rule calls into it independently, at the call shape it happens to need:

| Rule module                                                     | What it calls                                                                                                                                                                                       |
| --------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pathsafety`                                                    | `eval.IsDenyRead`/`IsDenyWrite`, `eval.Evaluate(path).CanRead()/.CanWrite()`, plus its own `isAgentConfigPath`/`isAgentHooksPath`/plugin-hooks predicates (ADR 0041/0049/0051)                      |
| `deniedroots`                                                   | `eval.MatchedDeniedRoot`                                                                                                                                                                            |
| `secrets`                                                       | `secretpath.Classify`/`IsSecret` directly, `eval.IsDenyRead`/`IsDenyWrite`, `eval.ResolvePath`, and its own `patheval.InGitRepo`-gated relaxation for the bare `secrets` path component (see below) |
| `gitdir`                                                        | `patheval.GitRoot`, `patheval.ResolveRealPath`, `patheval.PathContains`                                                                                                                             |
| `safecmds`                                                      | `eval.Evaluate(path).CanRead()/.CanWrite()` at ~10 call sites across cp/unzip/browsing/read-path handling                                                                                           |
| `git`, `buildtools`, `kubectl`, `monorepo`, `sqlite3`, `docker` | each holds its own `*patheval.PathEvaluator` field, called ad hoc within that rule's own logic                                                                                                      |

Eleven rule modules total consult `patheval`/`secretpath` this way (confirmed by grep across
`internal/rules/*`), each independently, with no shared "resolve this path once" step anywhere
in `RuleChain`'s construction (`internal/setup/factory.go:79-95`) or in `internal/engine`
(`internal/engine/engine.go`, the first-match-wins fold over `RuleModule.Evaluate` results —
unchanged, unrelated to path resolution itself).

**The not-yet-landed effect-graph spike** (`internal/effectpolicy`, worktree
`/home/tcadmin/workspace/.workforests/ceta-effect-graph-spike/nix-agent-support`, branch
`ceta-effect-graph-spike`, ADR 0067/0068 — neither landed on this repo's primary branch as of this
writing) takes a structurally different approach on **both** axes:

- **Engine shape**: `effectpolicy.Evaluate` parses a command once, builds a graph of
  `cmddesc.Effect` nodes (`internal/effectgraph`), and folds every `Policy.Judge(Effect,
PolicyContext) (Finding, bool)` result into per-node marks, then marks into one `Decision`
  (`internal/effectpolicy/policy.go:1-20`, `evaluate.go`). This is not a first-match-wins list
  over raw tool input — it is a full-graph fold over structured effects. `internal/effectpolicy`'s
  own `imports_guard_test.go` deliberately forbids it from importing `internal/hookio` at all: the
  spike is designed to be **hook-independent**, with `internal/claudecodeadapter` as the one
  purpose-built seam that would translate a real `hookio.HookInput` into the spike's
  `evalcontract.Request` shape. That seam exists in the spike today but **main.go's hook-mode entry
  point is unchanged** — it still calls `setup.NewEngineForCWD` → `RuleChain`, exactly as
  production does. Confirmed by reading `cmd/claude-extended-tool-approver/main.go` on the spike
  branch: no reference to `effectpolicy`, `claudecodeadapter`, or `effectgraph` anywhere in it.
  **The spike's engine is not wired into anything a real Claude Code session hits today.**
- **Path model** (ADR 0068's own scope, ratified but not yet implemented — `tc-2zu3e`, blocked-by
  this bead's parent): `internal/deletable`, to be renamed `internal/pathspec`, generalizes its
  existing Kind/Resolve mechanism (each `Kind` declares `Rules` opinions over a root it identifies;
  `Resolve` folds every matching `(root, Kind)` a path lies under, deepest-root-first, "a
  `Forbidden` opinion from any candidate wins outright, otherwise innermost non-`Unknown` wins")
  into a resolver that answers three independent facets per path — `PathAccess{Read, Write,
Delete Verdict}` — replacing `patheval.classify()`'s hardcoded ladder, `deletable.Resolve`'s
  existing walk, and the ad hoc `secretpath` calls with one algorithm over declared specs (OS spec,
  workspace spec, app/command spec, session/config spec). The five path-only policies
  (`NoWriteToReadOnlyPath`, `DeleteAccess`, `NoReadOfSecretPath`, `NoReadOfUnreadablePath`,
  `NoWriteToSecretPath`) collapse into one `PathAccessPolicy` Adapter over that resolver.

ADR 0068 is explicit, in its own opening scope note and its "Consequences / Negative" section,
that it deliberately does **not** touch production: `patheval.classify()`/`Evaluate()` stay
unmodified, `internal/rules/*`/`internal/engine`/`internal/setup.RuleChain` are untouched, and the
new OS spec is — its own words — "a second, spike-local encoding of the same zone facts... not a
shared source with production." That duplication, and whether/how to remove it, is exactly what
this ADR now scopes.

### The two sides have already diverged on how to solve the SAME sub-problem, not just on shape

This is a concrete, already-observed instance of the drift ADR 0068 warns about, not a
hypothetical: production's `secrets` rule relaxes the bare, role-describing `secrets` path
COMPONENT (`secretpath.GenericSecretsDir`) for reads **inside a git repository**, using
`patheval.InGitRepo` consulted directly inside the rule (package doc, decision 3, operator ruling
on `pg2-fhb9q`/`pg2-pmk9q`). The spike's `internal/deletable` package solves the **identical**
behavioral problem — "a directory named `secrets` in an ordinary source tree is not automatically
secret" — with a **deliberately different mechanism**: `Kind.Secrecy`, a declared per-workspace-Kind
fact resolved through the same innermost-wins candidate walk `Resolve` already uses for
deletability. The spike's own package comment records the operator explicitly rejecting the
production approach when asked to consider reusing it: _"i dont want a in-git-repo relaxation
rule. i would like [the] definition of a git repo project [to] contain information about
nonsecrets... can we bake it into a general specification of projects"_ (Phillip, 2026-09-07,
recorded on `tc-lc8f`/`tc-vn5z`). So unifying these two paths is not "port the built half onto the
unbuilt half" — at least one fact already has two independently-designed, operator-reviewed
mechanisms reaching compatible but structurally different answers, and a unification pass has to
reconcile that, not just copy code across.

## What "unify" could mean — options

### Option A: Full `pathspec` migration — production rule modules call the spike's resolver directly

Land ADR 0068/`tc-2zu3e` as scoped, then, in a **second** pass, change all eleven
`internal/rules/*` modules (and `patheval`'s own exported helpers those modules use directly —
`IsDenyRead`/`IsDenyWrite`/`GitRoot`/`ResolveRealPath`/`PathContains`) to call `pathspec.Resolve`
instead of `patheval.classify()`/`secretpath.Classify`. `patheval.PathAccess`'s 4-value enum and
`CanRead()`/`CanWrite()` predicates are retired from production's own call sites in favor of the
3-facet `PathAccess{Read, Write, Delete}` shape. This is the reading of "unify" that most literally
matches ADR 0068's own language ("same resolver, one encoding").

- **Pros**: the actual, complete fix — one resolver, one encoding, no possibility of the two
  answers drifting because there is only one answer. Also closes production's own missing-Delete-
  facet gap as a byproduct (production could finally distinguish "may write" from "may delete" the
  way ADR 0068's `GOMODCACHE` fix requires).
- **Cons**: the largest blast radius of any option — eleven rule modules plus every direct
  `patheval`/`secretpath` call site, all under a security-relevant control with extensive existing
  test coverage (`internal/engine_integration_test.go` alone is ~4,700 lines) that would need to
  keep passing unchanged or be deliberately, individually re-justified. It forces the already-
  diverged secrets-in-git-repo mechanism (above) to be reconciled as a precondition, not a detail
  discovered mid-migration. It also depends on `internal/effectpolicy`'s own shape being settled —
  migrating rule call sites onto a resolver whose surrounding engine (ADR 0067) may itself still be
  rejected, reworked, or never wired into `main.go` is a real risk of stranded migration cost.

### Option B: Extract the shared DATA and FOLD ALGORITHM only — not the call sites or output type

Pull the nine OS-spec zone facts `patheval.classify()` hardcodes (project root, `WORKSPACE_ROOT`,
`/tmp`, `/nix`, `~/.claude`, `~/go/pkg`, Gradle home, the XDG data-home subpaths, plus
env-configured extra roots) and the "deepest-root-first, Forbidden-wins-outright,
otherwise-innermost-wins" fold rule into one small, shared package/data table that **both** sides
consume independently: `patheval.classify()` keeps its existing signature and 4-value
`PathAccess` return, computed _from_ the shared table; `pathspec.Resolve` (once ADR 0068 lands)
computes its 3-facet `PathAccess` _from_ the same table. No call site in any of the eleven rule
modules changes at all — every one of them keeps calling `eval.Evaluate(path).CanRead()` exactly
as today.

- **Pros**: converges the actual thing ADR 0068's "Consequences / Negative" section names as the
  real, named cost ("a second, spike-local encoding of the same zone facts... can drift") without
  touching any of the eleven rule modules, `patheval`'s public API, or any of their existing tests.
  Smallest blast radius of any option that removes real duplication rather than just monitoring it.
  Does not depend on `internal/effectpolicy`'s engine shape being settled — the shared data table
  is useful to `pathspec.Resolve` regardless of what invokes it.
- **Cons**: partial — it unifies only the OS-zone third of ADR 0068's three original classifiers.
  Production still has no Delete facet and still calls `secretpath` ad hoc; the git-repo-secrets
  divergence (above) is untouched. Two output shapes (4-value enum vs. 3-facet struct) still exist
  side by side, so a future reader still has to know both APIs even though the underlying zone
  facts are now one table.

### Option C: A conformance/golden test bridging the two, no code merge

Accept two independent implementations permanently, but add an automated check — once
`pathspec.Resolve` exists — that runs both `patheval.classify()` and `pathspec`'s OS-spec walk over
a shared path corpus and fails CI the moment they disagree on a zone fact. This converts the
drift risk from silent (the failure mode ADR 0068 names) to loud and cheap.

- **Pros**: zero production code churn, ships independent of `internal/effectpolicy`'s own
  stabilization, catches drift immediately once both sides exist.
- **Cons**: does not reduce the actual maintenance burden — every zone edit still needs two manual
  updates to stay green — and, more importantly, this is explicitly the option the operator's own
  "Decided, tracked" ruling on ADR 0068 already ruled OUT as a _permanent_ answer ("the WHETHER is
  now decided — YES, unify... [not] is maintaining two independent encodings an accepted long-term
  state"). It can only be justified as a **transitional guard** worn between "now" and whichever of
  A/B actually ships — never presented as the destination itself.

### Option D: Defer path-unification entirely until ADR 0067's engine question resolves

Treat "does `effectpolicy` ever become (or feed) production's engine at all" as the parent
question, and decline to scope path-resolution unification as a standalone sub-problem until that
resolves.

- **Pros**: avoids solving a piece whose value depends on an answer nobody has given yet — if
  `effectpolicy` never replaces `RuleChain`, a full Option-A migration has a very different
  cost/benefit than if it does.
- **Cons**: this is the ordering the operator's own ruling on `tc-uxknt` already declined. The
  bead's dependency graph keeps `tc-2zu3e` (ADR 0068's implementation) `blocked-by` this scoping
  bead precisely so the "how" gets thought through _before_, not after, ADR 0068 lands and
  introduces the very duplication this ADR is about. Deferring here does not avoid the drift risk —
  it lets ADR 0068 land, immediately creating the first real instance of the duplicated zone table
  in the codebase, with no plan yet in place for un-duplicating it. That is a worse starting
  position than any of A/B/C, not a neutral one.

## Recommendation

**Phase B first, Phase A conditionally, C as the bridge between them — not D.**

1. Land ADR 0068 / `tc-2zu3e` as already scoped and approved (unaffected by this ADR).
2. As a follow-on epic (separately `plan-decompose`d from this document), do **Option B**: extract
   the shared OS-spec zone table and fold algorithm into one package both `patheval.classify()` and
   the landed `pathspec.Resolve` consume. This is the smallest change that removes the specific,
   named drift risk ADR 0068 flagged, touches none of the eleven rule modules' call sites, and does
   not require `internal/effectpolicy`'s engine question to be resolved first.
3. Pair step 2 with a narrow slice of **Option C**: a corpus-based conformance test asserting
   `patheval.Evaluate` and `pathspec`-derived `Read`/`Write` verdicts agree, so any _future_ zone
   edit that accidentally updates only one side fails loudly at that moment rather than being
   discovered later — a regression guard riding alongside the real fix, not a substitute for it.
4. Treat **Option A** (migrating all eleven rule modules onto `pathspec.Resolve` directly,
   retiring `patheval.PathAccess`'s 4-value enum from production, and giving production a real
   Delete facet) as a **separately-scoped, separately-decided** future epic — gated on two things
   that are not yet true: (a) `internal/effectpolicy`/ADR 0067's own engine-vs-`RuleChain` question
   has an actual answer (so migrated call sites are not stranded against an engine that gets
   reworked or never wired in), and (b) the secrets-in-git-repo divergence documented above has an
   explicit, operator-reviewed reconciliation — not a silent pick-one-and-port.

This recommendation is deliberately **not** "do Option A now." The full migration is real,
substantial engineering (matching the operator's own "plan-decompose/epic-decompose scale, not a
single-session task" framing on `tc-uxknt`), its payoff is contingent on an unresolved parent
decision (ADR 0067), and it is not required to eliminate the specific cost ADR 0068's Consequences
section names — Option B eliminates that cost on its own, at a small fraction of the risk.

## Rough phase sketch (input to a later `epic-decompose` pass, not a committed plan)

- **Phase 0 (prerequisite, already in motion)**: ADR 0068 / `tc-2zu3e` lands: `internal/deletable`
  renamed `internal/pathspec`, generalized to `PathAccess{Read, Write, Delete}`, `MatchedDeniedRoot`
  wired in, `allowRead` converted to an independent grant — all as already decided in ADR 0068.
  Not touched by this ADR; listed only so the phases below have a concrete starting point.
- **Phase 1 — shared zone table (Option B)**: extract `patheval.classify()`'s nine OS-spec zones
  and the deepest-root-first/Forbidden-wins fold rule into one shared package; repoint
  `patheval.classify()` and `pathspec`'s OS spec at it; no change to any `internal/rules/*` call
  site or to `patheval.PathAccess`'s public shape. Exit criterion: `patheval`'s existing test suite
  passes unchanged, and a new test proves the shared table is the _only_ place either side's zone
  list is declared (e.g., a "count of independent zone declarations" assertion, mirroring how this
  repo already guards `internal/rules/*`'s single-source-of-truth invariants elsewhere).
- **Phase 2 — conformance guard (slice of Option C)**: a corpus-driven test (can reuse
  `internal/engine`'s existing integration-test corpus infrastructure/asklog replay machinery
  rather than build new fixtures) asserting `patheval.Evaluate(...).CanRead()/.CanWrite()` agrees
  with `pathspec.Resolve(...).Read/.Write` for every path in the corpus. Exit criterion: the test
  exists, is wired into CI, and demonstrably fails if either side's table is edited without the
  other (verify by a deliberate, reverted one-line drift during implementation).
  This phase can run in parallel with or immediately after Phase 1; it is small and independent of
  Phase 3.
- **Phase 3 — reconcile the secrets-in-git-repo divergence** (prerequisite for Phase 4, not
  required for Phases 1-2): an explicit operator-reviewed decision on whether production's
  `patheval.InGitRepo`-gated relaxation or the spike's `Kind.Secrecy` declaration (or a third
  shape) becomes the single mechanism, given the operator has already reviewed and rejected one
  direction of this port once (see Context). This is a **decision bead**, not an implementation
  task — do not let an epic-decompose pass silently pick one.
- **Phase 4 — full call-site migration (Option A)**: gated on Phase 3's decision AND on ADR 0067's
  engine-vs-`RuleChain` question resolving. Migrate the eleven rule modules and `patheval`'s
  exported helpers to call `pathspec.Resolve` directly; retire `patheval.PathAccess`'s 4-value enum
  from production's own call sites (it may still exist internally if `pathspec`'s implementation
  finds it useful, but no `internal/rules/*` module should reference it after this phase); decide,
  as part of this phase, whether production's `pathsafety` rule gets a real Delete facet (today it
  has none — see Context) or deliberately keeps `Delete` folded into `CanWrite()` even after the
  resolver underneath it can tell the two apart. This is the phase actually described by ADR 0068's
  "same resolver, one encoding" language, and the one most likely to itself need `epic-decompose`
  treatment given its size (eleven rule modules, each with its own extensive existing test suite).

Each phase above is independently valuable and independently revertible — Phase 1 alone already
removes the specific drift risk ADR 0068 names, so a future decision to stop after Phase 1/2 and
never do Phase 4 is a legitimate, self-consistent outcome, not an unfinished migration.

## Consequences

### Positive

- Gives the operator's "yes, unify — scope the how" ruling on `tc-uxknt` a concrete, phased answer
  instead of leaving `tc-2zu3e` blocked on an open-ended question.
- Identifies a smaller, lower-risk fix (Phase 1/Option B) that addresses the specific, named cost
  in ADR 0068's Consequences section without waiting on ADR 0067's larger, unresolved engine
  question.
- Surfaces a concrete divergence (secrets-in-git-repo) that a naive "just port the spike's
  mechanism into production" instruction would have silently re-decided against an existing
  operator ruling on the other side.

### Negative

- This ADR itself does not close the duplication ADR 0068 opened — that only happens once Phase 1
  actually lands, which is separately-scoped future work, not part of this bead.
- The phase sketch's Phase 4 gate (ADR 0067 resolving) means the _complete_ fix ADR 0068 originally
  gestured at ("same resolver") has no committed timeline here — only a stated precondition.

### Neutral

- Nothing in `internal/effectpolicy`, `internal/rules/*`, `internal/engine`, or `internal/setup`
  changes as a result of this ADR. It is a planning artifact only.

## Alternatives Considered

### Doing the full migration (Option A) in one pass, skipping B/C

Rejected as the _first_ move (though kept as the eventual Phase 4) because it is the option whose
payoff is most contingent on an unresolved parent decision (ADR 0067) and whose blast radius is
largest against a heavily-tested security control, while Option B alone already eliminates the
specific drift risk ADR 0068's own Consequences section names.

### Treating this ADR itself as the operator decision on which option ships

Rejected: this ADR's own Deciders line is explicit that no operator was in the loop for the
option choice. `tc-uxknt`'s ruling asked for a scoping document as input to a later
`plan-decompose`/`epic-decompose` pass, not a self-authorizing architecture decision. Any of
Phases 1-4 above still needs its own review before implementation begins, exactly as any other
`Proposed` ADR in this repo does before flipping to `Accepted`.

## Related Decisions

- `ADR 0067` — CETA effect-graph spike architecture: the parent, still-open question (does
  `effectpolicy` ever become or feed production's engine) that this ADR's Phase 4 gates on.
- `ADR 0068` — CETA unified path-access resolution within `internal/effectpolicy`: the decision
  this ADR follows on from; its own "Consequences / Negative" section is the source of the drift
  risk this ADR scopes a response to, and its own text (`docs/adr/0068-...md:332-338` on the spike
  branch) already names `tc-uxknt` as the bead this document resolves.
- `tc-uxknt` — the design/scoping bead this ADR is the output of; the WHETHER-unify question it
  once carried is decided (see that bead's description and comment history), and this document is
  its HOW answer.
- `tc-2zu3e` — ADR 0068's own implementation bead; remains `blocked-by` `tc-uxknt` per the
  operator's explicit re-affirmation of that ordering, unaffected by this ADR.
- `tc-lc8f` / `tc-vn5z` — record the operator ruling behind the spike's `Kind.Secrecy` mechanism
  and the explicit rejection of porting production's `patheval.InGitRepo` relaxation into it; the
  source for this ADR's Phase 3 gate.
- `tc-z806` — the original `internal/deletable` design bead ADR 0068 generalizes; unaffected by
  this ADR directly, relevant background for Phase 4's Delete-facet question.
