# Cmux Sidebar Integration (Phase 2)

**Status**: Draft
**Date**: 2026-05-14
**Phase**: 2 of 2 (Phase 1 — `CmuxSignaler` — see `2026-05-14-cmux-signaler-design.md`)
**Bd issue**: Filed during Plan Task 1

## Context

`claude-agents-tui` runs in a terminal and surfaces session state — working / idle / paused / dormant — plus toggles for `caffeinate` and `auto-resume`. When the host is cmux, that state is invisible unless the user has the claude-agents-tui surface focused. cmux exposes a sidebar control plane that can host this state across all workspaces:

- `cmux set-status <key> <value> --icon --color` / `cmux clear-status <key>` — keyed multi-status entries shown in the cmux sidebar.
- `cmux set-progress <0..1> --label` / `cmux clear-progress` — single progress bar.
- `cmux notify --title --body` — one-shot toast.

Phase 1 already established cmux detection (`CMUX_WORKSPACE_ID` env) and the CLI invocation pattern (`RunCmd` injection seam, 5-second context timeout, log file for errors instead of stderr). Phase 2 reuses both.

## Decision

Add an `internal/cmuxstatus` package with a `Reporter` interface and two implementations (`Cmux` and `Noop`). Wire it into the TUI Model with two cadences, both pushing the **full status snapshot** (caffeinate + nudge + state + progress) in a single call — there is no per-slot push:

- **Immediate push on user toggle**: when the user presses `C` (caffeinate) or `R` (auto-resume), `m.reporter.Push(snapshot)` fires synchronously inside the keybinding handler after the bool flip.
- **Periodic push on poll tick**: every Nth poll cycle (default 5; configurable as `cmux_sidebar_interval_ticks`) the Model calls `m.reporter.Push(snapshot)` with the freshly aggregated values. Notifications fire as a separate discrete event (5h-block reset + nudge dispatched). On `tea.Quit` the Model calls `m.reporter.Clear()`.

Pushing the full snapshot every time keeps the reporter stateless (no diffing), the wiring uniform (toggle and tick share one code path), and the sidebar self-healing (a missed update on one slot is rectified by the next push). The cmux subprocess cost is 2 calls per push (1 `set-status` + 1 `set-progress`).

Reporter construction is gated on `CMUX_WORKSPACE_ID` and a new optional config flag `cmux_sidebar_enable` (default true). When either is unset, `NewReporter` returns the `Noop` implementation and every method is a no-op. No subprocesses run, no socket touches.

### Sidebar slot mapping

| Slot | Key / target | Source of truth | Update trigger |
| --- | --- | --- | --- |
| Status | `claude-agents` | aggregate state + caffeinate + nudge toggles | Immediate on `C`/`R` toggle; every N ticks |
| Progress | (singleton) | `100 * m.tree.ActiveBlock.CostUSD / m.tree.PlanCapUSD`, clamped to [0,1]; forced to 1.0 while `m.tree.WindowResetsAt != 0` | Every N ticks |
| Notification | (one-shot) | `autoResumeFireMsg` handler at the moment of fire | Per nudge event |

The single status pill keyed `claude-agents` collapses all toggle and state information into one entry (cmux has no ordering guarantee for multi-pill sets). Value format: `<state> [• caff] [• nudge]` where the toggle suffixes are appended only when the respective toggle is on.

Status values, icons, and colors:

- `claude-agents`: state text leads; icon and color follow aggregate state.
  - `"working"` (icon `play`, color `#00cc66`)
  - `"idle"` (icon `pause`, color `#888888`)
  - `"paused (resets HH:MM)"` (icon `clock`, color `#ff8800`)
  - `"dormant"` (icon `moon`, color `#555555`)
  - Aggregate state = highest precedence across sessions, with `paused` winning over everything when `m.tree.WindowResetsAt` is set. If at least one session is Working and no pause: `working`. Else `idle` / `dormant`.
- Toggle suffixes (appended only when on): `" • caff"` for caffeinate, `" • nudge"` for auto-resume.
- Examples: `"working"`, `"working • caff"`, `"working • caff • nudge"`, `"paused (resets 15:30) • caff • nudge"`, `"idle"`.

Progress metric: `100 * block.CostUSD / tree.PlanCapUSD` (matches the TUI header). Bar value is clamped to [0,1]. Label is `"5h block N% of cap"` where N is the raw unclamped percent (allowing >100 when over-budget). When `PlanCapUSD <= 0`, `HasProgress=false` and no `cmux set-progress` call fires (mirrors the TUI's "plan cap unknown" branch). While paused (`WindowResetsAt != 0`): label is `"5h block exhausted — waiting for reset"`, value `1.0`.

Notification fires from the `autoResumeFireMsg` handler, after `signalNonWorking` returns:

- Title: `claude-agents-tui`
- Body: `5h window reset. Nudged %d idle session(s) to continue.`

### `cmuxstatus.Reporter` interface

```go
type State int
const (
    StateUnknown State = iota
    StateDormant
    StateIdle
    StateWorking
    StatePaused
)

// Snapshot is the full sidebar state. The TUI builds one of these for every push
// (toggle or periodic tick) — the reporter does not diff or remember previous
// values.
type Snapshot struct {
    CaffeinateOn bool
    NudgeOn      bool
    State        State
    // PausedResetAt is the wall-clock time the 5h block is expected to reset.
    // Zero unless State == StatePaused.
    PausedResetAt time.Time
    // Progress is 0..1; values outside the range are clamped by the reporter.
    Progress      float64
    // ProgressLabel is the text shown next to the bar. Empty string is allowed.
    ProgressLabel string
    // HasProgress is false when no active 5h block is known; the reporter
    // should skip the cmux set-progress call rather than push a misleading
    // zero. The caller (TUI Model) is responsible for filling this in.
    HasProgress bool
}

type Reporter interface {
    Push(s Snapshot)
    Notify(title, body string)
    Clear()
}
```

Method failures are non-fatal: any `cmux` subprocess error is routed through the same log-file path as Phase 1's `signalNonWorking` (i.e. `<cacheDir>/signal-errors.log`). The `Cmux` implementation takes a `logf func(string)` in its constructor to decouple from the TUI Model.

### `Cmux.Push` invocation order

Per call:

1. `cmux set-status claude-agents <value> --icon <icon> --color <hex>` where `<value>` is `<state> [• caff] [• nudge]`.
2. If `s.HasProgress`: `cmux set-progress <value> --label "<label>"`; otherwise skip.

Both invocations share a 5-second `context.WithTimeout` per Push. If call 1 fails, call 2 still attempts — partial-success is better than all-or-nothing for sidebar UX. Each failure logs one line to `signal-errors.log` and execution continues.

### Files

New:

- `internal/cmuxstatus/reporter.go` — interface + two implementations.
- `internal/cmuxstatus/reporter_test.go` — unit tests via `RunCmd` injection.

Modified:

- `internal/tui/model.go` — add `reporter cmuxstatus.Reporter`, `tickCount int`, `sidebarIntervalTicks int`. Add a helper `func (m *Model) buildSidebarSnapshot() cmuxstatus.Snapshot` that gathers caffeinate, nudge, aggregate state, and progress into one struct. Add `Options.Reporter` and `Options.SidebarIntervalTicks`.
- `internal/tui/update.go` — invoke `m.reporter.Notify(...)` from `autoResumeFireMsg` after `signalNonWorking`. Increment `m.tickCount` on `pollResultMsg`; every Nth tick call `m.reporter.Push(m.buildSidebarSnapshot())`. On `tea.Quit` (intercepted via `isQuit`) call `m.reporter.Clear()`.
- `internal/tui/keybindings.go` — `handleToggleCaffeinate` and `handleToggleAutoResume` call `m.reporter.Push(m.buildSidebarSnapshot())` immediately after flipping the bool.
- `internal/config/config.go` — add `CmuxSidebarEnable bool` (default true) and `CmuxSidebarIntervalTicks int` (default 5).
- `cmd/claude-agents-tui/main.go` — construct `cmuxstatus.NewReporter(cfg, logf)` and pass to `tui.NewModel` via `Options.Reporter`.
- `internal/headless/headless.go` — also construct a reporter; in the headless loop, push state + progress at the same cadence so users running `--wait-until-idle` in a cmux surface still see the sidebar update.

No changes to `internal/poller/`, `internal/signal/`, `internal/aggregate/`, `internal/ccusage/`.

### State aggregation

Aggregate-state lives outside the reporter. Compute in `Model` (or a small helper in `internal/tui`):

```go
func aggregateState(tree *aggregate.Tree) (cmuxstatus.State, time.Time) {
    if tree == nil { return cmuxstatus.StateUnknown, time.Time{} }
    if !tree.WindowResetsAt.IsZero() { return cmuxstatus.StatePaused, tree.WindowResetsAt }
    anyWorking, anyIdle := false, false
    for _, d := range tree.Dirs {
        for _, sv := range d.Sessions {
            switch sv.Status {
            case session.Working: anyWorking = true
            case session.Dormant: // ignore
            default:              anyIdle = true
            }
        }
    }
    switch {
    case anyWorking: return cmuxstatus.StateWorking, time.Time{}
    case anyIdle:    return cmuxstatus.StateIdle, time.Time{}
    default:         return cmuxstatus.StateDormant, time.Time{}
    }
}

func windowProgress(tree *aggregate.Tree, now time.Time) (float64, string, bool) {
    _ = now // retained for signature parity; the cost-based metric doesn't depend on wall-clock
    if tree == nil { return 0, "", false }
    if !tree.WindowResetsAt.IsZero() {
        return 1.0, "5h block exhausted — waiting for reset", true
    }
    b := tree.ActiveBlock
    if b == nil || tree.PlanCapUSD <= 0 {
        return 0, "", false
    }
    pct := 100 * b.CostUSD / tree.PlanCapUSD
    v := pct / 100
    if v < 0 { v = 0 }
    if v > 1 { v = 1 }
    return v, fmt.Sprintf("5h block %.0f%% of cap", pct), true
}
```

`(progress, label, ok)` — when `ok` is false, the Reporter call is skipped for that tick (no cmux subprocess fires).

### Reporter contracts

`Cmux.Push(s)` invocation order is described in "`Cmux.Push` invocation order" above. Toggling to off does NOT call `cmux clear-status caffeinate`; we always overwrite with an "off"-valued set-status so the icon stays on the sidebar even when disabled. `Clear` is reserved for shutdown.

**Workspace scope.** None of the `Cmux.*` calls pass `--workspace`. Per `cmux --help`, when the flag is omitted cmux falls back to the caller's `CMUX_WORKSPACE_ID` — i.e. the workspace hosting the TUI process. Consequences:

- Status and progress entries appear only on the TUI's own workspace sidebar. Sibling workspaces are untouched. Switching to a different workspace inside cmux yields an empty sidebar w.r.t. our keys.
- `cmux notify` is app-level (toasts surface regardless of active workspace), so the 5h-reset notification is visible from anywhere.

Cross-workspace status broadcasting (e.g. iterate all workspaces and push status to each) is intentionally out of scope; it would clutter sibling sidebars with state owned by a process the user may not be focused on. Phase 3 could revisit.

`Cmux.Notify(title, body)` issues `cmux notify --title "<title>" --body "<body>"` — one subprocess call. Errors route to `logf`.

`Cmux.Clear()` issues `cmux clear-status claude-agents` and `cmux clear-progress`. Two subprocess calls. Notifications are not cleared (cmux owns notification retention). Errors route to `logf`. Best-effort — partial failures are ignored.

`Noop` has empty method bodies. Constructed when `CMUX_WORKSPACE_ID` is empty or `cmux_sidebar_enable=false`.

### Configuration

Append to `internal/config/config.go`:

```go
// Config
CmuxSidebarEnable        bool
CmuxSidebarIntervalTicks int

// raw
CmuxSidebarEnable        *bool `toml:"cmux_sidebar_enable"`
CmuxSidebarIntervalTicks *int  `toml:"cmux_sidebar_interval_ticks"`

// defaults
cfg.CmuxSidebarEnable = true
cfg.CmuxSidebarIntervalTicks = 5
```

A value of `0` or negative for the tick interval is treated as "every tick".

### Testing

`internal/cmuxstatus/reporter_test.go`:

- `TestCmuxPushEmitsOneStatusPlusProgress` — one `Push` call with `HasProgress=true` produces exactly 1 `cmux set-status claude-agents` call plus 1 `cmux set-progress`. Assert arg shapes including toggle suffixes.
- `TestCmuxPushOmitsToggleSuffixesWhenOff` — `HasProgress=false`, both toggles off → 1 `set-status` call with value `"idle"`, no caff/nudge suffix.
- `TestCmuxPushCaffOnlyShowsCaff` / `TestCmuxPushNudgeOnlyShowsNudge` — single-toggle suffix appears only when that toggle is on.
- `TestCmuxPushClampsProgress` — values `-1`, `0.5`, `2.5` produce clamped `0`, `0.5`, `1.0` respectively.
- `TestCmuxPushPartialFailureContinuesAndLogs` — `RunCmd` returns an error on the first call (status); the second (progress) still attempts; `logf` is invoked once.
- `TestCmuxNotifyEmitsCmuxNotify`.
- `TestCmuxClearIssuesTwoCalls` — `Clear()` produces exactly 2 calls: `clear-status claude-agents` + `clear-progress`.
- `TestNewReporterReturnsNoop*` — every method runs without subprocess calls outside cmux or when disabled.

`internal/tui/model_test.go` additions:

- `TestModelPushesSidebarOnCaffeinateToggle` — invoking `handleToggleCaffeinate` triggers exactly one `reporter.Push` with the new toggle state.
- `TestModelPushesSidebarOnAutoResumeToggle` — same shape.
- `TestModelPushesEveryNTicks` — fake reporter records calls; advance N `pollResultMsg`s; assert exactly one `Push` at tick N (not at ticks 1..N-1).
- `TestModelNotifiesOnAutoResumeFire` — drive an `autoResumeFireMsg` with a pause condition; assert `Notify` called once.
- `TestModelClearsSidebarOnQuit` — drive quit; assert `Clear` called.

Fake reporter is a struct implementing `Reporter` with call recorders, defined in `model_test.go`. Avoids the JSON-shaped subprocess fakes from Phase 1.

### Headless wiring

`internal/headless/headless.go` runs a poll loop that calls into `aggregate.Build`. Add reporter construction and call `SetState` / `SetProgress` at the existing poll cadence (no per-tick counter — headless polls less often by default). On exit, call `Clear`. No notifications from headless mode (rate-limit reset notification only makes sense alongside auto-resume, which is TUI-only).

### Failure modes

| Condition | Behavior |
| --- | --- |
| Outside cmux | `Noop` reporter; zero subprocesses; zero log lines. |
| `cmux_sidebar_enable=false` | Same as outside cmux — `Noop`. |
| `cmux` daemon dies mid-run | `Cmux.SetX` writes one error line per failed call to the log file; sidebar entries become stale but the TUI keeps running. No retry, no escalation. |
| Two `claude-agents-tui` instances in the same cmux | Last writer wins per sidebar key. v1 accepts this; Phase 3 could namespace keys by pid. |
| `m.tree.ActiveBlock == nil` at poll time | `windowProgress` returns `ok=false`; reporter is not called; no `cmux set-progress` runs that tick. |
| User quits with SIGKILL (no Quit handler) | Sidebar entries linger until another writer clears them or cmux restarts. Acceptable for v1. |

### Performance

- Toggle press: 1 `Push` → 2 subprocesses (1 set-status + 1 set-progress), each sub-ms socket round-trip.
- Periodic push (every 5 ticks ≈ 5s with default 1s poll): same 2 subprocesses.
- Notification: 1 subprocess on the discrete event.
- Quit: 2 subprocesses for `Clear`.

Steady-state load with no user input: ~24 subprocesses/minute (2 per push × ~12 pushes/minute). Burst on rapid toggle: bounded by keystroke rate. Trivial in absolute terms.

## Consequences

### Positive

- Sidebar surfaces caffeinate, nudge, state, progress, and notify-on-reset — full coverage of Phase 1 brainstorm wish list.
- Drop-in: no API change for existing tmux / non-cmux users; `Noop` reporter keeps them unaffected.
- Toggle-immediate cadence gives the user instant feedback that the keypress registered, even when the TUI isn't focused.

### Negative

- Reporter is a new abstraction layer touching 6 files. Surface area is wider than Phase 1.
- Sidebar slot ownership is implicit (last writer wins). Two parallel claude-agents-tui runs will fight.
- 5h-window progress depends on `ccusage` being available. When ccusage is missing (the "5h Block unavailable" fallback), progress is silently skipped — user may wonder why the bar isn't moving. Documented in failure-modes table.

### Neutral

- Aggregate-state logic lives in `internal/tui` rather than `internal/cmuxstatus` because it touches `*aggregate.Tree` and `session.Status`. Reporter stays free of domain types.

## Alternatives Considered

### Push every poll tick (no cadence throttling)

Rejected per brainstorm — wastes subprocess calls. Hybrid would have been "progress every poll, state on transition" but the user explicitly preferred "every 5 ticks, configurable."

### Bake into `CmuxSignaler` (single cmux client type)

Rejected — different lifecycle, different callers (reporter is per-TUI-instance, signaler is per-resolve-cycle), and different test shapes. Cleaner as a sibling package.

### One status key per session (per-PID sidebar entries)

Rejected — cmux sidebar slot count is finite and a per-session fan-out clutters it. Aggregate state is the right granularity. Per-session detail belongs in the TUI proper.

## Related Decisions

- See also: `docs/superpowers/specs/2026-05-14-cmux-signaler-design.md` — Phase 1 establishes `CmuxSignaler` and the env-based cmux detection convention reused here.
