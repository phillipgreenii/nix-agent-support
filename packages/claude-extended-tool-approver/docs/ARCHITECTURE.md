# CETA Architecture

CETA (`claude-extended-tool-approver`) is a Claude Code permission-hook binary: a small Go program
invoked once per tool call that decides Approve / Ask / Reject / no-opinion, and logs the decision
and its eventual outcome to a local SQLite database. This document describes the system's shape —
the design patterns it is built from, the lifecycle of one decision, and the two ways new policy
gets added to it. For the day-to-day operational reference (hook event table, decision-DB schema,
SQL queries, rule-module list, testing) see [`README.md`](../README.md); this document does not
repeat that material.

## Where CETA lives, and where its policy lives

CETA is split across two repositories, and this matters for anyone planning a change:

- **The engine and rule modules** — this package,
  `packages/claude-extended-tool-approver/` in `phillipgreenii-nix-agent-support` (this repo) — are
  generic and consumer-blind. No ZipRecruiter- or machine-specific literal MAY appear here.
- **Per-machine policy data** — the `rules.json` a given machine's home-manager module renders —
  lives in the consuming repo. For the `monorepod` machine that is
  `homelab/development/agent-support/ceta/` (`rules.example.json` + `schema.md`, the authoritative
  schema reference for the data-level extension point described below).

## Design patterns

CETA's evaluation core is a small composition of well-known patterns; naming them here is
deliberate, since the code comments use the pattern's own vocabulary throughout
(`internal/setup/factory.go`, `internal/engine/engine.go`):

- **Strategy** — every rule module implements one interface, `hookio.RuleModule` (`Name() string`,
  `Evaluate(*hookio.HookInput) (hookio.RuleResult, error)`). Each of the ~23 modules under
  `internal/rules/*/` is an interchangeable strategy for classifying one slice of tool calls; the
  engine holds them polymorphically and never type-switches on which one it is holding.
- **Chain of Responsibility** — `internal/setup.RuleChain` builds an ORDERED list of strategies,
  and `engine.Evaluate` walks it first-match-wins: a rule reports `hookio.ErrNotApplicable` to pass
  the input to the next link, or returns a verdict that (usually) terminates the chain. Ordering is
  itself part of the design — several rules exist only to run before a more permissive rule would
  otherwise have claimed the input (see "Rule Modules" in the README for the full annotated list).
- **Facade** — `Engine.EvaluateHook` is the single entry point real callers use; it hides the
  Bash-vs-everything-else branch (compound Bash commands go through `EvaluateExpression`, which
  splits and folds most-restrictive-wins; every other tool goes through the plain
  first-match-wins `Evaluate`).
- **Dependency Injection** — consumer policy (`rules.json`, loaded by `configrules.Load`) is parsed
  once per hook invocation and injected into the handful of rules that are config-driven
  (`kubectl`, `buildtools`, `ssh`, `vault`, `curl`, `monorepo`). The base binary carries no
  consumer literals: an absent or empty config block leaves its rule at a safe default (ADR 0033).
- **Composite / recursive evaluation** — a command is not always a single leaf. `nix`, `docker`,
  `kubectl`, `envvars`, `assume`, and `safecmds` take the `Engine` itself as an `Evaluator` so a
  nested body (a container's inner command, a `$(...)` substitution, an `xargs sh -c '...'`
  payload) is recursively re-evaluated through the _entire_ chain rather than judged by a
  bespoke sub-parser. `EvaluateExpression` / `EvaluateStructure` are the two entry points for this
  recursion (ADR 0039's "structural delegate" is the newer, parse-once form of the same idea).

## How it works: one decision's lifecycle

```mermaid
sequenceDiagram
    participant CC as Claude Code
    participant Hook as ceta (PreToolUse hook)
    participant Cfg as configrules.Load (rules.json)
    participant Eng as Engine (RuleChain)
    participant DB as asks.db (SQLite)

    CC->>Hook: PreToolUse(tool_name, tool_input, cwd, session_id, ...)
    Hook->>Cfg: load consumer rules.json (per invocation)
    Hook->>Eng: build engine for this CWD, register RuleChain
    Eng->>Eng: EvaluateHook (Bash: split+fold; else: first-match-wins)
    Eng-->>Hook: RuleResult{Decision, Reason, Module, Trace}
    Hook->>DB: RecordPreToolDecision (ask/deny only)
    Hook-->>CC: {} (no opinion) | {decision:"allow"} | {decision:"ask"} | {decision:"deny", reason}
```

1. **Claude Code fires `PreToolUse`** with the tool name, its input, and the session's cwd. CETA is
   registered as the hook handler (`cmd/claude-extended-tool-approver`'s hook mode).
2. **Consumer config is loaded fresh** — `configrules.Load(configrules.DefaultPath())` re-reads
   `$XDG_CONFIG_HOME/claude-extended-tool-approver/rules.json` on every call. The hook process is
   one-shot (one process per tool call), so this costs exactly one parse; it is not a caching layer
   (that only exists for the offline `evaluate`/`compare` CLI replaying historical rows).
3. **A per-CWD engine is assembled** — `internal/setup.newEngineForCWDWithConfig` detects the
   project root, builds a `PathEvaluator`, and calls `RuleChain(...)` to get the ordered rule list.
   `RuleChain` is documented in full in `internal/setup/factory.go`'s doc comment and is the
   canonical description of "what order do rules run in and why."
4. **The chain is evaluated.** For a Bash tool call, `EvaluateExpression` parses the command
   (`internal/cmdparse`, ADR 0039), splits it into leaves/redirections/substitutions, and folds
   each leaf's own first-match-wins verdict together most-restrictive-wins. For any other tool,
   `Evaluate` walks the chain once. See the **verdict vocabulary** below for what each rule may
   return and what it means for the chain.
5. **The verdict is serialized** and returned to Claude Code: `NoOpinion` emits `{}` (Claude Code
   decides on its own, per its normal permission settings); `Ask`/`Reject` emit a decision plus a
   human-readable reason; a plain Approve short-circuits Claude Code's own prompt.
6. **Ask/Reject decisions are logged** to the SQLite ask-log (`~/.local/share/claude-extended-tool-approver/asks.db`)
   as `pending`. Four other hook events (`PermissionRequest`, `PostToolUse`, `PermissionDenied`,
   `SessionEnd`) resolve that row's eventual `outcome` (`approved`/`denied`/`unresolved`) — see the
   README's "Decision Database" section for the full outcome-provenance table; this document is
   about how the _decision_ is reached, not how it is later graded.

### The verdict vocabulary

A rule module's return value is not one flat "verdict" — three outcomes are distinguished on
purpose, per [ADR 0043](../../docs/adr/0043-ceta-rule-verdict-vocabulary.md) (extended by
[ADR 0044](../../docs/adr/0044-ceta-verdict-provenance-and-the-refusal-outcome.md) and
[ADR 0060](../../docs/adr/0060-ceta-cleared-exhaustion-abstains-refused-floor-reject.md)):

| Return                                    | Meaning                                                                    | Effect on the chain                                                                                       |
| ----------------------------------------- | -------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| `hookio.NotApplicable()`                  | "not my business"                                                          | Chain continues to the next rule                                                                          |
| `{Decision: Approve\|Ask\|Reject}, nil`   | "I handled this, here is my verdict"                                       | Terminal — this rule's verdict wins (subject to the refusal floor)                                        |
| `{Decision: NoOpinion}, nil`              | "handled, and my answer is no gate"                                        | Terminal — emits `{}`; ADR 0041's agent-config carve-out is the one live user of this today               |
| a refusal (`hookio.ErrRefused`, ADR 0044) | "I examined this and will not clear it, but a later rule may still own it" | Folds into a **floor** (most-restrictive) and the chain continues; a later Reject/Ask still wins outright |
| any other error                           | "I could not determine" (a resolver failed, input unparseable)             | Counted per rule in `internal/metrics`/`rule_errors`, chain continues                                     |

The **decision ordering** is `Approve < NoOpinion < Ask < Reject`, and every fold in the engine
(`hookio.MostRestrictive`) is a max over that order — this is why a chain that finds nothing
decisive manufactures `NoOpinion` at exhaustion (ADR 0044's `ProvenanceExhaustion`) rather than
defaulting to Approve.

## Extension point 1: adding a rule module (code-level)

A **rule module** governs a _class of tool calls by policy logic_ — reachable only by changing Go
code and shipping a new binary. Use this when the classification cannot be expressed as data (it
needs to parse a command, walk a path, or call a resolver).

1. Create a new package under `internal/rules/<name>/` implementing `hookio.RuleModule`
   (`Name() string`, `Evaluate(*hookio.HookInput) (hookio.RuleResult, error)`). Follow the verdict
   vocabulary above precisely — the most common defect class here is returning a decisive verdict
   (even `NoOpinion`) for an input the rule does not actually govern, which makes it silently
   terminal and shadows every rule behind it.
2. **Register it in exactly one place: `internal/setup.RuleChain`** (`internal/setup/factory.go`).
   This requirement is enforced by construction, not merely documented: the engine integration
   suite (`internal/engine/engine_integration_test.go`) derives its own test chain from this same
   function, so a rule registered only in a hand-maintained test list is invisible to the
   integration suite and its interactions with every other rule go unexercised. (This is exactly
   how the `git-directory` rule once shipped a hard, non-overridable Reject with unit coverage
   only — see the doc comment above `RuleChain` for the incident.)
3. **Choose its position in the chain deliberately**, and say why in a comment next to its
   registration. First-match-wins makes ordering a load-bearing part of the policy: a generic
   approver (`safecmds`, `pathsafety`) MUST run after any rule that has to intercept a dangerous
   spelling of the same command first (`dangerouscmds`, `secrets`, `gitdir`). When in doubt, look
   at where a structurally similar rule already sits and follow its reasoning.
4. If the rule needs to recurse into an inner command (a container's entrypoint, a `-c` script
   argument, a substitution body), take the `Engine` as an `hookio.Evaluator` and delegate through
   `EvaluateExpression` (text) or `EvaluateStructure` (already-parsed structure, ADR 0039's I13
   entry point) rather than writing a bespoke sub-parser — this is the Composite pattern described
   above, and every existing recursive rule (`nix`, `docker`, `kubectl`, `envvars`, `assume`,
   `safecmds`) follows it.
5. Write unit tests alongside the rule (`go test ./...`). If the rule needs to exec the compiled
   binary or drive the SQLite ask log, that test belongs in a `*_integration_test.go` file behind
   the `integration` build tag instead — see the README's "Two suites, and why the split exists"
   for the reasoning; conflating the two slows down every `nix build` of this package.
6. If the rule embodies a genuine policy decision (not just a mechanical fix), write an ADR under
   `docs/adr/` per `docs/adr/0000-use-architecture-decision-records.md`, and add it to
   `docs/adr/index.md`. The bulk of this package's ~60 ADRs are exactly this: one rule's specific,
   security-relevant judgment call, recorded so a later change does not silently reverse it.
7. Update the annotated rule list in `README.md`'s "Rule Modules" section — it is documentation
   and may lag briefly, but the code (`RuleChain`) stays authoritative; do not let the two drift
   for long.

## Extension point 2: adding or changing an approved/blocked command (data-level)

Most "I want CETA to treat command X differently" requests do **not** need a code change. Consumer
policy is _data_, loaded once from a machine's `rules.json` and dependency-injected into the
generic, config-driven rules — no ZipRecruiter- or machine-specific literal belongs in this repo's
Go source.

- **Flat basename lists** — the top-level `approvedCommands` / `blockedCommands` arrays, decided by
  the `config-rules` rule (chain slot 1). `blockedCommands` is a straightforward Reject.
  `approvedCommands` is **absolute for its leaf**: it approves the command with any arguments and
  skips the entire early security band, including `git-directory`'s otherwise non-overridable hard
  deny. Read [ADR 0040](../../docs/adr/0040-ceta-approved-commands-are-absolute.md) before adding
  to this list — the bar is "I trust this command with any argument it is handed," and removal
  (not a code change) is the escalation path if that trust turns out to be wrong.
- **Structured per-tool blocks** — `kubectl`, `buildtools`, `ssh`, `vault`, `curl`, `monorepo` each
  feed one config-driven rule module (ADR 0033). An absent or empty block leaves that rule at its
  safe default (most of these Abstain/defer until configured).
- **The schema itself is documented outside this repo**, in the consuming machine's config. For the
  `monorepod` machine that is `homelab/development/agent-support/ceta/schema.md` — a 500+ line,
  field-by-field reference with worked examples for every block above. Treat that file, not this
  one, as authoritative for the exact shape of `rules.json`; this document only explains the
  _mechanism_ (why the split between code and data exists, and where each block's data ends up).
- To add a new _structured block_ (a new config-driven mechanism, not just a new value in an
  existing one) is extension point 1: it requires a new rule module, a `configrules.Config` field,
  and dependency injection wiring in `RuleChain` — `kubectl`/`buildtools` are the reference
  pattern cited by ADR 0033 for any future block of the same shape.

## File map

| Path                                 | Role                                                                                        |
| ------------------------------------ | ------------------------------------------------------------------------------------------- |
| `cmd/claude-extended-tool-approver/` | CLI entrypoint: hook handler + `evaluate`/`baseline`/`compare`/`report`/`show`              |
| `internal/setup/factory.go`          | `RuleChain` — single source of truth for which rules run, in what order                     |
| `internal/engine/engine.go`          | The Chain-of-Responsibility walker (`Evaluate`, `EvaluateExpression`, `EvaluateStructure`)  |
| `internal/hookio/`                   | Shared types: `RuleModule`, `RuleResult`, `Decision`, the verdict-vocabulary sentinels      |
| `internal/rules/*/`                  | The ~23 individual rule modules (one Go package each)                                       |
| `internal/rules/configrules/`        | Loads and validates consumer `rules.json`                                                   |
| `internal/cmdparse/`                 | The shell-parser front end (ADR 0039); `LOWERING.md` tracks migration coverage              |
| `internal/asklog/`                   | SQLite decision log (schema, migrations, resolvers for the four post-decision hook events)  |
| `docs/adr/` (repo root)              | Every CETA policy decision, numbered and indexed                                            |
| `README.md`                          | Operational reference: hook events, DB schema, full rule-module list, testing, dependencies |
