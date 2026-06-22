# pa-monitor: authentication failure as a first-class, alertable condition

**Status**: Draft
**Date**: 2026-06-22
**Deciders**: Phillip

## Context

Claude Code session `1fd33415-62e6-46da-abfc-6943ded13a30` hit an HTTP 401. The
synthetic api-error event in its transcript is:

```json
{
  "type": "assistant",
  "isApiErrorMessage": true,
  "apiErrorStatus": 401,
  "error": "authentication_failed",
  "message": {
    "content": [
      {
        "type": "text",
        "text": "Please run /login · API Error: 401 Invalid authentication credentials"
      }
    ]
  }
}
```

A 401 is **non-retryable**: the credentials/token are bad and the only fix is a
human running `/login`. It is also effectively **account-wide** — one expired
token breaks every Claude Code session running under that user, not just the one
that surfaced the error.

### What already works (verified, do not change)

The error pipeline already detects and correctly refuses to retry this:

- **Classification** — `claude-transcript/apierror.go` defines
  `ErrAuthFailed ErrorKind = "authentication_failed"` and keys classification off
  the JSONL `error` field, so this event already classifies as `ErrAuthFailed`.
  `ErrorRecord.RetryClass()` returns `ClassTerminal` for it (the `default` arm),
  and pa-monitor's `transcript.Retryable()`
  (`internal/core/transcript/classifier.go`) returns `false` for any non-transient
  class.
- **No auto-resume** — the nudger gate at
  `internal/daemon/nudger/disrupt.go:71` calls `transcript.Retryable(s.LastError)`
  and cancels any pending nudge when it is false. A 401 is never nudged or
  auto-resumed.
- **Transport** — the `ApiError` proto
  (`internal/proto/pa_monitor.proto`) already carries `kind`, `text`, `at`,
  `is_terminal`, `is_retryable`, `from_subagent`; the daemon already populates it.
- **OTel** — the daemon already emits `pa_monitor_sessions_errored{kind=...}`
  (an observable gauge: count of sessions currently in a terminal error, bucketed
  by kind) and `pa_monitor_session_api_error_observed_total{kind=...}`. An auth
  failure already increments the `kind="authentication_failed"` series.

### The gap

Auth failure is **indistinguishable and silent** in the human-facing surfaces:

| Surface                                      | Today                                                                     | Problem                                               |
| -------------------------------------------- | ------------------------------------------------------------------------- | ----------------------------------------------------- |
| TUI per-session glyph (`render/tree.go:218`) | renders `✗`                                                               | identical to every other non-retryable terminal error |
| TUI alert bar (`render/alerts.go`)           | no auth segment                                                           | account-wide outage is invisible at a glance          |
| CLI `status` (`cli_format.go:70`)            | raw `authentication_failed` in ERROR column, only when the table triggers | not a clear, actionable "run /login"                  |
| CLI `info` (`cli_format.go:134`)             | `last_error: authentication_failed`                                       | no remediation hint                                   |
| TUI legend (`modals.go:182`)                 | error glyphs undocumented                                                 | a new glyph would be undiscoverable                   |
| Grafana dashboard                            | no banner                                                                 | nothing surfaces a 401                                |
| Grafana alerting                             | none provisioned at all                                                   | a 401 does not "register as an alert"                 |

## Decision

Make authentication failure a **visible, red, actionable, account-wide,
alertable** condition across the TUI, CLI, and Grafana — without touching the
(already correct) classification, proto, OTel, or no-retry behavior.

Guiding rules:

- **One definition per package.** "Is this an auth failure?" is a single predicate,
  not an inline string match scattered across call sites. The render package keys it
  off `transcript.ErrAuthFailed` (it already imports `transcript` — that's the type
  of `LastError`). The `cmd` package compares the proto kind to the literal
  `"authentication_failed"` instead — this matches the existing `apiErrorIsEscalated`
  convention (`cli_format.go:167`, which compares `"unknown"`/`"server_error"`
  literals) and avoids a new `cmd → internal/core/transcript` import edge that does
  not exist today.
- **Always point at the fix.** Every surface says **run `/login`**.
- **Account-wide gets a banner.** Beyond per-session marks, auth failure raises a
  top-level banner (TUI alert bar, dashboard banner) because it usually means
  every session is broken.
- **Red.** Failure marks render red wherever color is available.
- **Self-healing.** When `/login` is run and a session emits any new
  user/assistant event, `IsTerminal` flips false (the tail-walk in
  `claude-transcript/apierror.go:LastAPIError`), so every glyph/banner/line clears
  on the next poll. No manual clear.

### 1. TUI (`packages/pa-monitor`)

**Shared predicate** — add a render-package helper:

```go
// authFailed reports a terminal authentication failure (non-retryable; run /login).
func authFailed(le *transcript.ErrorRecord) bool {
    return le != nil && le.IsTerminal && le.Kind == transcript.ErrAuthFailed
}
```

**Per-session glyph** — `render/tree.go:sessionGlyph`, inside the existing
`le.IsTerminal` block, special-case auth _before_ the retryable/non-retryable
split:

```go
switch {
case authFailed(le):
    primary = theme.Error.Render("⊘")          // auth failure — run /login
case s.SessionEnrichment.LastErrorRetryable:
    primary = "⚠"
default:
    primary = "✗"
}
```

`⊘` (U+2298) is single-width, so the status-glyph column does not shift. Nudge
markers continue to append as today (auth sessions will not have pending nudges
since they are non-retryable).

**Red theme style** — `render/theme.go` currently defines
`Working/Idle/Awaiting/Dormant/Branch` but no error style. Add `Error` styled with
ANSI palette color `1` (red). Render `⊘` with it. Leave `⚠`/`✗` as-is unless we
later choose to color them; the banner carries the primary emphasis.

**Global alert bar** — `render/alerts.go:Alerts` already receives the
`*aggregate.Tree`. Add a Tree helper:

```go
// AuthFailedCount returns the number of sessions whose most recent error is a
// terminal authentication failure (run /login). Account-wide outage signal.
func (t *Tree) AuthFailedCount() int { /* walk t.Sessions(); count authFailed */ }
```

Prepend a **highest-priority** segment (before resume/top-up — broken credentials
override everything), tier-aware via `wrap.Tier`:

- Wide: `⊘ AUTHENTICATION FAILURE — run /login`
- Narrow: `⊘ auth — run /login`
- Tiny: `⊘ /login`

To render the segment red, `Alerts` needs the theme. `AlertsOpts` has no `Theme`
field today, so this is three edits, not one: (1) add `Theme Theme` to `AlertsOpts`
(`alerts.go:13`), (2) pass `Theme: m.theme` in the `render.AlertsOpts{...}` literal
at the call site (`internal/tui/view.go:44`, where `m.theme` is in scope), and
(3) apply `opts.Theme.Error.Render(...)` to the auth segment inside `Alerts`.

**Legend** — `render/modals.go:legendRows`. The error glyphs are currently
undocumented; add all three:

```go
{Left: "⊘", Right: "auth       authentication failure — run /login"},
{Left: "⚠", Right: "error      retryable error (auto-resuming)"},
{Left: "✗", Right: "error      non-retryable error"},
```

### 2. CLI (`cmd/pa-monitor`)

**Shared predicate** (proto form, alongside `apiErrorIsEscalated` at
`cli_format.go:163`; literal kind string per that helper's convention — no
`cmd → transcript` import):

```go
func apiErrorIsAuthFailure(e *pb.ApiError) bool {
    return e != nil && e.GetIsTerminal() &&
        e.GetKind() == "authentication_failed"
}
```

**`status`** — `cli.go:runStatus` already collects `details []*pb.SessionDetail`
(`cli.go:57`). After collection, count auth failures; if any, print a prominent line
immediately after the `sessions:` summary line (`cli.go:42`), so it is seen before
scrolling. Use `⊘` for consistency with the TUI auth glyph (not `⚠`, which the TUI
uses for retryable errors):

```
⊘ authentication failure — run /login (2 sessions)
```

In `formatStatusSessions`, render the ERROR column as the compact `auth` for
auth-failure rows (the top banner carries the "run /login"); other kinds keep
showing `le.GetKind()`.

**`info`** — `formatSessionInfo`: when `apiErrorIsAuthFailure(le)`, append the
remediation hint to the kind line, keeping the existing text/age lines:

```
last_error:     authentication_failed — run /login
                Please run /login · API Error: 401 Invalid authentication credentials
                2 hours ago
```

### 3. Grafana dashboard (`packages/pa-monitor/grafana/pa-monitor-overview.json`)

Add a **full-width Stat panel at the very top of the dashboard**, above the
existing "Current status" row. Concretely: give the banner `gridPos {x:0, y:0, w:24, h:3}`
with a fresh unique panel `id` (100–104 and the per-panel ids are taken; use `105`),
and **shift every existing panel's `y` (and the "Current status" row's `y`) down by 3**
so nothing overlaps. It is **never blank** — that is the explicit requirement (no
"No data" gap when healthy):

- **Query** (Prometheus): `sum(pa_monitor_sessions_errored{kind="authentication_failed"}) or vector(0)`
  — the `or vector(0)` guarantees a value even when the series is absent/stale, so
  the panel never shows "No data".
- **Thresholds**: `0 → green`, `≥1 → red`.
- **Display**: color mode = **background** (the whole strip turns red on failure),
  large text.
- **Value mappings**: `0 → "✓ Auth OK"`, special/range `≥1 → "⊘ AUTHENTICATION FAILURE — run /login"`.

Healthy = a thin calm green "Auth OK" strip; failing = a loud full-width red
banner. Placement decision: **top of dashboard** (chosen).

Rationale for not "hiding when healthy": Grafana has no native hide-on-no-data
(open feature request `grafana/grafana#106672`), and the repeat-over-empty-variable
trick is unreliable (`grafana/grafana#8712`, `#23036` — it can render a stray
"All" panel). An always-present stat with `or vector(0)` is robust across versions
and provisioning-friendly, and satisfies "no blank gap" because it is never blank.

### 4. Grafana alerting — minimal registration (spans `phillipgreenii-nix-support-apps`)

Goal: make auth failure **register as an alert** in Grafana with the _minimum_
configuration — not a full alerting/notification build-out. Today the
observability module provisions datasources + dashboards but **no alerting**.

**Important format note (corrects an earlier draft):** Grafana **alerting** file
provisioning is _not_ shaped like dashboard provisioning. Dashboards use a
`dashboards.yaml` provider catalog that points at a directory of JSON files
(`dashboards.nix:27` renders that catalog). Alerting has **no provider-catalog
analog** — you drop the rule-definition YAML files directly into
`provisioning/alerting/` and Grafana loads them. The folder is named **inside each
rule's YAML**, not in a catalog. So the option below is a flat list of rule files,
**not** a `{folder, rules}` submodule.

**Observability module (`phillipgreenii-nix-support-apps/darwin/modules/observability`):**

- `ui.nix` — `mkdir -p .../provisioning/alerting` and symlink each contributed rule
  file into it (net-new wiring, parallel to the dashboards symlinks at `ui.nix:56-65`,
  but symlinking the rule YAML directly, not a catalog).
- New `alerting.nix` (sibling of `dashboards.nix`) — add option
  `phillipgreenii.observability.alertRuleFiles = lib.mkOption { type = listOf path; default = []; }`.
  The module symlinks each into `provisioning/alerting/`. Keep it minimal; this is
  the first alerting support in the module.
- **Verify unified alerting is enabled** in the rendered `grafana.ini`
  (`ui.nix` `grafanaIni`). Grafana ~11.x (nixpkgs 26.05) defaults `[unified_alerting] enabled = true`,
  but if the ini disables it or enables legacy alerting, `provisioning/alerting/` is
  silently ignored — confirm during implementation.
- Use Grafana's **default** contact point + default notification policy — no new
  contact points or routes. A well-formed rule transitions to **Firing** and shows
  in Alerting without any contact-point wiring; delivery (Slack/email) is wired later.

**pa-monitor (`phillipgreenii-nix-agent-support`):**

- Add `packages/pa-monitor/grafana/alerting/auth-failure.yaml` — a complete Grafana
  unified-alerting provisioning rule. The `condition` is a **refId** (not PromQL),
  and the query lives in a `data[]` array with a Prometheus query node plus
  `__expr__` reduce/threshold nodes. The Prometheus datasource UID must be the
  provisioned one (`prometheus`, matching the dashboard panels' `uid: prometheus`):

  ```yaml
  apiVersion: 1
  groups:
    - orgId: 1
      name: pa-monitor
      folder: Claude Agents
      interval: 1m
      rules:
        - uid: pa-monitor-auth-failure
          title: pa-monitor authentication failure
          condition: C
          for: 0s # fire immediately — the error is already terminal
          noDataState: OK
          execErrState: Error
          labels:
            severity: critical
          annotations:
            summary: "Authentication failure — run /login"
            description: "Claude Code session(s) have a terminal authentication failure (HTTP 401). Run /login to re-authenticate."
          data:
            - refId: A
              relativeTimeRange: { from: 600, to: 0 }
              datasourceUid: prometheus
              model:
                refId: A
                editorMode: code
                instant: true
                expr: sum(pa_monitor_sessions_errored{kind="authentication_failed"}) or vector(0)
            - refId: B # reduce A to a single number
              datasourceUid: __expr__
              model: { refId: B, type: reduce, expression: A, reducer: last }
            - refId: C # threshold: > 0 ⇒ firing
              datasourceUid: __expr__
              model:
                refId: C
                type: threshold
                expression: B
                conditions: [{ evaluator: { type: gt, params: [0] } }]
  ```

- Register it in `darwin/modules/pa-monitor/default.nix`, beside the existing
  `dashboardProviders.pa-monitor` block (`default.nix:50`), guarded the same way
  (no-op without the observability stack):

  ```nix
  phillipgreenii.observability.alertRuleFiles =
    [ ../../../packages/pa-monitor/grafana/alerting/auth-failure.yaml ];
  ```

**Folder caveat (decided):** alerting file provisioning resolves the folder **by
title** and auto-creates it if missing, but it does **not** share the UID of the
folder dashboard provisioning created — a known, still-open Grafana limitation
(`grafana/grafana#125079`). Result: the alert rule lands in a folder _titled_
"Claude Agents" that is **distinct** from the dashboards' "Claude Agents" folder
(two same-named folders). We **accept this** for the minimal registration; it does
not affect firing or the dashboard banner. Reconciling to a single shared folder
(e.g. provisioning the folder explicitly with a pinned UID) is deferred to the
future full-alerting build-out.

## Out of scope (already correct — regression-test only)

- Classifier (`claude-transcript`): `authentication_failed` already → `ClassTerminal`.
- `ApiError` proto: already carries `kind` + `is_terminal`.
- OTel metrics: `pa_monitor_sessions_errored{kind}` already emitted; no new metric.
- No-retry behavior: already enforced at `disrupt.go:71`.

## Testing

- `render/session_glyph_test.go`: terminal auth → glyph contains `⊘` and **not**
  `⚠`/`✗`.
- `render/alerts_test.go`: `AuthFailedCount() > 0` → banner segment present and
  sorted first; `== 0` → absent.
- `render/modals_test.go`: legend includes `⊘` (and `⚠`/`✗`).
- `cmd/pa-monitor/cli_format_test.go`: `status` prints the top banner line and the
  `auth` ERROR column for auth rows; `info` line carries `run /login`; non-auth
  rows unaffected.
- classifier guard (`internal/core/transcript`): a fixture event with
  `error:"authentication_failed"` and the 401 text flows through `Scan()` →
  `IsTerminal` + `Retryable()==false` (locks the upstream contract pa-monitor
  depends on).
- nudger (`internal/daemon/nudger`): a terminal auth error never produces a nudge
  intent.
- `nix flake check` in **both** repos; validate the dashboard JSON parses (and the
  new banner panel has a unique `id` + non-overlapping `gridPos`).
- Grafana alerting acceptance (manual/live, since it needs a running Grafana):
  confirm `[unified_alerting] enabled` in the rendered `grafana.ini`; load the
  provisioned rule and confirm it appears in Alerting → Alert rules and transitions
  to **Firing** when `pa_monitor_sessions_errored{kind="authentication_failed"} > 0`
  (and **Normal** when absent, via `or vector(0)`). A YAML lint/schema check can run
  in CI; firing behavior is verified live.

## Files touched

**`phillipgreenii-nix-agent-support`:**

- `packages/pa-monitor/internal/render/tree.go` — glyph + `authFailed` predicate
- `packages/pa-monitor/internal/render/alerts.go` — banner segment
- `packages/pa-monitor/internal/render/theme.go` — `Error` (red) style
- `packages/pa-monitor/internal/render/modals.go` — legend rows
- `packages/pa-monitor/internal/core/aggregate/tree.go` — `AuthFailedCount()`
- `packages/pa-monitor/internal/render/alerts.go` — add `Theme` to `AlertsOpts` + apply
- `packages/pa-monitor/internal/tui/view.go` — pass `Theme: m.theme` to `AlertsOpts`
- `packages/pa-monitor/cmd/pa-monitor/cli.go` — `status` banner line
- `packages/pa-monitor/cmd/pa-monitor/cli_format.go` — `apiErrorIsAuthFailure`, column/info
- `packages/pa-monitor/grafana/pa-monitor-overview.json` — top banner panel (id 105, y-shift)
- `packages/pa-monitor/grafana/alerting/auth-failure.yaml` — alert rule (new)
- `darwin/modules/pa-monitor/default.nix` — register `alertRuleFiles`
- tests alongside the above; README symbols/legend note

**`phillipgreenii-nix-support-apps`:**

- `darwin/modules/observability/ui.nix` — `provisioning/alerting/` mkdir + symlink
  rule files; confirm `[unified_alerting] enabled` in `grafanaIni`
- `darwin/modules/observability/alerting.nix` (new) — `alertRuleFiles` option
  (`listOf path`) + render (symlinks rule YAML directly into `provisioning/alerting/`)
- `darwin/modules/observability/default.nix` — import `alerting.nix`
- module README

## Consequences

### Positive

- A 401 is impossible to miss: red per-session glyph, red top banner (TUI +
  dashboard), clear CLI status/info lines, and a Firing Grafana alert.
- Reuses existing classification/proto/OTel; surfaces are thin and well-isolated.
- Establishes the first provisioned Grafana alert rule + a reusable `alertProviders`
  mechanism other apps can adopt later.

### Negative / trade-offs

- Cross-repo change: pa-monitor + the observability module. Inherent to "register
  as an alert," since alerting is provisioned by the shared module.
- The dashboard banner is always present (a thin green strip when healthy) rather
  than truly hidden — a deliberate choice given Grafana's lack of reliable
  hide-on-healthy.

### Neutral

- Alert delivery (contact points/routing) is intentionally deferred; the rule uses
  Grafana defaults and only needs to register/fire for now.

## Related decisions

- See also: `phillipgreenii-nix-support-apps` observability module
  (`darwin/modules/observability`) and the app-registration design
  (`docs/superpowers/specs/2026-06-15-observability-app-registration-design.md`).
- The new `alertProviders` mechanism in the observability module may warrant its
  own short ADR in `phillipgreenii-nix-support-apps/docs/adr/` when implemented.
