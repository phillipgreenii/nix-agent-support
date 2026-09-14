# CETA: unify zone/deletable/secret path classification into one per-path Read/Write/Delete resolution

**Status**: Proposed (design decided in discussion; not yet implemented)
**Date**: 2026-09-13
**Deciders**: Phillip Green II

> This ADR redesigns path-access resolution ONLY inside `internal/effectpolicy` — the not-yet-landed
> effect-graph spike (`ADR 0067`; landing is `tc-8og1` item 8, explicitly not yet approved) — not
> `internal/patheval`'s public API or production CETA's actual path handling. `internal/patheval`'s
> `Evaluate`/`classify`/`PathAccess` (and `internal/secretpath.Classify`) are shared, production-facing
> primitives, consulted directly and independently of `internal/effectpolicy` — untouched by anything
> this ADR changes — by eleven `internal/rules/*` packages (`buildtools`, `deniedroots`, `docker`,
> `git`, `gitdir`, `kubectl`, `monorepo`, `pathsafety`, `safecmds`, `secrets`, `sqlite3`),
> `internal/engine`, and `internal/setup.RuleChain`'s construction (`factory.go`) — production's
> actual policy engine, per `ADR 0067`'s own Context. Language below describing `patheval` as
> "retired" or as "stopping" to own the zone ladder means only that `internal/effectpolicy`'s own five
> path policies stop consulting it that way; `patheval.classify()`/`Evaluate()` themselves are
> unmodified, and the new `internal/pathspec` OS spec is, for now, a second, spike-local ENCODING of
> the same zone facts, not a shared source with production — a real, bounded cost this decision
> introduces, named explicitly in "Consequences" below.

## Context

`packages/claude-extended-tool-approver`'s path handling computes THREE independent
classifications for the same path, each its own `Policy` wrapper in `effectpolicy.DefaultPolicies()`
(`internal/effectpolicy/policy.go:167`), folded by `judgeNode`'s "first Forbidden in policy-array
order wins the reason text" rule:

1. **Zone** (`internal/patheval/evaluator.go`'s `classify()`) — a hardcoded ladder of filesystem
   conventions: project root, `WORKSPACE_ROOT`, `/tmp`, sandbox `allowWrite`, `/nix`, `~/.claude`,
   `~/go/pkg` (hardcoded `PathReadOnly`), Gradle home, XDG data-home subpaths, then env-configured
   extra roots. Answers `Reject` / `Unknown` / `ReadOnly` / `ReadWrite`.
2. **Deletable** (`internal/deletable`'s `Kind`/`Resolve`) — a genuinely well-designed multi-source
   resolver: every `(root, Kind)` a path lies under contributes an opinion; `Protected` from ANY
   candidate wins outright; otherwise the innermost (deepest-root) candidate with a non-`Silent`
   opinion wins. Answers `Silent` / `Deletable` / `Keep` / `Protected`, consulted only by
   `DeleteAccess`.
3. **Secret** (`internal/secretpath.Classify` + `deletable.NonSecret`) — name-pattern matching,
   invoked ad hoc from inside `NoReadOfSecretPath`/`NoWriteToSecretPath`/`DeleteAccess`'s own `Judge`
   methods rather than through either of the above mechanisms.

`DeleteAccess.Judge`'s ten-step ladder hardcodes an ORDER across all three: zone (step 5) is
checked and can return `Forbidden` before the deletable declaration (step 8) is ever consulted.
This is the mechanism behind a bug the operator identified while reviewing `tc-30nyw`'s two
remaining gate failures: `~/go/pkg/mod` (Go's module cache, `GOMODCACHE`) is an explicit `Deletable`
declaration (`internal/deletable/workspace.go`'s `goKind`) sitting inside the generic `~/go/pkg`
`ReadOnly` zone CONVENTION — and because zone is checked first, `rm -rf ~/go/pkg/mod` is Forbidden.
`workspace.go`'s own comment already names this a "documented gap ... changing that zone is a
separate decision", deferred rather than accidental.

The operator's ruling (recorded on `tc-iogin`, surfaced via `/unblock-human-beads` while triaging
`tc-30nyw`) was to stop and redesign the architecture rather than patch the two specific gate
failures: "why is a secret declaration different from a zone? I would have expected that the
engine determines if a specific path is not accessible, read-only, read-write, or deletable ...
the why for each could be from different sources: app spec, user rules, global rules, config ...
so what is being mixed up here?" This ADR is the design session `tc-iogin`'s
`planning-session-required` marker asked for, and its adoption is the evidence that clears that
bead.

## Decision

### One resolver over declared specs, replacing three independent walks

Within `internal/effectpolicy` (see the scope note at the top of this ADR — this does not touch
production's `RuleChain`), path access MUST be computed by one resolution algorithm consulting a set
of declared **specs**, rather than a hardcoded convention ladder (`patheval.classify()`) plus a
separate deletability walk (`deletable.Resolve`) plus ad hoc name-pattern checks scattered through
policy `Judge` methods.

`internal/deletable`'s existing `Kind`/`Resolve` mechanism is not replaced — it is the mechanism
this decision generalizes. It already implements the right algorithm (a Composite of per-root
Strategies, folded by a Chain-of-Responsibility rule); it just needs to carry read/write opinions
as well as deletability, and needs new `Kind`s to absorb what `patheval.classify()` currently
hardcodes.

The package MUST be renamed `internal/pathspec` (from `internal/deletable`) once it resolves more
than deletability.

### Output shape: three independent facets, not one ordinal enum

```go
type Verdict struct {
	Result effectpolicy.FindingVerdict // Permitted / Forbidden / Unknown — reused, not reinvented
	Reason string
}

type PathAccess struct {
	Read   Verdict
	Write  Verdict
	Delete Verdict
}
```

A resolved path MUST report an independent verdict for each of Read, Write, and Delete; no facet's
verdict MAY be derived from another's. This was tested against a concrete counterexample during
design: `~/.cache/go-build` is `Deletable` (`goKind.Roots`) but sits in no `ReadWrite` zone —
writing new files there is `Unknown`/needs-consent today, while removing it outright is `Permitted`.
A single ordinal scale (`NotAccessible < ReadOnly < ReadWrite < Deletable`) would force `Deletable`
to imply `ReadWrite`, silently widening write access on every path merely declared disposable. The
three-field shape has no such coupling.

### The fold: one algorithm, run once per facet

`deletable.Resolve`'s existing algorithm generalizes unchanged: sort every `(root, Kind)` a path
lies under, deepest-root-first; for each facet independently, a `Forbidden` opinion from ANY
candidate wins outright (this is `Resolve`'s existing "Protected always wins" rule, generalized past
deletability); otherwise the innermost candidate with a non-`Unknown` opinion for that facet wins;
otherwise `Unknown`. One pass over the sorted candidate list computes all three fields.

This is what makes the `GOMODCACHE` fix fall out of ordinary specificity rather than a hand-authored
precedence rule: once the blanket `~/go/pkg` convention and the `GOMODCACHE` root are BOTH `pathspec`
declarations (the former outer/shallower, the latter inner/deeper — `GOMODCACHE`'s default sits
under `~/go/pkg`), the existing innermost-wins rule already picks `GOMODCACHE`'s `Delete: Permitted`
over the outer convention's silence, with no new mechanism required.

This depends on the outer `~/go/pkg` convention staying SILENT (`Unknown`) on `Delete` — it MUST NOT
state `Delete: Forbidden` the way the completeness-review example two sections below recommends for
`/nix/store`. The fold rule above is "a `Forbidden` opinion from ANY candidate wins outright,
regardless of depth" (the generalized "Protected always wins" rule) — so if `~/go/pkg`'s `Delete`
facet were ever "completed" to `Forbidden` by the same reasoning the `/nix/store` example uses, that
OUTER `Forbidden` would win outright over `GOMODCACHE`'s INNER `Delete: Permitted`, and the exact bug
this ADR exists to fix would be silently reintroduced by a well-intentioned completeness pass. This
is therefore the one deliberate, NAMED exception to the completeness-review principle immediately
below: `~/go/pkg`'s `Delete` facet MUST stay `Unknown`, never `Forbidden`, so that a path directly
under `~/go/pkg` that neither `GOCACHE` nor `GOMODCACHE` covers resolves `Delete: Unknown` (needs
consent) rather than today's zone-driven automatic `Forbidden` — a small, deliberate,
narrower-than-`/nix/store` behavior change that is the necessary price of the fix, not an oversight
to complete away later. Whoever does the completeness pass MUST NOT add `Delete: Forbidden` to the
`~/go/pkg` OS-spec entry.

### Spec sources

- **OS spec** — ambient, project-independent conventions with no filesystem marker: `/tmp` (already
  `tempKind`, though see the completeness note below — its current `Rules` state only a `Deletable`
  Category, not a Read/Write opinion, while `patheval.classify()` treats all of `/tmp` as
  `ReadWrite`), and new ones absorbed from `patheval.classify()`'s zone ladder
  (`internal/patheval/evaluator.go`'s `classify` function) — `/nix` (`Read: Permitted, Write:
Forbidden, Delete: Forbidden, Reason: "immutable path owned by nix-daemon, so only read is
allowed"`), `~/.claude` (with its existing `plans`/`projects` read-write carve-out),
  `~/.claude.json`, the Gradle user cache (`GRADLE_USER_HOME` or `~/.gradle`, currently `ReadOnly`),
  `~/go/pkg`'s blanket `ReadOnly` convention (the OUTER half of the `GOMODCACHE` conflict this ADR
  fixes, below — distinct from `goKind`'s own `GOCACHE`/`GOMODCACHE` `Roots` declarations, which
  stay Workspace spec), and the `<xdgDataHome>` subpaths `nix-support-local-plugins/` and
  `contained-claude/` (both `ReadOnly`), `claude-extended-tool-approver/` itself (`ReadWrite`, for
  its own `asks.db`), and the old-name `claude-pretool-hook/` (`ReadOnly`) — nine zones in
  `classify()` total; an earlier draft of this list named only the first three. These need a new
  `Kind` root-identifier alongside `Markers`/`Home`/`Temp` for a marker-less fixed root (`/nix`, the
  `xdgDataHome` subpaths) — plausibly a `FixedRoot` field, though `Kind.Roots` (already used by
  `homeKind`/`goKind` today for a computed absolute root) can already return a literal constant
  with no computation at all, so whether a dedicated field is structurally required or merely a
  readability preference is worth confirming at implementation time rather than assumed here.
- **Workspace spec** — the existing `git`/`go`/`gradle`/`pn`/`home` `Kind`s (each of which already
  declares a `Rules` or `Classify` opinion today), widened so that opinion states Read/Write/Delete
  facets, not only a `Category`. (`just`/`npm`/`devbox` are workspace `Kind`s too, but today declare
  no `Rules`/`Classify` at all — only `Verbs`, the build-tool-family verb-discovery facet, unrelated
  to path access — so there is nothing on them to widen; they stay out of scope for this conversion
  unless a future need for their own path opinion arises.)
- **App/command spec** — a command schema's own declared safe targets (`Kind.Commands`, already
  present, currently informational only). Not required to change for this decision; noted as the
  natural home for a future "this build tool's own clean verb agrees with a plain `rm`" fact.
- **Session/config spec** — sandbox `allowWrite`, extra `CETA_EXTRA_READWRITE_ROOTS`/
  `CETA_EXTRA_READONLY_ROOTS`, the project root/`WORKSPACE_ROOT` grant, and `RemotePaths` overrides.
  These are runtime-derived rather than filesystem-declared, but the same shape (a root with an
  opinion) — they become one more `Kind`, computed once at `PathEvaluator` construction, rather than
  four separate sidecar checks threaded through `patheval` and the policies.

  `allowWrite` and `allowRead` are NOT symmetric today, and converting them must not accidentally
  make them so: `classify()` consults `sandboxConfig.AllowWrite` directly and grants a `ReadWrite`
  zone from it, but `sandboxConfig.AllowRead` is consulted ONLY inside `IsDenyRead`, as an override
  that cancels a matching `denyRead` entry — an `allowRead` entry with no corresponding `denyRead`
  match grants nothing today (the path stays whatever zone it would otherwise be, `Unknown` if none
  claims it). A conversion that treats `allowRead` as its own `Read: Permitted` grant, mirroring
  `allowWrite`, would silently WIDEN read access beyond what the sandbox config grants today; the
  completeness review must either preserve the current override-only behavior or call out the
  widening as a deliberate, separately-decided change, not an incidental side effect of the port.

Sandbox `denyRead`/`denyWrite` (operator hard overrides) and `CETA_DENIED_ROOTS` (fabricated-root
detection) stay OUTSIDE this walk — see "What does not collapse", below.

### Secret handling folds in, with one named, deliberately non-generalized exception

A well-known secret store match (`.ssh`, `.gnupg`, credential basenames, `*.pem`/`*.key`) folds in
as an ordinary spec entry at the SAME priority as an operator's `denyRead`/`denyWrite` — `Read`,
`Write`, `Delete` all `Forbidden`, `Reason: "well-known credential store"`. No output-level `Secret`
concept is needed for this case: `Forbidden` + `Reason` is indistinguishable in shape from any other
absolute veto.

The bare, role-describing `secrets` path-component match (`secretpath.GenericSecretsDir`) does NOT
fold into ordinary depth-precedence, and MUST remain a distinct, explicitly-scoped exception:
it is a low-priority DEFAULT (`Forbidden` unless vouched otherwise), overridable ONLY by a
workspace's tracked-and-not-ignored fact (the git `Kind`'s existing `Secrecy` facet) — not by "any
deeper spec's opinion wins". Two things were checked and ruled out during design:

- Depth alone points the wrong way: the vouching spec (git, rooted at the repository root) is
  usually SHALLOWER than the `secrets/` directory itself, so ordinary innermost-wins would let the
  generic guess beat the vouch, not the reverse.
- Widening to "any deeper spec's ordinary opinion overrides the guess" would silently loosen
  protection: an UNTRACKED file inside a `secrets/` directory in an ordinary git project would be
  waved through by git's broad "this is project content" opinion, which is not conditioned on
  tracked-ness the way the vouch specifically is.

So this one override stays a named exception in the resolver, exactly as narrow as it is today —
not a new type in `PathAccess`'s output, just a documented rule about which candidate's `Forbidden`
is rebuttable and by what.

### Every converted spec MUST be reviewed for completeness

Decoupling the three facets means a spec's silence on a facet now reads as `Unknown`, not an
inherited answer. Today `/nix/store` is unconditionally Forbidden-to-delete only BECAUSE it is
coupled to the write-Forbidden zone check; once decoupled, the OS spec must say so explicitly
(`Delete: Forbidden`) or `rm -rf /nix/store/...` degrades from Reject to Ask. Each converted `Kind`
MUST be reviewed against every path it is responsible for, confirming it states an opinion on every
facet it has actual knowledge about, before this design is considered correctly implemented.

### Closes a known gap from `ADR 0067`, by making an accident into a decision

`ADR 0067`'s "Known gaps and follow-ups" flagged that `git commit`/`add`/`rm`/`mv`'s implicit
`PathModify(".git")` effect is approved not because any policy examined `.git`'s `Protected`
declaration, but because `NoWriteToReadOnlyPath` never consults `deletable.Classify` for
non-delete writes at all — only `DeleteAccess` does. The verdict is correct; the reason is an
accident of which policy happens to run.

Under one resolved `PathAccess` per path, ALL access kinds (read/write/delete) query the SAME
resolution, so this accident cannot survive unexamined: the git `Kind`'s `.git`/`.worktrees` opinion
MUST be written as `Delete: Forbidden` only, leaving `Write` unopined (`Unknown`, deferring to the
ordinary working-tree `ReadWrite` zone) — which is what happens today by accident, made a deliberate,
documented per-facet choice instead. This decision closes that gap as a side effect of the
completeness review above, rather than leaving it as unresolved production-adjacent risk.

### Policy-layer consequence

The five path-only policies (`NoWriteToReadOnlyPath`, `DeleteAccess`, `NoReadOfSecretPath`,
`NoReadOfUnreadablePath`, `NoWriteToSecretPath`) collapse into ONE `PathAccessPolicy` — an Adapter
mapping `(resolved PathAccess, effect.Access)` to a verdict, replacing five parallel ladders with
one lookup. `remotePathGuard` continues to wrap it exactly as it wraps the five today; non-path
policies (`NetworkAccess`, `RemoteMutation`, `KubeContextPolicy`, `StdioIsLocal`,
`ProgramInterpreted`, `EnvAssignment`, `ChdirScoped`, `TrustedCheckoutExec`) are untouched — this
decision is scoped to the path axis only.

## What does not collapse into a static os/workspace/app path table

The operator asked directly whether all path limitations can be expressed as OS/workspace/app spec
data. Mostly yes for the STATIC "what access does this location have" facts — but five mechanisms
in the current code are not static path→access facts at all, and forcing them into `pathspec` would
either not work mechanically or would blur what a declared spec means. Each is a different KIND of
question than "what access does this path have":

- **Live filesystem/VCS state** (`deletable.ProbeWorktreeState`, `ProbeWorkforestSetState`) — "is
  this worktree root clean or dirty" depends on `git status` output AT EVALUATION TIME, not a
  static table. The ROOT is still spec-identified (a worktree root under `.worktrees`); the OPINION
  for that root requires a probe function. `Kind` MUST keep room for both a static `Rules` list and
  a dynamic `Classify`-shaped probe, exactly as it does today — this does not change.
- **Effect/access-sub-kind-dependent carve-outs** (the `AccessModify` carve-out: `git rm`/`mv` model
  their target as `PathModify`, and the `tc-z806` ruling treats that as recoverable-from-history, so
  a tracked well-known-secret gets a narrower relief than an ordinary write would). This depends on
  WHICH ACCESS SUB-KIND (`Create`/`Truncate`/`Modify`) touched the path — a property of the
  effect/command, not of the path. It stays a policy-layer rule combining "what does `pathspec` say"
  with "what kind of write is this"; it cannot be pushed down into `PathAccess` itself without
  `PathAccess` also encoding command provenance, which this spike's effects deliberately never carry.
- **Path resolution/normalization plumbing** (symlink-escape detection, `~`/env expansion, an
  unexpanded-variable or unknown-`~user` path returning "unresolvable", the raw-then-resolved
  double check secret matching already does). These decide WHICH STRING gets evaluated against
  specs in the first place. They are not expressible as spec data — they are `patheval`'s
  pre-processing, unchanged by this decision.
- **Machine-sanity / fabricated-root detection** (`CETA_DENIED_ROOTS`, `MatchedDeniedRoot`) answers
  "does this absolute root even exist on this machine", not "what access does it have" — its output
  is a redirect message, not a `Read`/`Write`/`Delete` verdict. It stays a separate sidecar check,
  consulted before or independent of the `pathspec` walk — mirroring how `IsDenyRead`/`IsDenyWrite`
  already ARE consulted directly inside `NoWriteToReadOnlyPath`/`NoReadOfUnreadablePath`/
  `DeleteAccess`'s `Judge` methods today. `MatchedDeniedRoot` itself is NOT currently consulted by
  `internal/effectpolicy` at all — today it is called only from production's
  `internal/rules/deniedroots`. Wiring it into `effectpolicy`'s resolution is therefore NEW ground
  this decision opens up, not a continuation of an existing effectpolicy call site, and should be
  treated as such by whoever implements it.
- **Remote-scope paths** (`RemotePathRule`, ssh/scp targets) ARE genuinely declarative data —
  `(host, prefix) -> category` is already a tiny spec — but keyed by remote host rather than local
  filesystem structure, since no local marker-walk (`os.Stat`) can apply to a path on a machine this
  process is not running on. This stays its own per-host spec dimension, consulted by
  `remotePathGuard`, rather than folded into the local OS/workspace/app/config `Kind` walk.

None of these five are gaps in the design — they answer different questions (state-of-the-world,
shape-of-the-command, which-string-am-I-even-resolving, is-this-root-real, which-remote-host) that a
per-path static table was never going to answer, and two of them (the secrets vouch generalization,
and folding denyRead/denyWrite into ordinary depth precedence) were specifically checked and would
have made behavior WORSE, not just structurally impure.

## Consequences

### Positive

- One shared vocabulary and one resolution algorithm replace three independent classifiers with
  implicit, order-dependent precedence. A path-access fix benefits every effect kind that consults
  it, not just the one policy that happened to be edited.
- The `GOMODCACHE`/`~/go/pkg` conflict — and any future conflict of the same shape — resolves by
  ordinary specificity (innermost root wins) rather than a hand-maintained precedence list.
- `ADR 0067`'s `.git`/`PathModify` known gap closes as a byproduct of the completeness-review
  requirement, rather than remaining unresolved production-adjacent risk.
- `internal/patheval`'s hardcoded zone ladder — the least discoverable, least testable part of the
  current design (a hidden ordering of unrelated `if` statements) — is retired from
  `internal/effectpolicy`'s own consumption (see the scope note at the top of this ADR;
  `patheval.classify()` itself is unmodified and stays production's zone authority) in favor of data
  any future project/OS convention can extend without touching Go control flow.

### Negative

- Nontrivial migration: every existing `Kind`, and every zone `patheval.classify()` currently
  encodes (ported into the new OS spec, not removed from `patheval` itself — see the scope note at
  the top of this ADR), needs conversion AND a completeness audit (per-path review, not just
  per-`Kind` compile success).
- Two hand-maintained exceptions remain, deliberately not generalized: the `AccessModify` secret
  carve-out (policy-layer, keyed on access sub-kind) and the generic-secrets vouch (resolver-layer,
  keyed on a specific override relationship depth precedence cannot express). Both are documented
  here rather than hidden, but neither disappears.
- `RemotePathRule`'s `"protected"`/`"secret"` categories (currently behaviorally identical, per
  `ADR 0067`'s own "Known gaps") are unaffected by this decision — the AccessModify-style relief this
  ADR preserves locally still has no local git-tracked-ness signal to ground an equivalent remote
  relief in, so the remote side stays conservative, unchanged.
- The OS spec's zone facts (`/nix`, `~/.claude`, the Gradle/Go caches, the `xdgDataHome` subpaths,
  ...) are, for now, a SECOND, spike-local encoding of exactly what `patheval.classify()` already
  encodes for production (see this ADR's opening scope note) — not a shared source. The two copies
  can drift; keeping them in sync across any future change to either is a real, ongoing cost this
  decision introduces rather than removes, until a later, separately-approved decision unifies
  `internal/effectpolicy` with production's `RuleChain` (`tc-8og1` item 8).

### Neutral

- `internal/patheval` does not disappear — it keeps its plumbing role (path cleaning, symlink
  resolution, `denyRead`/`denyWrite`, fabricated-root detection, the project-root/`rootGrantsZone`
  logic) for every caller, spike and production alike, and stops owning the zone-classification
  ladder only from `internal/effectpolicy`'s own point of view: `classify()`/`Evaluate()` are not
  removed or changed, since production's `RuleChain` (see the scope note at the top of this ADR)
  keeps calling them directly; `internal/pathspec`'s OS spec is a second encoding of the same zone
  facts, not a replacement of `patheval`'s own.
- `internal/deletable` is renamed `internal/pathspec`; every import site updates mechanically.

## Alternatives Considered

### A fourth ordinal state (`NotAccessible < ReadOnly < ReadWrite < Deletable`)

The operator's own opening framing ("those are the only 4 [states] I think we need") read this way
initially. Rejected once `~/.cache/go-build` was checked as a concrete counterexample: `Deletable`
is not a strict superset of `ReadWrite` in the current system, and forcing it to become one would
silently widen write access on every path merely declared disposable — a real behavior change nobody
asked for, discovered by testing the model against an existing case rather than assumed.

### An explicit "declaration beats zone" precedence table

The first proposal put to the operator in this design session: a hand-authored ordering (operator
override > secret > protected declaration > deletable declaration > zone convention > default).
Superseded once zone conventions themselves became depth-scoped `pathspec` declarations rather than
a separate hardcoded ladder — the existing innermost-wins rule already produces the correct
precedence via ordinary specificity, with no hand-authored table needed. Simpler, and one fewer
thing to keep in sync as new specs are added.

### Generalizing the generic-secrets vouch to "any deeper opinion wins"

Considered as a way to avoid a special-cased override rule. Rejected: it would let an ordinary
git-workspace "this is project content" opinion (not conditioned on tracked-ness) wave through an
UNTRACKED file sitting in a `secrets/`-named directory inside an otherwise-normal project — a real
loosening of the existing, narrower git-tracked-and-not-ignored vouch.

### Folding `denyRead`/`denyWrite` into the ordinary candidate walk

Considered so the walk would have exactly one kind of "wins outright" rule (`Forbidden` from any
candidate). Rejected: `denyRead`/`denyWrite` are operator CONFIGURATION with no notion of
"specificity" or nesting at all — they are meant to win regardless of what any spec, however deep,
says. Keeping them a hard override outside the walk keeps that guarantee explicit rather than
relying on every future spec never emitting a conflicting `Permitted` that happens to be deeper.

## Related Decisions

- `tc-z806` (design bead) — the original deletable-path-class design (`internal/deletable`'s
  `Category`/`Kind`/`Resolve`); this decision generalizes that mechanism rather than replacing it.
- `ADR 0067` — CETA effect-graph spike architecture: this decision is a refinement within that
  architecture's `internal/effectpolicy` path-policy set, and closes one of its two "Known gaps and
  follow-ups" (the `.git`/`PathModify` accidental-approval gap) as a byproduct.
- `ADR 0053` — CETA threat model: unaffected by this decision; the secret/credential threat surface
  this ADR's secret-handling section addresses is the same one `ADR 0053` scopes.
- `tc-iogin` — the design bead this ADR's adoption resolves (`planning-session-required`).
- `tc-30nyw` — land the `ceta-effect-graph-spike` workforest; blocked by `tc-iogin`, unblocked once
  this decision is recorded and (per its own scope note) validation is re-run fresh against whatever
  lands from it.
