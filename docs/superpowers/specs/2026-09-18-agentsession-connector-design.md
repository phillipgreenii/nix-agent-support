# agentsession connector capability — design

Status: brainstormed and converged interactively (2026-09-18); not yet decomposed into beads.
This file is an ephemeral extraction source per this repo's own citation conventions — do not
cite it from code; derive an ADR or behavior doc from it if any of this needs to outlive the
implementation.

Revision note (2026-09-18): an independent review of the companion implementation plan found
several concrete errors in an earlier draft of both documents — a nonexistent pa-monitor proto
type, a wrong pg-connector CLI output function signature, a fabricated "thread" registration
precedent in ziprecruiter's machine config, and (this doc) a wrong claim about how contract tests
are gated. This revision corrects the Testing strategy section below; see the plan's own
Self-Review Notes for the full list and the rest of the corrections.

## Summary

A new pg-connector capability, `agentsession`, backed by a new Tier-2 backend
`pg-connector-agentsession-pa-monitor`, exposing live/recent Claude Code agent sessions (liveness,
status, model, token/cost usage) sourced from pa-monitor. It participates in the two existing
generic capabilities — `attention` (escalations: blocked/long-idle sessions, and account-level
5h-block/7-day-week usage-limit hits) and `search` (transcript content search) — and requires three
small, additive changes to pa-monitor's own CLI rather than any new pa-monitor domain logic.

Everything the connector needs from Claude session/transcript data flows through pa-monitor's CLI.
The backend has no filesystem/Claude-domain knowledge of its own (operator decision, 2026-09-18):
pa-monitor already contains non-trivial resolution logic (`session.ResolveTranscript` handles
Claude Code rewriting a transcript to a new session id on resume/compact/fork) that would silently
drift if duplicated.

## Naming

- Capability token: `agentsession` — package `pkg/provider/agentsession`, schema
  `pkg/schema/agentsession.go`. Single word, matching the existing `pr`/`issue`/`ci`/`scm`/`thread`/
  `attention`/`search` convention (`cmd/pg-connector/naming_convention_test.go`'s
  `capabilityPackages`) — deliberately NOT hyphenated, even though the feature is discussed in
  prose as "agent-session."
- Backend binary: `pg-connector-agentsession-pa-monitor`, matching the `<capability>-<system>`
  convention (`pg-connector-thread-slack`, `pg-connector-issue-beads`).
- CLI verb group: `pg-connector agentsession show <selector>` / `pg-connector agentsession list` —
  a DEDICATED Tier-1 verb group, mirroring `pr`/`issue`/`ci`/`scm`. This deliberately diverges from
  `thread`'s precedent (no dedicated verb group at all today — it participates only via
  `attention`/`search`), because sessions are a first-class entity users will query directly, not
  just a cross-reference source.

## pa-monitor changes (Phase 1)

pa-monitor gains three additions, none of which add new domain logic — each is a JSON-formatting
sibling of something the CLI already does, or a thin new library primitive:

1. **`--json` on `status`.** `runStatus` (`cmd/pa-monitor/cli.go:15`) already calls `GetState`
   (every directory → every session) and then, for every session found, calls `GetSessionInfo` to
   build its error/nudge annotation table. `--json` emits that already-gathered data (directories,
   per-session `SessionView`+`SessionDetail`, active block/week) as one JSON document instead of
   (or alongside) the text format.
2. **`--json` on `info <selector>`.** `runInfo` (`cmd/pa-monitor/control.go:230`) already resolves
   a `session:<id>` / `path:<p>` / `cmux:<id>` selector via `GetSessionInfo`/`GetPathInfo`.
   `--json` emits the same `SessionDetail`/`Directory` response as JSON instead of formatted text.
3. **New `search` subcommand**: `pa-monitor search --json <query> [--session <id>] [--since
<bound>] [--before <bound>]`. Resolves the transcript(s) for sessions in scope (one, if
   `--session` is given; otherwise the same session set `status` enumerates) via the existing
   `session.ResolveTranscript`, then calls a **new** primitive added to the `claude-transcript`
   library — `Search(path, query string, since, before time.Time) ([]Match, error)`, a naive
   per-call scan over parsed text blocks reusing the package's existing oversized-line-safe scanner
   (`newTranscriptScanner`) — against each. Each transcript event already carries its own
   `Timestamp` (`Event.Timestamp time.Time`, confirmed in `claude-transcript/events.go`), so bounding
   by time needs no new data — only a comparison against it. `--since`/`--before` each accept either
   a `time.ParseDuration` string interpreted as "this long ago" (e.g. `24h`, mirroring this repo's
   existing `attention.perBackend.threshold` convention) or an absolute RFC3339 timestamp — the two
   forms never collide syntactically, so one flag serves both without an explicit mode switch.
   Returns JSON: `session_id` → matches (role, snippet, timestamp, approximate line/turn index).
   Naive/unindexed by design (operator decision, 2026-09-18): efficient search across full history
   is explicitly deferred — a time bound narrows the scan, it does not index it.

No new gRPC calls and no new daemon logic for (1)/(2); (3) is new pure-Go code in
`claude-transcript`, layered on the existing `ResolveTranscript` + the RPCs (1)/(2) already use to
enumerate sessions.

## New connector capability contract (Phase 2)

### Schema — `pkg/schema/agentsession.go`

```go
const AgentSessionSchemaVersion = 1

// AgentSession mirrors pa-monitor's own Session/SessionView/SessionInfo fields — nothing here is
// newly computed by this capability; it is a reprojection of facts pa-monitor already tracks.
type AgentSession struct {
	SessionID    string  `json:"session_id"`
	PID          *int    `json:"pid,omitempty"`     // nil when the process is dead
	Cwd          string  `json:"cwd"`
	Name         string  `json:"name,omitempty"`
	Model        string  `json:"model"`
	Status       string  `json:"status"`            // "working" | "blocked" | "idle"
	Blocker      string  `json:"blocker,omitempty"` // "human_input" | "human_authn" | "usage_limit" | "error"
	Branch       string  `json:"branch,omitempty"`
	TerminalHost string  `json:"terminal_host,omitempty"`
	StartedAt    string  `json:"started_at"` // RFC3339
	Tokens       uint64  `json:"tokens"`
	CostUSD      float64 `json:"cost_usd"`
	LongIdle     bool    `json:"long_idle"`

	// AsOf/Stale: INV-ASOF-1/2, same contract every other capability carries.
	AsOf  string `json:"as_of"`
	Stale bool   `json:"stale"`
}

// Deliberately NO TranscriptPath/transcript-content field. Exposing a raw filesystem path invites
// a caller to bypass pa-monitor's own (non-trivial) transcript-resolution logic — see Summary.
// Transcript content is reached only through the search capability below.

type AgentSessionListResult struct {
	Entities   []AgentSession `json:"entities"`
	PresentIDs []string       `json:"present_ids"`
	Cursor     *string        `json:"cursor"`
	Truncated  bool           `json:"truncated"`
}
```

### Provider interface — `pkg/provider/agentsession/iface.go`

```go
package agentsession

type Provider interface {
	// Show returns id's current state (selector form "session:<id>", passed through to
	// `pa-monitor info <selector> --json`).
	Show(ctx context.Context, id string) (*schema.AgentSession, error)

	// List returns sessions currently in scope. query is accepted for interface-shape symmetry
	// with thread/issue's List but is NOT resolved via config.queries today — this capability has
	// no caller-facing query concept yet (pa-monitor's `status` op returns one fixed default
	// scope, not distinct named queries); the dispatch table always passes nil. idsOnly mirrors
	// thread.Provider.List's identical convention.
	List(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.AgentSessionListResult, error)
}
```

Read-only — mirrors `thread.Provider`'s shape exactly (no write ops; you don't create or mutate a
session through this capability).

### Backend — `pg-connector-agentsession-pa-monitor`

- `Show`/`List` exec `pa-monitor info <selector> --json` / `pa-monitor status --json` respectively.
- Implements `attention.Provider.ListAttention`:
  - Per-session: a `Blocked` or `LongIdle` session becomes
    `AttentionItem{type: "agentsession", id: SessionID, severity}`. Severity: `human_input`/
    `human_authn` blocked → `high`; `usage_limit` blocked → `medium` (self-recovering); long-idle →
    `low`.
  - Account-level: active `Block.CapHitAt`/`Week.CapHitAt` non-nil → a separate
    `AttentionItem{type: "agentsession-usage-limit", id: <block-or-week-id>}` at `critical` (halts
    every session, not just one).
- Implements `search.Provider.Search`: execs `pa-monitor search --json <query>`, maps each match to
  `schema.SearchResult{type: "agentsession", id: session_id, source: "pa-monitor", attributes:
{...}}`.
- Daemon-unreachable handling: `capabilities` reports `disabled` (matching the no-credential-backend
  precedent the 2026-09-05 deep-review's finding A2 points toward — a correctly-configured host with
  the daemon simply not running must not read as a hard connector failure).
- `capabilities.SchemaVersions` must list all three capabilities this backend answers for
  (`agentsession`, `attention`, `search`), mirroring `pg-connector-issue-beads`'s own two-capability
  precedent — `scriptout.AddCapabilities` computes `Ops` automatically from the merged dispatch
  table, but `SchemaVersions` must be hand-populated for every merged-in capability or
  `config validate`'s schema-skew check is silently blind to two of the three.

## Mechanical registration points (Phase 2/3)

Each of these is an existing, independently-enforced convention point (mostly mechanical guard
tests) that a new capability/type must be added to. Missing any one is a real, easy-to-miss defect
class in this codebase, not hypothetical — several of these tests exist specifically because a
prior addition (`thread`, `calendar`) forgot one:

1. `cmd/pg-connector/registry.go:458` `entityTypes` — add `"agentsession"`.
2. `cmd/pg-connector/naming_convention_test.go:27` `capabilityPackages` — add `"agentsession"`
   (mirrors the `thread`/`calendar` precedent in that file's own doc comment exactly).
3. `cmd/pg-connector/entity_store_test.go`'s `entityKindTokens` map — add
   `"agentsession": "agentsession"`.
4. `cmd/pg-connector/root.go`'s `newRootCmd()` — add `root.AddCommand(newAgentSessionCmd())`.
5. New `cmd/pg-connector/agentsession.go` — the verb group itself (`show`/`list`), mirroring the
   list-valued, targeted-op pattern `issue.go`'s `show` command and `ci.go`'s dispatch helpers use
   (`DispatchTargeted`/`Dispatch` from `dispatch.go`), NOT `scm.go`'s single-valued
   (`dispatchScm`) pattern — `agentsession`'s home-manager option is list-shaped (see point 9).
6. `cmd/pg-connector/config_validate.go`'s `entityTypesWithList` — leave `agentsession` OUT
   (that list governs `pr`/`issue` specifically today, not a general single-vs-multi-backend
   switch; `ci`/`scm`/`thread` are already absent from it despite `ci` being list-shaped).
7. New backend dir `cmd/pg-connector-agentsession-pa-monitor/` — mirrors
   `cmd/pg-connector-thread-slack/`'s internal layout convention.
8. New `packages/pg-connector/pg-connector-agentsession-pa-monitor.nix`, wired into the repo-root
   `flake.nix`'s overlay (mirrors the existing `pg-connector-thread-slack` entry there — NOT a
   `packages/pg-connector/flake.nix`, which does not exist).
9. `home/programs/pg-connector/default.nix`: add an `agentsession` option under the `connector`
   submodule (list-of-str, default `[ ]`, mirroring `thread`'s addition there), and add the new
   package to `home.packages`.
10. `claude-transcript`: new `Search` primitive + its own tests/fixtures.

## Downstream consumer wiring (Phase 3)

- `phillipg-nix-ziprecruiter`'s `modules/daily-focus/df-attention` and `df-search` are confirmed
  pure generic passthroughs (checked against both the package wrapper AND the underlying `.sh`
  scripts) of `pg-connector attention list` / `pg-connector search <query>`. **No code changes
  needed there** — they pick up the new backend automatically once it is registered.
- The actual Phase-3 work is CONFIGURATION, not code, in
  `phillipg-nix-ziprecruiter/machines/phillipg-mbp-02/default.nix`'s
  `phillipgreenii.programs.pg-connector` block. There is **no existing `thread` registration to
  mirror** — confirmed by direct inspection: that block's `connector = { ... }` currently has only
  `pr`/`issue`/`ci` (all list-shaped) and `scm` (a bare string), with no `thread` key at all, and
  `attention.sources`/`search.sources` are two separate literal lists. The new work is adding
  `agentsession = [ "pg-connector-agentsession-pa-monitor" ];` to the `connector` block (list form,
  matching `pr`/`issue`/`ci`) and appending the same binary name to both `attention.sources` and
  `search.sources`.
- No other consumer of pg-connector was found outside pg-connector itself and
  `phillipg-nix-ziprecruiter`'s daily-focus modules / `pg-router-source-pg-connector`.

## Testing strategy

- New backend: fake pa-monitor JSON fixtures for unit tests (mirrors the `fake_backend_test.go`
  pattern every other backend uses) plus a `//go:build contract` real-daemon test.
- **Contract-test gating (corrected 2026-09-18):** this repo has an established, deliberate
  convention that `//go:build contract` suites driving a real external system (`pg-connector-contract`,
  `ccpool-contract`, `pg-pr-contract`, `pb-contract` in the repo-root `flake.nix`) are `nix run`-only
  apps, NOT `checks.*` flake-check derivations — adopted specifically to keep a real, potentially
  flaky external dependency (here: a running pa-monitor daemon) out of the sandboxed default check
  path. The earlier draft of this design and plan proposed wiring this backend's contract test into
  an actual `checks.*` gate, treating the issue-beads precedent's "exists but runs nowhere" as an
  oversight to fix; it is not — it is the SAME deliberate pattern, and the correct fix is a
  `pg-connector-agentsession-pa-monitor-contract` `nix run` app mirroring the other four, not a new
  check.
- pa-monitor: unit tests for the two `--json` paths and the new `search` subcommand against a
  fake/injected `rpcclient` (mirrors `control_test.go`/`daemon_test.go`), plus tests for the new
  `claude-transcript.Search` primitive with fixtures under `testdata/` (mirrors
  `awaiting-multievent.jsonl`). The `runStatus` refactor needed to share dial/`GetState` helpers with
  the new `search` subcommand has no existing characterization test to protect it (there is no
  `cli_test.go` — only `cli_format_test.go`, which tests formatting helpers, not `runStatus`/
  `runInfo` themselves) — a characterization test for `runStatus`'s current text output must be
  added FIRST, before the refactor, so the refactor has an actual regression net.
- pg-connector: `naming_convention_test.go`/`entity_store_test.go`/`layout_convention_test.go`
  continue to mechanically enforce the Phase-2/3 additions above — these ARE tests, not just
  things to satisfy once.

## Explicitly out of scope

- Jira/beads/Slack entity cross-linking ("show sessions related to bead X") — a different mechanism
  than `search.Provider` (query-string search, not entity-relationship lookup); needs its own
  design.
- New "hung" detection beyond whatever pa-monitor's existing `Status`/`Blocker`/`LongIdle` already
  report.
- Indexed/efficient search across full transcript history — naive per-call scan only.
- `agent-transcript` as a separate capability/split — this design's schema deliberately carries no
  path/content field, keeping that split available later without a breaking change.
- Exposing the store-layer `Filter` (Active/All) choice over gRPC — `GetStateRequest` has zero
  fields today; deferred until something actually needs the choice.
- A caller-facing named-query mechanism for `agentsession list` — deferred; today it always returns
  pa-monitor's own default session scope.
- **Time-bound filtering on pg-connector's generic `search` capability.** `--since`/`--before` are
  added only to `pa-monitor search` (a direct CLI flag) and to `claudetranscript.Search`'s own
  signature. `pkg/provider/search.Provider.Search(ctx, query, fields)` — the interface EVERY search
  backend implements, including pr-github and issue-jira, not just this one — has no time-bound
  parameter at all, so a caller of `pg-connector search <query>` (the generic Tier-1 fan-out) has no
  way to reach this filter today. Extending the shared interface to carry a time bound is a
  cross-cutting change affecting every existing search backend, not something to decide unilaterally
  inside this design — tracked as its own brainstorm/exploration bead: `pg2-emmut`.
