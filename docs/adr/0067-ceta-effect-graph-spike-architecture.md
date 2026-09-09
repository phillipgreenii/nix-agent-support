# CETA effect-graph spike: a unified effect model to replace per-command policy rules

**Status**: Proposed (implemented in a workforest spike, not yet approved for landing — see
"Status and landing", below)
**Date**: 2026-09-09
**Deciders**: Phillip Green II

> This ADR documents an architecture that exists today only in the workforest worktree
> `/home/tcadmin/workspace/.workforests/ceta-effect-graph-spike/nix-agent-support`, branch
> `ceta-effect-graph-spike`, module root `packages/claude-extended-tool-approver`. It is **not**
> wired into `internal/setup.RuleChain` and has **no effect on production CETA behavior**. Landing
> it — merging the branch, wiring it into `RuleChain`, and retiring or migrating
> `internal/rules/*` — is `tc-8og1` work item 8, and is **explicitly not yet approved**: "i will do
> a deeper dive and review before 8 is approved" (Phillip, 2026-09-09, via `/unblock-human-beads`).
> Nothing here should be read as describing current production policy.

## Context

CETA's production policy engine (`internal/setup.RuleChain`, consuming `internal/rules/*`) is one
Go package per consumer command — `nix`, `docker`, `kubectl`, `ssh`, `curl`, `git`, `secrets`,
`envvars`, `buildtools`, and so on (`ls internal/rules` currently lists 25 such packages). Each
rule independently re-parses the command it cares about (via `cmdparse`), branches on the leaf's
program name, and applies its own bespoke judgment. This has served CETA well for the commands it
was written against, but three structural gaps motivated a spike rather than incremental rule
additions:

- **No shared effect vocabulary.** Two rules that both care about "does this write to a read-only
  path" (say, `nix`'s `--out-link` handling and `docker`'s bind-mount handling) each re-derive the
  answer from raw argv rather than consulting one shared "this leaf writes here" fact. Policy
  logic and command-parsing logic are the same code, so a path-safety fix in one rule does not
  benefit any other rule that happens to touch a path.
- **No shared structural model for nested/composed commands.** `bash -c '...'`, `xargs`, `find
-exec`, `ssh HOST '...'`, and a build tool's own child-verb dispatch (`just build`, `npm run
test`) each recurse into "another command" in a shape specific to that wrapper. Production
  handles the ones it has rules for; anything else is either unmodeled or handled by a narrower,
  duplicated recursion.
- **No place to hang a build-tool "family" concept.** Production could not express "the same kind
  of question — is this verb one this project actually defines and is safe to run — applies to
  `just`, `npm run`, `devbox run`, and `nix run` alike" without writing four bespoke, unrelated
  rules, each re-inventing project-verb discovery from scratch.

The spike (predecessor chain `tc-c1en` → `tc-q9ak` → `tc-lc8f` → `tc-8og1`) replaces the
per-command-rule model with a two-stage pipeline that is data-first (schemas, not
command-name branches) and effect-first (a small, closed vocabulary of _what a command node does_,
judged by a small, general set of policies that never look at a command's name):

1. **Structural graph** (`internal/effectgraph`, built from a real shell-parser front end —
   `internal/cmdparse` — per `ADR 0039`): nodes for each parsed command leaf, file/stdin/stdout/env
   endpoints, and edges recording how they connect (redirects, pipes, substitutions, child
   invocations such as `bash -c`, `xargs`, `find -exec`, `ssh HOST ...`).
2. **Interpreted graph**: the same graph, with each command node's **effects**
   (`internal/cmddesc.Effect`) attached from its schema — `EffectPath`, `EffectProgram`,
   `EffectEnv`, `EffectNet`, `EffectStdio`, `EffectRemote`, `EffectChdir`, `EffectExec`,
   `EffectKeyMaterial`, `EffectOpaque` (schema-less; can never be permitted).

A small, fixed set of **policies** (`internal/effectpolicy`) then judges effects by _kind and
field_, never by command name — `NoWriteToReadOnlyPath`, `DeleteAccess`, `NoReadOfSecretPath`,
`NoWriteToSecretPath`, `NetworkAccess`, `RemoteMutation`, `KubeContextPolicy`,
`TrustedCheckoutExec`, and others — and a fail-closed fold (`effectpolicy.evaluate.go`'s
`judgeNode`) combines every node's findings into one of four decisions
(`evalcontract.Decision`): `Abstain` (zero value — a policy must actively vouch for something to
move off it), `Approve`, `Ask` (reserved, unused today), `Reject`.

The commits landed against `tc-8og1` (33 slices as of this ADR, `3p` through `3ap`, on top of an
earlier `tc-lc8f`/`tc-q9ak`/`tc-c1en` foundation) built out this model far enough to cover awk/find
interpreters, the build-tool family, kubectl per-context policy, ssh/scp remote scope, git
worktree/workforest lifecycle, and a hermetic env-value port from production — the vocabulary and
rulings below are the record of that work.

## Decision

### Core vocabulary introduced this spike

- **`internal/hooktypes.Redirection`** (slice 3r) — a zero-dependency value type for a shell
  redirection fact (target, direction, append-vs-truncate), extracted out of `internal/hookio` so
  that packages needing only the _shape_ of a redirect (not the whole hook-adapter type) can depend
  on it without pulling in `hookio`. Existed to break an import-cycle risk between `cmdparse` and
  `hookio` before that cycle was resolved outright by slice 3ap (below).
- **`Effect.DryRun`** (slice 3w) — a boolean mark on an `EffectRemote`, set when a `TransformDryRun`
  flag (e.g. `git push --force -n`) applied to that effect. Before this slice a dry-run flag
  _erased_ the mutation effect outright, so a forbidden-class operation (force-push, delete-ref)
  vanished into an automatic Approve; `DryRun` instead survives as a distinct, judgeable state on
  the effect, order-independent with respect to other transforms (`TransformForce`,
  `TransformDeleteRef`) that also touch the same effect.
- **`Effect.Family`** (slice 3y, extended by the build-tool-family sub-slices 3ai/3aj/3ak) — an
  open string marking which _family_ of policy should judge an `EffectRemote` or `EffectExec`.
  `""` (the zero value) keeps a producer's original, unconditional routing (e.g. git's remote
  positional, still judged by `RemoteMutation`'s fixed operation table). `"kubectl"` routes an
  `EffectRemote` to `KubeContextPolicy` instead, which judges per **kube context** rather than by a
  fixed operation table. On `EffectExec`, a non-empty `Family` (`just`, `npm`, `devbox`, `nix`)
  routes to `TrustedCheckoutExec`'s build-tool-verb branch (`judgeBuildToolVerb`) rather than its
  original "code inside a trusted checkout" branch. One field, reused for two unrelated routing
  questions, rather than two bespoke fields — deliberately, so a future family-scoped policy can
  reuse the same mechanism without a new field.
- **`Effect.Remote` / remote-scope tagging** (slice 3aa, extended 3ad) — `Effect.Remote` names the
  host a _path_ effect's target lives on, when the leaf producing it sits inside a **remote scope**:
  either an `ssh HOST '...'` child invocation (and anything nested inside it — a `bash -c` the
  remote command itself runs, an `xargs`/`find` argv it reconstructs — via effectgraph's
  remote-scope stamping in its graph-builder), or `scp`'s own `[user@]host:path` operand, stamped
  directly by `interpreter_scp.go` since a single `scp` leaf can mix local and remote operands on
  one node. `""` means local. This is the one field `effectpolicy.remotePathGuard` (below) reads to
  tell "this filesystem path is not this process's local filesystem" from the effect alone, without
  the policy itself walking the graph.
- **`EffectExec` / "trusted checkout code" / `TrustedCheckoutExec`** (slice 3x) — a distinct effect
  kind for a build/test tool operating _on or within a trusted checkout_: running the checkout's
  own code (`go test`'s compiled test binary, `go generate`'s directives) or writing to a tool's
  own declared, disposable build cache (`go build`/`vet`/`fmt`/... traffic against
  `GOCACHE`/`GOMODCACHE`, per `internal/deletable/workspace.go`'s `goKind`). Deliberately **not**
  `EffectProgram` (whose policy, `ProgramInterpreted`, is unconditionally Permitted once a dialect
  interpreter vouches for program text) — `EffectExec` is conditional on the invocation running
  inside a recognised checkout, judged by the dedicated `TrustedCheckoutExec` policy. This is also
  the effect kind the build-tool-family rulings (below) extended to route `Family`-tagged verbs.
- **`EffectKeyMaterial`** (slice 3aa) — a reference (by path) to a credential file authenticating a
  remote connection, e.g. ssh/scp's `-i FILE`. Deliberately distinct from `EffectPath`: the file's
  _content_ is never read/disclosed anywhere this model observes (it is handed to the local
  client's own key-exchange machinery), so modeling it as an ordinary read would make
  `NoReadOfSecretPath` Forbid every `-i ~/.ssh/id_rsa` invocation outright — conflating "reference a
  key to authenticate with" and "disclose a secret's bytes". No policy in `DefaultPolicies` judges
  this kind (deliberately), so the fail-closed fold always treats it as unjudged: `Abstain`, never
  `Approve` or `Reject`, until a future slice reviews key-material references on purpose.
- **`deletable.Kind.Secrecy` + git-tracked probe** (slice 3z) — a declaration, owned by a
  _workspace kind_ (the same `Kind` mechanism `internal/deletable` already uses for
  deletability), of whether a path under that kind's root is non-secret. The git workspace kind's
  `Secrecy` implementation is "tracked-by-git means non-secret": a path committed to the repository
  (in the index) and not gitignored is non-secret, via a hermetic, injectable `git ls-files` probe
  (`workspace.go`'s `gitTrackedProbe`, mirroring `worktree.go`'s `ProbeWorktreeState` pattern).
  Deliberately **not** a special case inside the secret-detection policy itself — the operator
  ruling that shaped this (2026-09-07, verbatim on `tc-lc8f`/`tc-vn5z`): "i dont want a in-git-repo
  relaxation rule. i would like [the] definition of a git repo project [to] contain[] information
  about nonsecrets... can we bake it into a general specification of projects." A well-known secret
  basename or directory (`.ssh`, `.gnupg`, credential basenames, `*.pem`/`*.key`) is never relaxed
  by this declaration, tracked or not — only the bare, role-describing `secrets` path component is
  a question `Secrecy` answers at all.
- **deletable worktree-state probe / `IsWorktreeRoot` / `IsDeclaredWorktreeSlot`** (slice 3t,
  extended by 3ac for `git worktree` verbs and by `tc-8og1` item 1/slice 3ae for pn-workforest set
  depth) — a hermetic, injectable git-state probe (`ProbeWorktreeState`, environment-stripped, per
  the bead's "tests MUST run in an isolated `t.TempDir()` repo ... never against the real checkout"
  constraint) answering "clean / dirty / clean-but-ignored-files-present / undeterminable" for a
  worktree root, plus two structural recognizers: `IsWorktreeRoot` (the reliable `.git`-FILE
  marker) and `IsDeclaredWorktreeSlot` (the declaration-level location — direct child of
  `.worktrees/`, or, after slice 3ae, `<workforests_dir>/<set>/<repo>` for a pn workforest's
  two-level nesting). This backs the operator's ruling that a worktree's _removal_ is judged by its
  _state_, not by a blanket `Protected` category: clean → Approve, dirty → Reject, clean-but-ignored
  → Abstain.
- **`NoWriteToSecretPath`** (slice 3ab) — the write-side counterpart of `NoReadOfSecretPath`: an
  `EffectPath` write (create/modify/truncate) targeting a well-known secret store is Forbidden,
  mirroring the read side, except for an `AccessModify` carve-out (slice 3af, `tc-8og1` item 2) —
  see "Known gaps and follow-ups" below for the write-side ladder's own latent inconsistency.
- **`remotePathGuard`** (slice 3aa) — a `Policy` decorator wrapping one of the ordinary path
  policies (`DefaultPolicies` wraps `NoWriteToReadOnlyPath`): before delegating, it checks whether
  the effect is a remote-scoped path (`Effect.Remote != ""`) and, if so, first consults
  `Request.RemotePaths`' categorized-path override table (see below) rather than letting the local
  read/write-zone logic (which has no notion of a remote filesystem) answer at all. Implements,
  once, for every path policy, the operator's ruling: "for ssh, abstain for paths should be the
  default. however, we should allow some way to specify a list of categorized paths."
- **`RemoteLifecycle`** (slice 3u) — request-level operator configuration: a `map[target]class`
  (e.g. `"dolt" -> "reject"`) governing a remote resource's lifecycle-verb class. This spike's
  stand-in for a future `rules.json` binding — production wiring is a follow-up, not part of this
  spike.
- **`KubeContexts` / `KubeContextDefaultAllow`** (slice 3y) — request-level operator configuration
  for kubectl's per-kube-context policy: each configured context name maps to an `Allow` list of
  `EffectRemote` operation classes (`read`/`mutation`/`exec`); a context absent from the map falls
  back to `KubeContextDefaultAllow` (`nil` by default — no class allowed, the conservative reading
  for an _unconfigured_ context). Answers the operator's ruling that "kubectl should be configured
  to vary per context... a 'dev' cluster... vs a 'prod' which could be more restricted."
- **`RemotePaths`** (slice 3aa, wildcard/default entry added slice 3am) — request-level operator
  configuration: `map[host][]RemotePathRule`, an ordered, first-match-wins list of
  `(Prefix, Category)` overrides per remote host, consulted by `remotePathGuard`. `Category` reuses
  `internal/deletable`'s local taxonomy (`read-only`/`writable`/`deletable`/`protected`/`secret`)
  rather than inventing a new one. The map also recognises a wildcard key
  (`evalcontract.RemoteHostWildcard`, `"*"`) applying to any host with no per-host entry, or whose
  per-host entries produce no match — a per-host entry, when present and matching, always wins over
  the wildcard.
- **`Effect.NetProducer`** (slice 3ao, `tc-8og1` item 4a) — names _which client program_ produced
  an `EffectNet` connection: `""` (curl and any other unmarked producer) or `"ssh"`/`"scp"`. Exists
  because the "Permit outbound connections to a vetted host" ruling (`tc-dpfl`) was found,
  mid-implementation, to be generic over every `EffectNet` producer — a blanket fix would have
  silently loosened `curl`'s own confirmed-upload marking too, a behavior change never discussed in
  that ruling. `NetworkAccess`'s Outbound+Vetted branch, and
  `NoContentFlowToUnvettedNetwork`'s parallel vetted-sink-consent branch, both consult this field
  (`effectpolicy.isVettedConnectionProducer`) and Permit only for `"ssh"`/`"scp"`; every other
  producer (including curl) keeps its original, more conservative verdict.

### Major RULED decisions (`tc-8og1` items 3-7)

All five items below were explicitly gated on operator decisions and were ruled via
`/unblock-human-beads`; each ruling is recorded verbatim on the bead named.

**Item 3 — build-tool family design (RULED 2026-09-08, `tc-vn5z` Q1-Q5).** Five questions, answered
together:

- **Q1 (verb discovery):** live static/lexical parsing of `justfile`/`package.json`/`devbox.json`
  (implemented as `internal/deletable`'s `Verbs` facet, slice 3ag) — a project genuinely defining a
  verb is discovered by reading its own build files, not by trusting operator data alone.
  `rules.json` data is reserved _only_ for `flake.nix` apps ("can't be safely lexically scanned").
- **Q2 (exec-policy routing):** not a clean pick of either option originally proposed — the
  operator's own words: "there should be a spec for build tools which i hope [will] be sufficient
  for most situations. if we have an example of a needed extension, we can discuss it then." The
  spike built one general, data-driven spec by extending `TrustedCheckoutExec`'s existing marker
  list (`VerbScopedApproval`, below) rather than pre-designing bespoke per-tool policies.
- **Q3 (WORKSPACE vouching):** live discovery (Q1) vouches _only_ for verbs literally defined
  in-project; operator data (`rules.json`/`Request.BuildToolVerbs`) governs everything reached by
  reference (e.g. a `nix run` installable, which requires evaluating a flake to resolve).
- **Q4 (wrapper-child `RoleKind`):** reuse `interpreter_subcommand.go`'s existing recursion for
  argv-shaped children; a new descriptor (`VerbChild`, `Kind`/`ArgSeparator`) is reserved only for
  shapes that recursion cannot express (a name-lookup, an installable reference).
- **Q5 (`rules.json` shape):** extend `VerbScopedApproval` in place with optional `Class`/`Child`
  fields (`Class` defaulting to `"project-tied"`, `VerbClassProjectTied`), per `ADR 0033`'s
  additive-only invariant — mirroring production's own `configrules.VerbScopedApproval{Tool, Verb}`
  shape exactly, but as a separate spike-local type (the spike does not import
  `internal/rules/configrules`, which is wired into production's `RuleChain`).

Implemented across five sub-slices (3ag-3ak): the verb-discovery facet, the treefmt data-first
schema, `Request`/`rules.json` wiring (`TrustedCheckoutExec` extended in place, `Family != ""`
routes to `judgeBuildToolVerb`), the `cmddesc` child-expression descriptor
(`CommandSchema.VerbFamily` + `interpretVerbDispatch` for `just`/`npm run`/`devbox run`), and a
second judged verb class, `VerbClassInstallableReference`, added for `nix run`'s installable
child — scoped deliberately narrow to **local** flake references only (`.`, `.#attr`, an optional
`^output`); a remote/registry reference (`nixpkgs#...`, `github:...`) stays `Abstain` even if an
operator declares it, since vetting a remote installable's trust is a materially different,
higher-stakes question this spike does not attempt.

**Item 4 — vetted-host outbound network model (RULED 2026-09-08, `tc-dpfl` + `tc-hjtb`).** The
operator's initial ruling on `tc-dpfl` — "Yes, Permit for vetted hosts" — was, on inspection, not
directly implementable: `NetworkAccess` judges `EffectNet` generically, with no producer tag, so a
blanket "vetted host → Permit" change would have silently loosened `curl`'s own confirmed-upload
marking too (never discussed in the original ruling), and `NoContentFlowToUnvettedNetwork`'s
consumes-content branch is `Unknown` on _every_ ssh invocation regardless of real content (ssh's
schema models stdin as always-possible), so the fix wouldn't even have delivered the promised
outcome for ssh. This was escalated as `tc-hjtb`, ruled 2026-09-08: the outbound Permit is scoped to
`ssh`/`scp` only, via the new `Effect.NetProducer` field (above) — not a blanket loosening of every
`EffectNet` producer. The companion `RemotePaths` shape question (`tc-vn5z` item 4b) was ruled at
the same time: a wildcard/default entry, and "protected"/"secret" stay two distinct categories,
mirroring `internal/deletable`'s local taxonomy exactly (though not given different operational
severity in this slice — see "Known gaps and follow-ups").

**Item 5 — env-value modeling port (RULED 2026-09-08, `tc-ife3` item 5: "port now, not defer").**
Slice 3an ported production's `internal/rules/envvars.go` hermetic-env-value approval logic —
`preservesCallerValue`'s EXTEND shape, `isHermeticEnvReplacement`, `isHermeticHomeReplacement`'s
`mktemp -d` idiom — into the spike's `EnvAssignment` policy, judging the actual assigned _value_
(e.g. `export PATH=/known/safe/dirs`) rather than the variable name alone. Several of production's
further widenings were deliberately **not** ported (documented in the code): each needs leaf-wide
context a single `Effect` cannot carry, and porting them was out of this item's scope.

**Item 6 — `cmdparse.LeavesOf`/`RootLeavesOf` relocation (RULED 2026-09-09, together with item 7:
"6 and 7 are approved").** A pure relocation, not a policy-behavior change: moved out of
`internal/cmdparse` into `internal/hookio` (slice 3ap, commit `2cc8fb5e`) so that `hookio` can
import `cmdparse` — the reverse import had been blocked by `cmdparse`'s only dependency on
`hookio`, which was exactly the two functions' shared `*hookio.HookInput` parameter type. With the
cycle resolved, `HookInput.ParsedLeaf`/`ParsedRoot` and `Evaluator.EvaluateStructure`'s `leaves`
parameter tighten from `any` to the concrete `[]cmdparse.ParsedCommand`, and roughly 25 call sites
across `internal/engine` and `internal/rules/*` were mechanically updated
(`cmdparse.LeavesOf` → `hookio.LeavesOf`, etc.). This is the change that lets the Claude Code
adapter package (item 7's second half, not part of this ADR slice) import both `hookio` and
`cmdparse`/`effectgraph` without an import cycle.

## Known gaps and follow-ups

**Decided in place, rather than left as a defect this ADR resolves:**

- The **build-tool family** design's `Class`/`Child` vocabulary reserves room for a future
  "wrapper" shape (`VerbChild.Kind`/`ArgSeparator`) that no policy yet reads — deliberately: the
  operator's own Q2 ruling was "bring a concrete example back for a ruling only if the spec is
  found insufficient", not "pre-design every extension now".
- **`EffectKeyMaterial`** is deliberately unjudged by any policy (`Abstain` always) — reviewing
  key-material references is explicitly future work, not a gap this ADR needs to flag as urgent,
  since the fail-closed fold means an unjudged effect can never silently Approve.
- **`nix run` on a remote/registry-resolved installable** stays `Unknown` even when an operator
  declares it under `VerbClassInstallableReference` — this is the sub-slice's own deliberately
  narrow scope choice (see Item 3 above), not an unruled gap.

**Genuinely open, carried forward for whoever finalises the policies (per `tc-8og1` item 7's own
note, found during slice 3ac):**

- **`git commit`/`add`/`rm`/`mv` emit an implicit `PathModify(".git")` effect, and this passes
  cleanly — not because any policy examined it and approved it, but because `NoWriteToReadOnlyPath`
  never consults `deletable.Classify`/`Protected` for a non-delete write.** `NoWriteToReadOnlyPath`
  (`internal/effectpolicy/policy.go`) judges every write-class effect except delete by patheval
  _zone_ alone (`IsDenyWrite`, then the read/write zone); it does not ask `deletable.Classify`
  whether the target path is `Protected` — only `DeleteAccess` (the delete-only sibling policy)
  consults that classification. `.git` is `Protected` under `deletable.Classify` (workspace.go's
  git kind), but since `gitCommitSchema`/`gitAddSchema`/`gitRmSchema`/`gitMvSchema` all model their
  implicit `.git` write as `PathModify` — not `PathDelete` — the `Protected` declaration is never
  consulted for these effects at all; the ordinary write-zone check (the working tree is writable)
  silently approves them. The verdict for these four commands happens to be the intuitively correct
  one — `git commit`/`add`/`rm`/`mv` genuinely should be permitted to touch `.git` — but the
  _reason_ is an accident of which policy runs, not a deliberate judgment that `.git`'s
  `Protected` declaration is inapplicable to a modify-class write. Slice 3ac's own investigation
  (commit `a81b20a7`) found and named this precisely while modeling `git worktree prune`/`add`,
  which deliberately avoided emitting an equivalent implicit `.git` effect at all rather than
  relying on the same accident. **This ADR records the inconsistency as a known gap for whoever
  finalises the policy set to resolve — either by making `NoWriteToReadOnlyPath` consult
  `deletable.Classify` for modify-class writes too (with an explicit carve-out for git's own
  bookkeeping paths), or by deciding explicitly that `Protected` is scoped to delete-class writes
  only and documenting that scoping on `deletable.Classify` itself. Fixing the policy code is
  explicitly out of scope for this documentation-only ADR slice.**
- **`RemotePathRule`'s `"protected"` and `"secret"` categories are distinct data but currently
  identical behavior** (both unconditionally Forbid every access class) — the operator's ruling
  ("keep protected/secret distinct") settled that they must be _representable_ separately, not that
  they must ever _diverge_ in verdict. `evalcontract.RemotePathRule`'s own doc comment flags this as
  "unruled but not consequential": an operator who wants `"secret"` to ever loosen (e.g. mirroring
  `NoWriteToSecretPath`'s `AccessModify` carve-out for a tracked, recoverable secret) has no local
  git-tracked-ness signal to ground that carve-out in for a _remote_ path, so the spike made the
  conservative call rather than inventing one.

## Consequences

### Positive

- One shared effect vocabulary (`cmddesc.Effect`, ~10 kinds) and a small, fixed policy set replace
  25+ independent per-command rule packages' worth of duplicated path/secret/network reasoning. A
  fix to how a write-zone check works benefits every command that produces an `EffectPath`, not
  just the one rule package that happened to be edited.
- The structural graph (`effectgraph`) gives every wrapper/recursion shape (`bash -c`, `xargs`,
  `find -exec`, `ssh HOST ...`, a build tool's own verb dispatch) one general mechanism —
  child invocations and scope propagation — rather than N bespoke recursion implementations, one
  per wrapper production happened to write a rule for.
- Every corpus/golden/agreement validation this spike ran against production's own historical
  ~52,800-command corpus is auditable per-row (root-caused, not just counted) — the discipline
  `tc-8og1`'s "HOW TO WORK THIS BEAD" section imposed on every slice.
- The build-tool family design (Item 3) generalizes past `just`/`npm`/`devbox`/`nix` to any future
  project-verb tool without a new rule package, by construction (live discovery + a data-driven
  approval class).

### Negative

- The spike duplicates, rather than reuses, several production types
  (`evalcontract.VerbScopedApproval`, `RemoteLifecycle`, `KubeContexts`, `RemotePaths`,
  `BuildToolVerbs`) as "stand-ins for a future `rules.json` binding" — every one of these carries an
  explicit code comment that production wiring is a follow-up, not this spike. Landing this
  architecture therefore requires a second, non-trivial migration to fold these into (or replace)
  production's actual `rules.json`/`configrules` shapes, not just a `RuleChain` rewire.
- The known gap above (`.git` `PathModify` bypassing `Protected`) is real production-adjacent
  behavior this architecture would inherit if landed as-is; it must be resolved (or explicitly
  ruled inapplicable) before item 8 (landing) can be approved as a like-for-like or improved
  replacement for production's git handling.
- `Ask` exists in the decision vocabulary (`evalcontract.Decision`) but nothing in the spike emits
  it yet — human-in-the-loop escalation is unimplemented, unlike production's own carve-outs for
  ambiguous cases.

### Neutral

- The spike is intentionally **not** wired into `internal/setup.RuleChain` and has made zero
  observable changes to production CETA behavior on this or any other host — every validation
  command in every slice's commit message ran against the spike's own isolated evaluator, never
  the live hook path.
- Vocabulary added by this spike (`Effect.Family`, `Effect.NetProducer`, the `deletable` package,
  the hermetic worktree/git-tracked probes) has no naming collision with any `internal/rules/*`
  or `internal/hookio` symbol as of this writing; a future migration is a genuine port, not a
  rename-around-conflicts exercise.

## Status and landing

This architecture is a **spike**: implemented across ~33 commits in an isolated workforest
worktree, validated slice-by-slice against production's historical corpus and its own golden/
agreement suites, but **not landed**. `tc-8og1` work item 8 (landing — integrating the branch,
wiring `RuleChain`, retiring or migrating `internal/rules/*`) remains explicitly gated on a
separate, deeper operator review: "i will do a deeper dive and review before 8 is approved"
(Phillip, 2026-09-09). This ADR itself (item 7, first half) and the planned Claude Code adapter
package (item 7, second half — translating `HookInput` into `evalcontract.Request`, including
`Write`/`Edit` tool calls as one-node graphs so file policies are single-sourced) were approved
together ("6 and 7 are approved") **independently of and prior to** item 8's landing approval.
Nothing in this ADR should be read as authorizing landing; a future ADR revision (or a fresh ADR)
should record item 8's own approval, once given, along with the resolution of the known gaps above.

## Alternatives Considered

### Keep adding per-command rules to `internal/rules/*`

Production's existing model. Rejected as the spike's premise, not evaluated fresh here: the three
structural gaps in "Context" above — no shared effect vocabulary, no shared recursion model, no
place to hang a cross-tool "family" concept — are inherent to a per-command-rule architecture, not
fixable by adding more rules of the same shape. `ADR 0033` (config-driven kubectl/build-tools) was
itself an earlier, narrower attempt to make two of those rules data-driven without changing the
underlying per-command architecture; this spike generalizes that same instinct (data over
hardcoded literals) to the whole policy surface.

### A fifth `patheval.PathAccess` zone value, instead of a layered `deletable` classification

Considered and rejected during the `deletable` package's own design (`tc-z806.1`): `PathAccess` is
an ordered enum every existing rule compares directly, and the bead required existing patheval
tests to pass unchanged. `deletable` is a separate package layered _over_ `patheval`'s four zones
instead, importing both `patheval` and `internal/temproot` (which `patheval` cannot import back
without a cycle).

### Copy production's "in-git-repo relaxation" pattern for non-secret detection

Rejected by explicit operator ruling (2026-09-07, verbatim, recorded on `tc-lc8f`/`tc-vn5z`): "i
dont want a in-git-repo relaxation rule... can we bake it into a general specification of
projects." The spike instead put the declaration in the workspace `Kind` mechanism
(`deletable.Kind.Secrecy`) rather than as a special case inside the secret-detection policy itself.

### A blanket "vetted host → Permit" network fix, ungated by producer

The operator's first-pass ruling on `tc-dpfl` ("Yes, Permit for vetted hosts") read this way, but
implementation found it would silently loosen `curl`'s own confirmed-upload marking too — a
behavior change never discussed in that ruling. Escalated as `tc-hjtb` and re-ruled narrower
(`Effect.NetProducer`, ssh/scp only) rather than implemented as originally worded. See Item 4 above.

## Related Decisions

- `ADR 0033` — CETA config-driven kc/kubectl + build-tools: the earlier, narrower data-driven
  precedent this spike's build-tool family design (Item 3) explicitly follows ("per ADR 0033's
  additive-only invariant").
- `ADR 0039` — CETA: one real shell parser front end for `cmdparse`: the parsing layer
  `internal/effectgraph`'s structural graph is built on top of.
- `ADR 0043` — CETA rule verdicts: separate "does not apply" from "no opinion" from "failed to
  determine": the spike's `Decision` vocabulary (`Abstain`/`Approve`/`Ask`/`Reject`) and its
  fail-closed fold are a direct continuation of that verdict taxonomy.
- `ADR 0053` — CETA threat model: agent-as-adversary, repo trusted, config injection screened: the
  threat model this spike's policies (secret paths, remote-write guards, key-material handling)
  operate under, unchanged by this architecture.
- `tc-z806` (design bead) — the deletable-path-class design this spike's `internal/deletable`
  package implements.
- `tc-8og1` — the resume bead this entire spike was executed under; its "Vocabulary added this
  session" note and "RULED" items 3-7 are the primary source for this ADR's content.
