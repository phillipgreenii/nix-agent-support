# pa-monitor Disrupt Detection Gaps Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the real disrupt-detection gaps behind bead pg2-lpxq — lock in stream-idle-timeout detection, add the unhandled `model_not_found` kind, and surface subagent-scoped disrupts — without adding the redundant text-matching the bead originally proposed.

**Architecture:** Disrupt classification lives in `internal/core/transcript/{disrupt.go,snapshot.go}` and keys off the `error` field of synthetic `isApiErrorMessage` assistant events, gated by a closed allowlist of `ErrorKind` values. Detection flows `Scan → Snapshot.LastError → poller → aggregate.SessionView.LastError → render (⚠/✗ glyph) + nudger`. This plan extends the allowlist and adds a subagent-transcript scan in the poller, surfaced through the existing `LastError` plumbing with one new `FromSubagent` flag.

**Tech Stack:** Go 1.x, protobuf (protoc via nix devShell), table-driven Go tests. Module path `github.com/phillipgreenii/pa-monitor`; all commands run from `packages/pa-monitor` inside the nix devShell (`nix develop -c <cmd>` if not already in it).

---

## Background: why this plan, not the bead-as-written

Research (2026-06-16) against all 71 real `isApiErrorMessage` events in `~/.claude/projects` established:

- **7/7** real `"API Error: Stream idle timeout - partial response received"` events carry `error:"unknown"`, `isApiErrorMessage:true`. `unknown` is already in the allowlist (`disrupt.go:18`), already `IsRetryable()` (`disrupt.go:27`), and already renders as ⚠ (`internal/render/tree.go:209`). **No text-matching is needed** — the bead's ACs #2/#3/#5 text logic would be dead code.
- The observed "misses" trace to: (a) legitimate resume — e.g. transcript `eab2662f` line 733 is the error, line 737 is the user typing `"retry"`, so `IsTerminal=false` is correct; (b) **subagent blind spot** — `Scan()` only reads the main session JSONL; transcript `f138a858`'s idle-timeout exists only in `subagents/agent-a505295….jsonl` (main has 0).
- Discovered unrelated gap: `error:"model_not_found"` (2 real events, text `"There's an issue with the selected model (claude-fable-5). It may not exist…"`) is **not** in the allowlist → silently dropped, never surfaced.

This plan therefore delivers: a regression guard for idle-timeout (Task 1), the `model_not_found` kind (Task 2), and subagent disrupt surfacing for visibility only — no auto-nudge of the parent (Task 3). Task 4 updates the bead and docs.

## File Structure

- `internal/core/transcript/disrupt.go` — add `ErrModelNotFound`; add it to the allowlist switch. Add `FromSubagent` field to `ErrorRecord`. Add `LastSubagentError` helper.
- `internal/core/transcript/snapshot.go` — add `ErrModelNotFound` to the `Scan` allowlist switch.
- `internal/core/transcript/disrupt_test.go` — idle-timeout regression test; `model_not_found` cases; `LastSubagentError` test.
- `internal/core/transcript/snapshot_test.go` — snapshot-level idle-timeout + `model_not_found` regression test.
- `internal/proto/pa_monitor.proto` + regenerated `*.pb.go` — `from_subagent` field on `ApiError`.
- `internal/proto/translate.go`, `internal/proto/from_proto.go` — map `FromSubagent` ↔ `from_subagent`.
- `internal/core/poller/poller.go` — call `LastSubagentError` and fold a terminal subagent error into `snap.LastError` when the main session has none.
- `internal/daemon/nudger/disrupt.go` — guard: never auto-nudge a `FromSubagent` error.
- `internal/tui/details.go` — annotate the Last-error block with "(in subagent)" when `FromSubagent`.

---

## Task 1: Regression guard — stream-idle-timeout is detected as retryable unknown

**Files:**

- Test: `internal/core/transcript/disrupt_test.go`
- Test: `internal/core/transcript/snapshot_test.go`

This task adds _only_ tests. It must pass against the current code (it proves the existing behavior and guards against future allowlist regressions). Exact text is the real transcript shape.

- [ ] **Step 1: Add the disrupt-level regression test**

Append to `internal/core/transcript/disrupt_test.go`:

```go
// TestLastAPIErrorDetectsStreamIdleTimeout guards bead pg2-lpxq: Claude Code
// emits "API Error: Stream idle timeout - partial response received" as a
// synthetic isApiErrorMessage with error="unknown" (verified against real
// transcripts, 2026-06-16). It must classify as a terminal, retryable unknown
// disrupt with no text-matching special-case.
func TestLastAPIErrorDetectsStreamIdleTimeout(t *testing.T) {
	ts := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC)
	const text = "API Error: Stream idle timeout - partial response received"
	path := t.TempDir() + "/t.jsonl"
	if err := writeTestFile(path, apiErrorEvent(ts, ErrUnknown, text)+"\n"); err != nil {
		t.Fatal(err)
	}
	got, err := LastAPIError(path)
	if err != nil {
		t.Fatalf("LastAPIError err = %v, want nil", err)
	}
	if got.Kind != ErrUnknown {
		t.Errorf("Kind = %q, want %q", got.Kind, ErrUnknown)
	}
	if got.Text != text {
		t.Errorf("Text = %q, want %q", got.Text, text)
	}
	if !got.IsTerminal {
		t.Error("IsTerminal = false, want true (no event follows)")
	}
	if !got.IsRetryable {
		t.Error("IsRetryable = false, want true (unknown is retryable)")
	}
}
```

- [ ] **Step 2: Add the snapshot-level regression test**

Append to `internal/core/transcript/snapshot_test.go`:

```go
// TestScanSurfacesStreamIdleTimeout mirrors TestLastAPIErrorDetectsStreamIdleTimeout
// at the Snapshot level: Scan must populate LastError for the unknown-kind
// stream-idle-timeout event so it reaches the poller/TUI. (bead pg2-lpxq)
func TestScanSurfacesStreamIdleTimeout(t *testing.T) {
	ts := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC)
	const text = "API Error: Stream idle timeout - partial response received"
	path := t.TempDir() + "/t.jsonl"
	if err := writeTestFile(path, apiErrorEvent(ts, ErrUnknown, text)+"\n"); err != nil {
		t.Fatal(err)
	}
	snap, err := Scan(path)
	if err != nil {
		t.Fatalf("Scan err = %v, want nil", err)
	}
	if snap.LastError == nil {
		t.Fatal("LastError = nil, want populated stream-idle-timeout error")
	}
	if snap.LastError.Kind != ErrUnknown || !snap.LastError.IsRetryable || !snap.LastError.IsTerminal {
		t.Errorf("LastError = %+v, want unknown/retryable/terminal", snap.LastError)
	}
}
```

> Note: `apiErrorEvent` and `writeTestFile` are existing package test helpers (`disrupt_test.go:32`, `first_prompt_test.go:9`). No new helper needed.

- [ ] **Step 3: Run the new tests to verify they PASS against current code**

Run: `cd packages/pa-monitor && go test ./internal/core/transcript/ -run 'StreamIdleTimeout' -v`
Expected: PASS (both tests). If either fails, the allowlist or pipeline regressed — stop and investigate before continuing.

- [ ] **Step 4: Commit**

```bash
git add packages/pa-monitor/internal/core/transcript/disrupt_test.go \
        packages/pa-monitor/internal/core/transcript/snapshot_test.go
git commit -m "test(pa-monitor): regression guard for stream-idle-timeout=unknown disrupt (pg2-lpxq)"
```

---

## Task 2: Add the `model_not_found` error kind to the allowlist

**Files:**

- Modify: `internal/core/transcript/disrupt.go`
- Modify: `internal/core/transcript/snapshot.go`
- Test: `internal/core/transcript/disrupt_test.go`

`model_not_found` is a configuration error (wrong/unavailable model) — **not** retryable; it must surface to a human as ✗, like `authentication_failed`.

- [ ] **Step 1: Write failing tests for the new kind**

Append to `internal/core/transcript/disrupt_test.go`:

```go
// TestLastAPIErrorDetectsModelNotFound covers the model_not_found kind, which
// Claude Code emits when the selected model is unavailable (verified against
// real transcripts, 2026-06-16). It is non-retryable (human must fix the model).
func TestLastAPIErrorDetectsModelNotFound(t *testing.T) {
	ts := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC)
	const text = "There's an issue with the selected model (claude-fable-5). It may not exist or you may not have access."
	path := t.TempDir() + "/t.jsonl"
	if err := writeTestFile(path, apiErrorEvent(ts, ErrModelNotFound, text)+"\n"); err != nil {
		t.Fatal(err)
	}
	got, err := LastAPIError(path)
	if err != nil {
		t.Fatalf("LastAPIError err = %v, want nil", err)
	}
	if got.Kind != ErrModelNotFound {
		t.Errorf("Kind = %q, want %q", got.Kind, ErrModelNotFound)
	}
	if got.IsRetryable {
		t.Error("IsRetryable = true, want false (model_not_found needs human fix)")
	}
	if !got.IsTerminal {
		t.Error("IsTerminal = false, want true")
	}
}
```

Add a `model_not_found` row to the existing `TestErrorKindIsRetryable` table (after the `ErrAuthFailed` row):

```go
		{ErrModelNotFound, false},
```

- [ ] **Step 2: Run the tests to verify they FAIL**

Run: `cd packages/pa-monitor && go test ./internal/core/transcript/ -run 'ModelNotFound|IsRetryable' -v`
Expected: FAIL to compile with `undefined: ErrModelNotFound`.

- [ ] **Step 3: Add the constant and allowlist entries**

In `internal/core/transcript/disrupt.go`, add to the `const (...)` block (after `ErrAuthFailed`):

```go
	ErrModelNotFound  ErrorKind = "model_not_found"
```

In the same file, extend the allowlist switch in `LastAPIError` (currently `disrupt.go:111`):

```go
		case ErrRateLimit, ErrUnknown, ErrServerError, ErrInvalidRequest, ErrAuthFailed, ErrModelNotFound:
```

In `internal/core/transcript/snapshot.go`, extend the matching allowlist switch in `Scan` (currently `snapshot.go:150`):

```go
			case ErrRateLimit, ErrUnknown, ErrServerError, ErrInvalidRequest, ErrAuthFailed, ErrModelNotFound:
```

> `IsRetryable()` (`disrupt.go:27`) needs no change: it returns true only for `ErrUnknown`/`ErrServerError`, so `model_not_found` correctly falls through to `false`.

- [ ] **Step 4: Run the tests to verify they PASS**

Run: `cd packages/pa-monitor && go test ./internal/core/transcript/ -run 'ModelNotFound|IsRetryable' -v`
Expected: PASS.

- [ ] **Step 5: Run the whole transcript package to confirm no regression**

Run: `cd packages/pa-monitor && go test ./internal/core/transcript/`
Expected: `ok  github.com/phillipgreenii/pa-monitor/internal/core/transcript`

- [ ] **Step 6: Commit**

```bash
git add packages/pa-monitor/internal/core/transcript/disrupt.go \
        packages/pa-monitor/internal/core/transcript/snapshot.go \
        packages/pa-monitor/internal/core/transcript/disrupt_test.go
git commit -m "feat(pa-monitor): classify model_not_found as a non-retryable disrupt (pg2-lpxq)"
```

---

## Task 3: Surface subagent-scoped disrupts (visibility only, no auto-nudge)

**Files:**

- Modify: `internal/core/transcript/disrupt.go` (add `FromSubagent` field + `LastSubagentError`)
- Test: `internal/core/transcript/disrupt_test.go`
- Modify: `internal/proto/pa_monitor.proto` (+ regenerate `*.pb.go`)
- Modify: `internal/proto/translate.go`, `internal/proto/from_proto.go`
- Modify: `internal/core/poller/poller.go`
- Modify: `internal/daemon/nudger/disrupt.go`
- Modify: `internal/tui/details.go`

**Design decision (baked in):** A subagent's terminal disrupt is surfaced as the _parent session's_ `LastError` **only when the parent's own main transcript has no terminal error**, and is tagged `FromSubagent=true`. The nudger never auto-nudges a `FromSubagent` error (nudging the parent won't revive a dead child); it is shown for operator visibility only. This is the proven gap ("didn't _surface_"); auto-recovery of subagents is out of scope.

- [ ] **Step 1: Add `FromSubagent` to `ErrorRecord` and write the failing helper test**

In `internal/core/transcript/disrupt.go`, add a field to `ErrorRecord` (after `IsContextLimit`):

```go
	// FromSubagent is true when this error was found in a subagent transcript
	// (subagents/agent-*.jsonl) rather than the main session transcript. Such
	// errors are surfaced for visibility but excluded from auto-nudge.
	FromSubagent bool
```

Append to `internal/core/transcript/disrupt_test.go`:

```go
// TestLastSubagentErrorFindsTerminalChildDisrupt covers bead pg2-lpxq's
// subagent blind spot: a stream-idle-timeout that occurs inside a subagent
// lands only in subagents/agent-*.jsonl, which Scan() does not read. The most
// recent *terminal* subagent error is returned with FromSubagent=true.
func TestLastSubagentErrorFindsTerminalChildDisrupt(t *testing.T) {
	dir := t.TempDir()
	mainPath := dir + "/sess.jsonl"
	if err := writeTestFile(mainPath, ""); err != nil {
		t.Fatal(err)
	}
	subDir := dir + "/sess/subagents"
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC)
	const text = "API Error: Stream idle timeout - partial response received"
	if err := writeTestFile(subDir+"/agent-aaaa.jsonl", apiErrorEvent(ts, ErrUnknown, text)+"\n"); err != nil {
		t.Fatal(err)
	}
	got, ok := LastSubagentError(mainPath)
	if !ok {
		t.Fatal("LastSubagentError ok = false, want true")
	}
	if got.Kind != ErrUnknown || !got.IsRetryable || !got.IsTerminal {
		t.Errorf("got %+v, want unknown/retryable/terminal", got)
	}
	if !got.FromSubagent {
		t.Error("FromSubagent = false, want true")
	}
}

// TestLastSubagentErrorIgnoresRecoveredChild verifies a subagent that resumed
// after its error (IsTerminal=false) is not surfaced — the child recovered.
func TestLastSubagentErrorIgnoresRecoveredChild(t *testing.T) {
	dir := t.TempDir()
	mainPath := dir + "/sess.jsonl"
	if err := writeTestFile(mainPath, ""); err != nil {
		t.Fatal(err)
	}
	subDir := dir + "/sess/subagents"
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC)
	body := apiErrorEvent(ts, ErrUnknown, "API Error: Stream idle timeout - partial response received") + "\n" +
		`{"type":"user","message":{"role":"user","content":"continue"}}` + "\n"
	if err := writeTestFile(subDir+"/agent-aaaa.jsonl", body); err != nil {
		t.Fatal(err)
	}
	if _, ok := LastSubagentError(mainPath); ok {
		t.Error("LastSubagentError ok = true, want false (child recovered)")
	}
}

// TestLastSubagentErrorNoSubagentDir returns ok=false when there is no
// subagents directory (the common case).
func TestLastSubagentErrorNoSubagentDir(t *testing.T) {
	dir := t.TempDir()
	mainPath := dir + "/sess.jsonl"
	if err := writeTestFile(mainPath, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := LastSubagentError(mainPath); ok {
		t.Error("LastSubagentError ok = true, want false (no subagents dir)")
	}
}
```

Add **only** `"os"` to `disrupt_test.go`'s import block (currently `fmt`, `testing`, `time`) — used via `os.MkdirAll`. Do **not** add `"path/filepath"`: the test code uses only `os` and string concatenation, and Go rejects unused imports (`path/filepath` belongs in the implementation file `disrupt.go`, Step 3).

- [ ] **Step 2: Run the tests to verify they FAIL**

Run: `cd packages/pa-monitor && go test ./internal/core/transcript/ -run 'LastSubagentError' -v`
Expected: FAIL to compile with `undefined: LastSubagentError`.

- [ ] **Step 3: Implement `LastSubagentError`**

Add to `internal/core/transcript/disrupt.go` (and add `"path/filepath"` to its imports):

```go
// LastSubagentError scans the subagent transcripts of a session for the most
// recent *terminal* api-error and returns it tagged FromSubagent=true. The
// subagent directory is derived from the main transcript path:
// "<dir>/<sessionid>.jsonl" -> "<dir>/<sessionid>/subagents/agent-*.jsonl".
// Only terminal errors are returned (a child that resumed after its error has
// recovered and is not a disrupt). Returns ok=false if the directory is absent
// or no terminal subagent error exists.
//
// Note: for resumed/forked sessions ResolveTranscript may return a transcript
// whose basename differs from the session-id that spawned the subagents, so the
// derived subagents dir won't exist and this returns ok=false. That is graceful
// (no crash) and correct for the common non-resumed case; a resumed session
// with a dead subagent is a known coverage gap, not handled here.
func LastSubagentError(mainTranscriptPath string) (ErrorRecord, bool) {
	if mainTranscriptPath == "" {
		return ErrorRecord{}, false
	}
	subDir := strings.TrimSuffix(mainTranscriptPath, ".jsonl") + "/subagents"
	entries, err := os.ReadDir(subDir)
	if err != nil {
		return ErrorRecord{}, false
	}
	var best ErrorRecord
	found := false
	for _, e := range entries {
		if e.IsDir() ||
			!strings.HasPrefix(e.Name(), "agent-") ||
			filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		rec, err := LastAPIError(filepath.Join(subDir, e.Name()))
		if err != nil || rec.Kind == "" || !rec.IsTerminal {
			continue
		}
		if !found || rec.At.After(best.At) {
			best = rec
			found = true
		}
	}
	if !found {
		return ErrorRecord{}, false
	}
	best.FromSubagent = true
	return best, true
}
```

- [ ] **Step 4: Run the helper tests to verify they PASS**

Run: `cd packages/pa-monitor && go test ./internal/core/transcript/ -run 'LastSubagentError' -v`
Expected: PASS (all three).

- [ ] **Step 5: Commit the transcript-layer change**

```bash
git add packages/pa-monitor/internal/core/transcript/disrupt.go \
        packages/pa-monitor/internal/core/transcript/disrupt_test.go
git commit -m "feat(pa-monitor): LastSubagentError scans subagent transcripts for disrupts (pg2-lpxq)"
```

- [ ] **Step 6: Add the `from_subagent` proto field and regenerate**

In `internal/proto/pa_monitor.proto`, add to `message ApiError`:

```proto
  bool from_subagent = 6;
```

Regenerate (requires the nix devShell for protoc):

Run: `cd packages/pa-monitor && nix develop -c ./scripts/gen-proto.sh`
Expected: `Generated: internal/proto/pa_monitor.pb.go internal/proto/pa_monitor_grpc.pb.go` and `git status` shows `pa_monitor.pb.go` modified with a `FromSubagent` getter.

- [ ] **Step 7: Map `FromSubagent` through the proto translators**

In `internal/proto/translate.go`, in the `out := &ApiError{...}` literal inside `func apiErrorToProto(e *transcript.ErrorRecord)` (the literal at `translate.go:194`), add — note the parameter is `e`, there is **no `sv` in scope** here:

```go
		FromSubagent: e.FromSubagent,
```

(Place it alongside the existing `IsTerminal: e.IsTerminal,` / `IsRetryable: e.IsRetryable,` assignments in the same struct literal.)

In `internal/proto/from_proto.go`, inside `apiErrorFromProto` (the block at `from_proto.go:225`), populate the field back:

```go
		FromSubagent: e.GetFromSubagent(),
```

- [ ] **Step 8: Build proto package to confirm field wiring compiles**

Run: `cd packages/pa-monitor && go build ./internal/proto/...`
Expected: no output (success).

- [ ] **Step 9: Commit the proto change**

```bash
git add packages/pa-monitor/internal/proto/pa_monitor.proto \
        packages/pa-monitor/internal/proto/pa_monitor.pb.go \
        packages/pa-monitor/internal/proto/pa_monitor_grpc.pb.go \
        packages/pa-monitor/internal/proto/translate.go \
        packages/pa-monitor/internal/proto/from_proto.go
git commit -m "feat(pa-monitor): add ApiError.from_subagent and wire proto translators (pg2-lpxq)"
```

- [ ] **Step 10: Fold subagent errors into the poller snapshot**

In `internal/core/poller/poller.go`, immediately after the transcript-cache block sets `snap` (after the `else { snap, _ = transcript.Scan(path) ... }` block closes, around line 135 — i.e. once `snap` is finalized for this session and before `enriched[s.SessionID] = ...` at line 190), insert:

```go
		// Subagent disrupt surfacing: a stream-idle-timeout (or any disrupt)
		// inside a subagent lands only in subagents/agent-*.jsonl, which Scan
		// does not read. When the main session has no terminal error of its
		// own, surface the most recent terminal subagent error (tagged
		// FromSubagent) so it shows in the TUI. Scanned outside the transcript
		// cache because subagent files change independently of the main one.
		if path != "" &&
			(snap.LastError == nil || !snap.LastError.IsTerminal) {
			if subErr, ok := transcript.LastSubagentError(path); ok {
				e := subErr
				snap.LastError = &e
			}
		}
```

> **Gate — do NOT add `snap.SubagentCount > 0`.** `Scan` derives `SubagentCount` from `Task` tool_use events only (`snapshot.go:176`), but Claude Code spawns subagents via the **`Agent`** tool, so `SubagentCount` is `0` in every real subagent transcript (verified: `f138a858` main has `Agent`=13, `Task`=0). Gating on it would make this feature dead code despite green unit tests. Gate only on `path != ""` and the main-error check, and rely on `LastSubagentError`'s `os.ReadDir` returning an error (→ `ok=false`) when no `subagents/` dir exists. (That `SubagentCount` counts only `Task` is a separate latent bug — out of scope for this plan; do not fix it here.)
>
> Cache note: this runs every tick for sessions without a terminal main error, bypassing `transcriptCache`, because a subagent JSONL can change while the main transcript is idle. Cost is one `os.ReadDir` per such session per tick (fast, and a no-op when the dir is absent), plus one `LastAPIError` per subagent file only when the dir exists.

- [ ] **Step 11: Guard the nudger against subagent errors**

In `internal/daemon/nudger/disrupt.go`, in `reconcileSession`, add a guard immediately after the existing `IsRetryable` check (after `disrupt.go:67`):

```go
	if s.LastError.FromSubagent {
		// Surface-only: nudging the parent will not revive a dead subagent.
		cancel()
		return
	}
```

- [ ] **Step 12: Annotate the TUI details pane**

In `internal/tui/details.go`, in the Last-error block (`details.go:51`), change the `kindStr` assembly so a subagent-origin error is marked. Replace:

```go
		kindStr := string(le.Kind)
		if isEscalated(le) {
			kindStr += "  (escalated)"
		}
```

with:

```go
		kindStr := string(le.Kind)
		if le.FromSubagent {
			kindStr += "  (in subagent)"
		}
		if isEscalated(le) {
			kindStr += "  (escalated)"
		}
```

- [ ] **Step 13: Build everything and run the full package test suite**

Run: `cd packages/pa-monitor && go build ./... && go test ./...`
Expected: all packages build; `ok` for every package (notably `internal/core/transcript`, `internal/core/poller`, `internal/daemon/nudger`, `internal/proto`, `internal/tui`).

- [ ] **Step 14: Commit the surfacing integration**

```bash
git add packages/pa-monitor/internal/core/poller/poller.go \
        packages/pa-monitor/internal/daemon/nudger/disrupt.go \
        packages/pa-monitor/internal/tui/details.go
git commit -m "feat(pa-monitor): surface subagent disrupts in TUI, exclude from auto-nudge (pg2-lpxq)"
```

---

## Task 4: Update bead and documentation

**Files:**

- Modify: `packages/pa-monitor/README.md` (if it documents disrupt kinds / error surfacing)
- Bead: `pg2-lpxq`

- [ ] **Step 1: Re-scope the bead to match the delivered fix**

Run:

```bash
bd update pg2-lpxq --notes "RESOLVED via research 2026-06-16: 'Stream idle timeout' IS emitted as isApiErrorMessage error='unknown' (7/7 real events) and was always classified/surfaced; no text-matching added. Delivered instead: regression guard (Task 1), model_not_found kind (Task 2, a real dropped kind), and subagent-disrupt surfacing (Task 3, the structural blind spot — Scan read only the main transcript). Subagent errors surface for visibility but are excluded from auto-nudge."
```

- [ ] **Step 2: Update README disrupt-kind list if present**

Check whether `packages/pa-monitor/README.md` enumerates the api-error kinds. If it does, add `model_not_found` (non-retryable) and a note that subagent disrupts surface with an "(in subagent)" marker.

Run: `grep -n "rate_limit\|unknown\|server_error\|authentication_failed\|disrupt" packages/pa-monitor/README.md`
If matches exist, edit the relevant list/section; if none, skip.

- [ ] **Step 3: Run repo-wide checks**

Run: `prek run --all-files` (or `pre-commit run --all-files` if `prek` is unavailable)
Expected: all hooks pass.

Run: `nix flake check`
Expected: passes.

- [ ] **Step 4: Commit docs/bead updates**

```bash
git add packages/pa-monitor/README.md
git commit -m "docs(pa-monitor): document model_not_found + subagent disrupt surfacing (pg2-lpxq)"
```

(If README needed no change, skip the add and this commit.)

- [ ] **Step 5: Close the bead**

```bash
bd close pg2-lpxq
```

---

## Self-Review notes

- **Spec coverage vs. bead ACs:** AC #1 (surface idle-timeout within a poll cycle) and #4 (no regression to existing kinds) are satisfied by existing behavior + Task 1's guard + Task 3's full-suite run. AC #5 (regression fixture) = Task 1. AC #2/#3 (text-matching) are intentionally **not** implemented — research proved them redundant; Task 4 records why. This is a deliberate, documented deviation approved by the scope decision on 2026-06-16.
- **Type consistency:** `ErrModelNotFound` (Task 2) and `FromSubagent`/`LastSubagentError` (Task 3) names are used identically across implementation, tests, proto translators, poller, nudger, and TUI.
- **Scope flag:** Task 3 is the largest unit and touches proto. If the proto regen or the no-auto-nudge policy needs separate review, Tasks 1–2 are independently shippable and can land first.
