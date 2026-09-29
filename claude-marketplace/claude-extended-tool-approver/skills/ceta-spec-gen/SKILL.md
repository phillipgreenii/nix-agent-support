---
name: ceta-spec-gen
description: Author or regenerate a v1 command/path/target spec (internal/specfmt.CommandSpecV1 or TargetSpecV1) with real per-fact citations, generated goldens, and a passing lint run. Use when a command lacks a spec, an existing spec has thin/placeholder citations, or a custom (non-built-in) command needs a spec routed to the correct P17 layer.
---

# ceta-spec-gen

**This is a Claude Code skill — markdown instructions read and followed by an invoking agent**
(via the Skill tool where the `claude-extended-tool-approver` marketplace plugin is installed, or
by directly reading and manually following this file's text when it is not). **It is NOT a CLI
binary or subcommand.** There is no "run ceta-spec-gen" invocation surface; there is only "read
this file and do what it says."

Module root: `packages/claude-extended-tool-approver` (all relative paths below are relative to
this root unless stated otherwise). Ceta marketplace plugin root:
`claude-marketplace/claude-extended-tool-approver` (this skill's own home).

## What you produce

Given ONE of:

- **(a) a command name**, plus an optional path to that command's own source (for a custom /
  local script or a tool whose source you have on disk), or
- **(b) a path or host subject** (a filesystem zone or a docker/kube/vault/ssh/git-remote target
  — see "Path/target subjects" below),

you emit a `internal/specfmt` v1 spec: a `KindCommand` spec (`internal/specfmt.CommandSpecV1`) for
(a), or a `KindTarget` spec (`internal/specfmt.TargetSpecV1`) for (b) — plus a generated-goldens
sidecar (see "Generated goldens" below) and a completed, HARD-clean `lint` run. Read
`internal/specfmt/v1.go` (the wire types), `internal/specfmt/validate.go` (what gets rejected —
including a missing citation) and `internal/specfmt/repository.go` (the P17 layered loader your
output must be readable by) before writing anything; the rest of this file assumes you have.

## Citation methodology (the part every downstream packet depends on)

**Every fact-bearing element carries a real citation.** For a command spec that is: the four
top-level `Citations` keys (`provenance`, `stdin`, `stdout`, `unknownFlag`, plus `interpreter`
when `Interpreter` is non-empty), `Positionals.Citation`, every `FlagSpecV1.Citation`, and every
`ImplicitEffectV1.Citation`. For a target spec: `TargetSpecV1.Citation`.

A **real citation** is one of:

1. A verbatim excerpt (or precise paraphrase naming the exact line) from `<command> --help`.
2. A man-page section/paragraph reference (`man <command>`, e.g. `"man jq(1), OPTIONS section,
--arg entry"`).
3. A source-line reference into the tool's own source (`<file>:<line>`, when a source path was
   given as part of the input, or the command is itself part of this repo).

A citation is **NOT real** — it is the **thin, interim placeholder** — when its `Source` string
contains both the substrings `"registry.go"` and `"DefaultRegistry()"` (this is exactly
`internal/speclint`'s `isThinCitation` check, `internal/speclint/lint.go`). That shape is what
Phase 1.2's mechanical marshaller stamped on every fact it pulled from
`cmddesc.DefaultRegistry()["<name>"]` — it points at _where the field lives in Go_, not at the
`--help`/man/source evidence a real citation records. **Never produce that shape.** See
`references/worked-example-citation.md` for one fully worked command → excerpt → `Citation` →
`Role` example (`jq`'s `--from-file`), and `references/interpreters-and-dialects.md` for where to
find `--help`/source evidence for a `KindProgram` role's `Dialect` or a command's own
`Interpreter`.

**Keep your raw evidence.** Paste the `--help`/`man`/source excerpt into your own scratch notes
_before_ writing the JSON — the `Citation.Source` string should quote (or precisely locate) what
you actually read, not what you remember reading.

## Step-by-step procedure (command spec)

1. **Classify: built-in or custom.** Built-in = the name already has an entry in
   `cmddesc.DefaultRegistry()` (`internal/cmddesc/registry.go`) and/or an existing file at
   `internal/embeddedspecs/data/<name>.json`. Custom = neither. This decides your output location
   (see "Output routing" below) and which severity a thin citation would trigger if you slipped
   (WARN for builtin-only `lint --embedded`, HARD everywhere else per
   `internal/speclint/lint.go`'s `citationFinding`) — but you must never rely on that severity
   distinction: skill-generated specs carry real citations unconditionally, per this docket's own
   binding decision.
2. **Gather ground truth.** Run `<command> --help` (and `--version` for `provenance`). Check
   `man <command>` if present. If a source path was given, read the actual flag-parsing code for
   exact arity/operand semantics. For a built-in you are _regenerating_ (not authoring fresh),
   cross-check against the existing `cmddesc.DefaultRegistry()["<name>"]` entry in
   `internal/cmddesc/registry.go` for the STRUCTURE (which flags exist, their arity, their current
   `Role`) — you are re-sourcing its citations, not re-inventing its shape, unless your fresh
   `--help` reading disagrees with it (in which case the fresh evidence wins; note the
   discrepancy).
3. **Determine `Provenance`, `Stdin`, `Stdout`, `UnknownFlag`.**
   - `Provenance`: a short string naming the version/source you read (e.g. `"jq 1.8.2, jq
--help"`), cited by the `provenance` key.
   - `Stdin`/`Stdout` wire values are `"never"` / `"always"` / `"when-no-path-operands"` (stdin)
     and `"none"` / `"content"` / `"metadata"` (stdout) — see `internal/specfmt/convert.go`'s wire
     constants; derive them from the `--help`/man text describing the command's stdin/stdout
     behavior.
   - `UnknownFlag` MUST be `"insufficient"`. **Never** `"inert"`
     (`cmddesc.UnknownFlagInert`'s wire spelling) — see "UnknownFlagInert is forbidden" below.
4. **Model each flag** as a `FlagSpecV1`: `Arity` (`"none"` / `"one"` / `"optional-glued"` /
   `"n"`), `Operand`/`Operands` (an `OperandRoleV1{Kind: ...}` — `Kind` is one of
   `cmddesc.RoleKind.String()`'s spellings: `literal`, `path-read`, `path-create`, `path-modify`,
   `path-delete`, `path-truncate`, `program` (+ `Dialect`), `message`, `data-or-at-file`, `remote`
   (+ `Operation`), `env-assign`, `unmodeled`, `chdir`, `exec`, `key-material` — see
   `internal/cmddesc/schema.go`'s `RoleKind` doc comments for what each means), `Transform` when
   the flag rewrites the effect's shape (e.g. `--force` → `TransformForce`'s wire spelling), and
   its own `Citation`.
5. **Model `Positionals`** the same way (`Leading`/`Rest`/`Trailing`/`RestOverride`, each an
   `OperandRoleV1`), with its own single `Citation` covering the whole positional layout (usually
   the `--help` synopsis line or man SYNOPSIS).
6. **Model `ImplicitEffects`** only if the command has an effect that fires without consuming an
   operand (e.g. a subcommand that always touches a fixed resource) — each entry needs its own
   `Citation`.
7. **Resolve `Interpreter`/`Dialect` names against the registered set, never invent one.** There
   are exactly 8 command-level interpreters (`""` generic, `xargs`, `curl`, `find`, `kubectl`,
   `kubectl-manifest`, `kubectl-cp`, `ssh`, `scp` — `internal/cmddesc/interpreter.go`'s
   `interpreters` map) and 4 program dialects (`sed`, `awk`, `shell`, `bash` —
   `internal/cmddesc/dialect.go`'s `dialects` map). `specfmt.Validate` fail-closed rejects any
   other spelling (`cmddesc.LookupInterpreter`/`LookupDialect`). See
   `references/interpreters-and-dialects.md`.
8. **Run the danger-shaped-flag check over your own output before reporting success** (see next
   section) and **run `unknownFlag` justification / citation-presence checks** — i.e. run `lint`
   (below) and resolve every finding, never suppress one.
9. **Generate the goldens sidecar** (see "Generated goldens" below).
10. **Route the output file** to the correct location (see "Output routing" below).
11. **Run the lint CLI** and confirm zero HARD findings for the spec you just wrote (see
    "Validation" below).

### Path/target subjects (input shape (b))

For a path/host subject, you instead emit a `KindTarget` spec (`internal/specfmt.TargetSpecV1`):
`TargetKind` (one of `docker-context`, `docker-host`, `kube-context`, `kube-server`,
`vault-address`, `ssh-host`, `git-remote` — `internal/specfmt/v1.go`'s `TargetKind` constants),
`Class` (`trusted-dev` or `production`), and a `Citation` recording WHY this target is classified
that way (an infrastructure inventory entry, an ADR, a runbook — not a `--help`/man excerpt, since
a target is an operator fact, not a command's documented behavior). `TargetClassProduction`
targets are always rejected on mutation; `TargetClassTrustedDev` targets defer to whatever
effect-level policy already applies. The same "no thin citation" rule applies — a target's
`Citation.Source` must name real provenance, not a placeholder.

## Danger-shaped-flag resolution (mandatory, before reporting success)

`internal/speclint`'s danger-shaped-flag check (`CheckDangerFlagRole`) flags any flag matching
`-o`, `--output*`, `--exec*`, `-c`, `--config*`, `--command`, `-e`, `--*-program`, `--*-hook*`,
`--receive-pack`, `--upload-pack`, `--prune`, `--mirror`, `--all`, `--delete`, `-f`, `--force*`,
`-r`, `-R`, `--recursive` (an `@file` value is separately covered: any `data-or-at-file` role is
already an effect role, so it participates in this same check generically — see
`internal/speclint/lint.go`'s `dangerFlagFindings`). Every skill-generated spec you write MUST be
run through this check (see "Validation" below), and any finding MUST be **resolved** — assign the
flag a correct effect role (or, for an arity-0 flag, show it participates in a `RestOverride`/
`*SkippedByFlags`/`ImplicitEffect.WhenFlags` elsewhere) — **never suppressed** and never left as a
reported-but-ignored finding.

## UnknownFlagInert is forbidden in skill-generated specs

`CommandSpecV1.UnknownFlag` MUST always be `"insufficient"` for anything this skill produces.
`"inert"` (`cmddesc.UnknownFlagInert`'s wire spelling — treats every unmodeled flag as a silent
no-op) is unconditionally forbidden here: `internal/speclint`'s `unknownFlagInertFindings` treats
ANY skill-generated `"inert"` value as a HARD finding with **no justification-field escape
hatch** — that escape hatch (a real citation on the `unknownFlag` key excusing a hand-written
`"inert"` use) exists only for pre-existing HAND-WRITTEN specs, not for anything this skill emits.
If you find yourself wanting `"inert"` because a command has many flags you have not individually
modeled, that is the fail-closed default (`"insufficient"`) working as intended — leave it there.

## Generated goldens

The design calls for "generated goldens (expected verdict per flag role under current policies)"
alongside each spec. **Neither existing golden mechanism in this repo stores that shape**:
`internal/goldencorpus` (`schema.go`, `testdata/corpus.json`) is a Phase-0 REPLAY corpus — one
expected verdict per whole logged ROW (tool input + fixtures), not per flag. `internal/effectpolicy/
testdata/` + `golden_test.go` store `.mmd` effect-GRAPH structural diagrams for ~411 hand-picked
cases, not per-flag verdicts either. **This skill defines its own golden shape and location** (the
docket's binding decision leaves this to the implementer) — do NOT modify `internal/goldencorpus`
or `internal/effectpolicy/testdata` to try to force either into this per-flag shape.

### Format

One JSON file per command, reusing `internal/goldencorpus`'s existing three-value verdict
vocabulary (`approve` / `reject` / `not-approve` — see `internal/goldencorpus/schema.go`'s
`Verdict` type) for consistency with the rest of the repo, plus a documented fourth marker,
`context-dependent`, for a role whose verdict genuinely depends on runtime context this skill
cannot resolve in isolation (a vetted-host list, a kube-context allowlist, a trusted-checkout CWD
— see below):

```json
{
  "version": "v1",
  "command": "<name>",
  "generatedAt": "<YYYY-MM-DD>",
  "flags": {
    "<flag spelling>": {
      "role": "<RoleKind.String() spelling>",
      "verdict": "approve | reject | not-approve | context-dependent",
      "policyBasis": "<effectpolicy.<PolicyName>: one-line reason, or \"no EffectPolicy judges this effect kind (fail-closed abstain)\">"
    }
  },
  "positionals": {
    "verdict": "...",
    "policyBasis": "..."
  }
}
```

`policyBasis` names the specific `internal/effectpolicy` `Policy` implementation
(`internal/effectpolicy/policy.go`'s `Judge` methods — e.g. `PathAccessPolicy`, `NetworkAccess`,
`RemoteMutation`, `KubeContextPolicy`, `TargetSpecPolicy`, `ProgramInterpreted`, `EnvAssignment`,
`ChdirScoped`, `StdioIsLocal`, `TrustedCheckoutExec`, or the `remotePathGuard` helper) that would
judge the effect this role/flag produces — or explicitly says no policy judges that effect kind
(e.g. `key-material` roles: `internal/cmddesc/effect.go`'s `EffectKeyMaterial` doc comment records
that no `DefaultPolicies` entry judges it, so it fails closed to Abstain/`not-approve`, never
Approve).

### Generation procedure (there is no automated generator; this IS the procedure)

There is no existing CLI that evaluates a single synthetic flag/role against the new
`internal/effectpolicy` engine in isolation (`claude-extended-tool-approver evaluate` replays
_logged historical rows_ through the OLD, frozen `internal/rules` engine — see
`internal/settingseval`/`internal/asklog` — not a fresh synthetic input against the new engine).
So: for each flag/role you just wrote, (1) map its `RoleKind` to the `cmddesc.EffectKind` it would
produce (`internal/cmddesc/effect.go`'s `EffectKind` doc comments — e.g. `path-read`/`path-create`/
etc. → `EffectPath`, `program` → `EffectProgram`, `remote` → `EffectRemote`, `env-assign` →
`EffectEnv`, `chdir` → `EffectChdir`, `key-material` → `EffectKeyMaterial`, `literal`/`unmodeled` →
no effect at all), (2) find which `internal/effectpolicy/policy.go` `Policy.Judge` implementation
(if any) handles that `EffectKind`, (3) read that `Judge` method's logic under a DEFAULT context
(no vetted hosts, no kube-context allowlist entries, CWD not a recognised trusted checkout, mode
`default`) to determine what it would decide for an effect of this shape **in isolation** (as if it
were the only node in the graph — this is a documented simplification: a real invocation's overall
verdict folds every effect it produces together, worst-wins; a single flag's golden is
necessarily an approximation of that), and (4) record the verdict and a one-line `policyBasis`
citing the `Policy` name and the deciding condition. If step (3) genuinely depends on runtime
context you cannot resolve generically (a specific vetted host, a specific kube-context), use
`context-dependent` and say what the missing context is — do not guess a verdict.

**"Under CURRENT policies" means re-derive this at generation time, every time** — read
`internal/effectpolicy/policy.go` as it stands today; never copy a golden verdict forward from a
previous run of this skill without re-checking it still matches the current `Judge` logic.

### Storage location (the loadLayer footgun)

**Do not place a goldens file anywhere `specfmt.Repository`'s loader will walk.**
`internal/specfmt/repository.go`'s `loadLayer` recursively walks the ENTIRE `UserDir`/`RepoDir`
tree for any `*.json` file and attempts to `json.Unmarshal` it as a `Spec` — a goldens sidecar
dropped anywhere under `specfmt.DefaultUserDir()` (`~/.config/claude-extended-tool-approver/`,
where command specs themselves go — see "Output routing" below) or `.ceta/` would be picked up,
fail `Validate` (empty `version`/`kind`/`name`), and show up as a spurious `InvalidSpec` /
`CheckInvalidSpec` WARN finding on every future `lint --user-dir=.../--repo-dir=...` run. The
embedded layer has the same shape of risk in principle, but `internal/embeddedspecs/embed.go`'s
`//go:embed data/*.json` directive only embeds _direct children_ of `data/` (it does not recurse),
so a goldens file under a `data/goldens/` subdirectory is never embedded and never reachable by
`loadLayer` in the first place.

Use a **sibling root**, never a subdirectory of the spec layer itself:

- **Built-in** command goldens: `internal/embeddedspecs/data/goldens/<name>.goldens.json` (not
  embedded, not walked — safe by construction, per the paragraph above).
- **User-level** custom command goldens: `~/.config/claude-extended-tool-approver-goldens/
commands/<name>.goldens.json` — a directory NAME distinct from (a sibling of, not nested under)
  `specfmt.DefaultUserDir()`'s `~/.config/claude-extended-tool-approver/`.
- **Repo-level** custom command goldens: `.ceta-goldens/commands/<name>.goldens.json` at the repo
  root — a sibling of `.ceta/` (`specfmt.DefaultRepoDir`), not nested inside it.

## Output routing (P17)

**A custom (non-built-in) command's spec MUST be written to the user-level or repo-level layer —
never into this repo's own `internal/embeddedspecs/data/`, which is built-in-only.** A built-in
command's _regenerated_ spec (re-sourcing its citations) is the one case that DOES go into
`internal/embeddedspecs/data/<name>.json` — that is what makes it "built-in" — but that file is
generated/maintained data, and the backfill of all 45 embedded specs is explicitly out of this
packet's own scope (tc-o14i5.4.3's job); this skill only documents the mechanism, it does not
itself edit that directory's checked-in files as part of authoring this skill.

For a **custom** command's spec:

- **User-level root**: resolve it via `internal/specfmt.DefaultUserDir()` — **`os.UserHomeDir()` +
  `.config/claude-extended-tool-approver`** — **never** `internal/userconfig` (a _different_
  package resolving a _different_ directory, `~/.config/claude-extended-tool-approver-engine/`,
  for a _different_ flat engine-settings struct consumed only by
  `internal/pathspec/worktree.go` and `internal/patheval/trustedexec.go`). Using `userconfig`'s
  directory here would silently write your spec to a path `specfmt.Repository`'s real P17 loader
  never scans — defeating this whole routing requirement with **no error at all**.
- **Repo-level root**: `internal/specfmt.DefaultRepoDir(repoRoot)` — `<repoRoot>/.ceta`.
- **File path convention** (the loader itself imposes no fixed subpath — `loadLayer` walks for
  ANY `*.json` — this naming is this skill's own convention, so tc-o14i5.4.3/.4.4 can find it
  without guessing): `<DefaultUserDir()>/specs/commands/<name>.json` (user-level) or
  `<DefaultRepoDir(root)>/specs/commands/<name>.json` (repo-level, i.e.
  `.ceta/specs/commands/<name>.json`).
- Set `Spec.Overrides: true` if you are intentionally replacing a lower-precedence layer's spec of
  the same `(Kind, Name)` — otherwise `specfmt.Repository.Load` records an undeclared `Conflict`,
  which `internal/speclint`'s `overrides-conflict` check reports as HARD.

For a **`KindTarget`** spec, the same user-level/repo-level roots apply; convention:
`<root>/specs/targets/<target-kind>/<name>.json`.

## Validation (run this yourself before reporting success)

From the module root (`packages/claude-extended-tool-approver`):

- **Built-in regeneration**: `go run ./cmd/claude-extended-tool-approver lint --embedded` lints
  the compiled-in `internal/embeddedspecs.FS`. To exercise a REGENERATED built-in spec, temporarily
  overwrite the corresponding `internal/embeddedspecs/data/<name>.json` with your new content,
  re-run the command above (`go run` recompiles the embed each time), confirm no NEW
  `citation-presence` finding fires for that command's own entries, then **discard the change**
  (`git checkout -- internal/embeddedspecs/data/<name>.json`) — do not leave a partial backfill
  committed from a single spec-authoring pass.
- **Custom / bare-file spec**: `go run ./cmd/claude-extended-tool-approver lint
<path-to-your-spec.json>` lints one file directly (no `Repository`/layer machinery, so no
  overrides-conflict detection — that only fires when loaded through `--user-dir`/`--repo-dir`).
  To exercise it through the real P17 loader, `go run ./cmd/claude-extended-tool-approver lint
--user-dir=<DefaultUserDir()>` (or `--repo-dir=<...>`) once the file is in place at its
  routed location.
- Confirm the run reports **zero HARD findings** for the spec you just authored (a pre-existing
  HARD/WARN finding on an unrelated, not-yet-backfilled built-in is expected and out of scope —
  see `internal/speclint/lint.go`'s own `LintInvalid` doc comment for the two pre-existing `bash`/
  `sh` gaps).

## Completion checklist

- [ ] Spec carries a real citation for every fact-bearing element (no thin
      `registry.go`/`DefaultRegistry()` placeholder anywhere)
- [ ] `UnknownFlag` is `"insufficient"`, never `"inert"`
- [ ] Danger-shaped-flag check run and every finding resolved (correct role assigned, never
      suppressed)
- [ ] Goldens sidecar generated at the correct sibling location (never inside a
      `specfmt.Repository`-walked directory), verdicts re-derived from current
      `internal/effectpolicy` policy, not copied forward
- [ ] Custom-command output routed to `specfmt.DefaultUserDir()`/`DefaultRepoDir()` (never
      `internal/userconfig`'s directory, never `internal/embeddedspecs/data/`)
- [ ] `lint` run against the spec reports zero new HARD findings

## References

- `references/worked-example-citation.md` — one fully worked command (`jq`, `--from-file`): the
  `--help` excerpt, the resulting `Citation{Source: ...}` value, and the resulting flag `Role`.
- `references/interpreters-and-dialects.md` — the 8 registered command interpreters and 4 program
  dialects, and where to find `--help`/source evidence for each.
