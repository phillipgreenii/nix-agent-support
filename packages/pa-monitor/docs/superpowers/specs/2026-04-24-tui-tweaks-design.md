# claude-agents-tui: Tweaks Batch 2

**Date:** 2026-04-24

## Summary

Nine targeted improvements to `claude-agents-tui`: PID-correlated session lifetime, startup UX for ccusage, XML stripping in prompt display, awaiting-input indicator, directory stat bugs, column alignment, and branch info.

---

## 1. Session lifetime — PID correlation

**Problem:** Sessions go Dormant based solely on transcript mtime. A running agent that is idle (waiting, blocked on user input) stops writing the transcript, so its mtime goes stale and the session appears Dormant even though the process is alive.

**Constraint:** A single Claude Code PID can host multiple session IDs over its lifetime (user runs `/resume`, switches conversations). Naive PID-alive → clamp-to-Idle is wrong: the PID being alive does not prove _this_ session is active.

**Solution:** In `poller.go`, after resolving transcript mtimes for all sessions, build a `pidActiveSID` map — for each PID, record the session ID whose `TranscriptMTime` is most recent. Only clamp `Dormant → Idle` for the session that "owns" the PID (i.e., has the freshest transcript for that PID). Sessions with stale transcripts that share a PID with a fresher session remain Dormant.

```
pidActiveSID[pid] = sessionID with max TranscriptMTime among sessions sharing that pid
if s.Status == Dormant && PidAlive(s.PID) && pidActiveSID[s.PID] == s.SessionID {
    s.Status = Idle
}
```

**Files:** `internal/poller/poller.go`

---

## 2. Startup "ccusage not found" message

**Problem:** `Tree.ActiveBlock == nil` is ambiguous: it means either "first poll not yet run" or "ccusage unavailable." The header shows the "not found on PATH" error for both, causing a false alarm on every startup.

**Solution:** Add two fields to `Tree`:

```go
CCUsageProbed bool  // true once first ccusage attempt has run
CCUsageErr    error // non-nil if exec failed (i.e., not on PATH or other error)
```

The poller sets `CCUsageProbed = true` and captures the error after each `CCUsageFn` call. Header render logic:

| `CCUsageProbed` | `CCUsageErr` | `ActiveBlock` | Display                                     |
| --------------- | ------------ | ------------- | ------------------------------------------- |
| false           | —            | —             | _(blank / "loading…")_                      |
| true            | non-nil      | —             | "unavailable — `ccusage` not found on PATH" |
| true            | nil          | nil           | "no active block"                           |
| true            | nil          | non-nil       | _(current bar rendering)_                   |

**Files:** `internal/aggregate/tree.go`, `internal/poller/poller.go`, `internal/render/header.go`

---

## 3. XML in displayed prompts

**Problem:** `local-command-caveat` is not in `envelopeTagNames`, so prompts that include tool-output caveat blocks show raw XML.

**Solution:**

1. Add `"local-command-caveat"` to `envelopeTagNames` in `internal/transcript/first_prompt.go`.
2. Defensive fallback: if after stripping the result still starts with `<`, `FirstPrompt` advances to the next user text event and retries, up to the end of the file. This handles any future unknown tags gracefully.

**Files:** `internal/transcript/first_prompt.go`

---

## 4. Awaiting-user-input indicator

**Problem:** No visual distinction between a session that is idle (agent finished its turn) and one that is actively waiting for the human to respond (agent issued `AskUserQuestion`).

**Solution:** Add `AwaitingInput bool` to `SessionEnrichment`. New transcript function `AwaitingInput(path string) (bool, error)` scans from the end: if the last `assistant` event contains a `tool_use` block with `name == "AskUserQuestion"` and no subsequent `tool_result` with the same ID, return `true`.

**Display:** Replace the `○` idle symbol with `?` for sessions where `AwaitingInput == true`. Sessions keep their position (sorted by `StartedAt` descending, newest at top — see §9). No reordering on state change.

**Legend update:** Add `? awaiting input` to the bottom legend line.

**Files:** `internal/transcript/subagents.go` (or new `internal/transcript/awaiting.go`), `internal/aggregate/tree.go`, `internal/render/tree.go`, `internal/poller/poller.go`

---

## 6. Multiple models per session

`LatestContext` already returns the model from the last assistant event, which is the current active model. If a session switches models mid-conversation, the display reflects the current model. **No change needed.**

---

## 7. Directory token totals — bug + related unpopulated fields

**Root cause of directory 0 tok:** `SessionEnrichment.SessionTokens` is never set in the poller. `Directory.TotalTokens` sums it, so directories always show 0. `CostUSD` per session is also 0 as a consequence (derived from `SessionTokens / grandTokens`).

**Full audit — unpopulated fields in `SessionEnrichment`:**

| Field           | Status    | Fix                                                                                  |
| --------------- | --------- | ------------------------------------------------------------------------------------ |
| `SessionTokens` | never set | Sum `output_tokens` across all assistant events in transcript                        |
| `SubshellCount` | never set | `subshell.Counter` is fully implemented; call `Counter.Count(s.PID)` in poller loop  |
| `BurnRateShort` | never set | Poller must maintain per-session `burnrate.Buffer` (short window) across poll cycles |
| `BurnRateLong`  | never set | Same, long window                                                                    |
| `CostUSD`       | always 0  | Derived — fixed automatically once `SessionTokens` is correct                        |

**Fixes:**

- `LatestContext` (already scans all assistant events) is extended to also accumulate `output_tokens` into a new `TotalTokens` field on `ContextSnapshot`. Poller sets `SessionTokens = ctxSnap.TotalTokens`.
- Poller calls `subshell.Counter.Count(s.PID)` and sets `SubshellCount`.
- `Poller` struct gains `burnShort map[string]*burnrate.Buffer` and `burnLong map[string]*burnrate.Buffer` (keyed by session ID). On each poll, add a sample for each session (total output tokens at current time), compute rate, set `BurnRateShort` / `BurnRateLong`. Stale entries (session no longer in results) are pruned each cycle.

**Test gap:** `TestSnapshotProducesTree` only asserts the tree is non-nil. Add `TestSnapshotEnrichmentFields` using the existing transcript fixture to assert `SessionTokens > 0`, `ContextTokens > 0`, `Model != ""`, `SubagentCount` is an integer. Burn rate can be tested with a two-snapshot sequence.

**Files:** `internal/transcript/context.go`, `internal/poller/poller.go`, `internal/poller/poller_test.go`

---

## 8. Directory stats right-alignment

**Problem:** Directory header rows use plain `fmt.Sprintf("%s   %s\n", d.Path, rollup)` — no column alignment with the session rows beneath them.

**Solution:** Apply the same column layout as session rows. Right-align the rollup into a block of width `statsBlockCols` (41 chars) using lipgloss. Left-pad the path into `labelStyle(termWidth)` width + `prefixCols` (3) chars. Result: token/cost totals sit flush against the right edge, matching session stat columns.

The directory header does not have model/pct/bar/burn columns, so those columns render as blank; only the rightmost "amount" column (tokens or cost) is filled.

**Files:** `internal/render/tree.go`

---

## 9. Session sort order

**Problem:** Sessions within a directory have undefined order.

**Solution:** In `aggregate.Build()`, after grouping sessions into directories, sort each directory's `Sessions` slice by `Session.StartedAt` descending (newest first). `Session.StartedAt` is populated from `rawSession.StartedAt` (millisecond epoch). This order is stable across polls — state changes (working/idle/awaiting) do not reorder sessions.

**Files:** `internal/aggregate/aggregate.go`

---

## 10. Branch information

**Problem:** Git branch info was present previously; no longer shown.

**Solution:** Add `Branch string` to `session.Session`. New helper `session.GitBranch(cwd string) string` reads `.git/HEAD` directly (no subprocess):

- `ref: refs/heads/<name>` → return `<name>`
- Raw SHA (detached HEAD) → return first 7 chars
- File missing or unreadable → return `""`

Poller calls `session.GitBranch(s.Cwd)` and sets `s.Branch`. Tree render appends `[<branch>]` after the session label in dim style, only when `Branch != ""`.

**Files:** `internal/session/session.go` (add field), new `internal/session/git.go`, `internal/poller/poller.go`, `internal/render/tree.go`

---

## Affected files

| File                                  | Changes                                                                        |
| ------------------------------------- | ------------------------------------------------------------------------------ |
| `internal/poller/poller.go`           | PID clamp, ccusage state, subshell count, burn buffers, branch, awaiting-input |
| `internal/poller/poller_test.go`      | `TestSnapshotEnrichmentFields`                                                 |
| `internal/aggregate/tree.go`          | `CCUsageProbed`, `CCUsageErr` fields                                           |
| `internal/aggregate/aggregate.go`     | Sort sessions by `StartedAt` desc                                              |
| `internal/transcript/context.go`      | Add `TotalTokens` to `ContextSnapshot`                                         |
| `internal/transcript/first_prompt.go` | Add `local-command-caveat`; retry-on-XML fallback                              |
| `internal/transcript/awaiting.go`     | New: `AwaitingInput()` function                                                |
| `internal/render/header.go`           | Three-state ccusage display                                                    |
| `internal/render/tree.go`             | `?` symbol, right-aligned dir stats, branch display                            |
| `internal/session/session.go`         | Add `Branch string` field                                                      |
| `internal/session/git.go`             | New: `GitBranch()` helper                                                      |
