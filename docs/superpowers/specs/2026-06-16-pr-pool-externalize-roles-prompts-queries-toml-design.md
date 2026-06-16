# pr-pool: externalize roles, prompts & queries to TOML config (spec C)

**Status**: Draft
**Date**: 2026-06-16
**Deciders**: Phillip Green II
**Bead**: `pg2-kplb` (depends on `pg2-ktqh` spec B, closed; builds on spec A `fbe23eb`)

## Context

pr-pool's roles, prompts, queries, and per-role characteristics are baked into the
Go binary:

- `internal/roles/roles.go` hardcodes exactly two roles (`Feedback`, `Worker`) in a
  two-field `Registry` struct built by `NewRegistry(cfg)`, discriminated by a
  `RoleKind` int enum. The role prompts are Go `const` strings (`feedbackNudge`,
  `workerNudge`) interpolated with positional `fmt.Sprintf`.
- `internal/discover/discover.go` switches on `RoleKind` to pick a query
  (`discoverFeedback` / `discoverWorker`); spec B already reduced both to pure
  `bd ready` label filters, leaving only a client-side `process-feedback:` title +
  `task` type guard on the feedback path.
- `internal/config/config.go` is a flat struct: `Default()` then a `PR_POOL_*` env
  overlay in `Load()` (which returns `Config`, no error). Its header comment
  (`config.go:1-4`) names TOML/XDG as a _deliberate future seam_.

The `RoleKind` enum is not just a query selector — it drives behavior in five
places, and that coupling is what makes the two roles un-externalizable today:

1. query selection (`discover.ForRole`)
2. budget prompt-line **+ watchdog** (worker only) (`orchestrator.workOneWithID`)
3. on send-failure: feedback **unclaims**, worker is **left** (`orchestrator`)
4. completion semantics: worker counts "open after seen-claimed" as a hand-back;
   feedback only "closed" (`complete.DoneSignal`)
5. failure action: worker adds `human`, feedback unclaims (`complete.OnFailure`)

This spec pulls roles, prompts, queries, and characteristics **out of the binary
into a repo-local TOML file**, modeling roles and queries as **typed tagged unions**
discriminated by a `type` field, each type carrying its own config struct decoded by
an **instance-scoped factory registry** (open/closed: adding a type is "register a
factory", no core change). The behavioral bundle keyed on `RoleKind` is decomposed
into declarative, code-owned configuration so the two existing roles become two
ordinary config entries and the enum is deleted.

Decided across the 2026-06-15 A→B→C brainstorm and a 2026-06-16 design + review
session (captured in Decisions; the review's findings are folded into the design).

## Decisions

1. **Scope: full typed-union surface.** Build now — role types `ccpool` (real) and
   `command` (real); query types `beads-ready` (real), `beads-list` (real),
   `command` (real), `github-issues` (decode/validate stub), `jira-issues`
   (decode/validate stub). Defer the `event` query type entirely (→ `pg2-r6cf`).
2. **Intra-type behavior = explicit, code-owned enum attributes nested under the
   type name.** e.g. `ccpool.completion = "close-or-handback"`. The discriminator
   `type` and the sub-table key are the same string; the factory for `type` decodes
   the same-named sub-table. Operators pick enum _values_; Go owns each value's
   _implementation_ (including the safety-critical `seenClaimed` and single-terminal
   watchdog mechanics).
3. **Decode mechanism: `BurntSushi/toml` `Primitive` + an instance-scoped factory
   registry.** The library is the workspace standard (shared with `ccpool`,
   `pa-monitor`) **but is a new direct dependency for pr-pool** (`go.mod` add +
   `update-locks.sh`), and the `Primitive`/`PrimitiveDecode`/`MetaData` two-pass
   pattern is **new to the workspace** — the closest existing analogue is ccpool's
   `notify.FromConfig` `switch`. The fat-struct fallback (Alternatives) stays ready.
4. **Safety rails: Go-injected non-editable preamble (for now).** Gated by
   `ccpool.authorship_guard = true`. The `prompt_file` holds only the task; Go
   prepends the author/branch/no-force-push rails. Explicitly "good enough for now;
   a better (structural) mechanism comes later."
5. **Config location: repo-local `<RepoRoot>/.pr-pool/config.toml`**, with a
   `PR_POOL_CONFIG` path override. `prompt_file` resolves relative to the config
   file's directory.
6. **Role presence: TOML replaces built-ins.** A present `[[role]]` array is
   authoritative; a file with neither a `[[role]]` array nor a `[role]` table
   (pool-only, or no file) → the built-in feedback+worker `RoleSet`. A top-level
   single-bracket `[role]` table → **error** (catches the common `[role]`-vs-
   `[[role]]` typo so it can't silently fall back to built-ins).
7. **Drop the legacy role-specific env vars** (`PR_POOL_MAX_WORKER`,
   `PR_POOL_MAX_FEEDBACK`, `PR_POOL_*_ENABLED`, `PR_POOL_SKILL_MD`,
   `PR_POOL_WORKER_SKILL_MD`). Role identity lives only in config / built-in
   defaults. A one-release **deprecation warning** fires if any are set (§7).
8. **Generalize the dispatch item** to a typed `Item` (`id`/`type`/`title`/
   `metadata`); status/labels/created-by stay re-fetched by id (§2).
9. **Structured dispatch `Result`** (action log) returned by each dispatch,
   superseding the ad-hoc `slog` "created" markers, with an explicit
   `indeterminate` state preserving today's `created="unknown"`.
10. **Interpolation via Go `text/template`** (`missingkey=error`, parsed once),
    validated at config-load with a dry render.
11. **Subsume `pg2-wgg0`** — pool-level + per-role budget defaults delivered here.
12. **Stage the work** (§Implementation staging): Phase 1 = config/decode/Item/
    query/`complete`-enum + built-in `RoleSet` + command role via a **thin
    type-switch** in the existing orchestrator. Phase 2 = extract the `Executor`
    interface (the high-risk move of the watchdog/single-terminal code) with its
    own race test. Phase 1 ships value without touching the pg2-c1vp machinery.

## Design

### 1. Config resolution, layering & validation

Config file path: `PR_POOL_CONFIG` if set, else `<RepoRoot>/.pr-pool/config.toml`.
`RepoRoot` is `PR_POOL_REPO_ROOT` or cwd (unchanged). At startup the resolver
**logs the decision** ("loaded config <path>" / "no config found at <path>; using
built-in roles") so a cwd≠RepoRoot mismatch is visible, not silent (UX trap).
`prompt_file` and relative paths resolve relative to the config file's directory.

**`Load()` returns `(Config, error)`** (was `Config`). A present-but-malformed file,
an unknown type, a failed `Validate()`, or a bad `prompt_file` path is a **hard
error** (fail fast, mirroring ccpool's `loadFrom`), never a silent fallback. The
three call sites (`drain.go:43`, `runrole.go:36`, `runrole.go:70`) gain error
handling (the `precheck`/`resolveSelf` error idiom is already there to copy).

**Layering — pool scalars** (`RepoRoot`, gates, `PollInterval`, `MaxWait`,
`SessionPrefix`, `Model`, `Effort`, `PermissionMode`, budget defaults, watchdog
thresholds/messages, `LogDir`, `self_login`): `Default() → TOML [pool] →
PR_POOL_* env`. To avoid BurntSushi's **zero-value-vs-unset gotcha** (a decoded
`bool=false`/`int=0` is indistinguishable from "absent" when decoding over non-zero
defaults), any pool scalar whose default is non-zero and which must be overridable
_downward_ is decoded via a presence check (`MetaData.IsDefined("pool","key")`) or a
`*T` pointer field, then collapsed onto the concrete `Config` field. A test asserts
`enabled = false` in TOML actually disables.

**Layering — roles:** the `[[role]]` array, or the built-in `RoleSet` (§8). **No env
overlay reaches role fields** (decision 7). The decoded, validated `RoleSet` is
stored on `Config`; `Config.Validate()` stays a pure method over it (one fail-fast
gate). Validation **aggregates** with `errors.Join`, each joined error wrapping the
offending role/query name and TOML key path (the `DispatchContext.Validate`
all-errors-at-once style, generalized).

### 2. The Item model (`internal/item`)

```go
package item

// Item is one unit of work a query yields. It generalizes a bead. Metadata carries
// source-specific fields exposed to prompt interpolation.
type Item struct {
    ID       string
    Type     string
    Title    string
    Metadata map[string]any
}
```

`beads.Issue → item.Item` maps `ID, Type, Title, Metadata` directly (`Issue` already
has `Metadata map[string]any`, `issue.go:18`). `Issue.Status`, `.Labels`,
`.CreatedBy`, `.Parent` are **not** copied into `Item`: the flows that need them
re-fetch by id — `complete.DoneSignal` reads status via `beads.Status(id)`, and the
created-marker snapshot diff (`createdByActor`) reads `beads.List → Issue.CreatedBy`
directly, independent of the dispatch `Item`. `DispatchContext.BeadID` becomes
`DispatchContext.Item`; `Item.ID` is the id every bead-backed mutation addresses.

### 3. Typed-union decode (`Primitive` + instance Registry)

Decode is **two passes** (the per-element sub-table validation §3 requires cannot be
done from the typed struct alone — `MetaData.IsDefined` does not traverse
arrays-of-tables, verified against BurntSushi v1.6.0):

1. Decode the file into typed elements capturing common fields, with the
   `type`-named sub-table held as a `toml.Primitive` (retaining `MetaData`).
2. Decode `[role]` **also** into `[]map[string]toml.Primitive` to enumerate each
   element's actual sub-table keys, so a typo (`[role.cppool]` under `type="ccpool"`)
   is detected as "stray/missing sub-table" per element. The query's own `[<type>]`
   typo (e.g. `[role.query.beads_ready]` under `type="beads-ready"`) is checked the
   same way, recursing one level into each element's `query` sub-map.

The factory registry is an **instance value** (not package-level `init()` maps —
that fights this codebase's pervasive constructor-injection convention):

```go
type Registry struct {
    roles   map[string]roleFactory
    queries map[string]queryFactory
}
func NewRegistry() *Registry { /* seeds built-in role & query types */ }
func (r *Registry) DecodeRole(md toml.MetaData, common roleCommon, prim toml.Primitive) (Role, error)
func (r *Registry) DecodeQuery(md toml.MetaData, common queryCommon, prim toml.Primitive) (Query, error)
```

`Load()` constructs one `Registry` and threads it through decode. "Adding a type =
register a factory" becomes one line in `NewRegistry`, not a scattered `init()`.

**Enums are typed string constants with `UnmarshalText` validators** (mirroring
ccpool's `Duration`), not raw strings validated late: `type Completion string`
(`close-only`|`close-or-handback`), `OnFailure` (`unclaim`|`add-human`),
`OnDispatchFail` (`unclaim`|`leave`), `QueryFormat` (`jsonl`|`json`). A bad value
fails _at decode_ with BurntSushi's line/key context, and `DoneSignal`/`OnFailure`
take the typed values so a typo can't reach them.

**TOML shape** (worker; `type` selects factory + same-named sub-table):

```toml
[pool]
self_login = "phillipg"          # optional; see §6 precedence
max_wait   = "30m"
budget     = { time = "25m" }    # pool default; per-role budget overlays it (subsumes pg2-wgg0)

[[role]]
name    = "worker"
type    = "ccpool"
cap     = 1
enabled = true
  [role.query]
  type = "beads-ready"
    [role.query.beads-ready]
    labels         = ["worker-ready"]
    exclude_labels = ["human"]
  [role.ccpool]
  actor            = "pgii-pool__worker"
  completion       = "close-or-handback"
  on_failure       = "add-human"
  on_dispatch_fail = "leave"
  authorship_guard = true
  prompt_file      = "worker.md"
  budget           = { time = "25m" }   # overlays [pool].budget field-by-field
```

**Validation rules** (all aggregated, surfaced at pre-flight): unknown role/query
`type` (lists registered types); the `[<type>]` sub-table present exactly once for
the declared `type`; a sub-table for a non-declared type is an error (typo guard,
via the §3 second pass); `prompt` XOR `prompt_file` on `ccpool` roles; missing
`prompt_file` path; unknown enum value (caught at decode); duplicate role `name`;
empty `name`; a top-level single-bracket `[role]` table (the `[[role]]` typo) →
error. **Templates are dry-rendered at load** against a dummy context so a `{{.Typo}}`
fails at pre-flight, not mid-drain.

### 4. Query types (`internal/query`)

```go
type Query interface {
    Validate() error
    Run(ctx context.Context, env Env) ([]item.Item, error)
}

// Env carries the capabilities a query needs. Commander is a one-method interface
// (consistent with beads.Runner / ccpool.Runner), not a bare func field.
type Env struct {
    BD       beads.Runner
    RepoRoot string
    Cmd      Commander // exec seam; fake in tests, os/exec default
}
type Commander interface { Run(ctx context.Context, argv []string) ([]byte, error) }
```

In Phase 1 the orchestrator constructs `Env` from its own fields (`o.BD`,
`o.Cfg.RepoRoot`, a `Commander`) and passes it into `role.Query().Run(ctx, env)`.
(The `Deps` bag — §5 — arrives in Phase 2; Phase 1 needs no `Deps`.)

- **`beads-ready` / `beads-list` (real).** Config:
  `{ labels, exclude_labels, title_prefix?, item_type? }`. Runs `bd ready` /
  `bd list` with the label args, then applies the optional `title_prefix` +
  `item_type` **client-side post-filters** — this is exactly how the feedback
  cycle's `strings.HasPrefix(title,"process-feedback:") && type=="task"` guard
  (`discover.go:94`) becomes config. The built-in feedback default sets both.
- **`command` (real).** Config: `{ argv: []string, format: QueryFormat }`. Runs
  `argv` via `Env.Cmd`, parses stdout into `[]Item`. **Item contract:** `format =
"jsonl"` is one JSON object per line; `format = "json"` is a top-level array.
  Each record: `{"id": string (required), "type": string, "title": string,
"metadata": object}`. A record missing `id` → error. A **non-zero exit or
  unparseable output → error, propagated** (never silently "no work" — the spec-B
  `pg2-qq9v` rule); **exit 0 with empty stdout → legitimately zero items** (the one
  real "no work" case). This is the only non-bead real query, so it exercises the
  `Item` model end to end.
- **`github-issues` / `jira-issues` (stubs).** Config decodes and `Validate()`
  succeeds; `Run()` returns `"github-issues query not yet implemented (<bead>)"`.
  Pre-flight emits a **warning** ("query type X is a stub") so the operator learns
  before drain time, while `Validate()` still passes (design intent).

`discover.ForRole` collapses to: `items := role.Query().Run(ctx, env)`, wrap each in
`DispatchContext{Role, Item}`. The `RoleKind` switch and
`discoverFeedback`/`discoverWorker` are deleted. `Discover` iterates the ordered
`RoleSet` in config order, honoring each role's `Enabled`, and **propagates** a
`Run` error (never returns it as empty).

### 5. Roles, `RoleSet`, and the `RoleKind` → attributes refactor

`RoleKind` is **deleted**. `roles.Registry` (the 2-field struct) becomes an ordered
**`RoleSet []Role`** (built-in or TOML-derived via the one code path of decision 6).
`Orchestrator.Reg` becomes the `RoleSet`; `DrainOnce` **ranges over it** instead of
calling `drain` twice by name; `drain`'s per-role filter changes from
`d.Role.Kind != role.Kind` to `d.Role.Name != role.Name` (names are unique per §3);
`countByRole` becomes per-name tallies. A role is:

```go
type Role struct {
    Name    string
    Type    string        // "ccpool" | "command"
    Cap     int
    Enabled bool
    Query   query.Query
    CCPool  *CCPoolConfig  // set iff Type=="ccpool"
    Command *CommandConfig // set iff Type=="command"
}
```

`ExternalID`/`DisplayName` (which use `r.Name`) survive unchanged.

**Per-dispatch execution — staged (decision 12):**

- **Phase 1 — thin type-switch** inside the existing `workOneWithID`: `switch
role.Type`. `ccpool` runs today's `ensure → send → wait` path _in place_; the
  former `RoleKind` branches become reads of `role.CCPool`:
  - `completion` → `complete.DoneSignal(completion, status, seenClaimed)` (the
    `seenClaimed` startup-race guard stays a caller-supplied loop arg; it belongs to
    `close-or-handback`).
  - `on_failure` → `complete.OnFailure(ctx, br, onFailure, beadID)`.
  - `on_dispatch_fail` → the send-failure action (`unclaim`|`leave`).
  - `budget` finite → append budget prompt-line **and** run the watchdog; ≤0 →
    neither (this is what makes "feedback has no watchdog" fall out of config). The
    per-role `budget` **overlays** `[pool].budget` field-by-field; an unset role
    field inherits the pool default; precedence `role > pool > Default()`.
  - `authorship_guard` → injected preamble (§6); plus `actor`, `skill_md`,
    `prompt`/`prompt_file`.
    `command` runs `role.Command.argv` (interpolated) once per item via the `Commander`
    seam; success iff exit 0. `on_failure` applies only when the role's **query is a
    `beads-*` type** (bead-backed, known at config time — not inferred per item, not a
    probe `bd show`); for a command role over a `command`/stub query it is a no-op
    (logged). The created-marker snapshot diff is skipped when `actor` is empty.
- **Phase 2 — extract `Executor`** (separate plan): `type Executor interface {
Dispatch(ctx, d DispatchContext, deps Deps) (report.Result, error) }`, with
  `ccpoolExecutor` / `commandExecutor` concrete types. The watchdog/single-terminal
  (`pg2-c1vp`) code moves here verbatim; the move ships only behind its own
  golden + race test (§9). `Deps` is the explicit seam bag the executor needs:

```go
type Deps struct {
    CC   ccpool.Runner
    BD   beads.Runner
    Cmd  query.Commander
    Log  *eventlog.Writer // may be nil
    Cfg  config.Config
    now  func() time.Time          // clock/tick/stamp/reader seams, as today
    tick func(context.Context, time.Duration) error
    stamp func() string
    usageReader usage.Reader
}
```

`Executor` is declared in `roles` (alongside `Role`); concrete executors live in
their own package importing `roles`/`report`/`ccpool`/`watchdog`/`complete`
downward — see the import DAG in §10.

### 5a. Dispatch result / action log (`internal/report`)

A pure-value leaf package (no I/O, imports nothing in-repo → no cycle):

```go
package report
type Verb    string                              // typed; closed vocabulary
const ( Created Verb="created"; Closed Verb="closed"; HandedBack Verb="handed-back";
        Unclaimed Verb="unclaimed"; Escalated Verb="escalated"; Indeterminate Verb="indeterminate" )
type Ref    struct { Type, ID string }            // {"bead","pg2-xyz"} — Type expandable
type Action struct { Verb Verb; Refs []Ref }
type Result struct { Actions []Action }
```

- Population (the dispatch path has all inputs): the `createdByActor` snapshot diff →
  `Created`; a failed snapshot read → **`Indeterminate`** (preserves today's
  `created="unknown"`, `orchestrator.go:158-160`); terminal status →
  `Closed` / `HandedBack`; `OnFailure`/dispatch-fail → `Unclaimed` (feedback,
  budget hard-stop) / `Escalated` (worker add-human). `command` over a non-bead item
  → empty `Actions`.
- Signature: dispatch returns `(report.Result, error)` — `error` still signals
  _flagged_ for the complete/flagged counts; `Result` is emitted regardless.
- **Sink:** serialize `Result` into `eventlog.Emit(level, "dispatch", msg, fields)`
  where `fields` carries `actions` as a slice of `{verb, refs}` (Emit takes a flat
  `map[string]any`, `eventlog.go:50`). When `o.Log == nil` (the `run-role`/smoke
  path), the `Result` is printed to stdout (so the harness still shows what
  happened). The actions also roll into the human-readable drain summary, not only
  the JSONL.
- `report` holds only value types; `complete` and the dispatch path _import_ it
  one-way (`report` never imports them).

### 6. Prompt interpolation & safety preamble (`internal/prompt`)

- **Engine:** Go `text/template`, **parsed once** at role build (a `*template.Template`
  on the role/executor, `Option("missingkey=error")` at parse), reused per render —
  not re-parsed per dispatch. Named fields: `{{.BeadID}}` (= `.Item.ID`),
  `{{.Item.Type}}`, `{{.Item.Title}}`, `{{.WorktreeDir}}`, `{{.SkillMD}}`,
  `{{.SelfLogin}}`, `{{.RepoRoot}}`, `{{.Item.Metadata.x}}`. `missingkey=error` fires
  on a missing map key; a present non-string `Metadata` value renders via `%v`
  (documented). `DispatchContext` grows the resolved fields.
- **`self_login` precedence:** `[pool] self_login` if set, else the retained
  `pg-pr config show --json` `resolveSelf` (`drain.go:79-80`), else precheck error.
  (The spec-B daemon precheck stays; config just lets it be pinned/overridden.)
- **Safety preamble:** when `ccpool.authorship_guard = true`, the ccpool path
  **prepends a fixed, code-owned block** to the rendered task prompt (author==me;
  branch `phillipg.`-prefix; make NO changes / add `human` otherwise; NEVER
  `git push --force`). Not in any `prompt_file`, so editing config can't weaken it.
  (Decision 4 — structural enforcement is future work.)

### 7. Orchestrator & CLI

- **Orchestrator:** `DrainOnce`/`RunOne` keep their shape; Phase 1 adds the
  `role.Type` switch in `workOneWithID` and the `RoleSet` range (§5). `teardownAll`
  and gates are unchanged (still keyed on `SessionPrefix`).
- **CLI tokens are the role `Name`** (one name to learn, not the legacy
  `feedback`→`feedback-processor` short-token mapping). Arg parsing stays **pure**
  (no config load, per `pg2-52rn`): it checks a role token is _present_ only. The
  **configured-name** check moves into the `run-role`/`run-query` handlers _after_
  `config.Load()`, returning `exitUsage` with the message **enumerating the
  configured role names** (`unknown role "x" (configured: feedback, worker)`).
  `args_test.go`'s current "unknown role → routeUsageErr at parse" case **flips** and
  must be re-pointed to the handler (not deleted). Ordering note: the name check now
  runs after `precheck`, so a bad name on a failing-precheck repo reports precheck
  first (acceptable; documented).
- **Discoverability deliverables:** ship `packages/pr-pool/config.example.toml` that
  is the exact serialization of the built-in `RoleSet`, and add `pr-pool config
--print-defaults` (writes that canonical TOML to stdout) and `pr-pool config
--show` (prints the resolved config path + effective role set). `--help` documents
  the config path resolution, the retained `PR_POOL_*` pool scalars, and the
  **removed** role env vars with their config replacements (for one release).
- **Safety/UX prechecks:** (a) if `<RepoRoot>/.pr-pool/config.toml` is **git-tracked**,
  warn loudly (prompts may be committed to the monorepo; add `.pr-pool/` to
  `.git/info/exclude`); (b) if any **dropped role env var** is set, warn naming the
  config replacement (one-release deprecation).

### 8. Backward compatibility & built-in defaults

With no `<RepoRoot>/.pr-pool/config.toml`, behavior is **identical to today**. The
built-in `RoleSet` is an in-Go value equivalent to a canonical `config.toml`:

- **feedback** — `type=ccpool`, `cap=1`, `actor=pgii-pool__process-feedback`,
  `completion=close-only`, `on_failure=unclaim`, `on_dispatch_fail=unclaim`,
  no budget (no watchdog), `authorship_guard=false`, today's feedback prompt;
  query `beads-ready { labels=[mine], exclude_labels=[human],
title_prefix="process-feedback:", item_type="task" }`.
- **worker** — `type=ccpool`, `cap=1`, `actor=pgii-pool__worker`,
  `completion=close-or-handback`, `on_failure=add-human`, `on_dispatch_fail=leave`,
  budget from `[pool]`/defaults, `authorship_guard=true`, today's worker _task_
  prompt (rails moved to the injected preamble); query
  `beads-ready { labels=[worker-ready], exclude_labels=[human] }`.

`config.example.toml` is generated from this set (the `--print-defaults` serializer),
so it doubles as living documentation and the copy-paste starting point.

**Behavior change to call out (decision 7):** the role env vars `PR_POOL_MAX_WORKER`,
`PR_POOL_MAX_FEEDBACK`, `PR_POOL_FEEDBACK_ENABLED`, `PR_POOL_WORKER_ENABLED`,
`PR_POOL_SKILL_MD`, `PR_POOL_WORKER_SKILL_MD` are removed (verified: referenced only
in historical plan docs, not in any live nix module / wrapper). Document the removal
in README + `--help`; the precheck warning (§7) covers stragglers.

### 9. Testing strategy

**Port-then-refactor mandate (highest priority).** Before deleting `RoleKind`, keep
`complete_test.go`, `roles_test.go`, `orchestrator_test.go`, `runrole_test.go`
**green through the refactor** by re-expressing each case on the new enum/`RoleSet`
shapes — with explicit case-count parity. Named must-survive cases: `complete`'s
`worker open pre-claim NOT done (startup race)` and the two
`orchestrator` `TestWaitDone_lostRace_*` (`pg2-c1vp` single-terminal) guards.

- **Decode/validate — table-driven with inline TOML string bodies** (not dozens of
  `testdata/` files): one row = body + expected error substring, covering valid
  round-trip, unknown role/query type, wrong/duplicate/missing `[<type>]` sub-table,
  `prompt` XOR `prompt_file`, bad enum (decode-time), duplicate name, missing
  `prompt_file` path, `[role]`-vs-`[[role]]` typo, template dry-render typo.
  Reserve `testdata/` only for the canonical full config golden.
- **Config resolution/layering** (in `t.TempDir()`, never a real `.pr-pool/`):
  `PR_POOL_CONFIG` wins over repo-local; absent file → built-in `RoleSet`; malformed
  → hard error; `prompt_file` resolved against config dir; `PR_POOL_*` overlays a
  `[pool]` scalar but a removed role env var is a no-op; `enabled=false` in TOML
  actually disables (the zero-value gotcha test).
- **Query types:** `beads-ready`/`beads-list` with a fake `beads.Runner` incl. the
  `title_prefix`/`item_type` post-filters; `command` query through a **fake
  `Commander`** — exit≠0 → error propagated (not empty), malformed output → error,
  **empty stdout + exit 0 → legitimate zero items**; stubs assert the
  not-implemented error + `Validate()` passes + the pre-flight warning.
- **Direct `discover_test.go`** (new): config order preserved; disabled role yields
  no dispatches; a query `Run` error propagates.
- **Behavior enums:** `DoneSignal` `close-only` vs `close-or-handback` (incl. the
  `seenClaimed` race); `OnFailure` `unclaim` vs `add-human`; budget finite →
  watchdog / ≤0 → none.
- **Interpolation:** named fields incl. `.Item.Metadata.x`; missing key → render
  error; present non-string metadata value → defined behavior; preamble present iff
  `authorship_guard`.
- **`command` role:** via the fake `Commander` — exit 0 → completed; exit≠0 →
  flagged, with `OnFailure` fired iff the role's query is a `beads-*` type, and a
  no-op for a `command`-query command role.
- **Dispatch `Result`:** a table mapping each terminal outcome → expected
  `Action.Verb` + refs (closed/handed-back/created/unclaimed/escalated/
  **indeterminate** on snapshot-read failure); plus an eventlog round-trip
  (mirroring `eventlog/schema_test.go`) asserting the serialized `actions` field.
- **Backward-compat golden — structural + literal** (NOT diff-the-binary): pin
  `o.stamp` (reuse `testStamp`) so external_id is deterministic; assert `env` as a
  key/value map; assert the contract substrings against the **full sent nudge
  (preamble + rendered task)** — reusing `TestWorkerNudge_contract`'s rail substrings
  (`phillipg.`, `--add-label human`, `NEVER git push --force`), which now live in the
  injected preamble, **not** in the `prompt_file` task body (the task body alone must
  no longer contain them — assert that too, so decision 4 can't silently regress);
  assert deterministic `Result.Actions` order.
- **CLI:** `args_test.go` updated so an unknown role is _not_ a parse-time usage
  error (only missing/extra args are); a handler-level test that `resolveRole`
  against the configured set returns `exitUsage` before any dispatch.
- Gate: both Go suites + `nix flake check` + `prek run --all-files` green.

## Implementation staging

- **Phase 1 (this spec's core):** `internal/item`, `internal/query` (+ `Commander`),
  `internal/report`, `internal/prompt`, the `config` decode/`Registry`/typed-enums/
  `Load()→(Config,error)`, `RoleSet` + `complete` enum signatures, `discover`
  rewrite, the orchestrator **thin type-switch** (ccpool in place + command), CLI +
  example-config/`--print-defaults`/prechecks, and the structural backward-compat
  golden. Ships config-driven roles + a working command role **without touching the
  pg2-c1vp watchdog/single-terminal code**.
- **Phase 2 (separate plan, separate bead):** extract the `Executor` interface and
  move the watchdog/single-terminal mechanics into `ccpoolExecutor`/`commandExecutor`
  verbatim, behind its own race + golden test. Pure structural refactor, no new
  behavior.

The writing-plans step produces the Phase 1 plan; Phase 2 gets its own bead +
plan after Phase 1 merges.

## Out of scope / deferred

- **`event` query type and the full event/queue model** (`pg2-r6cf`).
- **Real `github-issues` / `jira-issues` clients** — new follow-up bead(s); the stub
  `Run()` errors name them.
- **Structural enforcement of the worker safety invariants** (decision 4 keeps the
  injected preamble for now).
- **`pg2-wgg0`** — delivered by `[pool]`/per-role `budget` here; close/coordinate on
  merge.
- **Operational:** add `.pr-pool/` to the ZipRecruiter monorepo's `.git/info/exclude`
  (the §7 precheck warns if this is missed).

## Import DAG (verify during implementation)

```
item            (leaf; no in-repo imports)
report          (leaf; value types only)
query    → item, beads
roles    → query, item, report, config, prompt        (declares Executor interface)
prompt   → item
complete → roles, beads, report                        (imports report one-way)
executors→ roles, report, ccpool, watchdog, complete   (Phase 2; imports down)
orchestrator → roles, discover, complete, report, eventlog, ccpool, watchdog, config
```

`report` imports no other internal package (asserted by a test/lint). The only cycle
risk is `roles ↔ executors`, avoided by declaring `Executor` in `roles` and keeping
concrete executors in their own downward-importing package.

## Alternatives considered

- **`go-toml/v2` custom `UnmarshalTOML`** — rejected: a second TOML library in a
  workspace standardized on `BurntSushi/toml`.
- **Fat struct, one optional pointer per variant** (no `Primitive`) — kept as the
  fallback if `Primitive` two-pass threading proves too heavy; rejected as primary
  because every new type edits the core struct (breaks "register a factory").
- **Package-level `init()` factory registry** — rejected: global mutable state +
  init-ordering + broken test isolation; contradicts the codebase's
  constructor-injection convention. Replaced by the instance `Registry` (§3).
- **Explicit free attributes vs named code-owned behavior profiles** — settled on
  explicit _typed-enum_ attributes nested under the type name.
- **XDG / dual-mode config location** — rejected for repo-local; pr-pool always
  operates on a specific repo and the prompts/roles are repo-shaped.

## Related decisions

- Spec A: `docs/superpowers/specs/2026-06-15-pr-pool-stop-on-done-and-role-smoke-harness-design.md`.
- Spec B: `docs/superpowers/specs/2026-06-15-pr-pool-eliminate-feedback-discovery-join-design.md`.
- `pg2-r6cf` (event model), `pg2-wgg0` (budget seam), ADR 0015 (resumability /
  per-attempt external_id), `pg2-c1vp` (single-terminal race), `pg2-qq9v`
  (propagate-don't-swallow), `pg2-52rn` (no fall-through on bad CLI input).
