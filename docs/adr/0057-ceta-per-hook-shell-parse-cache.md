# CETA: a per-hook-evaluation shell-parse cache, shared across every recursion call site

**Status**: Proposed
**Date**: 2026-08-25
**Deciders**: autonomous design pass (bead `pg2-bbsqp`); no operator was in the loop for this call —
this ADR proposes, it does not itself decide.

## Context

`pg2-k1c91` (closed, landed `67b023e8`) found three residual re-parse patterns left over after
ADR 0039's shell-parser migration, all accepted as non-correctness architectural cleanup rather
than fixed in that bead. Its own recommendation text split the third pattern into two follow-ups:
(a) a bounded, single-file `secrets.go` refactor — landed in the same bead as
`shellCScriptCache` (see below) — and (b) **this bead**: "design a per-hook-evaluation
memoization layer for `EvaluateExpression`/`ParseShell`, keyed on normalized source text, shared
across every recursion call site," flagged as "a substantial, separate engineering undertaking"
warranting its own design pass before implementation.

The pattern being closed, in `LOWERING.md`'s own words (Guard 3 (I7) section, "Cross-occurrence,
non-memoized rule recursion"): "a byte-identical substitution/script body recurring at two or more
DISTINCT source locations in one script (a `before=$(probe); after=$(probe)` idiom, or several
`cat <<EOF | bash -c '...'` blocks sharing one script) is independently, legitimately re-parsed
once per occurrence" — measured at **161 corpus rows** on the 2026-08-21 replay pass, out of
153,140 rows checked, on top of 533 rows from the (now-fixed) `secrets.go` pattern and 140
further unattributed rows `TestGuard3_ParseCountCorpus` monitors against a calibrated ceiling
(250).

**This is explicitly not a correctness defect.** Guard 3 / I7 is deliberately weaker than "one
parse per command" by design — its own text (LOWERING.md, and `ADR 0039` `:253-263`) says the
text entry point is permanent and re-entry through it is sanctioned. None of the three residual
patterns produces a wrong verdict, only redundant work in a short-lived, per-hook-invocation CLI
that is not performance-critical. This ADR is architectural cleanup: it proposes _how_ to share
parse work across call sites that today have no way to know about each other, not a fix for any
observed wrong decision.

### The sibling precedent this design must be consistent with

`pg2-k1c91` item (a) landed `internal/rules/secrets/secrets.go`'s `shellCScriptCache`
(`secrets.go:405-423`): a plain `map[string][]cmdparse.ParsedCommand`, with no locking, created
fresh **once per `bashRef` call** and shared across that one rule's three internal candidate-match
passes (`lexicalRef`/`resolvedRef`/`configRef`). Its own doc comment records the two properties
this ADR's design reuses directly:

- **Keying purely on the raw script text is safe regardless of which pass or recursion depth
  reaches it first**, because `cmdparse.Parse` is a pure function of its argument.
- **"One instance is created per bashRef call ... and never shared beyond it, so there is no
  cross-call staleness to reason about and no lifetime beyond a single hook decision."**

This bead widens the _same_ idea — memoize a pure parse by its exact input text, scoped to one
hook decision — from one rule's own internal passes to every call site in the module. It is the
same relationship ADR 0055 has to the point fixes it subsumes: a shared owner replacing several
independent, correct-in-isolation local mechanisms that cannot see each other.

A second, independently-landed precedent confirms the purity assumption is already load-bearing
elsewhere: `cmdparse.IsFreshTempDirAssignment` (`internal/cmdparse/incommandvars.go:678-693`) is
memoized in a **package-level, process-wide `sync.Map`**, justified by the same "pure function of
its inputs" argument, explicitly because "this binary is a short-lived per-hook-call CLI, never a
long-running daemon" and the cache is "safe for concurrent use (the hook may evaluate substitution
bodies from more than one goroutine, same reasoning as `shellparse.go`'s `parserPool`)." That
comment's forward-looking concurrency caveat and the ADR 0039-era `parserPool` sync.Pool
(`shellparse.go:63-76`) both exist for a real but narrower reason than "the hook itself is
multi-threaded" — see Concurrency below.

### Grounding: the actual signatures and the real call sites

`EvaluateExpression` and `ParseShell` are:

```go
// internal/engine/engine.go:321
func (e *Engine) EvaluateExpression(expr string, stack []hookio.StackFrame, origin *hookio.HookInput) hookio.RuleResult

// internal/cmdparse/shellparse.go:134
func ParseShell(command string) ShellParse
```

`EvaluateExpression` parses `expr` **once**, at line 351 (`cmdparse.ParseShell(expr)`), then runs
the parsed leaves through the shared core `evaluateParsed`, which recurses into
`foldSubstitutionScan` for every substitution/heredoc body found in the _already-parsed_ subtree
— that internal recursion never re-parses (ADR 0039 step 4; `Substitution.Leaves` is populated by
`lowerSubtree` on the same tree walk that found the substitution, per `parser.go:1207-1220`'s own
field doc). So **engine.go's own recursion is already single-pass over one top-level parse.**

The residual is not inside that recursion. It is that `cmdparse.Parse` — the ostensible "other"
function named in this bead's title — is not a second code path at all:

```go
// internal/cmdparse/parser.go:1899-1900
func Parse(command string) []ParsedCommand {
	return ParseShell(command).Leaves
}
```

`Parse` is a bare facade over `ParseShell`. Grepping every call to `cmdparse.Parse(...)` and
`cmdparse.ParseShell(...)` across the module (excluding tests) finds it invoked independently,
once per call site, wherever a rule package has extracted or constructed _new_ text that was never
part of the enclosing expression's own parse:

| Caller                                                                          | Call                                     | What text                                                                  |
| ------------------------------------------------------------------------------- | ---------------------------------------- | -------------------------------------------------------------------------- |
| `internal/engine/engine.go:351`                                                 | `cmdparse.ParseShell(expr)`              | `EvaluateExpression`'s own top-level text entry                            |
| `internal/engine/engine.go:1518` (`parsedLeafFor`)                              | `cmdparse.Parse(pc.Raw)`                 | deliberate heredoc-bleed re-parse (I12) — the ONE re-parse kept on purpose |
| `internal/rules/docker/docker.go:454,552` (`resolveScriptLeaves`/`resolveLeaf`) | `cmdparse.Parse(script)`                 | a nested `bash`/`sh -c` script extracted from a container invocation       |
| `internal/rules/nix/nix.go:333,433`                                             | `cmdparse.Parse(runStr)` / `(source)`    | a `nix run`/build-tool inner command                                       |
| `internal/rules/primarycommit/primarycommit.go:199,372`                         | `cmdparse.Parse(scope)` / `(shellBody)`  | a `cd`-scope root re-derivation and a shell body                           |
| `internal/rules/primarypush/primarypush.go:279`                                 | `cmdparse.Parse(shellBody)`              | same shape as primarycommit                                                |
| `internal/rules/safecmds/safecmds.go:278`                                       | `cmdparse.Parse(scriptText)`             | `xargs sh -c`/`bash -c` inner script                                       |
| `internal/rules/gitdir/gitdir.go:298,871`                                       | `cmdparse.Parse(leafText)` / `(p.scope)` | git-directory scope leaves                                                 |
| `internal/rules/secrets/secrets.go:420`                                         | `cmdparse.Parse(script)`                 | already behind `shellCScriptCache` (see above)                             |
| `internal/rules/ssh/ssh.go:404`                                                 | `cmdparse.ParseShell(remoteCmd)`         | a remote command, LOCAL one-shot scan (never re-enters the evaluator)      |
| `internal/rules/kubectl/kubectl.go:341`                                         | `cmdparse.ParseShell(source)`            | a kubectl exec's inner command                                             |

Every one of these fans out through `Parse`/`ParseShell` to the **same** function — but nothing
connects one caller's parse to another's, so a script that hands the identical text to two of
these call sites (or the same call site twice, at two source locations) pays for two real
grammar-parser invocations. This is exactly `pg2-k1c91`'s named idiom: `before=$(probe);
after=$(probe)` reaching `EvaluateExpression`'s recursive re-entry (below) with byte-identical
`sub.Body` text, or two `heredoc | bash -c` blocks sharing one script each independently reaching
`docker.go`/`safecmds.go`'s own `cmdparse.Parse` call.

**`EvaluateExpression` itself has exactly two production callers**, per `hookio.Evaluator`'s own
doc comment (`hookio/types.go:648-650`) and confirmed by grep:

- `internal/engine/engine.go:276` — the hook boundary (`EvaluateHook` routing a `Bash` tool call).
- `internal/rules/envvars/envvars.go:1033` — a **recursive** call, on `sub.Body`, where `sub` comes
  from `cmdparse.EnumerateSubstitutions(ev.Value)` at line 1009 (or 446). This is the exact
  `before=$(probe); after=$(probe)` idiom: each qualifying env assignment leaf independently
  re-derives its own value's embedded substitutions and re-enters `EvaluateExpression` on each —
  so if two assignment leaves in one script carry byte-identical values, this loop runs the full
  parse-then-evaluate path twice for identical text.

### The residual has a second, independent source `cmdparse.Parse`'s facade does not cover

`EnumerateSubstitutions`/`ScanSubstitutions` do **not** route through `ParseShell` at all — they
call the underlying `*syntax.Parser` directly:

```go
// internal/cmdparse/shellparse.go:1675-1687
func ScanSubstitutions(s string) SubstitutionScan {
	p, _ := parserPool.Get().(*syntax.Parser)
	file, err := p.Parse(strings.NewReader(s), "command")
	...
}
```

`cmdparse`'s own field doc for `Substitution.Leaves` names this explicitly: `Leaves` is populated
"ONLY along the subtree-walking path `ParsedCommand.Substitutions`/`Heredoc.Substitutions` use;
the remaining TEXT entry points — `ScanSubstitutions`, `ScanSubstitutionsInHeredocBody` and the
`EnumerateSubstitutions` facade ... — have no already-parsed source to walk and leave this nil"
(`parser.go:1212-1219`). These are **permanent, independent grammar-parser entry points**, not an
oversight — `gitdir.go`'s own doc explains why: "cmdparse never lowers an assignment's value into
leaf structure" for a plain `NAME=VALUE` leaf, so a rule that needs to find substitutions inside
an _assignment's value_ (as opposed to a command's args/redirections, which the one enclosing
parse already lowered) has no pre-built structure to consult and must scan the value text fresh.

Their production callers, confirmed by grep, are exactly the assignment-value idiom:

- `internal/rules/envvars/envvars.go:446,1009` — `cmdparse.EnumerateSubstitutions(value)` /
  `(ev.Value)`.
- `internal/rules/gitdir/gitdir.go:485` — `cmdparse.EnumerateSubstitutions(ev.Value)`, in
  `envValueSubstitutionLeaves` (whose own doc comment names the _exact_ corpus idiom this bead
  cites: `GD=…/rebase-merge` then `ORIG=$(cat "$GD/orig-head")` — a "genuinely read-only bound
  path" case found empirically, not assumed).
- `internal/rules/ssh/ssh.go:588` — `cmdparse.ScanSubstitutions(raw)`, a local one-shot scan.

So a naive design that only memoizes `ParseShell` would **miss** the `before=$(probe);
after=$(probe)` idiom's outer half: two assignment leaves with identical values each independently
re-scan the value text via `EnumerateSubstitutions`/`ScanSubstitutions` _before_ either one's found
substitution body ever reaches `EvaluateExpression`/`ParseShell`. Both layers need covering.
`ScanSubstitutionsInHeredocBody` (`shellparse.go:1722`, a third grammar mode — `p.Document(...)`,
not `p.Parse(...)`) is the third such entry point but currently has **no production caller** at
all (only fuzz/unit tests exercise it directly) — included below for completeness and because a
future caller reaching for "scan an unquoted heredoc body's substitutions from scratch" would
otherwise silently reproduce this same gap.

### Purity, process lifetime, and concurrency — confirmed against the actual architecture

- **Purity.** `ParseShell`/`ScanSubstitutions`/`ScanSubstitutionsInHeredocBody` are pure functions
  of their input string given a fixed parser configuration. The configuration is in fact fixed —
  `parserPool.New` always builds `syntax.NewParser(syntax.Variant(syntax.LangBash),
syntax.KeepComments(true))` (`shellparse.go:67-76`), with no runtime variant switching anywhere
  in the module. This is the same property `shellCScriptCache` and `IsFreshTempDirAssignment`
  already rely on; this design does not introduce a new trust assumption, only a third consumer of
  an existing one.
- **Process lifetime.** `EngineCache`'s own doc (`internal/setup/enginecache.go:18-29`) states the
  live path plainly: "the live PreToolUse handler ... runs inside `main()`, which parses exactly
  one `hookio.HookInput` from stdin and exits — **one process per real tool-use invocation**." So
  for hook mode, "per-hook-evaluation" and "per-process" already coincide — confirming the bead's
  own stated assumption ("no cross-invocation persistence needed") against the real architecture,
  not merely asserting it. The one place a single process _does_ span many hook evaluations is
  offline replay (`cmd_evaluate.go`'s `evaluate`/`baseline`/`compare` subcommands), which reuses
  one process across up to hundreds of thousands of rows via `EngineCache` — this is exactly why
  this design needs an explicit reset boundary rather than relying on process exit (see Decision).
- **Concurrency.** No production code in this module spawns a goroutine (`grep -rn "go func"`
  across `internal/` returns nothing outside comments) — `evaluateParsed`'s own leaf loop
  (`engine.go:525`) is a plain sequential `for`, and every recursive call (`foldSubstitutionScan`,
  `envvars.go`'s `EvaluateExpression` re-entry) happens synchronously within that same call stack.
  **One hook evaluation is single-threaded.** The reason `parserPool` is a `sync.Pool` and
  `freshTempDirCache` a `sync.Map` is `go test` running package tests in **parallel goroutines**
  against shared package-level state (`shellparse.go:64-66` says this explicitly) — a real
  concurrency source, but at the test-parallelism layer, not inside one hook decision. Any
  package-level cache this design adds needs the same protection, for the same reason, even though
  no _production_ caller ever races it.

## Decision

**Memoize at the seam, not at the callers.** Add a small, package-level parse cache inside
`internal/cmdparse`, consulted and populated by the three text entry points identified above
(`ParseShell`, `ScanSubstitutions`, `ScanSubstitutionsInHeredocBody`), reset once per top-level
hook evaluation. No signature in `hookio.Evaluator`, `Engine`, or any rule package changes.

This is the direct architectural consequence of the finding above: `Parse` is a facade over
`ParseShell`, and the module has **no other path into the real grammar parser** than these three
functions (`parserPool`/`p.Parse`/`p.Document` calls occur nowhere outside `shellparse.go`).
Placing the cache at the seam means every existing caller — `engine.go`'s own recursion, all six
rule packages that call `cmdparse.Parse`/`ParseShell` directly, and `envvars`/`gitdir`/`ssh`'s
`EnumerateSubstitutions`/`ScanSubstitutions` calls — starts sharing cached parses **with zero code
changes at the call site**, because they already all go through the seam today.

### What gets cached

The **full result value** of each entry point, keyed on the **exact, unmodified argument string**:

- `ParseShell(command string) ShellParse` → cache `map[string]ShellParse`.
- `ScanSubstitutions(s string) SubstitutionScan` → cache `map[string]SubstitutionScan`.
- `ScanSubstitutionsInHeredocBody(body string) SubstitutionScan` → its own
  `map[string]SubstitutionScan`, kept **separate** from `ScanSubstitutions`'s cache even though the
  value type matches, because the two parse the _same text_ under **different grammars**
  (`p.Parse` "command" mode vs. `p.Document` heredoc-body mode per `shellparse.go:1676` vs.
  `:1724`) — a shared keyspace would let one grammar's cached result satisfy a lookup meant for the
  other.

`cmdparse.Parse`'s own facade (`return ParseShell(command).Leaves`) needs no changes at all: it
inherits the cache transparently because it calls the now-memoized `ParseShell`.

**The cache key is the raw source text, never a normalized form.** This corrects a literal reading
of the bead's own phrasing ("keyed on normalized source text"): the one normalization this module
already has — `engine.normalizeExpression` (`engine.go:1475-1477`, `strings.Join(strings.Fields(...), " ")`)
— collapses whitespace, and is used _only_ as the cycle-detection key in `detectCycle`, where a
false _negative_ (missing a real cycle because whitespace differs) is the only risk and is already
accepted. Reusing it as a parse-cache key would be **unsafe**: whitespace inside a double-quoted
string or a heredoc body is semantically significant to the shell grammar (`echo "a   b"` and
`echo "a b"` parse to different `Args`), so collapsing it could produce a **false cache hit** —
returning one text's parse for a different text that happens to normalize the same way. The cache
key must be exactly what `ParseShell`/`ScanSubstitutions`/`ScanSubstitutionsInHeredocBody` already
receive as their sole parameter, byte for byte.

### Where it lives and its lifetime

A package-level cache inside `cmdparse`, following the existing `parseObserver` idiom
(`shellparse.go:96-121`, itself a package-level, swappable/resettable piece of state controlling
`ParseShell`'s behavior) rather than a value threaded through every caller:

```go
// sketch, not implementation
var parseShellCache sync.Map           // string -> ShellParse
var scanSubstitutionsCache sync.Map    // string -> SubstitutionScan
var scanHeredocBodyCache sync.Map      // string -> SubstitutionScan

func ResetParseCache() {
	parseShellCache = sync.Map{}
	scanSubstitutionsCache = sync.Map{}
	scanHeredocBodyCache = sync.Map{}
}
```

**Lifetime: reset once per top-level hook evaluation**, at the top of
`(*Engine).EvaluateHook` (`engine.go:273`) — confirmed to be the single choke point both real
paths already share: `main.go`'s `handlePreToolUse` calls `eng.EvaluateHook(input)` once per
process (`main.go:185`), and `cmd_evaluate.go`'s replay row loop calls the identical method once
per row, inside `for _, row := range rows` (`cmd_evaluate.go:224`). One call site, in `engine.go`,
covers both live hook mode and offline replay with no change to either `main.go` or
`cmd_evaluate.go`.

This scoping choice is deliberate given the confirmed architecture above: for live hook mode the
reset is not load-bearing for correctness (the process exits after the one `EvaluateHook` call
regardless), but it _is_ load-bearing for replay mode, where one process now runs the cache across
hundreds of thousands of rows unless reset per row — bounding growth to one row's own duplicate
text rather than the whole run's. It also keeps the cache's scope identical to Guard 3's own
accounting boundary ("per DISTINCT SOURCE STRING PER HOOK EVALUATION," `shellparse.go:107-116`),
which matters for the follow-up in Consequences below.

### How it threads through the cross-cutting call sites

No thread-through is required — that is the point of memoizing at the seam instead of at the
`hookio.Evaluator` interface. Concretely, once the cache lands inside `cmdparse`:

- **`engine.go`'s own recursion** (`EvaluateExpression` → `evaluateParsed` → `foldSubstitutionScan`)
  needed no re-parse before this change (ADR 0039 step 4) and needs none now; it benefits only
  indirectly, if a substitution body it recurses into happens to match text already cached from an
  earlier leaf's `cmdparse.Parse` call elsewhere in the same evaluation.
- **`envvars.go:1033`'s recursive `EvaluateExpression(sub.Body, ...)`** — the named
  `before=$(probe); after=$(probe)` idiom's inner half — gets its embedded `ParseShell(sub.Body)`
  call served from cache on the second occurrence. The **outer** half —
  `EnumerateSubstitutions(ev.Value)` at `envvars.go:1009`/`gitdir.go:485` scanning each
  assignment's own value — is served from the separate `ScanSubstitutions` cache, closing the
  pattern `cmdparse.Parse`'s facade alone cannot reach (see Context).
- **Every rule's own direct `cmdparse.Parse`/`ParseShell` call** (docker, nix, primarycommit,
  primarypush, safecmds, gitdir, secrets, ssh, kubectl — the table above) is covered automatically,
  because each one already calls into the now-memoized seam; none of their call sites change.
- **`secrets.go`'s own `shellCScriptCache`** becomes a redundant _second_ layer once the shared
  cache lands underneath it — harmless (a hit on the local cache short-circuits before ever
  reaching `cmdparse.Parse`), but a natural retirement candidate, the same relationship ADR 0055
  describes for the point fixes it subsumes: this design does not "fix" `pg2-k1c91`'s item (a), it
  makes the mechanism that already existed for one rule available to every rule, at which point the
  rule-local copy is redundant, not wrong.
- **`ssh.go`/`kubectl.go`'s local one-shot scans** — not previously named in `LOWERING.md`'s
  write-up, since they never re-enter the evaluator — incidentally also benefit if the same
  extracted inner-command text recurs across two `ssh`/`kubectl exec` invocations within one Bash
  tool call.

### Concurrency

The cache MUST be safe for concurrent access — `sync.Map`, matching `freshTempDirCache`'s existing
choice — even though no _production_ caller races it (Context: one hook evaluation is
single-threaded). This is purely for `go test`'s parallel test execution against the shared
package-level state, the same justification `parserPool`/`freshTempDirCache` already document. A
duplicate parse under a genuine race (two goroutines missing the same key simultaneously) is not a
correctness bug: both would compute the identical, pure result and one `Store` simply wins,
costing one redundant parse that one time — never a wrong answer.

### Correctness invariants

1. **Purity precondition.** The design rests entirely on `ParseShell`/`ScanSubstitutions`/
   `ScanSubstitutionsInHeredocBody` being pure functions of their input text under this module's
   fixed parser configuration (confirmed in Context). If a future change ever makes parsing depend
   on anything outside the input string — a config flag, an environment variable, a different
   `syntax.Variant` per call — this cache becomes unsafe and MUST be revisited before that change
   lands.
2. **Exact-text keys only; no normalization.** Established above: the key must be the raw string
   the caller passed, never a whitespace-collapsed or otherwise lossy transform, because such a
   transform can conflate texts that parse differently.
3. **Cached values MUST be treated as read-only by every caller.** A cache hit returns a value
   shared with every other caller that already saw or will see that same text within the same hook
   evaluation. Grepped across `internal/rules` and `internal/engine` for in-place mutation of a
   `ParsedCommand`'s slice fields (e.g. `.Args[i] =`) found none — the one place a leaf's
   `Executable`/`Args` are rewritten (`docker.go`'s `resolveLeaf`) takes `leaf` **by value** and
   reassigns to **freshly built** slices (`unwrapPassthroughs(append([]string{leaf.Executable},
leaf.Args...))`), never aliasing the original backing array. This existing discipline is a
   precondition the cache relies on and MUST be preserved: an implementation should say so
   explicitly in the cached types' doc comments once this lands, since a future in-place mutation
   of a cached slice would silently corrupt every other consumer sharing that cache entry within
   the same evaluation.
4. **This is a PARSE cache, not a VERDICT cache.** `EvaluateExpression`'s returned `RuleResult` —
   or `Evaluate`'s per-rule verdicts — MUST NOT be memoized by this design, only the `ShellParse`/
   `SubstitutionScan` a parse produces. A verdict can legitimately differ between two
   byte-identical-text occurrences because it also depends on caller-supplied context that varies
   per occurrence: `stack` (cycle detection), and `origin`'s `CWD`/`PathEval`/`InCommandVars`/
   `InCommandTempDirVars` (`evaluateParsed`'s own extensive doc, `engine.go:452-472`, on why
   `outerVars` must be threaded per-occurrence rather than assumed constant). A rule that resolves
   an in-command variable reference could reach a different answer for the identical substitution
   text recursed from two different leaves with different preceding assignments. Caching only the
   parse keeps the design inside the domain where purity is actually proven; caching the verdict
   would reintroduce, in a new shape, exactly the "two independent things silently disagreeing"
   risk ADR 0055 and `pg2-zpct4` already had to reconcile once.
5. **Scope boundary: reset once per `EvaluateHook` call, never shared across two distinct hook
   decisions.** Not required for correctness under invariant 1 (a stale cross-invocation hit would
   still be the _correct_ parse, by purity) — required to keep the cache's memory bounded across
   replay's many-rows-per-process reuse, and to keep its scope legible and matched to Guard 3's own
   "per hook evaluation" accounting rather than silently becoming process-wide.

## Migration / rollout shape (described, not implemented in this change)

This ADR proposes the design; implementing it is separate follow-up work, consistent with this
repo's own practice of writing the decision and its replay obligations before landing the code
(see ADR 0055's Migration section for the same split, for the same reason: a change touching every
Bash-command evaluation warrants its own reviewed, replayed implementation bead).

**Two independent slices**, either of which can land alone:

1. **`ParseShell` only.** The single highest-traffic entry point — every `cmdparse.Parse` call and
   `engine.go`'s own top-level parse funnel through it — for the smallest, most self-contained
   first change. Closes the `heredoc | bash -c` idiom and any rule-extracted-script idiom (docker/
   nix/safecmds/primarycommit/primarypush/gitdir sharing identical extracted text across two
   source locations), plus the inner half of the `before=$(probe); after=$(probe)` idiom once its
   substitution bodies reach `EvaluateExpression`.
2. **`ScanSubstitutions`/`ScanSubstitutionsInHeredocBody`.** Closes the _outer_ half of the
   `before=$(probe); after=$(probe)` idiom — the assignment-value scan in `envvars.go`/`gitdir.go`
   that never routes through `ParseShell` at all. `ScanSubstitutionsInHeredocBody` has no
   production caller today, so this slice's practical yield is entirely from
   `ScanSubstitutions`; the heredoc-body cache is added for completeness and to close the gap
   before any future caller reintroduces it, not because a current caller needs it.

**Verification gates**, matching this repo's own established discipline for a Guard-3-adjacent
change (ADR 0039's replay discipline; this repo's `.pre-commit-config.yaml` and `flake.nix`):
`go test ./... -race` (race matters here specifically, given the new shared package-level state),
`prek run --files <changed>`, `nix flake check`, and — because "verdict-neutral by construction"
was also the _stated_ intent of Guard 3's own four fixes before replay found the one named,
individually-justified exception (the docker `Raw`-staleness transition) — a zero-unexplained-
transition corpus replay against the same 2026-08-21 snapshot `LOWERING.md` already establishes as
this area's baseline, re-confirming "same verdicts, fewer parses" empirically rather than resting
on the argument alone.

**A new fixture, mirroring `TestGuard3_ParseCountFixtures`'s existing shape**: a synthetic
`before=$(probe); after=$(probe)`-shaped command and a synthetic repeated-heredoc-script command,
asserting (via `SetParseObserver` or a companion cache-hit counter) that the second occurrence is
served from cache — i.e., that the fix actually closes the named idiom, not merely that verdicts
stay unchanged.

**Guard 3's own instrumentation will need a follow-up, out of this ADR's scope but foreseeable**:
once `ParseShell` is memoized, a "repeat call for an already-seen string" is no longer an expensive
re-parse — `knownGuard3Residual`'s class-2 exemption (`guard3_parsecount_test.go`, the whole reason
this bead exists) becomes vacuous and should be deleted, and the corpus ceiling (250) recalibrated
against whatever the 140-row unattributed bucket becomes once slice 1 and 2 both land. `SetParseObserver`'s
own semantics (fires on every logical call, hit or miss) should stay unchanged — it is answering
"how many _logical_ parse requests happened," which remains meaningful; a separate, explicit hit/miss
counter is a cleaner way to answer "how many were actually expensive" if that number is ever needed,
rather than overloading the existing observer's contract.

## Consequences

### Positive

- Closes the named 161-corpus-row residual (and, incidentally, the previously-unnamed `ssh`/
  `kubectl` local-scan case) with **no signature changes** anywhere outside `internal/cmdparse`:
  `hookio.Evaluator`'s two methods, `Engine`'s exported methods, and every rule package's own
  `cmdparse.Parse`/`ParseShell`/`EnumerateSubstitutions` call sites are untouched.
- Generalizes a mechanism (`shellCScriptCache`) already proven correct for one rule's own three
  passes to every recursion path in the module, the same "shared owner over drifting local copies"
  move ADR 0055 makes for path-argument classification.
- Makes `secrets.go`'s local cache a retirement candidate rather than a permanent fixture, once the
  shared cache is proven — one fewer rule-local mechanism to keep consistent with a module-wide
  invariant.
- Directly enables retiring `knownGuard3Residual`'s class-2 exemption and recalibrating Guard 3's
  corpus ceiling, tightening the guard this bead's own parent (`pg2-x9452`) established.

### Negative

- **This ADR does not implement the change.** Everything above is a design; a shared,
  process-lifetime-adjacent cache touching the module's single most heavily-exercised code path
  (every `Bash` tool call) needs its own implementation bead with the replay verification named
  above, not a same-session follow-on to a design pass.
- **A cached value is now aliased across callers within one hook evaluation.** Invariant 3 records
  that no current caller mutates a returned `ParsedCommand`/`ShellParse` in place, but this
  invariant was previously true "by accident" (no caller had a reason to reuse a returned value
  across occurrences); once a cache makes reuse routine, a future caller mutating a returned value
  in place becomes a real, and possibly hard-to-diagnose, correctness bug rather than a merely
  wasteful one. The implementation should make this an explicit, checkable contract (a doc comment
  on `ShellParse`/`ParsedCommand`, and ideally a test that mutates a cache-hit result and asserts
  the _next_ hit is unaffected — which would fail today's `shellCScriptCache` too, so this is not a
  new risk, only a wider blast radius for an existing one).
- **Two independent grammar entry points (`ParseShell`'s "command" mode and
  `ScanSubstitutions`'s/`ScanSubstitutionsInHeredocBody`'s two further modes) mean three caches to
  reason about, not one.** A single unified cache keyed only on text, without the mode
  discriminator, would be a subtle correctness bug (Decision, "What gets cached") — this is called
  out explicitly so an implementer does not simplify it away for tidiness.

### Neutral

- The choice of a package-level cache reset at `EvaluateHook`'s top, rather than a cache object
  threaded explicitly through every call site, is a judgment call — see Alternatives Considered
  for why the ABI-preserving shape was chosen over the "obviously explicit" one, and what would
  make the alternative worth revisiting.
- This design changes no currently-shipped verdict. Its effect is entirely on how much parsing
  work a hook evaluation containing repeated text does, never on what decision that evaluation
  reaches.

## Alternatives Considered

### Thread an explicit cache object through `EvaluateExpression`/`EvaluateStructure`/`ParseShell`

**Rejected as the default, kept as a fallback if a narrower lifetime is ever needed.** This is the
"obviously correct in isolation" shape: add a cache parameter to `hookio.Evaluator`'s two methods
and to `cmdparse.ParseShell`, and have every caller pass one down. It was rejected because the seam
(`ParseShell`/`ScanSubstitutions`/`ScanSubstitutionsInHeredocBody`) is _already_ the one place
every caller in the module funnels through — six files implement or consume `hookio.Evaluator`
(`engine.go` plus five rule packages: `envvars`, `docker`, `nix`, `safecmds`, `kubectl`), and
changing its method signatures would touch all of them plus every test mock, for a lifetime
("one hook evaluation") that a package-level reset already provides with zero interface change.
This alternative becomes worth reconsidering only if a future need arises for a cache scope
_narrower_ than one hook evaluation (e.g., per-leaf), which nothing in the current architecture
needs.

### A verdict-level cache (memoize `EvaluateExpression`'s `RuleResult`, not just the parse)

**Rejected.** Unsafe by construction: a verdict depends on `stack`/`origin` context that
legitimately varies between two textually-identical occurrences (Correctness invariant 4). Caching
at this layer risks silently reusing a verdict computed under the wrong `InCommandVars`/`CWD`
context — a real correctness regression, in the same class of bug Guard 3/I1b exists to prevent,
in exchange for closing an efficiency gap. The bead's own framing ("keyed on normalized source
text," about _parsing_) and I7's own scope (parse count, not verdict count) both point at the parse
layer, not the verdict layer.

### A process-wide cache with no reset boundary, matching `freshTempDirCache`'s own idiom exactly

**Considered, not adopted as the default.** The purity argument justifying `freshTempDirCache`'s
unbounded, process-wide `sync.Map` applies equally here — but that cache's key/value pair is small
(an enum plus a typically-short value string, and a bool), while this cache's values are full
parsed ASTs, potentially large (embedded scripts, heredoc bodies). Leaving it unbounded across a
350k-row replay run risks a materially different memory profile than the existing cache's, which
`freshTempDirCache`'s own "not a growth concern" argument does not by itself cover. Resetting per
`EvaluateHook` call bounds growth to one row's own duplicate-text budget (measured in the hundreds
per LOWERING.md's own corpus figures, not the run's total). A process-wide variant remains a
low-risk future widening if replay-mode profiling ever shows the per-row reset itself costing
more than it saves — unlikely, since `EngineCache`'s own measurement shows the big replay win is
CWD/engine reuse, not text reuse across otherwise-unrelated rows.

### An LRU or size-capped cache

**Considered, deferred as premature.** The reset-per-hook-evaluation boundary already bounds
growth to one tool call's own duplicate text, and the corpus evidence motivating this bead
(hundreds of rows, not tens of thousands, per named pattern) gives no signal that unbounded growth
within _one_ evaluation is a real problem today. An eviction policy is a straightforward addition
later if a pathological case (one enormous script duplicated many times in one command) is ever
measured; adding it now would be optimizing against a risk this bead's own evidence does not show.

### Status quo — leave the residual documented and accepted, as `pg2-k1c91` left it

**Rejected; this is the option `pg2-k1c91`'s own recommendation already argued against.** The
residual is real (161 measured rows), the fix generalizes a mechanism already proven for one rule,
and leaving it "accepted" indefinitely is exactly the shape ADR 0055's evidence table warns about
for a different defect class: an unfixed structural gap does not get smaller on its own, and the
next rule needing recursive re-entry (there is no reason to expect `envvars` is the last) would
reproduce the identical pattern rather than reuse a shared answer.

## Related Decisions

- `pg2-k1c91` (closed, `67b023e8`) — the bead this one splits off from; recorded the corpus
  evidence and the sibling `secrets.go` fix this design generalizes.
- [ADR 0039](0039-ceta-shell-parser-front-end.md) — established the single lowering seam
  (`cmdparse`, one real parser) this design's cache sits inside; I7's "parse count, not verdict
  count" framing is the same one this ADR's invariant 4 relies on.
- [ADR 0055](0055-ceta-candidate-path-argument-classification-shared-owner.md) — the direct
  structural precedent: "several independent passes answering the same question is where
  divergence lives," resolved by a shared owner rather than parallel local mechanisms. This ADR is
  that same move one layer below argument classification, at the parse layer.
- `internal/cmdparse/LOWERING.md`, Guard 3 (I7) section — the corpus evidence and the exact
  recommendation text this bead was split from.
- `internal/rules/secrets/secrets.go`'s `shellCScriptCache` (`pg2-k1c91` item (a)) and
  `internal/cmdparse/incommandvars.go`'s `IsFreshTempDirAssignment`/`freshTempDirCache` — the two
  existing memoizations this design's purity and lifetime arguments are built from.
