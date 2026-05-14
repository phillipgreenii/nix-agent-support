# Cmux Signaler (Phase 1)

**Status**: Draft
**Date**: 2026-05-14
**Phase**: 1 of 2 (Phase 2 covers cmux sidebar status / progress / notification integration; out of scope here.)

## Context

`claude-agents-tui` already nudges idle Claude sessions back into work after the 5h billing window resets. Today only `TmuxSignaler` is implemented (`internal/signal/tmux.go`); `CmuxSignaler` is a stub that returns `ErrNotImplemented`. Users running `claude-agents-tui` from inside [cmux](https://cmux.com) get no auto-resume.

cmux exposes a control plane equivalent to `tmux send-keys`:

- CLI binary `cmux` shipping commands `send`, `send-key`, `list-workspaces`, `list-pane-surfaces`, `tree --all`.
- JSON-RPC over a unix socket (`CMUX_SOCKET_PATH`, default `~/Library/Application Support/cmux/cmux.sock`).
- Socket auth defaults to "cmux processes only" — only processes whose ancestry includes a cmux terminal can connect. `claude-agents-tui` running inside cmux satisfies this; running outside cmux it does not.

cmux object model (relevant subset):

| Term | Meaning |
| --- | --- |
| window | OS-level window |
| workspace | project / tab group inside a window; auto-exposed as `CMUX_WORKSPACE_ID` |
| pane / panel | split region (pane) or sidebar slot (panel) inside a workspace |
| surface | concrete content (terminal or browser) hosted inside a pane or panel; auto-exposed as `CMUX_SURFACE_ID` |
| tab | alternative grouping; `tab:<n>` and `surface:<n>` overlap |

One cmux daemon serves one socket; every window / workspace / pane / panel / surface is reachable from any in-cmux process. Per `cmux --help`, `CMUX_WORKSPACE_ID` is used as the *default* `--workspace` for every command — so naive `cmux list-pane-surfaces` would only enumerate the caller's own workspace and miss agents running in sibling workspaces.

## Decision

Implement `CmuxSignaler` as a CLI-driven signaler that mirrors `TmuxSignaler` in shape, dependency injection, and error semantics. Detection is a pure environment check on `claude-agents-tui`'s own process; sending performs one shot `cmux --json top --processes` whose JSON output enumerates every surface in the cmux instance (each surface entry carries `workspace_ref`, `pane_ref`, `surface_ref`, and `tty_process_pids` — already inclusive of every descendant of the surface's controlling tty), direct-matches the agent pid into the union of `tty_process_pids`, then issues `cmux send` + `cmux send-key enter` with both `--workspace` and `--surface` pinned. No `ps`-based ancestry walk is needed: cmux's own process-attribution already reports descendants per surface.

### Why CLI, not JSON-RPC

- One file, one dependency-injection seam (`RunCmd`) — matches `TmuxSignaler`.
- No new JSON-RPC client, no socket-auth handling (`--password` / `CMUX_SOCKET_PASSWORD`), no fake-socket test fixtures.
- Auto-resume fires roughly once per 5h window. Two extra `exec` calls per fire are not a measurable cost.
- If a future feature demands lower latency (e.g. Phase 2 sidebar updates on every poll), it can switch to the socket independently.

### Files touched

- `internal/signal/cmux.go` — replace stub with full implementation.
- `internal/signal/signal_test.go` — remove `CmuxSignaler` from `TestStubSignalersSendNotImplemented`'s stubs list.
- `internal/signal/cmux_test.go` — new test file, mirrors `signal_test.go` patterns.

No changes to `Signaler` interface, `ResolveSignaler`, `DefaultSignalers` order, or any caller in `cmd/`, `internal/poller/`, `internal/tui/`.

## Component design

### Type

```go
type CmuxSignaler struct {
    // RunCmd executes external commands. nil falls back to exec.CommandContext.
    RunCmd func(ctx context.Context, name string, args ...string) ([]byte, error)
    // LookupEnv reads env vars. nil falls back to os.LookupEnv.
    LookupEnv func(key string) (string, bool)
}

func (c *CmuxSignaler) Name() string                    { return "cmux" }
func (c *CmuxSignaler) Detect(pid int) bool             { /* env check */ }
func (c *CmuxSignaler) Send(pid int, text string) error { /* enumerate + match + send */ }
```

Same shape as `TmuxSignaler`. `LookupEnv` is a new seam (tmux had no env to inject); needed for unit tests that toggle `CMUX_WORKSPACE_ID` without mutating the test process env.

### Detect

```
return c.lookupEnv("CMUX_WORKSPACE_ID") != ""
```

Rationale: cmux auto-sets `CMUX_WORKSPACE_ID` in every cmux terminal, and the socket auth refuses connections from non-cmux ancestry. So `claude-agents-tui`'s own env is a tight equivalence for "we can talk to cmux right now." `pid` is intentionally ignored: cmux's socket is global to the instance, so reachability does not depend on the target agent's process tree — only on whether the *caller* (us) is in cmux.

Detection is the in-cmux guard. When the env var is absent, `ResolveSignaler` skips `CmuxSignaler`, `signalNonWorking` never calls `Send`, and nothing in `cmux.go` runs. Zero subprocesses, zero socket touches, zero log lines — exactly what the user asked for when running outside cmux.

### Send

1. **Enumerate every surface in the cmux instance in one call.** Run `cmux --json top --processes` and JSON-decode the response. Surface entries are reachable via `.windows[].workspaces[].panes[].surfaces[]`; the relevant fields per surface are `ref` (surface ref), `type`, `tty`, and `tty_process_pids`. The enclosing `.windows[].workspaces[].ref` provides `workspace_ref`. Build `map[int]surfaceLoc` keyed by every pid in any surface's `tty_process_pids`. Skip non-terminal surfaces and any surface whose `tty` is empty.

2. **Match agent pid directly.** `loc, ok := surfaceMap[pid]`. cmux's own per-tty process attribution already includes descendants of the surface shell, so the `ps`-based ancestry walk used by `TmuxSignaler` is unnecessary.

3. **Send keystrokes to the matched surface.**

   ```text
   cmux send     --workspace <wref> --surface <sref> <text>
   cmux send-key --workspace <wref> --surface <sref> enter
   ```

   Two calls. `cmux send` writes literal text without an automatic newline; `cmux send-key enter` produces the Enter that submits the prompt. Same pattern as tmux's `send-keys <text> Enter`. Both `--workspace` and `--surface` are pinned so the implicit `CMUX_WORKSPACE_ID` default cannot misroute when the target lives in a different workspace.

4. **No match** → `fmt.Errorf("signal: no cmux surface found for pid %d", pid)`. Same error shape as `TmuxSignaler`.

All three `cmux` invocations share a 5-second timeout via a single `context.WithTimeout` per `Send` call, matching `TmuxSignaler`.

### Failure handling

| Condition | Behavior |
| --- | --- |
| Outside cmux (`CMUX_WORKSPACE_ID` unset) | `Detect=false`. Signaler is never invoked. Silent. |
| Inside cmux, agent pid lives in a non-cmux ancestry (e.g. orphaned shell that escaped its surface tty) | `Detect=true`, `Send` returns "no cmux surface found" — one log line per nudge cycle, same as tmux. |
| Inside cmux, cmux daemon dead mid-run | `cmux --json top --processes` exits non-zero. `Send` returns the wrapped error — one log line per nudge cycle. Not retried specially. |
| `cmux` binary missing from `PATH` | `exec.ErrNotFound` surfaces as one log line. Theoretically possible if user breaks PATH while `CMUX_WORKSPACE_ID` is set; not worth special-casing. |
| Nested tmux-inside-cmux | `TmuxSignaler.Detect` returns true first (process-tree match on the agent's immediate host). `DefaultSignalers` order keeps tmux winning. `CmuxSignaler` is not reached. Documented behavior, no code change. |

The "no signaler for pid X" log line that `signalNonWorking` already emits when *all* signalers miss is unchanged — it predates this work and is identical for tmux users.

## Testing

New `internal/signal/cmux_test.go`. Reuses the `fakeRun` pattern from `signal_test.go`. Cases:

- `TestCmuxDetectReturnsTrueWhenWorkspaceEnvSet` — `LookupEnv` returns `"ws-123", true`.
- `TestCmuxDetectReturnsFalseWhenWorkspaceEnvUnset` — `LookupEnv` returns `"", false`.
- `TestCmuxSendFindsSurfaceInOwnWorkspace` — fake `RunCmd` synthesizes a `cmux --json top --processes` payload from a `[]fakeSurface` fixture and records `cmux send` and `cmux send-key` args; asserts both target the matched `--workspace` and `--surface` refs in the correct order.
- `TestCmuxSendErrorsWhenNoSurfaceFound` — synthesized payload contains surfaces whose `tty_process_pids` never include the target pid; assert error message.
- `TestCmuxSendCrossesWorkspaces` — match lives in a workspace different from the one set in `LookupEnv("CMUX_WORKSPACE_ID")`; assert correct `--workspace` flag passed (regression guard for the "default workspace" bug this design dodges).

Updates to existing tests:

- `TestStubSignalersSendNotImplemented` — drop `&signal.CmuxSignaler{}` from the stubs list.
- `TestResolveSignalerReturnsNilWhenNoneMatch` — construct the `CmuxSignaler` with a `LookupEnv` that returns false so `Detect` is false (so the test still verifies "none match").

## Spike (completed during planning)

Captured `cmux --json top --processes` output (and the per-command alternates `list-workspaces`, `list-pane-surfaces`, `tree --all`) inside a live cmux session with 10 workspaces and 19 surfaces. Findings:

- `cmux --json top --processes` is the single-call enumeration source. It emits a JSON object reachable via `.windows[].workspaces[].panes[].surfaces[]` where each terminal surface entry includes `ref`, `tty`, and `tty_process_pids` (descendants of the surface's tty). All 19 captured surfaces had non-null `tty` and non-empty `tty_process_pids`.
- `cmux list-pane-surfaces` returns only the *caller's* pane (one surface in our capture), not a per-workspace listing — useless for the cross-workspace need.
- `cmux tree --all` enumerates all surfaces but exposes only `tty`, not pids; would require a separate tty→pid resolution step.

Decision: use `cmux --json top --processes`. Pid-keyed map of `surfaceLoc` is a direct flatten of the JSON; no fallback path required.

## Non-goals (Phase 2)

The following user-requested items are deferred to a separate spec and explicitly out of scope here:

- `cmux set-status` to expose caffeinate / auto-resume toggle state and the "paused — waiting for 5h reset" state.
- `cmux set-progress` driven by 5h-window utilization.
- `cmux notify` fired when the window resets and a nudge is dispatched.

These features depend on lifecycle hooks in `internal/tui/` (toggle handlers, poll callbacks) and on contention rules with other sidebar producers, and warrant their own design.

## Consequences

### Positive

- Cmux users get the same auto-resume behavior tmux users already have.
- Out-of-cmux runs remain silent — no noisy errors when the user forgets they're not in cmux.
- Test surface area matches existing tmux tests; reviewer mental load is low.
- No new dependencies, no Go-level socket client, no JSON-RPC plumbing.

### Negative

- Three `cmux` subprocesses per nudge fire (`--json top --processes` enumerate, then `send` + `send-key`), once per ~5h. Trivial in absolute terms but not free.
- Parser is coupled to the `cmux --json top --processes` schema, which is not formally contracted (per cmux's own `cli-contract.md`). Schema additions are non-breaking thanks to `encoding/json` ignoring unknown keys; removals or renames of `windows`/`workspaces`/`panes`/`surfaces`/`tty_process_pids`/`type`/`tty` would break enumeration. Mitigated by the cross-workspace regression test, which fails if the parser stops yielding matched pids.
- Detection ignores `pid`, which is asymmetric with `TmuxSignaler.Detect`. Justified by cmux's global socket but worth noting for future maintainers.

### Neutral

- Tmux-inside-cmux precedence is by `DefaultSignalers` order, not by an explicit policy. Future signalers must respect this when added.

## Alternatives Considered

### JSON-RPC over the unix socket (Approach B in brainstorming)

Lower per-call latency, no `exec` overhead. Rejected:

- Adds ~150 LOC of JSON-RPC client plus auth handling (`CMUX_SOCKET_PASSWORD` / `--password`).
- Requires new test scaffolding (fake socket server) that has no parallel in the codebase.
- Auto-resume fires once per 5h block. The latency budget is essentially unbounded.

If Phase 2 demands frequent sidebar updates (per-poll progress recalculation), the socket transport can be introduced then without touching the Phase 1 signaler.

### Signaler-only own-surface fallback

Only nudge agents that share `claude-agents-tui`'s own surface (use `CMUX_SURFACE_ID` as the implicit target). Rejected: defeats the purpose. Real users run each agent in its own surface; the whole motivation for the signaler is cross-surface reach.

## Related Decisions

- See also: `docs/superpowers/specs/2026-05-07-fix-auto-resume-design.md` — establishes the auto-resume fire path that this spec plugs into.
