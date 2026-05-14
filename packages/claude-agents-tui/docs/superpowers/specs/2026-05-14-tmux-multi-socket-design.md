# TmuxSignaler Multi-Socket + Help-Popup Log Path (Phase 3)

**Status**: Draft
**Date**: 2026-05-14
**Phase**: 3 (follow-up to Phase 1 `CmuxSignaler` and Phase 2 `cmuxstatus` sidebar)
**Bd issue**: Filed during Plan Task 1

## Context

Two issues remain after Phases 1 and 2 of the cmux integration work:

1. **`TmuxSignaler.Send` fails on non-default tmux sockets.** Today it shells out to `tmux list-panes -a -F ...` without `-L`, which only enumerates panes on tmux's default socket. Users who run multiple tmux servers (or any server started with `-L <name>`) get a "no pane found for pid X" failure for sessions hosted on those servers — even though their pids' ancestry walks correctly identify a tmux ancestor.

   Surfaced during the Phase 1 smoke test: the user has two tmux servers on the machine (pids 52795 and 36990) running with `-L <name>`. `TmuxSignaler.Detect` correctly returned true for an agent under one of these (post-Phase-1.5 exact-match fix), but `Send` then failed because `tmux list-panes -a` (default socket) didn't see the pane.

2. **The signal-error log path is undiscoverable.** Phase 1.5 routed `signalNonWorking` errors to `<cacheDir>/signal-errors.log` so they no longer corrupt the TUI display. The user has no way to discover that path from inside the running TUI. The `?` help modal lists keybindings but says nothing about where errors go.

A previously-considered in-TUI log viewer is explicitly out of scope (user direction: "drop the tui log viewer, the log file exists, so we can just check the file"). The minimum needed is a single line in the help modal showing the resolved path.

## Decision

Two changes in one phase, sharing one bd issue:

### 1. `TmuxSignaler` becomes multi-socket-aware, mirroring `CmuxSignaler` Phase 1.5

- **Discovery via `ps`, not filesystem glob.** macOS `TMPDIR` varies (`/var/folders/.../T/`) and tmux on some Nix builds places sockets elsewhere. `ps -A -o pid,comm,args` reliably surfaces every running tmux server; parsing argv for `-L <name>` (defaulting to `default` when absent) yields the socket name.
- **Detect becomes pid-aware** by enumerating panes across every discovered socket. Returns true only when the pid's process ancestry walk finds a known pane's shell pid. The current comm-based-only Detect (Phase 1.5 exact match on `"tmux"`) is replaced.
- **Send reuses the cached enumeration**, mirroring the `CmuxSignaler` 2-second TTL cache pattern.
- **Per-socket subprocess calls** thread `-L <name>` through every `tmux` invocation. No `-S <path>` form — the `-L` name is enough since tmux resolves the path internally.

### 2. Help modal gains one bottom line

Render a single line at the bottom of the `?` modal's body:

```
Signal errors logged to: <absolute path>
```

Where `<absolute path>` is the resolved `cacheDir + "/signal-errors.log"` (computed at render time, so the path stays accurate when `cacheDir` is overridden). When `cacheDir` is empty (rare — only some headless test paths), the line is omitted entirely.

## Architecture

### Type

```go
type TmuxSignaler struct {
    RunCmd func(ctx context.Context, name string, args ...string) ([]byte, error)
    // No LookupEnv needed; the comm-based ancestor check uses RunCmd ("ps").

    cacheMu   sync.Mutex
    cacheAt   time.Time
    cacheLocs map[int]paneLoc
    cacheErr  error
}

type paneLoc struct {
    socketName string // "-L" argument; "default" if the server has no -L
    paneID     string // "<session>:<window>.<pane>" form
}
```

Same shape as the post-Phase-1.5 `CmuxSignaler`. Field names match across the two types where the concepts are the same; this keeps the cognitive load low when reading both files side by side.

### Detect

```
1. Acquire cached pane map (refresh if stale).
2. Walk pid's process ancestry (existing `ps -o ppid=,comm= -p <pid>` loop).
3. At each ancestor, check the pane map.
4. Return true on first match; false if walk reaches pid<1 or a `seen` cycle.
```

The cache hides the per-pid enumeration cost behind a 2s TTL — N non-Working sessions in `signalNonWorking` produce one `ps` survey, not N.

### Send

```
1. Acquire cached pane map.
2. Walk pid's ancestry to find a matching pane.
3. Issue: tmux -L <socketName> send-keys -t <paneID> <text> Enter
```

Errors from `tmux send-keys` log via the existing `signalLog` → `signal-errors.log` path (Phase 1.5). No retry, no fallback to another socket — the discovery step already picked the right one.

### Cache helper

```go
const tmuxCacheTTL = 2 * time.Second

func (t *TmuxSignaler) cachedPanes() (map[int]paneLoc, error) {
    t.cacheMu.Lock()
    defer t.cacheMu.Unlock()
    if t.cacheAt != (time.Time{}) && time.Since(t.cacheAt) < tmuxCacheTTL {
        return t.cacheLocs, t.cacheErr
    }
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    locs, err := t.enumeratePanes(ctx)
    t.cacheLocs, t.cacheErr, t.cacheAt = locs, err, time.Now()
    return locs, err
}
```

### Enumeration

```
1. Run `ps -A -o pid,comm,args`. Filter rows where comm == "tmux".
   (Server processes have comm "tmux"; transient `tmux attach` clients also
   appear as "tmux" but their argv lacks "new-session"/"start-server"/etc. We
   accept the over-listing — duplicate -L names dedupe to the same socket, and
   talking to an already-running server via `-L <name>` is the same operation.)
2. For each row, parse argv for the `-L <name>` token. If absent, name is "default".
3. Dedupe socket names.
4. For each name, run:
     tmux -L <name> list-panes -a -F "#{pane_pid} #{session_name}:#{window_index}.#{pane_index}"
   Skip names where the call errors (e.g. tmux server died between ps and the call).
5. Build map[int]paneLoc keyed by pane_pid.
```

### Failure handling

| Condition | Behavior |
| --- | --- |
| No tmux servers exist | `ps` yields no rows, map is empty, Detect=false, signaler is silent. |
| One server dies between discovery and list-panes | List-panes errors; that one socket's panes are skipped, others still enumerated. No log entry — partial discovery is the normal case for transient process churn. |
| One server dies between list-panes and send-keys | `tmux send-keys -L <name>` errors; one line in `signal-errors.log`; pid won't be retried this poll cycle. |
| Pid has tmux ancestry but no enclosing pane | Detect=false (pid not in any pane's ancestry). ResolveSignaler falls through to subsequent signalers (cmux, ghostty, etc.). |
| Argv parsing finds no `-L` token | Defaults to socket name `"default"`. |

### Test surface (`internal/signal/`)

New tests use the existing `fakeRun` pattern:

- `TestTmuxDetectReturnsTrueOnlyWhenPidInPane` — pid is in one socket's panes; Detect=true.
- `TestTmuxDetectReturnsFalseWhenNoPaneMatches` — pid has a tmux ancestor but no pane lists its shell pid in any socket.
- `TestTmuxSendFindsPaneOnNonDefaultSocket` — two sockets (`default` and `gc`); pid is on `gc`; send-keys is invoked with `-L gc`.
- `TestTmuxSendErrorsWhenNoPaneFound` — keep; assert error message format unchanged.
- `TestTmuxEnumerationSkipsDeadSocket` — `tmux -L deadsock list-panes` returns error; other sockets still enumerated; no fan-out error logging.
- `TestTmuxDetectIsCachedAcrossCalls` — count `RunCmd` invocations across 5 sequential Detect calls; expect one ps + one list-panes per discovered socket, not 5×.

Removed/superseded:

- `TestTmuxDetectReturnsTrueWhenTmuxIsAncestor` (Phase 1 era) — superseded by `TestTmuxDetectReturnsTrueOnlyWhenPidInPane` (stricter assertion).
- `TestTmuxDetectReturnsFalseForLookalikeComm` (Phase 1.5) — keep, still correct: a `tmuxinator` ancestor with no real tmux server should yield Detect=false because no pane match exists.

### Help-modal change

The help modal is built by `render.HelpModal(rows, width, height, scroll)` in `internal/render/modals.go:157`, which wraps `render.Modal(title, rows, width, height, scroll)`. Today's Modal renders the title, the rows, and a built-in `footerHint` (e.g. close-hint text). Add an optional caller-supplied footer line that renders between the last row and the built-in footerHint.

API change (smallest possible — additive parameter; preserves call sites that don't care):

```go
// HelpModal is a thin wrapper over Modal with a "Help — keybindings" title.
// extraFooter renders as one centered line below the keybindings table and
// above the built-in close-hint. Empty string disables it.
func HelpModal(rows []HelpRow, extraFooter string, width, height, scroll int) string
```

`Modal` similarly grows an `extraFooter string` parameter (positioned the same way). Only `HelpModal` consumers pass a non-empty value; `LegendModal` and other callers pass `""`.

In `internal/tui/view.go:120`, change the call to:

```go
extra := ""
if m.errorLogger != nil && m.errorLogger.CacheDir != "" {
    extra = "Signal errors logged to: " + filepath.Join(m.errorLogger.CacheDir, "signal-errors.log")
}
return render.HelpModal(bindingsToHelpRows(), extra, m.width, m.height, m.modalScrollOffset)
```

Add `"path/filepath"` import to `view.go`.

Tests:

- `TestHelpModalRendersExtraFooter` (in `internal/render/modals_test.go` — adjacent to the renderer): pass a non-empty `extraFooter`; assert the output contains it.
- `TestHelpModalOmitsExtraFooterWhenEmpty`: pass `""`; assert no extra footer line.
- `TestViewHelpModalIncludesSignalLogPathWhenCacheDirSet` (in `internal/tui/view_test.go`): construct a Model with a populated `errorLogger.CacheDir`, render with `ModalKind == ModalHelp`, assert the rendered string contains the expected path.

The Legend modal (and any other Modal call site) gets the `""` argument with no behavior change.

## Files

Modified:

- `internal/signal/tmux.go` — full rewrite of Detect/Send + new helpers (`cachedPanes`, `enumeratePanes`, `paneLoc`). The existing `findPaneForPID` shape is preserved as an internal walker.
- `internal/signal/signal_test.go` — replace Tmux tests with the new set above. The `fakeRun` helper extends to support multiple `tmux -L <name> list-panes` invocations.
- `internal/tui/view.go` (or `internal/render/help.go` — whichever owns the help modal) — append the one-line log-path footer.
- `internal/tui/view_test.go` (or equivalent) — add the two help-modal tests.

No changes to:

- `internal/signal/cmux.go`, `internal/cmuxstatus/`, `internal/signal/signal.go` (no interface change).
- `internal/poller/`, `cmd/`, `internal/aggregate/`.

## Out of scope

Following user direction:

- **In-TUI log viewer.** Dropped. The log file exists; users `tail` it directly.
- **`-S <socket-path>` form.** `-L <name>` is sufficient for all tmux invocations.
- **TmuxSignaler caching that survives across program runs.** 2s TTL is the only persistence.
- **Notifications/sidebar surface for tmux-side failures.** Existing `signal-errors.log` plus the help-modal pointer covers visibility.
- **Refactoring `TmuxSignaler` and `CmuxSignaler` into a common base type.** Tempting after Phase 1.5 — both share a cache pattern — but the discovery mechanics differ enough (ps + argv parsing vs JSON-RPC) that a shared base would be a leaky abstraction. Revisit if a third signaler shows up.

## Consequences

### Positive

- Multi-socket users get a working signaler; the user's smoke-test failures around pid 95352 / 71003 disappear.
- Detect/Send asymmetry between Tmux and Cmux signalers eliminated — both are now pid-aware with TTL caching.
- Help-modal log pointer makes the existing log file discoverable without leaving the TUI.

### Negative

- TmuxSignaler.Detect now costs one `ps` survey plus N tmux subprocess calls (one per discovered socket) per cache TTL window. For users with one server this is the same as today plus one extra subprocess; for users with multiple servers it scales linearly. Cache hides the cost across the per-poll signalNonWorking loop.
- `ps -A -o pid,comm,args` returns every process on the system; argv strings can be huge (the user's gascity tmux had ~3 KB of `-e KEY=VALUE` args). Parsing is byte-level cheap but the read is non-trivial. Cache amortizes.
- Argv parsing for `-L` is heuristic. A pathological tmux invocation that puts `-L` inside a quoted arg or relies on `--` after `-L` could mis-parse. We accept this for v1; tmux's documented CLI doesn't allow that shape.

### Neutral

- The post-Phase-1.5 `TestTmuxDetectReturnsFalseForLookalikeComm` keeps passing for a different reason: under the new pid-aware Detect, a `tmuxinator` ancestor still yields false because `tmuxinator` is not a tmux server and thus has no panes in the discovered map.

## Alternatives Considered

### Filesystem glob for sockets

`ls $TMPDIR/tmux-$UID/` plus `ls /tmp/tmux-$UID/`. Simpler but unreliable: macOS `TMPDIR` varies, the user's `/tmp/` had non-tmux sockets (`/tmp/gc`, `/tmp/gt-*` from gascity), and Nix-wrapped tmux can land sockets elsewhere. The `ps`-based approach is the source of truth.

### `lsof` for socket bindings

`lsof -U -p <tmux-pid>` would surface the exact socket file. Cleanest discovery, but `lsof` is slow (tens of ms) and not always on PATH. Skip.

### Try-each-socket-and-best-effort Send

Keep Detect comm-based; Send iterates discovered sockets, attempts send-keys against each, stops on first success. Avoids per-Detect cost but means a failed nudge produces one error per socket (N log lines). Worse UX. Rejected.

### Single `tmux info`-style endpoint

There is no tmux command that lists "all running servers". Each `tmux` invocation talks to exactly one server. So enumeration via ps + per-server queries is inherent.

## Related Decisions

- See also: `docs/superpowers/specs/2026-05-14-cmux-signaler-design.md` — Phase 1 established the `Signaler` interface and the `RunCmd` injection seam this work reuses.
- Phase 1.5 (commits `5de820c`, `d60aad9`, `1f893ef`) introduced the `errorLogger` and the comm-exact-match Detect tightening — both are direct ancestors of this work.
