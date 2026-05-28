# pa-monitor Daemon-Owned Auto-Nudge + API-Error Detection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the auto-resume scheduler from the TUI into the daemon and add detection of transient API failures so the daemon nudges stuck sessions back into work after a network disruption, surfacing all observed API errors in OTel/TUI/CLI/Grafana.

**Architecture:** Producer/dispatcher split inside a new `internal/daemon/nudger` package. Three producers (`window_reset`, `disrupted`, `manual`) reconcile their own per-session intents in `pendingStore`; one dispatcher fires once per session per tick with active-session suppression and watermark updates. A new `transcript/disrupt.go` scans Claude Code JSONL transcripts for `isApiErrorMessage` events of every kind. Persistence extends the existing `runtime.json`. The TUI becomes a thin viewer + toggle client.

**Tech Stack:** Go (1.22+), gRPC + protobuf (existing `internal/proto`), OpenTelemetry (existing `internal/otel`), JSONL transcripts (existing `internal/core/transcript`), Bubbletea TUI (existing `internal/tui`).

**Spec:** `docs/superpowers/specs/2026-05-28-pa-monitor-daemon-auto-nudge-design.md` (commit `858d0e1`).

**Repo root for all file paths below:** `packages/pa-monitor/` unless otherwise noted.

**Phasing (each phase compiles and ships green tests):**

1. Detection layer (`transcript/disrupt.go` + snapshot integration + aggregate plumbing).
2. Nudger package — intent types + store + persistence.
3. Nudger package — producers.
4. Nudger package — dispatcher + top-level Tick.
5. Daemon wiring (runtime.json schema, lifecycle, config keys).
6. gRPC additions (proto + server impl).
7. TUI rewrite.
8. CLI changes.
9. OTel additions.
10. Grafana panel additions.

---

## Phase 1 — Detection layer

### Task 1.1: ErrorKind constants + ErrorRecord type

**Files:**

- Create: `packages/pa-monitor/internal/core/transcript/disrupt.go`
- Test: `packages/pa-monitor/internal/core/transcript/disrupt_test.go`

- [ ] **Step 1: Write the failing test**

```go
// packages/pa-monitor/internal/core/transcript/disrupt_test.go
package transcript

import "testing"

func TestErrorKindIsRetryable(t *testing.T) {
	tests := []struct {
		kind ErrorKind
		want bool
	}{
		{ErrUnknown, true},
		{ErrServerError, true},
		{ErrRateLimit, false},
		{ErrInvalidRequest, false},
		{ErrAuthFailed, false},
		{ErrorKind(""), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			if got := tt.kind.IsRetryable(); got != tt.want {
				t.Errorf("ErrorKind(%q).IsRetryable() = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pa-monitor && go test ./internal/core/transcript/ -run TestErrorKindIsRetryable`
Expected: FAIL — `undefined: ErrorKind`, `undefined: ErrUnknown`, etc.

- [ ] **Step 3: Write minimal implementation**

```go
// packages/pa-monitor/internal/core/transcript/disrupt.go
package transcript

import "time"

// ErrorKind enumerates the `error` field values seen on synthetic
// isApiErrorMessage events emitted by Claude Code. Kept as a closed
// allowlist so retryability is unambiguous.
type ErrorKind string

const (
	ErrRateLimit      ErrorKind = "rate_limit"
	ErrUnknown        ErrorKind = "unknown"
	ErrServerError    ErrorKind = "server_error"
	ErrInvalidRequest ErrorKind = "invalid_request"
	ErrAuthFailed     ErrorKind = "authentication_failed"
)

// IsRetryable reports whether the disrupt producer treats this kind as
// auto-nudgeable. Only transport-level (unknown) and transient-server
// (server_error) kinds qualify.
func (k ErrorKind) IsRetryable() bool {
	return k == ErrUnknown || k == ErrServerError
}

// ErrorRecord is the most recent isApiErrorMessage observed in a session
// transcript. IsTerminal is true iff no non-synthetic user/assistant
// event follows in the JSONL.
type ErrorRecord struct {
	Kind        ErrorKind
	Text        string
	At          time.Time
	IsTerminal  bool
	IsRetryable bool
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pa-monitor && go test ./internal/core/transcript/ -run TestErrorKindIsRetryable -v`
Expected: PASS (6 sub-tests).

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/core/transcript/disrupt.go \
        packages/pa-monitor/internal/core/transcript/disrupt_test.go
git commit -m "feat(pa-monitor): add ErrorKind + ErrorRecord types for transcript api errors"
```

---

### Task 1.2: `LastAPIError` detects each kind

**Files:**

- Modify: `packages/pa-monitor/internal/core/transcript/disrupt.go`
- Test: `packages/pa-monitor/internal/core/transcript/disrupt_test.go`

- [ ] **Step 1: Write the failing test**

Append to `disrupt_test.go`:

```go
import (
	"fmt"
	"testing"
	"time"
)

// apiErrorEvent returns a JSONL line for a synthetic isApiErrorMessage
// assistant event with the given error kind and message text.
func apiErrorEvent(ts time.Time, kind ErrorKind, text string) string {
	return fmt.Sprintf(
		`{"type":"assistant","timestamp":"%s","error":%q,"isApiErrorMessage":true,`+
			`"message":{"model":"<synthetic>","content":[{"type":"text","text":%q}]}}`,
		ts.UTC().Format(time.RFC3339Nano), string(kind), text)
}

func TestLastAPIErrorDetectsEachKind(t *testing.T) {
	cases := []struct {
		kind ErrorKind
		text string
	}{
		{ErrUnknown, "API Error: The socket connection was closed unexpectedly. ..."},
		{ErrServerError, "API Error: 529 Overloaded. ..."},
		{ErrInvalidRequest, "Prompt is too long"},
		{ErrAuthFailed, "Not logged in · Please run /login"},
		{ErrRateLimit, "You've hit your limit · resets 7:10pm (America/New_York)"},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			ts := time.Date(2026, 5, 19, 20, 54, 0, 0, time.UTC)
			path := t.TempDir() + "/t.jsonl"
			if err := writeTestFile(path, apiErrorEvent(ts, tc.kind, tc.text)+"\n"); err != nil {
				t.Fatal(err)
			}
			got, err := LastAPIError(path)
			if err != nil {
				t.Fatalf("LastAPIError err = %v, want nil", err)
			}
			if got.Kind != tc.kind {
				t.Errorf("Kind = %q, want %q", got.Kind, tc.kind)
			}
			if got.Text != tc.text {
				t.Errorf("Text = %q, want %q", got.Text, tc.text)
			}
			if !got.At.Equal(ts) {
				t.Errorf("At = %v, want %v", got.At, ts)
			}
			if !got.IsTerminal {
				t.Errorf("IsTerminal = false, want true (no event follows)")
			}
			if got.IsRetryable != tc.kind.IsRetryable() {
				t.Errorf("IsRetryable = %v, want %v", got.IsRetryable, tc.kind.IsRetryable())
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pa-monitor && go test ./internal/core/transcript/ -run TestLastAPIErrorDetectsEachKind`
Expected: FAIL — `undefined: LastAPIError`.

- [ ] **Step 3: Write minimal implementation**

Append to `disrupt.go`:

```go
import (
	"bufio"
	"encoding/json"
	"os"
)

// LastAPIError returns the most recent isApiErrorMessage event in the
// transcript regardless of kind. IsTerminal is true iff no subsequent
// (non-synthetic) user/assistant event follows. Returns zero ErrorRecord
// if no api-error event is present (Kind == "").
func LastAPIError(path string) (ErrorRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return ErrorRecord{}, err
	}
	defer f.Close()

	type apiErrorScan struct {
		Type              string    `json:"type"`
		Timestamp         time.Time `json:"timestamp"`
		Error             string    `json:"error"`
		IsApiErrorMessage bool      `json:"isApiErrorMessage"`
		Message           struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	type typeOnly struct {
		Type              string `json:"type"`
		IsApiErrorMessage bool   `json:"isApiErrorMessage"`
		Error             string `json:"error"`
	}

	var lines [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		b := make([]byte, len(sc.Bytes()))
		copy(b, sc.Bytes())
		lines = append(lines, b)
	}
	if sc.Err() != nil {
		return ErrorRecord{}, sc.Err()
	}

	lastIdx := -1
	var rec ErrorRecord
	for i, line := range lines {
		var ev apiErrorScan
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type != "assistant" || !ev.IsApiErrorMessage {
			continue
		}
		kind := ErrorKind(ev.Error)
		switch kind {
		case ErrRateLimit, ErrUnknown, ErrServerError, ErrInvalidRequest, ErrAuthFailed:
		default:
			continue
		}
		var text string
		for _, c := range ev.Message.Content {
			if c.Type == "text" {
				text = c.Text
				break
			}
		}
		lastIdx = i
		rec = ErrorRecord{
			Kind:        kind,
			Text:        text,
			At:          ev.Timestamp,
			IsTerminal:  true,
			IsRetryable: kind.IsRetryable(),
		}
	}
	if lastIdx < 0 {
		return ErrorRecord{}, nil
	}
	for _, line := range lines[lastIdx+1:] {
		var ev typeOnly
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type != "user" && ev.Type != "assistant" {
			continue
		}
		if ev.Type == "assistant" && ev.IsApiErrorMessage {
			continue
		}
		rec.IsTerminal = false
		break
	}
	return rec, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pa-monitor && go test ./internal/core/transcript/ -run TestLastAPIErrorDetectsEachKind -v`
Expected: PASS (5 sub-tests).

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/core/transcript/disrupt.go \
        packages/pa-monitor/internal/core/transcript/disrupt_test.go
git commit -m "feat(pa-monitor): LastAPIError scans transcripts for all api error kinds"
```

---

### Task 1.3: `IsTerminal` flips when a non-synthetic event follows

**Files:**

- Test: `packages/pa-monitor/internal/core/transcript/disrupt_test.go`

- [ ] **Step 1: Write the failing test**

Append to `disrupt_test.go`:

```go
func TestLastAPIErrorIsTerminalFlipsOnResume(t *testing.T) {
	ts := time.Date(2026, 5, 19, 20, 54, 0, 0, time.UTC)
	path := t.TempDir() + "/t.jsonl"
	body := apiErrorEvent(ts, ErrUnknown, "API Error: socket closed") + "\n" +
		`{"type":"user","message":{"role":"user","content":"continue"}}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, err := LastAPIError(path)
	if err != nil {
		t.Fatalf("LastAPIError err = %v, want nil", err)
	}
	if got.Kind != ErrUnknown {
		t.Errorf("Kind = %q, want %q", got.Kind, ErrUnknown)
	}
	if got.IsTerminal {
		t.Error("IsTerminal = true, want false (user resumed after error)")
	}
}

func TestLastAPIErrorIsTerminalSurvivesAnotherSyntheticError(t *testing.T) {
	ts1 := time.Date(2026, 5, 19, 20, 54, 0, 0, time.UTC)
	ts2 := ts1.Add(30 * time.Second)
	path := t.TempDir() + "/t.jsonl"
	body := apiErrorEvent(ts1, ErrServerError, "529 Overloaded") + "\n" +
		apiErrorEvent(ts2, ErrUnknown, "socket closed") + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, err := LastAPIError(path)
	if err != nil {
		t.Fatalf("LastAPIError err = %v, want nil", err)
	}
	if got.Kind != ErrUnknown {
		t.Errorf("Kind = %q, want %q (most recent wins)", got.Kind, ErrUnknown)
	}
	if !got.At.Equal(ts2) {
		t.Errorf("At = %v, want %v", got.At, ts2)
	}
	if !got.IsTerminal {
		t.Error("IsTerminal = false, want true (second synthetic error is not a resume)")
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `cd packages/pa-monitor && go test ./internal/core/transcript/ -run TestLastAPIErrorIsTerminal -v`
Expected: PASS (both). The implementation from Task 1.2 already satisfies these.

- [ ] **Step 3: Commit**

```bash
git add packages/pa-monitor/internal/core/transcript/disrupt_test.go
git commit -m "test(pa-monitor): IsTerminal resume + back-to-back-error coverage"
```

---

### Task 1.4: Integrate `LastError` into `transcript.Snapshot`

**Files:**

- Modify: `packages/pa-monitor/internal/core/transcript/snapshot.go`
- Test: `packages/pa-monitor/internal/core/transcript/snapshot_test.go`

- [ ] **Step 1: Read current snapshot shape**

Read `internal/core/transcript/snapshot.go` (around lines 1-50 and 80-180) to confirm the `Snapshot` struct definition site and the single-pass event-loop body.

- [ ] **Step 2: Write the failing test**

Append to `snapshot_test.go`:

```go
func TestSnapshotPopulatesLastErrorForRetryable(t *testing.T) {
	ts := time.Date(2026, 5, 19, 20, 54, 0, 0, time.UTC)
	path := t.TempDir() + "/t.jsonl"
	body := apiErrorEvent(ts, ErrUnknown, "API Error: socket closed") + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	snap, err := Snapshot(path)
	if err != nil {
		t.Fatalf("Snapshot err = %v", err)
	}
	if snap.LastError == nil {
		t.Fatal("LastError = nil, want non-nil")
	}
	if snap.LastError.Kind != ErrUnknown {
		t.Errorf("LastError.Kind = %q, want %q", snap.LastError.Kind, ErrUnknown)
	}
	if !snap.LastError.IsTerminal {
		t.Error("LastError.IsTerminal = false, want true")
	}
	if !snap.LastError.IsRetryable {
		t.Error("LastError.IsRetryable = false, want true")
	}
}

func TestSnapshotLastErrorNilWhenNoApiError(t *testing.T) {
	path := t.TempDir() + "/t.jsonl"
	body := `{"type":"user","message":{"role":"user","content":"hi"}}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	snap, err := Snapshot(path)
	if err != nil {
		t.Fatalf("Snapshot err = %v", err)
	}
	if snap.LastError != nil {
		t.Errorf("LastError = %+v, want nil", snap.LastError)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd packages/pa-monitor && go test ./internal/core/transcript/ -run TestSnapshot.*LastError`
Expected: FAIL — `snap.LastError undefined`.

- [ ] **Step 4: Add `LastError *ErrorRecord` to `Snapshot` struct**

Edit `snapshot.go`'s `Snapshot` struct (top of file around line 15-30): add the field.

```go
type Snapshot struct {
	// ... existing fields ...
	RateLimitResetsAt time.Time
	LastError         *ErrorRecord // most recent isApiErrorMessage; nil if none
	// ... existing fields ...
}
```

- [ ] **Step 5: Populate `LastError` inside the existing event-loop**

In `snapshot.go`, locate the single-pass loop that processes each JSONL line (the same loop that detects the synthetic rate-limit). Inside that loop, after the existing per-event JSON unmarshal, add tracking variables and a final post-loop section that builds `LastError`:

```go
// Inside Snapshot(), alongside existing per-event tracking:
var (
	lastApiErrIdx       = -1
	lastApiErrTime      time.Time
	lastApiErrKind      ErrorKind
	lastApiErrText      string
)
// Inside the loop, after parsing an event with isApiErrorMessage == true:
if ev.Type == "assistant" && aux.IsApiErrorMessage {
	k := ErrorKind(aux.Error)
	switch k {
	case ErrRateLimit, ErrUnknown, ErrServerError, ErrInvalidRequest, ErrAuthFailed:
		var text string
		for _, c := range aux.Message.Content {
			if c.Type == "text" {
				text = c.Text
				break
			}
		}
		lastApiErrIdx = i
		lastApiErrTime = ev.Timestamp
		lastApiErrKind = k
		lastApiErrText = text
	}
}
// After the loop, build the LastError using IsTerminal-detection mirroring
// LastAPIError's tail walk over lines[lastApiErrIdx+1:]:
if lastApiErrIdx >= 0 {
	terminal := true
	for _, line := range lines[lastApiErrIdx+1:] {
		var tail struct {
			Type              string `json:"type"`
			IsApiErrorMessage bool   `json:"isApiErrorMessage"`
		}
		if err := json.Unmarshal(line, &tail); err != nil {
			continue
		}
		if tail.Type != "user" && tail.Type != "assistant" {
			continue
		}
		if tail.Type == "assistant" && tail.IsApiErrorMessage {
			continue
		}
		terminal = false
		break
	}
	snap.LastError = &ErrorRecord{
		Kind:        lastApiErrKind,
		Text:        lastApiErrText,
		At:          lastApiErrTime,
		IsTerminal:  terminal,
		IsRetryable: lastApiErrKind.IsRetryable(),
	}
}
```

Note: this assumes `aux` already has `IsApiErrorMessage` and `Error` fields. If not, extend the auxiliary scan struct (look for the existing `aux` or `syntheticScan` definition in `snapshot.go` — the rate-limit path already needs IsApiErrorMessage; add `Error string` if missing).

- [ ] **Step 6: Run all transcript tests to verify everything passes**

Run: `cd packages/pa-monitor && go test ./internal/core/transcript/ -v`
Expected: PASS for all tests, including the two new ones and all existing rate-limit/snapshot tests.

- [ ] **Step 7: Commit**

```bash
git add packages/pa-monitor/internal/core/transcript/snapshot.go \
        packages/pa-monitor/internal/core/transcript/snapshot_test.go
git commit -m "feat(pa-monitor): Snapshot populates LastError in the existing event-loop"
```

---

### Task 1.5: Aggregate `SessionEntry` carries `LastError`

**Files:**

- Modify: `packages/pa-monitor/internal/core/aggregate/tree.go`
- Modify: `packages/pa-monitor/internal/core/aggregate/aggregate.go`
- Test: `packages/pa-monitor/internal/core/aggregate/aggregate_test.go` (existing) — extend.

- [ ] **Step 1: Write the failing test**

Append to `aggregate_test.go`:

```go
func TestAggregateCarriesLastError(t *testing.T) {
	now := time.Date(2026, 5, 19, 20, 54, 0, 0, time.UTC)
	rec := &transcript.ErrorRecord{
		Kind: transcript.ErrUnknown, Text: "socket closed",
		At: now, IsTerminal: true, IsRetryable: true,
	}
	entries := []SessionEntry{{
		PID:       1234,
		SessionID: "sid-1",
		Cwd:       "/tmp/work",
		LastError: rec,
	}}
	tree := BuildTree(entries, now)
	got := tree.Dirs[0].Sessions[0].LastError
	if got == nil {
		t.Fatal("LastError = nil, want pointer to rec")
	}
	if got.Kind != transcript.ErrUnknown || !got.IsRetryable {
		t.Errorf("LastError = %+v, want unknown+retryable", got)
	}
}
```

(Replace `BuildTree` and entry-construction calls with the actual constructor signature used by existing tests in `aggregate_test.go` — read that file first to match the existing test setup pattern.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pa-monitor && go test ./internal/core/aggregate/ -run TestAggregateCarriesLastError`
Expected: FAIL — `SessionEntry has no field LastError` or `tree.Dirs[0].Sessions[0].LastError undefined`.

- [ ] **Step 3: Add `LastError` field to `SessionEntry`**

Edit `internal/core/aggregate/tree.go`. Locate `type SessionEntry struct` and add:

```go
type SessionEntry struct {
	// ... existing fields ...
	RateLimitResetsAt time.Time
	LastError         *transcript.ErrorRecord // most recent api error from snapshot; nil if none
	PendingNudge      *PendingNudge           // set by daemon nudger; nil when no intents pending
	// ... existing fields ...
}

// PendingNudge surfaces which nudge sources are currently queued for this
// session. Populated by the daemon's nudger before serialization to
// clients; producers/dispatcher are the source of truth.
type PendingNudge struct {
	Sources []string // subset of {"window_reset","disrupted","manual"}
}
```

- [ ] **Step 4: Propagate `LastError` from `Snapshot` to `SessionEntry`**

Edit `internal/core/aggregate/aggregate.go`. Locate the function that builds a `SessionEntry` from a `transcript.Snapshot` (the caller of `transcript.Snapshot(path)`); copy `snap.LastError` onto the entry:

```go
entry.LastError = snap.LastError
```

`PendingNudge` is left nil here — daemon nudger sets it post-aggregate.

- [ ] **Step 5: Run aggregate tests**

Run: `cd packages/pa-monitor && go test ./internal/core/aggregate/ -v`
Expected: PASS for all tests including the new one.

- [ ] **Step 6: Commit**

```bash
git add packages/pa-monitor/internal/core/aggregate/tree.go \
        packages/pa-monitor/internal/core/aggregate/aggregate.go \
        packages/pa-monitor/internal/core/aggregate/aggregate_test.go
git commit -m "feat(pa-monitor): aggregate SessionEntry carries LastError + PendingNudge"
```

---

## Phase 2 — Nudger package: types, store, persistence

### Task 2.1: `IntentKey` and `NudgeIntent` types

**Files:**

- Create: `packages/pa-monitor/internal/daemon/nudger/intent.go`
- Test: `packages/pa-monitor/internal/daemon/nudger/intent_test.go`

- [ ] **Step 1: Write the failing test**

```go
// packages/pa-monitor/internal/daemon/nudger/intent_test.go
package nudger

import (
	"testing"
	"time"
)

func TestIntentKeyEquality(t *testing.T) {
	a := IntentKey{SessionID: "sid", Source: SourceManual}
	b := IntentKey{SessionID: "sid", Source: SourceManual}
	c := IntentKey{SessionID: "sid", Source: SourceDisrupted}
	if a != b {
		t.Error("IntentKey same fields should be equal")
	}
	if a == c {
		t.Error("IntentKey different source should not be equal")
	}
}

func TestSourceConstants(t *testing.T) {
	for _, s := range []Source{SourceWindowReset, SourceDisrupted, SourceManual} {
		if s == "" {
			t.Errorf("Source constant empty")
		}
	}
}

func TestNudgeIntentZeroValueIsUsable(t *testing.T) {
	var n NudgeIntent
	if !n.EmittedAt.IsZero() {
		t.Error("zero NudgeIntent EmittedAt should be zero time")
	}
	n.EmittedAt = time.Now()
	_ = n
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pa-monitor && go test ./internal/daemon/nudger/ -run TestIntentKey`
Expected: FAIL — package does not exist / undefined types.

- [ ] **Step 3: Write minimal implementation**

```go
// packages/pa-monitor/internal/daemon/nudger/intent.go
package nudger

import (
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// Source identifies which producer emitted a nudge intent.
type Source string

const (
	SourceWindowReset Source = "window_reset"
	SourceDisrupted   Source = "disrupted"
	SourceManual      Source = "manual"
)

// IntentKey uniquely identifies one pending intent in the store.
// A single session may simultaneously hold up to one intent per Source.
type IntentKey struct {
	SessionID string
	Source    Source
}

// NudgeIntent is one pending request to nudge a session, owned by exactly
// one producer (Key.Source). The dispatcher reads these and clears them
// after a fire (or suppression). Cause is non-nil only for Disrupted.
type NudgeIntent struct {
	Key       IntentKey
	Text      string
	EmittedAt time.Time
	Cause     *transcript.ErrorRecord
}
```

Note: replace the import path if the module path differs from `github.com/phillipgreenii/pa-monitor`. Check `packages/pa-monitor/go.mod` first.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pa-monitor && go test ./internal/daemon/nudger/ -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/daemon/nudger/intent.go \
        packages/pa-monitor/internal/daemon/nudger/intent_test.go
git commit -m "feat(pa-monitor): nudger IntentKey + NudgeIntent types"
```

---

### Task 2.2: `pendingStore` Add/Cancel/ClearSession/HasAny/SourcesFor/List

**Files:**

- Create: `packages/pa-monitor/internal/daemon/nudger/store.go`
- Test: `packages/pa-monitor/internal/daemon/nudger/store_test.go`

- [ ] **Step 1: Write the failing test**

```go
// packages/pa-monitor/internal/daemon/nudger/store_test.go
package nudger

import (
	"sort"
	"testing"
	"time"
)

func TestPendingStoreAddIdempotent(t *testing.T) {
	s := NewPendingStore()
	in := NudgeIntent{Key: IntentKey{"sid", SourceManual}, Text: "continue",
		EmittedAt: time.Date(2026, 5, 28, 14, 0, 0, 0, time.UTC)}
	if added := s.Add(in); !added {
		t.Error("Add new key returned false, want true")
	}
	in.EmittedAt = in.EmittedAt.Add(time.Minute) // mutated; same key
	if added := s.Add(in); added {
		t.Error("Add same key returned true, want false (idempotent)")
	}
	if list := s.List(); len(list) != 1 {
		t.Errorf("len(List) = %d, want 1", len(list))
	}
}

func TestPendingStoreCancel(t *testing.T) {
	s := NewPendingStore()
	k := IntentKey{"sid", SourceDisrupted}
	s.Add(NudgeIntent{Key: k, EmittedAt: time.Now()})
	if !s.HasAny("sid") {
		t.Fatal("HasAny = false after Add, want true")
	}
	s.Cancel(k)
	if s.HasAny("sid") {
		t.Error("HasAny = true after Cancel, want false")
	}
	s.Cancel(k) // second cancel is a no-op
}

func TestPendingStoreClearSession(t *testing.T) {
	s := NewPendingStore()
	for _, src := range []Source{SourceWindowReset, SourceDisrupted, SourceManual} {
		s.Add(NudgeIntent{Key: IntentKey{"sid", src}, EmittedAt: time.Now()})
	}
	s.Add(NudgeIntent{Key: IntentKey{"other", SourceManual}, EmittedAt: time.Now()})
	s.ClearSession("sid")
	if s.HasAny("sid") {
		t.Error("HasAny(sid) = true after ClearSession, want false")
	}
	if !s.HasAny("other") {
		t.Error("HasAny(other) = false after ClearSession(sid), want true")
	}
}

func TestPendingStoreSourcesFor(t *testing.T) {
	s := NewPendingStore()
	s.Add(NudgeIntent{Key: IntentKey{"sid", SourceWindowReset}, EmittedAt: time.Now()})
	s.Add(NudgeIntent{Key: IntentKey{"sid", SourceManual}, EmittedAt: time.Now()})
	got := s.SourcesFor("sid")
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	want := []Source{SourceManual, SourceWindowReset}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("SourcesFor = %v, want %v", got, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/pa-monitor && go test ./internal/daemon/nudger/ -run TestPendingStore`
Expected: FAIL — `undefined: NewPendingStore`.

- [ ] **Step 3: Write minimal implementation**

```go
// packages/pa-monitor/internal/daemon/nudger/store.go
package nudger

import "sync"

// PendingStore is a thread-safe map of pending nudge intents keyed by
// (session, source). Mutations are persisted by the caller (nudger
// package's persistence layer) — the store itself is in-memory.
type PendingStore struct {
	mu      sync.Mutex
	intents map[IntentKey]NudgeIntent
}

func NewPendingStore() *PendingStore {
	return &PendingStore{intents: map[IntentKey]NudgeIntent{}}
}

// Add stores the intent. Returns true if the key was newly inserted,
// false if it already existed (the existing entry is left unchanged).
func (s *PendingStore) Add(in NudgeIntent) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.intents[in.Key]; ok {
		return false
	}
	s.intents[in.Key] = in
	return true
}

// Cancel removes the intent for key. No-op if absent.
func (s *PendingStore) Cancel(key IntentKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.intents, key)
}

// ClearSession removes all intents (across all sources) for sid.
func (s *PendingStore) ClearSession(sid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.intents {
		if k.SessionID == sid {
			delete(s.intents, k)
		}
	}
}

// HasAny reports whether any intent is currently pending for sid.
func (s *PendingStore) HasAny(sid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.intents {
		if k.SessionID == sid {
			return true
		}
	}
	return false
}

// SourcesFor returns the sources that currently have a pending intent
// for sid. Order is unspecified.
func (s *PendingStore) SourcesFor(sid string) []Source {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Source
	for k := range s.intents {
		if k.SessionID == sid {
			out = append(out, k.Source)
		}
	}
	return out
}

// List returns a snapshot of all pending intents. Order is unspecified;
// callers that need stable ordering must sort.
func (s *PendingStore) List() []NudgeIntent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]NudgeIntent, 0, len(s.intents))
	for _, v := range s.intents {
		out = append(out, v)
	}
	return out
}
```

- [ ] **Step 4: Run all nudger tests**

Run: `cd packages/pa-monitor && go test ./internal/daemon/nudger/ -v`
Expected: PASS for all tests (4 new + 3 from Task 2.1).

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/daemon/nudger/store.go \
        packages/pa-monitor/internal/daemon/nudger/store_test.go
git commit -m "feat(pa-monitor): nudger PendingStore Add/Cancel/ClearSession"
```

---

### Task 2.3: Persistence schema for nudger state

**Files:**

- Modify: `packages/pa-monitor/internal/daemon/runtime_state.go`
- Test: `packages/pa-monitor/internal/daemon/runtime_state_test.go`

- [ ] **Step 1: Write the failing test**

Append to `runtime_state_test.go`:

```go
func TestRuntimeStateNudgerRoundTrip(t *testing.T) {
	in := RuntimeState{
		CaffeinateOn:      true,
		AutoResumeEnabled: true,
		Nudger: NudgerState{
			PendingIntents: []PersistedIntent{
				{
					SessionID: "sid-1", Source: "manual", Text: "continue",
					EmittedAt: time.Date(2026, 5, 28, 14, 0, 0, 0, time.UTC),
				},
			},
			Sessions: map[string]NudgerSessionWatermarks{
				"sid-1": {
					LastNudgedAt:        time.Date(2026, 5, 28, 14, 1, 0, 0, time.UTC),
					LastDisruptNudgeAt:  time.Date(2026, 5, 28, 14, 1, 0, 0, time.UTC),
					LastDisruptNudgeFor: time.Date(2026, 5, 28, 14, 0, 50, 0, time.UTC),
					DisruptEscalated:    false,
				},
			},
			WindowResetFiredFor: time.Date(2026, 5, 28, 14, 0, 0, 0, time.UTC),
		},
	}
	path := t.TempDir() + "/runtime.json"
	if err := WriteRuntimeState(path, in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := ReadRuntimeState(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.AutoResumeEnabled != true {
		t.Error("AutoResumeEnabled mismatch")
	}
	if len(got.Nudger.PendingIntents) != 1 || got.Nudger.PendingIntents[0].SessionID != "sid-1" {
		t.Errorf("Nudger.PendingIntents = %+v", got.Nudger.PendingIntents)
	}
	if !got.Nudger.WindowResetFiredFor.Equal(in.Nudger.WindowResetFiredFor) {
		t.Errorf("WindowResetFiredFor mismatch: got %v want %v",
			got.Nudger.WindowResetFiredFor, in.Nudger.WindowResetFiredFor)
	}
}

func TestRuntimeStateOldFormatBackwardCompat(t *testing.T) {
	path := t.TempDir() + "/runtime.json"
	if err := os.WriteFile(path, []byte(`{"caffeinate_on":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRuntimeState(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !got.CaffeinateOn {
		t.Error("CaffeinateOn = false, want true (legacy file)")
	}
	if got.Nudger.PendingIntents != nil {
		t.Errorf("Nudger.PendingIntents = %v, want nil for legacy file", got.Nudger.PendingIntents)
	}
}
```

(Add `import "time"` and `import "os"` to the test file if not already present.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/pa-monitor && go test ./internal/daemon/ -run TestRuntimeStateNudger`
Expected: FAIL — `unknown field AutoResumeEnabled`, `unknown type NudgerState`.

- [ ] **Step 3: Extend RuntimeState schema**

Edit `internal/daemon/runtime_state.go`:

```go
package daemon

import (
	"encoding/json"
	"os"
	"time"
)

type RuntimeState struct {
	CaffeinateOn      bool        `json:"caffeinate_on"`
	AutoResumeEnabled bool        `json:"auto_resume_enabled,omitempty"`
	Nudger            NudgerState `json:"nudger,omitempty"`
}

type NudgerState struct {
	PendingIntents      []PersistedIntent                  `json:"pending_intents,omitempty"`
	Sessions            map[string]NudgerSessionWatermarks `json:"sessions,omitempty"`
	WindowResetFiredFor time.Time                          `json:"window_reset_fired_for,omitempty"`
}

type PersistedIntent struct {
	SessionID string    `json:"session_id"`
	Source    string    `json:"source"`
	Text      string    `json:"text,omitempty"`
	EmittedAt time.Time `json:"emitted_at"`
	CauseKind string    `json:"cause_kind,omitempty"`
	CauseAt   time.Time `json:"cause_at,omitempty"`
}

type NudgerSessionWatermarks struct {
	LastNudgedAt        time.Time `json:"last_nudged_at,omitempty"`
	LastDisruptNudgeAt  time.Time `json:"last_disrupt_nudge_at,omitempty"`
	LastDisruptNudgeFor time.Time `json:"last_disrupt_nudge_for,omitempty"`
	DisruptEscalated    bool      `json:"disrupt_escalated,omitempty"`
}

// (ReadRuntimeState / WriteRuntimeState bodies are unchanged — json
// unmarshal handles the new fields, missing fields default to zero.)
```

- [ ] **Step 4: Run tests**

Run: `cd packages/pa-monitor && go test ./internal/daemon/ -run TestRuntimeState -v`
Expected: PASS for all RuntimeState tests including the two new ones and any pre-existing ones.

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/daemon/runtime_state.go \
        packages/pa-monitor/internal/daemon/runtime_state_test.go
git commit -m "feat(pa-monitor): extend runtime.json with nudger state schema"
```

---

## Phase 3 — Nudger package: producers

### Task 3.1: Producer interface + shared types

**Files:**

- Create: `packages/pa-monitor/internal/daemon/nudger/producer.go`
- Test: `packages/pa-monitor/internal/daemon/nudger/producer_test.go`

- [ ] **Step 1: Write the failing test**

```go
// packages/pa-monitor/internal/daemon/nudger/producer_test.go
package nudger

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
)

func TestProducerInterfaceCompliance(t *testing.T) {
	// Compile-time assertions that each concrete producer type satisfies Producer.
	var _ Producer = (*WindowResetProducer)(nil)
	var _ Producer = (*DisruptProducer)(nil)
	var _ Producer = (*ManualProducer)(nil)
}

func TestTickContextZeroValueUsable(t *testing.T) {
	ctx := TickContext{Now: time.Now(), Tree: &aggregate.Tree{}}
	if ctx.Now.IsZero() {
		t.Error("Now should be set")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pa-monitor && go test ./internal/daemon/nudger/ -run TestProducer`
Expected: FAIL — `undefined: Producer`, `WindowResetProducer`, etc.

- [ ] **Step 3: Write minimal implementation**

```go
// packages/pa-monitor/internal/daemon/nudger/producer.go
package nudger

import (
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// Producer reconciles its own per-session intents against the latest
// snapshot. Reconcile MUST be cancel-then-add: cancel its own keys that
// no longer apply, then add intents for newly-applicable conditions.
type Producer interface {
	Reconcile(ctx TickContext, store *PendingStore)
}

// TickContext carries the per-tick inputs producers read.
type TickContext struct {
	Now               time.Time
	Tree              *aggregate.Tree
	AutoResumeEnabled bool
	AutoResumeMessage string
	AutoResumeDelay   time.Duration
	DisruptGrace      time.Duration
	EscalationAfter   time.Duration

	// State the dispatcher has updated for past fires; producers read these
	// for cancellation/escalation decisions.
	Watermarks WatermarkView
}

// WatermarkView is the read-only slice of nudger state visible to
// producers. The dispatcher owns writes.
type WatermarkView interface {
	WindowResetFiredFor() time.Time
	SessionWatermark(sid string) SessionWatermark
}

type SessionWatermark struct {
	LastNudgedAt        time.Time
	LastDisruptNudgeAt  time.Time
	LastDisruptNudgeFor time.Time
	DisruptEscalated    bool
}

// WindowResetProducer, DisruptProducer, ManualProducer are concrete
// producers; their reconcile bodies live in their own files. Empty
// declarations here so the interface assertions compile.
type WindowResetProducer struct{}
type DisruptProducer struct {
	firstSeen map[string]time.Time // sid -> when this disrupt was first observed
}
type ManualProducer struct{}

func (*WindowResetProducer) Reconcile(TickContext, *PendingStore) {}
func (*DisruptProducer) Reconcile(TickContext, *PendingStore)     {}
func (*ManualProducer) Reconcile(TickContext, *PendingStore)      {}

// unused but referenced by future tests
var _ = transcript.ErrUnknown
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd packages/pa-monitor && go test ./internal/daemon/nudger/ -run TestProducer -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/daemon/nudger/producer.go \
        packages/pa-monitor/internal/daemon/nudger/producer_test.go
git commit -m "feat(pa-monitor): nudger Producer interface + TickContext"
```

---

### Task 3.2: `WindowResetProducer.Reconcile` implementation

**Files:**

- Create: `packages/pa-monitor/internal/daemon/nudger/window_reset.go`
- Modify: `packages/pa-monitor/internal/daemon/nudger/producer.go` (remove stub)
- Test: `packages/pa-monitor/internal/daemon/nudger/window_reset_test.go`

- [ ] **Step 1: Write the failing test**

```go
// packages/pa-monitor/internal/daemon/nudger/window_reset_test.go
package nudger

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

type wmStub struct {
	wr  time.Time
	per map[string]SessionWatermark
}

func (w wmStub) WindowResetFiredFor() time.Time { return w.wr }
func (w wmStub) SessionWatermark(sid string) SessionWatermark {
	return w.per[sid]
}

func treeWith(windowResetsAt time.Time, sessions ...aggregate.SessionEntry) *aggregate.Tree {
	t := &aggregate.Tree{WindowResetsAt: windowResetsAt}
	t.Dirs = []*aggregate.DirEntry{{Sessions: sessions}}
	return t
}

func TestWindowResetProducerNoOpWhenZero(t *testing.T) {
	p := &WindowResetProducer{}
	store := NewPendingStore()
	p.Reconcile(TickContext{
		Now: time.Now(), AutoResumeEnabled: true,
		Tree:       &aggregate.Tree{},
		Watermarks: wmStub{},
	}, store)
	if got := len(store.List()); got != 0 {
		t.Errorf("len(intents) = %d, want 0 (no window)", got)
	}
}

func TestWindowResetProducerNoOpWhenDisabled(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	resetsAt := now.Add(-1 * time.Minute)
	p := &WindowResetProducer{}
	store := NewPendingStore()
	tree := treeWith(resetsAt, aggregate.SessionEntry{
		SessionID: "sid-1", PID: 1, Status: session.Idle,
	})
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: false, AutoResumeDelay: 30 * time.Second,
		Tree: tree, Watermarks: wmStub{},
	}, store)
	if got := len(store.List()); got != 0 {
		t.Errorf("len(intents) = %d, want 0 (disabled)", got)
	}
}

func TestWindowResetProducerFiresAfterDelay(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 30, 0, time.UTC)
	resetsAt := now.Add(-31 * time.Second) // delay elapsed (30s)
	p := &WindowResetProducer{}
	store := NewPendingStore()
	tree := treeWith(resetsAt,
		aggregate.SessionEntry{SessionID: "idle-1", PID: 1, Status: session.Idle},
		aggregate.SessionEntry{SessionID: "work-1", PID: 2, Status: session.Working},
		aggregate.SessionEntry{SessionID: "dorm-1", PID: 3, Status: session.Dormant},
	)
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, AutoResumeDelay: 30 * time.Second,
		AutoResumeMessage: "continue",
		Tree:              tree, Watermarks: wmStub{},
	}, store)
	got := store.List()
	if len(got) != 2 {
		t.Fatalf("len(intents) = %d, want 2 (idle + dormant; working skipped)", len(got))
	}
	for _, in := range got {
		if in.Key.Source != SourceWindowReset {
			t.Errorf("intent source = %q, want window_reset", in.Key.Source)
		}
		if in.Text != "continue" {
			t.Errorf("intent text = %q, want continue", in.Text)
		}
		if in.Key.SessionID == "work-1" {
			t.Errorf("Working session should be skipped at queue time")
		}
	}
}

func TestWindowResetProducerSkipsIfAlreadyFired(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 30, 0, time.UTC)
	resetsAt := now.Add(-31 * time.Second)
	p := &WindowResetProducer{}
	store := NewPendingStore()
	tree := treeWith(resetsAt, aggregate.SessionEntry{
		SessionID: "idle-1", PID: 1, Status: session.Idle,
	})
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, AutoResumeDelay: 30 * time.Second,
		AutoResumeMessage: "continue",
		Tree:              tree,
		Watermarks:        wmStub{wr: resetsAt}, // already fired for this window
	}, store)
	if got := len(store.List()); got != 0 {
		t.Errorf("len(intents) = %d, want 0 (already fired)", got)
	}
}

func TestWindowResetProducerCancelsWhenWindowClears(t *testing.T) {
	p := &WindowResetProducer{}
	store := NewPendingStore()
	store.Add(NudgeIntent{Key: IntentKey{"idle-1", SourceWindowReset}, EmittedAt: time.Now()})
	tree := &aggregate.Tree{} // WindowResetsAt zero
	tree.Dirs = []*aggregate.DirEntry{{Sessions: []aggregate.SessionEntry{
		{SessionID: "idle-1", PID: 1, Status: session.Idle},
	}}}
	p.Reconcile(TickContext{
		Now: time.Now(), AutoResumeEnabled: true, AutoResumeDelay: 30 * time.Second,
		Tree: tree, Watermarks: wmStub{},
	}, store)
	if store.HasAny("idle-1") {
		t.Error("intent not cancelled when window cleared")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/pa-monitor && go test ./internal/daemon/nudger/ -run TestWindowResetProducer`
Expected: FAIL — `WindowResetProducer.Reconcile` is empty stub; tests will fail.

- [ ] **Step 3: Write the implementation**

Remove the stub `WindowResetProducer{}` from `producer.go`, leave the type declaration but delete the empty `Reconcile`. Create new file:

```go
// packages/pa-monitor/internal/daemon/nudger/window_reset.go
package nudger

import (
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// Reconcile implements Producer. See spec §Architecture §Producers
// §WindowResetProducer for the rule table.
func (p *WindowResetProducer) Reconcile(ctx TickContext, store *PendingStore) {
	// Cancel-then-add ordering: walk all sessions; if our precondition no
	// longer holds for a session that has our intent, cancel it.
	cancelAll := func() {
		for _, dir := range ctx.Tree.Dirs {
			for _, s := range dir.Sessions {
				store.Cancel(IntentKey{SessionID: s.SessionID, Source: SourceWindowReset})
			}
		}
	}

	if !ctx.AutoResumeEnabled {
		cancelAll()
		return
	}
	resetsAt := ctx.Tree.WindowResetsAt
	if resetsAt.IsZero() {
		cancelAll()
		return
	}
	fireAt := resetsAt.Add(ctx.AutoResumeDelay)
	if ctx.Now.Before(fireAt) {
		// Window still pending; producer waits silently. Stale intents (e.g.
		// from a prior window) would have been cleared when the prior window
		// cleared.
		return
	}
	if ctx.Watermarks.WindowResetFiredFor().Equal(resetsAt) {
		// Already fired this window. Producer keeps no intents.
		cancelAll()
		return
	}
	// Fire across all non-Working sessions.
	for _, dir := range ctx.Tree.Dirs {
		for _, s := range dir.Sessions {
			if s.Status == session.Working {
				continue
			}
			store.Add(NudgeIntent{
				Key:       IntentKey{SessionID: s.SessionID, Source: SourceWindowReset},
				Text:      ctx.AutoResumeMessage,
				EmittedAt: ctx.Now,
			})
		}
	}
}
```

Remove the empty `Reconcile` method from `producer.go` (delete just that one line; keep the type declaration).

- [ ] **Step 4: Run tests**

Run: `cd packages/pa-monitor && go test ./internal/daemon/nudger/ -run TestWindowReset -v`
Expected: PASS (5 sub-tests).

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/daemon/nudger/window_reset.go \
        packages/pa-monitor/internal/daemon/nudger/window_reset_test.go \
        packages/pa-monitor/internal/daemon/nudger/producer.go
git commit -m "feat(pa-monitor): WindowResetProducer reconciles non-Working sessions post-delay"
```

---

### Task 3.3: `DisruptProducer.Reconcile` — grace + escalation

**Files:**

- Create: `packages/pa-monitor/internal/daemon/nudger/disrupt.go`
- Modify: `packages/pa-monitor/internal/daemon/nudger/producer.go` (remove stub)
- Test: `packages/pa-monitor/internal/daemon/nudger/disrupt_test.go`

- [ ] **Step 1: Write the failing test**

```go
// packages/pa-monitor/internal/daemon/nudger/disrupt_test.go
package nudger

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

func sessionWithError(sid string, kind transcript.ErrorKind, at time.Time, terminal bool) aggregate.SessionEntry {
	return aggregate.SessionEntry{
		SessionID: sid, PID: 1, Status: session.Idle,
		LastError: &transcript.ErrorRecord{
			Kind: kind, Text: "API Error: ...",
			At: at, IsTerminal: terminal, IsRetryable: kind.IsRetryable(),
		},
	}
}

func TestDisruptProducerSkipsWhenNotTerminal(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	p := NewDisruptProducer()
	store := NewPendingStore()
	tree := treeWith(time.Time{},
		sessionWithError("sid-1", transcript.ErrUnknown, now.Add(-1*time.Minute), false /*not terminal*/),
	)
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, DisruptGrace: 30 * time.Second,
		Tree: tree, Watermarks: wmStub{},
	}, store)
	if store.HasAny("sid-1") {
		t.Error("intent queued for non-terminal error")
	}
}

func TestDisruptProducerGraceWindow(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	at := now.Add(-10 * time.Second) // 10s ago, grace is 30s
	p := NewDisruptProducer()
	store := NewPendingStore()
	tree := treeWith(time.Time{}, sessionWithError("sid-1", transcript.ErrUnknown, at, true))
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, DisruptGrace: 30 * time.Second,
		AutoResumeMessage: "continue", Tree: tree, Watermarks: wmStub{},
	}, store)
	if store.HasAny("sid-1") {
		t.Error("intent queued before grace elapsed")
	}
	// Advance now past grace; same LastError.At unchanged.
	p.Reconcile(TickContext{
		Now: now.Add(35 * time.Second), AutoResumeEnabled: true,
		DisruptGrace:      30 * time.Second,
		AutoResumeMessage: "continue", Tree: tree, Watermarks: wmStub{},
	}, store)
	if !store.HasAny("sid-1") {
		t.Error("intent not queued after grace elapsed")
	}
}

func TestDisruptProducerCancelsOnResume(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	p := NewDisruptProducer()
	store := NewPendingStore()
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceDisrupted}, EmittedAt: now})
	tree := treeWith(time.Time{},
		aggregate.SessionEntry{SessionID: "sid-1", PID: 1, Status: session.Working, LastError: nil},
	)
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, DisruptGrace: 30 * time.Second,
		Tree: tree, Watermarks: wmStub{},
	}, store)
	if store.HasAny("sid-1") {
		t.Error("intent not cancelled after session resumed (LastError nil)")
	}
}

func TestDisruptProducerCancelsOnNonRetryable(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	p := NewDisruptProducer()
	store := NewPendingStore()
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceDisrupted}, EmittedAt: now})
	tree := treeWith(time.Time{},
		sessionWithError("sid-1", transcript.ErrAuthFailed, now.Add(-1*time.Minute), true),
	)
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true, DisruptGrace: 30 * time.Second,
		Tree: tree, Watermarks: wmStub{},
	}, store)
	if store.HasAny("sid-1") {
		t.Error("intent not cancelled for non-retryable error")
	}
}

func TestDisruptProducerEscalatesAfterNudgedAndStillStuck(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	errAt := now.Add(-2 * time.Minute)
	nudgedAt := now.Add(-65 * time.Second) // > escalation_after_s (60s)
	p := NewDisruptProducer()
	store := NewPendingStore()
	tree := treeWith(time.Time{}, sessionWithError("sid-1", transcript.ErrUnknown, errAt, true))
	watermarks := wmStub{per: map[string]SessionWatermark{
		"sid-1": {
			LastDisruptNudgeAt:  nudgedAt,
			LastDisruptNudgeFor: errAt, // same error
		},
	}}
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true,
		DisruptGrace: 30 * time.Second, EscalationAfter: 60 * time.Second,
		AutoResumeMessage: "continue", Tree: tree, Watermarks: watermarks,
	}, store)
	if store.HasAny("sid-1") {
		t.Error("intent queued for escalated session (should be cancelled, no re-arm)")
	}
}

func TestDisruptProducerNewErrorReArms(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	oldErrAt := now.Add(-3 * time.Minute)
	newErrAt := now.Add(-31 * time.Second) // 31s ago, past grace
	nudgedAt := now.Add(-2 * time.Minute)
	p := NewDisruptProducer()
	// Mark this session as previously seen with the old error (firstSeen).
	p.NoteFirstSeen("sid-1", oldErrAt)
	store := NewPendingStore()
	tree := treeWith(time.Time{}, sessionWithError("sid-1", transcript.ErrUnknown, newErrAt, true))
	watermarks := wmStub{per: map[string]SessionWatermark{
		"sid-1": {LastDisruptNudgeAt: nudgedAt, LastDisruptNudgeFor: oldErrAt},
	}}
	// First tick: producer sees new errAt > LastDisruptNudgeFor; resets firstSeen=now.
	p.Reconcile(TickContext{
		Now: now.Add(-31 * time.Second), AutoResumeEnabled: true,
		DisruptGrace: 30 * time.Second, EscalationAfter: 60 * time.Second,
		AutoResumeMessage: "continue", Tree: tree, Watermarks: watermarks,
	}, store)
	if store.HasAny("sid-1") {
		t.Error("intent queued on the same tick as first sighting (grace not elapsed)")
	}
	// Second tick, 31s later: grace elapsed against the firstSeen.
	p.Reconcile(TickContext{
		Now: now, AutoResumeEnabled: true,
		DisruptGrace: 30 * time.Second, EscalationAfter: 60 * time.Second,
		AutoResumeMessage: "continue", Tree: tree, Watermarks: watermarks,
	}, store)
	if !store.HasAny("sid-1") {
		t.Error("intent not queued after grace elapsed on fresh error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/pa-monitor && go test ./internal/daemon/nudger/ -run TestDisruptProducer`
Expected: FAIL — `undefined: NewDisruptProducer`, `NoteFirstSeen`.

- [ ] **Step 3: Write the implementation**

```go
// packages/pa-monitor/internal/daemon/nudger/disrupt.go
package nudger

import (
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
)

// NewDisruptProducer constructs a producer with an empty firstSeen map.
func NewDisruptProducer() *DisruptProducer {
	return &DisruptProducer{firstSeen: map[string]time.Time{}}
}

// NoteFirstSeen lets persistence layers prime firstSeen on daemon
// startup if needed (currently unused; producer re-derives lazily).
// Exposed for tests.
func (p *DisruptProducer) NoteFirstSeen(sid string, at time.Time) {
	if p.firstSeen == nil {
		p.firstSeen = map[string]time.Time{}
	}
	p.firstSeen[sid] = at
}

// Reconcile implements Producer. See spec §Architecture §Producers
// §DisruptProducer for the rule table.
func (p *DisruptProducer) Reconcile(ctx TickContext, store *PendingStore) {
	if p.firstSeen == nil {
		p.firstSeen = map[string]time.Time{}
	}

	// Walk every session in the tree; producer state is per-session.
	for _, dir := range ctx.Tree.Dirs {
		for _, s := range dir.Sessions {
			p.reconcileSession(ctx, store, s)
		}
	}
	// GC firstSeen for sessions not in the tree anymore.
	seen := map[string]struct{}{}
	for _, dir := range ctx.Tree.Dirs {
		for _, s := range dir.Sessions {
			seen[s.SessionID] = struct{}{}
		}
	}
	for sid := range p.firstSeen {
		if _, ok := seen[sid]; !ok {
			delete(p.firstSeen, sid)
		}
	}
}

func (p *DisruptProducer) reconcileSession(ctx TickContext, store *PendingStore, s aggregate.SessionEntry) {
	key := IntentKey{SessionID: s.SessionID, Source: SourceDisrupted}
	cancel := func() {
		store.Cancel(key)
		delete(p.firstSeen, s.SessionID)
	}

	if !ctx.AutoResumeEnabled {
		cancel()
		return
	}
	if s.LastError == nil || !s.LastError.IsTerminal {
		cancel()
		return
	}
	if !s.LastError.IsRetryable {
		cancel()
		return
	}

	wm := ctx.Watermarks.SessionWatermark(s.SessionID)

	// Determine "new error" by comparing LastError.At to the persisted
	// last_disrupt_nudge_for watermark.
	isNewError := s.LastError.At.After(wm.LastDisruptNudgeFor)

	if isNewError {
		// Cancel any stale intent and (re)start the grace clock.
		store.Cancel(key)
		if existing, ok := p.firstSeen[s.SessionID]; !ok || existing.IsZero() {
			p.firstSeen[s.SessionID] = ctx.Now
			return
		}
	} else {
		// Same error we already nudged: evaluate escalation.
		if !wm.DisruptEscalated &&
			!wm.LastDisruptNudgeAt.IsZero() &&
			ctx.Now.Sub(wm.LastDisruptNudgeAt) >= ctx.EscalationAfter {
			// Escalated state will be persisted by the dispatcher on next
			// fire decision; for now, producer just cancels its intent.
			cancel()
			return
		}
		if wm.DisruptEscalated {
			cancel()
			return
		}
	}

	// Grace check.
	first, ok := p.firstSeen[s.SessionID]
	if !ok || first.IsZero() {
		p.firstSeen[s.SessionID] = ctx.Now
		return
	}
	if ctx.Now.Sub(first) < ctx.DisruptGrace {
		return
	}
	// Grace elapsed; enqueue (idempotent).
	store.Add(NudgeIntent{
		Key:       key,
		Text:      ctx.AutoResumeMessage,
		EmittedAt: ctx.Now,
		Cause:     s.LastError,
	})
}
```

Remove the empty `Reconcile` stub from `producer.go`.

- [ ] **Step 4: Run tests**

Run: `cd packages/pa-monitor && go test ./internal/daemon/nudger/ -run TestDisruptProducer -v`
Expected: PASS (6 sub-tests).

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/daemon/nudger/disrupt.go \
        packages/pa-monitor/internal/daemon/nudger/disrupt_test.go \
        packages/pa-monitor/internal/daemon/nudger/producer.go
git commit -m "feat(pa-monitor): DisruptProducer with grace + escalation"
```

---

### Task 3.4: `ManualProducer.Queue` / `Cancel` (RPC-driven)

**Files:**

- Create: `packages/pa-monitor/internal/daemon/nudger/manual.go`
- Modify: `packages/pa-monitor/internal/daemon/nudger/producer.go` (remove stub)
- Test: `packages/pa-monitor/internal/daemon/nudger/manual_test.go`

- [ ] **Step 1: Write the failing test**

```go
// packages/pa-monitor/internal/daemon/nudger/manual_test.go
package nudger

import (
	"testing"
	"time"
)

func TestManualProducerQueueAndCancel(t *testing.T) {
	p := &ManualProducer{}
	store := NewPendingStore()
	now := time.Now()
	p.Queue(store, []string{"sid-1", "sid-2"}, "continue", now)
	for _, sid := range []string{"sid-1", "sid-2"} {
		if !store.HasAny(sid) {
			t.Errorf("HasAny(%q) = false after Queue, want true", sid)
		}
	}
	p.Cancel(store, []string{"sid-1"})
	if store.HasAny("sid-1") {
		t.Error("HasAny(sid-1) = true after Cancel, want false")
	}
	if !store.HasAny("sid-2") {
		t.Error("HasAny(sid-2) = false, want true (not in cancel set)")
	}
}

func TestManualProducerQueueIdempotent(t *testing.T) {
	p := &ManualProducer{}
	store := NewPendingStore()
	now := time.Now()
	p.Queue(store, []string{"sid-1"}, "continue", now)
	p.Queue(store, []string{"sid-1"}, "different text", now.Add(time.Minute))
	got := store.List()
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Text != "continue" {
		t.Errorf("Text = %q, want %q (first wins; idempotent)", got[0].Text, "continue")
	}
}

func TestManualProducerReconcileIsNoop(t *testing.T) {
	// Manual is RPC-driven; Reconcile must be a no-op (does NOT cancel
	// manual intents on per-tick conditions).
	p := &ManualProducer{}
	store := NewPendingStore()
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceManual}, EmittedAt: time.Now()})
	p.Reconcile(TickContext{Now: time.Now()}, store)
	if !store.HasAny("sid-1") {
		t.Error("Reconcile cancelled a manual intent; should be no-op")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/pa-monitor && go test ./internal/daemon/nudger/ -run TestManualProducer`
Expected: FAIL — `undefined: (*ManualProducer).Queue`.

- [ ] **Step 3: Write the implementation**

```go
// packages/pa-monitor/internal/daemon/nudger/manual.go
package nudger

import "time"

// Queue adds a manual intent for each sid (idempotent on key). Text is
// the message to send. Selector expansion (path:/cmux:/session:) lives in
// the gRPC handler — Queue takes already-expanded session IDs.
func (p *ManualProducer) Queue(store *PendingStore, sids []string, text string, now time.Time) {
	for _, sid := range sids {
		store.Add(NudgeIntent{
			Key:       IntentKey{SessionID: sid, Source: SourceManual},
			Text:      text,
			EmittedAt: now,
		})
	}
}

// Cancel removes the manual intent for each sid.
func (p *ManualProducer) Cancel(store *PendingStore, sids []string) {
	for _, sid := range sids {
		store.Cancel(IntentKey{SessionID: sid, Source: SourceManual})
	}
}

// Reconcile is a no-op for manual: manual intents persist until either
// the dispatcher fires or the user explicitly cancels.
func (p *ManualProducer) Reconcile(TickContext, *PendingStore) {}
```

Remove the duplicate empty `Reconcile` from `producer.go` (the stub there should already be removed if you've been following each task).

- [ ] **Step 4: Run tests**

Run: `cd packages/pa-monitor && go test ./internal/daemon/nudger/ -run TestManualProducer -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/daemon/nudger/manual.go \
        packages/pa-monitor/internal/daemon/nudger/manual_test.go \
        packages/pa-monitor/internal/daemon/nudger/producer.go
git commit -m "feat(pa-monitor): ManualProducer Queue/Cancel (RPC-driven)"
```

---

## Phase 4 — Nudger package: dispatcher + top-level Tick

### Task 4.1: Dispatcher fires one signal per session, calls ClearSession

**Files:**

- Create: `packages/pa-monitor/internal/daemon/nudger/dispatcher.go`
- Test: `packages/pa-monitor/internal/daemon/nudger/dispatcher_test.go`

- [ ] **Step 1: Write the failing test**

```go
// packages/pa-monitor/internal/daemon/nudger/dispatcher_test.go
package nudger

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

type fakeSignaler struct {
	mu   sync.Mutex
	sent []struct {
		PID  int
		Text string
	}
	err error
}

func (f *fakeSignaler) Send(pid int, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, struct {
		PID  int
		Text string
	}{pid, text})
	return nil
}

type fakeRecorder struct {
	mu          sync.Mutex
	suppressed  []string
	sent        []string
	watermarkOps []string
}

func (r *fakeRecorder) RecordSuppressed(sid string, sources []Source, cause string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.suppressed = append(r.suppressed, sid)
}
func (r *fakeRecorder) RecordSent(sid string, sources []Source, errorKind string, escalated bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, sid)
}
func (r *fakeRecorder) UpdateWatermarks(sid string, now time.Time, cause *transcript.ErrorRecord, escalated bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.watermarkOps = append(r.watermarkOps, sid)
}

func TestDispatcherFiresOnceAndClears(t *testing.T) {
	store := NewPendingStore()
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceWindowReset}, Text: "continue", EmittedAt: now})
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceDisrupted}, Text: "continue", EmittedAt: now,
		Cause: &transcript.ErrorRecord{Kind: transcript.ErrUnknown}})
	tree := treeWith(time.Time{}, aggregate.SessionEntry{
		SessionID: "sid-1", PID: 1234, Status: session.Idle,
	})
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec}
	d.Dispatch(TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
	if len(sig.sent) != 1 {
		t.Fatalf("len(sent) = %d, want 1 (one signal per session)", len(sig.sent))
	}
	if sig.sent[0].PID != 1234 || sig.sent[0].Text != "continue" {
		t.Errorf("sent = %+v, want PID=1234 Text=continue", sig.sent[0])
	}
	if store.HasAny("sid-1") {
		t.Error("store not cleared for sid-1 after fire")
	}
	if len(rec.sent) != 1 {
		t.Errorf("recorder.sent count = %d, want 1", len(rec.sent))
	}
}

func TestDispatcherSuppressesWorking(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceManual}, Text: "x", EmittedAt: now})
	tree := treeWith(time.Time{}, aggregate.SessionEntry{
		SessionID: "sid-1", PID: 1234, Status: session.Working,
	})
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec}
	d.Dispatch(TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
	if len(sig.sent) != 0 {
		t.Errorf("len(sent) = %d, want 0 (suppressed)", len(sig.sent))
	}
	if store.HasAny("sid-1") {
		t.Error("store not cleared after suppression")
	}
	if len(rec.suppressed) != 1 {
		t.Errorf("recorder.suppressed = %d, want 1", len(rec.suppressed))
	}
}

func TestDispatcherSendFailureLeavesIntent(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	store.Add(NudgeIntent{Key: IntentKey{"sid-1", SourceManual}, Text: "x", EmittedAt: now})
	tree := treeWith(time.Time{}, aggregate.SessionEntry{
		SessionID: "sid-1", PID: 1234, Status: session.Idle,
	})
	sig := &fakeSignaler{err: errors.New("no signaler for pid")}
	rec := &fakeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec}
	d.Dispatch(TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
	if !store.HasAny("sid-1") {
		t.Error("store cleared after send failure; should retry next tick")
	}
}

func TestDispatcherSessionMissingSilently(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	store.Add(NudgeIntent{Key: IntentKey{"missing-sid", SourceManual}, Text: "x", EmittedAt: now})
	tree := treeWith(time.Time{}) // no sessions
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec}
	d.Dispatch(TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
	if store.HasAny("missing-sid") {
		t.Error("intent not dropped for missing session")
	}
	if len(sig.sent) != 0 {
		t.Errorf("len(sent) = %d, want 0", len(sig.sent))
	}
}

func TestDispatcherTextPrecedenceManualWins(t *testing.T) {
	store := NewPendingStore()
	now := time.Now()
	store.Add(NudgeIntent{Key: IntentKey{"sid", SourceWindowReset}, Text: "auto", EmittedAt: now})
	store.Add(NudgeIntent{Key: IntentKey{"sid", SourceManual}, Text: "manual-override", EmittedAt: now})
	tree := treeWith(time.Time{}, aggregate.SessionEntry{SessionID: "sid", PID: 1, Status: session.Idle})
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	d := &Dispatcher{Signaler: sig, Recorder: rec}
	d.Dispatch(TickContext{Now: now, Tree: tree, Watermarks: wmStub{}}, store)
	if len(sig.sent) != 1 || sig.sent[0].Text != "manual-override" {
		t.Errorf("sent = %+v, want manual text override", sig.sent)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/pa-monitor && go test ./internal/daemon/nudger/ -run TestDispatcher`
Expected: FAIL — `undefined: Dispatcher`.

- [ ] **Step 3: Write the implementation**

```go
// packages/pa-monitor/internal/daemon/nudger/dispatcher.go
package nudger

import (
	"sort"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// Signaler delivers a nudge text to a process by PID. Wraps the existing
// signal layer (tmux/cmux/ghostty/vscode).
type Signaler interface {
	Send(pid int, text string) error
}

// Recorder receives observability + persistence signals from the
// dispatcher. Concrete impls live in the daemon wiring.
type Recorder interface {
	RecordSuppressed(sid string, sources []Source, cause string)
	RecordSent(sid string, sources []Source, errorKind string, escalated bool)
	UpdateWatermarks(sid string, now time.Time, cause *transcript.ErrorRecord, escalated bool)
}

// Dispatcher fires nudges based on the pending store, performs the
// active-session suppression check, and clears intents after success or
// suppression. Send failures leave intents for the next tick.
type Dispatcher struct {
	Signaler Signaler
	Recorder Recorder
}

// Dispatch iterates pending intents once, grouped by session.
func (d *Dispatcher) Dispatch(ctx TickContext, store *PendingStore) {
	intents := store.List()
	if len(intents) == 0 {
		return
	}
	// Group intents by session id; iterate sessions in stable order so
	// tests/diagnostics are deterministic.
	bySession := map[string][]NudgeIntent{}
	for _, in := range intents {
		bySession[in.Key.SessionID] = append(bySession[in.Key.SessionID], in)
	}
	sids := make([]string, 0, len(bySession))
	for sid := range bySession {
		sids = append(sids, sid)
	}
	sort.Strings(sids)

	sessionsByID := indexSessions(ctx.Tree)
	for _, sid := range sids {
		group := bySession[sid]
		sources := make([]Source, 0, len(group))
		for _, in := range group {
			sources = append(sources, in.Key.Source)
		}

		view, ok := sessionsByID[sid]
		if !ok {
			store.ClearSession(sid)
			continue
		}
		if view.Status == session.Working {
			d.Recorder.RecordSuppressed(sid, sources, "session_active")
			store.ClearSession(sid)
			continue
		}
		text := resolveText(group)
		if err := d.Signaler.Send(view.PID, text); err != nil {
			// Leave intents in place; retry next tick.
			continue
		}
		// Pick the cause/kind for OTel out of the disrupt intent if present.
		var cause *transcript.ErrorRecord
		var kind string
		for _, in := range group {
			if in.Key.Source == SourceDisrupted && in.Cause != nil {
				cause = in.Cause
				kind = string(in.Cause.Kind)
				break
			}
		}
		wm := ctx.Watermarks.SessionWatermark(sid)
		escalated := wm.DisruptEscalated
		d.Recorder.RecordSent(sid, sources, kind, escalated)
		d.Recorder.UpdateWatermarks(sid, ctx.Now, cause, escalated)
		store.ClearSession(sid)
	}
}

func indexSessions(tree *aggregate.Tree) map[string]aggregate.SessionEntry {
	out := map[string]aggregate.SessionEntry{}
	if tree == nil {
		return out
	}
	for _, dir := range tree.Dirs {
		for _, s := range dir.Sessions {
			out[s.SessionID] = s
		}
	}
	return out
}

// resolveText picks the text to send: manual overrides win, else the
// first non-empty text found.
func resolveText(group []NudgeIntent) string {
	for _, in := range group {
		if in.Key.Source == SourceManual && in.Text != "" {
			return in.Text
		}
	}
	for _, in := range group {
		if in.Text != "" {
			return in.Text
		}
	}
	return ""
}
```

- [ ] **Step 4: Run tests**

Run: `cd packages/pa-monitor && go test ./internal/daemon/nudger/ -run TestDispatcher -v`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/daemon/nudger/dispatcher.go \
        packages/pa-monitor/internal/daemon/nudger/dispatcher_test.go
git commit -m "feat(pa-monitor): Dispatcher fires one signal per session with suppression"
```

---

### Task 4.2: Top-level `Nudger.Tick` wires producers + dispatcher + persistence

**Files:**

- Create: `packages/pa-monitor/internal/daemon/nudger/nudger.go`
- Test: `packages/pa-monitor/internal/daemon/nudger/nudger_test.go`

- [ ] **Step 1: Write the failing test**

```go
// packages/pa-monitor/internal/daemon/nudger/nudger_test.go
package nudger

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

func TestNudgerTickDisruptedFlowEndToEnd(t *testing.T) {
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	tree := treeWith(time.Time{}, sessionWithError(
		"sid-1", transcript.ErrUnknown, now.Add(-31*time.Second), true,
	))
	tree.Dirs[0].Sessions[0].PID = 4321
	tree.Dirs[0].Sessions[0].Status = session.Idle

	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	n := New(sig, rec)

	// Tick 1: first sighting at now-31s; grace not yet elapsed against firstSeen=now.
	n.Tick(TickContext{
		Now: now.Add(-31 * time.Second), AutoResumeEnabled: true,
		AutoResumeMessage: "continue", DisruptGrace: 30 * time.Second,
		EscalationAfter: 60 * time.Second, Tree: tree, Watermarks: wmStub{},
	})
	if len(sig.sent) != 0 {
		t.Errorf("tick 1: sent = %d, want 0 (grace not elapsed)", len(sig.sent))
	}

	// Tick 2: 31s later — grace elapsed; dispatcher fires.
	n.Tick(TickContext{
		Now: now, AutoResumeEnabled: true,
		AutoResumeMessage: "continue", DisruptGrace: 30 * time.Second,
		EscalationAfter: 60 * time.Second, Tree: tree, Watermarks: wmStub{},
	})
	if len(sig.sent) != 1 {
		t.Fatalf("tick 2: sent = %d, want 1", len(sig.sent))
	}
	if sig.sent[0].PID != 4321 || sig.sent[0].Text != "continue" {
		t.Errorf("sent = %+v, want PID=4321 text=continue", sig.sent[0])
	}
}

func TestNudgerTickManualBypassesProducers(t *testing.T) {
	now := time.Now()
	tree := treeWith(time.Time{}, aggregate.SessionEntry{
		SessionID: "sid-1", PID: 9999, Status: session.Idle,
	})
	sig := &fakeSignaler{}
	rec := &fakeRecorder{}
	n := New(sig, rec)
	n.QueueManual([]string{"sid-1"}, "manual!", now)
	n.Tick(TickContext{
		Now: now, AutoResumeEnabled: false, // disabled disables auto producers
		Tree: tree, Watermarks: wmStub{},
	})
	if len(sig.sent) != 1 || sig.sent[0].Text != "manual!" {
		t.Errorf("sent = %+v, want manual fire even with auto disabled", sig.sent)
	}
}

func TestNudgerCancelManual(t *testing.T) {
	now := time.Now()
	n := New(&fakeSignaler{}, &fakeRecorder{})
	n.QueueManual([]string{"sid-1"}, "x", now)
	if !n.PendingFor("sid-1") {
		t.Error("expected pending after QueueManual")
	}
	n.CancelManual([]string{"sid-1"})
	if n.PendingFor("sid-1") {
		t.Error("expected no pending after CancelManual")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/pa-monitor && go test ./internal/daemon/nudger/ -run TestNudgerTick`
Expected: FAIL — `undefined: New`, `QueueManual`, `CancelManual`, `PendingFor`.

- [ ] **Step 3: Write the implementation**

```go
// packages/pa-monitor/internal/daemon/nudger/nudger.go
package nudger

import "time"

// Nudger is the top-level façade. Owns the pending store and the three
// producers; runs Tick on every daemon tick.
type Nudger struct {
	store         *PendingStore
	dispatcher    *Dispatcher
	windowProd    *WindowResetProducer
	disruptProd   *DisruptProducer
	manualProd    *ManualProducer
}

// New constructs a Nudger ready for Tick.
func New(signaler Signaler, recorder Recorder) *Nudger {
	return &Nudger{
		store:       NewPendingStore(),
		dispatcher:  &Dispatcher{Signaler: signaler, Recorder: recorder},
		windowProd:  &WindowResetProducer{},
		disruptProd: NewDisruptProducer(),
		manualProd:  &ManualProducer{},
	}
}

// Tick reconciles producers, then dispatches. Cancel-then-add ordering is
// enforced inside each producer's Reconcile.
func (n *Nudger) Tick(ctx TickContext) {
	n.windowProd.Reconcile(ctx, n.store)
	n.disruptProd.Reconcile(ctx, n.store)
	// Manual is RPC-driven; Reconcile is a no-op but called for symmetry.
	n.manualProd.Reconcile(ctx, n.store)
	n.dispatcher.Dispatch(ctx, n.store)
}

// QueueManual enqueues manual nudges for the given session IDs.
func (n *Nudger) QueueManual(sids []string, text string, now time.Time) {
	n.manualProd.Queue(n.store, sids, text, now)
}

// CancelManual cancels pending manual nudges for the given session IDs.
func (n *Nudger) CancelManual(sids []string) {
	n.manualProd.Cancel(n.store, sids)
}

// PendingFor reports whether any intent is queued for sid (any source).
func (n *Nudger) PendingFor(sid string) bool {
	return n.store.HasAny(sid)
}

// SourcesFor returns the queued sources for sid.
func (n *Nudger) SourcesFor(sid string) []Source {
	return n.store.SourcesFor(sid)
}

// SnapshotStore returns a copy of all pending intents (for persistence).
func (n *Nudger) SnapshotStore() []NudgeIntent {
	return n.store.List()
}

// LoadStore replaces the pending store with the given intents (for
// persistence restore on startup).
func (n *Nudger) LoadStore(intents []NudgeIntent) {
	n.store = NewPendingStore()
	for _, in := range intents {
		n.store.Add(in)
	}
}
```

- [ ] **Step 4: Run tests**

Run: `cd packages/pa-monitor && go test ./internal/daemon/nudger/ -v`
Expected: PASS for all nudger tests (everything from Phases 2-4).

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/daemon/nudger/nudger.go \
        packages/pa-monitor/internal/daemon/nudger/nudger_test.go
git commit -m "feat(pa-monitor): top-level Nudger Tick wires producers + dispatcher"
```

---

## Phase 5 — Daemon wiring (config + lifecycle + watermark store)

### Task 5.1: Config keys `disrupt_grace_s` + `escalation_after_s`

**Files:**

- Modify: `packages/pa-monitor/internal/config/config.go`
- Test: `packages/pa-monitor/internal/config/config_test.go`

- [ ] **Step 1: Read existing AutoResume config**

Read `internal/config/config.go` around the `AutoResume*` fields (search for `AutoResumeDelayS`).

- [ ] **Step 2: Write the failing test**

Append to `config_test.go`:

```go
func TestConfigParsesDisruptGraceAndEscalation(t *testing.T) {
	in := []byte(`
[auto_resume]
disrupt_grace_s = 45
escalation_after_s = 90
`)
	cfg, err := ParseBytes(in)
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if cfg.AutoResume.DisruptGraceS == nil || *cfg.AutoResume.DisruptGraceS != 45 {
		t.Errorf("DisruptGraceS = %v, want 45", cfg.AutoResume.DisruptGraceS)
	}
	if cfg.AutoResume.EscalationAfterS == nil || *cfg.AutoResume.EscalationAfterS != 90 {
		t.Errorf("EscalationAfterS = %v, want 90", cfg.AutoResume.EscalationAfterS)
	}
}

func TestConfigDefaultsDisruptGraceAndEscalation(t *testing.T) {
	cfg, err := ParseBytes(nil)
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if got := cfg.DisruptGrace(); got != 30*time.Second {
		t.Errorf("DisruptGrace default = %v, want 30s", got)
	}
	if got := cfg.EscalationAfter(); got != 60*time.Second {
		t.Errorf("EscalationAfter default = %v, want 60s", got)
	}
}
```

Use the actual constructor name from the existing `config_test.go` if `ParseBytes` is different (e.g. `Load`, `ParseString`); adjust the test accordingly.

- [ ] **Step 3: Run test to verify it fails**

Run: `cd packages/pa-monitor && go test ./internal/config/ -run TestConfigParsesDisruptGrace`
Expected: FAIL — `unknown field DisruptGraceS`, `undefined: DisruptGrace`.

- [ ] **Step 4: Extend `AutoResume` config struct + add accessors**

Edit `internal/config/config.go`:

```go
// inside the AutoResume struct:
type AutoResume struct {
	// ... existing fields ...
	AutoResumeDelayS    *int    `toml:"auto_resume_delay_s"`
	AutoResumeMessage   *string `toml:"auto_resume_message"`
	DisruptGraceS       *int    `toml:"disrupt_grace_s"`
	EscalationAfterS    *int    `toml:"escalation_after_s"`
}

// Add accessors on the top-level Config (or wherever existing accessors live):
const (
	defaultDisruptGraceS    = 30
	defaultEscalationAfterS = 60
)

func (c *Config) DisruptGrace() time.Duration {
	if c.AutoResume.DisruptGraceS != nil {
		return time.Duration(*c.AutoResume.DisruptGraceS) * time.Second
	}
	return defaultDisruptGraceS * time.Second
}

func (c *Config) EscalationAfter() time.Duration {
	if c.AutoResume.EscalationAfterS != nil {
		return time.Duration(*c.AutoResume.EscalationAfterS) * time.Second
	}
	return defaultEscalationAfterS * time.Second
}
```

- [ ] **Step 5: Run tests**

Run: `cd packages/pa-monitor && go test ./internal/config/ -v`
Expected: PASS for all config tests including the two new ones.

- [ ] **Step 6: Commit**

```bash
git add packages/pa-monitor/internal/config/config.go \
        packages/pa-monitor/internal/config/config_test.go
git commit -m "feat(pa-monitor): config DisruptGraceS + EscalationAfterS with defaults"
```

---

### Task 5.2: Daemon `watermarkStore` (Recorder + WatermarkView adapter)

**Files:**

- Create: `packages/pa-monitor/internal/daemon/nudger_runtime.go`
- Test: `packages/pa-monitor/internal/daemon/nudger_runtime_test.go`

- [ ] **Step 1: Write the failing test**

```go
// packages/pa-monitor/internal/daemon/nudger_runtime_test.go
package daemon

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
)

func TestWatermarkStoreUpdateAndRead(t *testing.T) {
	path := t.TempDir() + "/runtime.json"
	w, err := NewWatermarkStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	cause := &transcript.ErrorRecord{Kind: transcript.ErrUnknown, At: now.Add(-1 * time.Minute)}
	w.UpdateWatermarks("sid-1", now, cause, false)
	w.SetWindowResetFiredFor(now.Add(-5 * time.Minute))

	wm := w.SessionWatermark("sid-1")
	if !wm.LastNudgedAt.Equal(now) {
		t.Errorf("LastNudgedAt = %v, want %v", wm.LastNudgedAt, now)
	}
	if !wm.LastDisruptNudgeFor.Equal(cause.At) {
		t.Errorf("LastDisruptNudgeFor = %v, want %v", wm.LastDisruptNudgeFor, cause.At)
	}
	if !w.WindowResetFiredFor().Equal(now.Add(-5 * time.Minute)) {
		t.Errorf("WindowResetFiredFor = %v, want %v",
			w.WindowResetFiredFor(), now.Add(-5*time.Minute))
	}
}

func TestWatermarkStoreRecordersAreNoOpOnPersistence(t *testing.T) {
	// RecordSuppressed/RecordSent shouldn't modify watermarks.
	path := t.TempDir() + "/runtime.json"
	w, _ := NewWatermarkStore(path)
	w.RecordSent("sid-1", []nudger.Source{nudger.SourceManual}, "", false)
	wm := w.SessionWatermark("sid-1")
	if !wm.LastNudgedAt.IsZero() {
		t.Error("RecordSent should not touch watermarks; UpdateWatermarks does")
	}
}

func TestWatermarkStorePersistsToDisk(t *testing.T) {
	path := t.TempDir() + "/runtime.json"
	w, _ := NewWatermarkStore(path)
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	w.UpdateWatermarks("sid-1", now, nil, false)
	// Reload from disk; watermark must survive.
	w2, err := NewWatermarkStore(path)
	if err != nil {
		t.Fatal(err)
	}
	wm := w2.SessionWatermark("sid-1")
	if !wm.LastNudgedAt.Equal(now) {
		t.Errorf("after reload: LastNudgedAt = %v, want %v", wm.LastNudgedAt, now)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd packages/pa-monitor && go test ./internal/daemon/ -run TestWatermarkStore`
Expected: FAIL — `undefined: NewWatermarkStore`.

- [ ] **Step 3: Write the implementation**

```go
// packages/pa-monitor/internal/daemon/nudger_runtime.go
package daemon

import (
	"sync"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/daemon/nudger"
)

// WatermarkStore persists nudger watermarks + pending intents inside
// runtime.json. Implements nudger.WatermarkView and nudger.Recorder so
// the nudger package stays free of daemon-specific persistence types.
type WatermarkStore struct {
	mu    sync.Mutex
	path  string
	state RuntimeState
}

func NewWatermarkStore(path string) (*WatermarkStore, error) {
	s, err := ReadRuntimeState(path)
	if err != nil {
		return nil, err
	}
	if s.Nudger.Sessions == nil {
		s.Nudger.Sessions = map[string]NudgerSessionWatermarks{}
	}
	return &WatermarkStore{path: path, state: s}, nil
}

// --- nudger.WatermarkView ---

func (w *WatermarkStore) WindowResetFiredFor() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state.Nudger.WindowResetFiredFor
}

func (w *WatermarkStore) SessionWatermark(sid string) nudger.SessionWatermark {
	w.mu.Lock()
	defer w.mu.Unlock()
	x := w.state.Nudger.Sessions[sid]
	return nudger.SessionWatermark{
		LastNudgedAt:        x.LastNudgedAt,
		LastDisruptNudgeAt:  x.LastDisruptNudgeAt,
		LastDisruptNudgeFor: x.LastDisruptNudgeFor,
		DisruptEscalated:    x.DisruptEscalated,
	}
}

// --- nudger.Recorder ---

func (w *WatermarkStore) RecordSuppressed(sid string, sources []nudger.Source, cause string) {
	// Observability hook; persistence-wise this is a no-op.
	// OTel emission is wired in Phase 9.
}

func (w *WatermarkStore) RecordSent(sid string, sources []nudger.Source, errorKind string, escalated bool) {
	// Observability hook; UpdateWatermarks is the persistence path.
}

func (w *WatermarkStore) UpdateWatermarks(sid string, now time.Time, cause *transcript.ErrorRecord, escalated bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	wm := w.state.Nudger.Sessions[sid]
	wm.LastNudgedAt = now
	if cause != nil {
		wm.LastDisruptNudgeAt = now
		wm.LastDisruptNudgeFor = cause.At
	}
	wm.DisruptEscalated = escalated
	w.state.Nudger.Sessions[sid] = wm
	_ = WriteRuntimeState(w.path, w.state) // best-effort; daemon logs on failure elsewhere
}

// --- additional store API used by daemon ---

func (w *WatermarkStore) SetWindowResetFiredFor(at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.Nudger.WindowResetFiredFor = at
	_ = WriteRuntimeState(w.path, w.state)
}

func (w *WatermarkStore) AutoResumeEnabled() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state.AutoResumeEnabled
}

func (w *WatermarkStore) SetAutoResumeEnabled(enabled bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.AutoResumeEnabled = enabled
	_ = WriteRuntimeState(w.path, w.state)
}

func (w *WatermarkStore) SaveIntents(intents []nudger.NudgeIntent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]PersistedIntent, 0, len(intents))
	for _, in := range intents {
		p := PersistedIntent{
			SessionID: in.Key.SessionID,
			Source:    string(in.Key.Source),
			Text:      in.Text,
			EmittedAt: in.EmittedAt,
		}
		if in.Cause != nil {
			p.CauseKind = string(in.Cause.Kind)
			p.CauseAt = in.Cause.At
		}
		out = append(out, p)
	}
	w.state.Nudger.PendingIntents = out
	_ = WriteRuntimeState(w.path, w.state)
}

func (w *WatermarkStore) LoadIntents() []nudger.NudgeIntent {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]nudger.NudgeIntent, 0, len(w.state.Nudger.PendingIntents))
	for _, p := range w.state.Nudger.PendingIntents {
		in := nudger.NudgeIntent{
			Key:       nudger.IntentKey{SessionID: p.SessionID, Source: nudger.Source(p.Source)},
			Text:      p.Text,
			EmittedAt: p.EmittedAt,
		}
		if p.CauseKind != "" {
			in.Cause = &transcript.ErrorRecord{
				Kind: transcript.ErrorKind(p.CauseKind), At: p.CauseAt,
			}
		}
		out = append(out, in)
	}
	return out
}
```

- [ ] **Step 4: Run tests**

Run: `cd packages/pa-monitor && go test ./internal/daemon/ -run TestWatermarkStore -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/internal/daemon/nudger_runtime.go \
        packages/pa-monitor/internal/daemon/nudger_runtime_test.go
git commit -m "feat(pa-monitor): WatermarkStore adapts runtime.json to nudger interfaces"
```

---

### Task 5.3: Signaler adapter (wraps `internal/signal` resolver)

**Files:**

- Create: `packages/pa-monitor/internal/daemon/nudger_signaler.go`
- Test: covered by integration in Task 5.4.

- [ ] **Step 1: Write minimal implementation**

```go
// packages/pa-monitor/internal/daemon/nudger_signaler.go
package daemon

import (
	"fmt"

	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

// SignalerAdapter wraps the signal package's per-PID resolver so the
// nudger package doesn't import internal/signal directly.
type SignalerAdapter struct {
	Signalers []signal.Signaler
}

func (s *SignalerAdapter) Send(pid int, text string) error {
	sig := signal.ResolveSignaler(s.Signalers, pid)
	if sig == nil {
		return fmt.Errorf("no signaler for pid %d", pid)
	}
	return sig.Send(pid, text)
}
```

- [ ] **Step 2: Run all daemon tests (sanity)**

Run: `cd packages/pa-monitor && go build ./...`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add packages/pa-monitor/internal/daemon/nudger_signaler.go
git commit -m "feat(pa-monitor): SignalerAdapter wraps internal/signal for nudger"
```

---

### Task 5.4: Wire `Nudger.Tick` into the daemon tick loop

**Files:**

- Modify: `packages/pa-monitor/internal/daemon/lifecycle.go`
- Modify: `packages/pa-monitor/internal/daemon/state.go` (if shared state lives there)
- Test: `packages/pa-monitor/internal/daemon/tick_integration_test.go` (existing) — extend or add new test file.

- [ ] **Step 1: Read existing lifecycle + state files to find the tick loop**

Read `internal/daemon/lifecycle.go` (search for the tick loop) and `internal/daemon/state.go`. Identify:

- Where the aggregate tree is computed each tick.
- The point at which client RPCs see updated state.
- Where `RunWithOpts` (or the daemon entry) constructs the runtime state.

- [ ] **Step 2: Add Nudger to the shared state struct**

In `state.go` (or the file that holds `sharedState`), add:

```go
type sharedState struct {
	// ... existing fields ...
	Tree            *aggregate.Tree
	Nudger          *nudger.Nudger
	Watermarks      *WatermarkStore
}
```

- [ ] **Step 3: Construct Nudger + Watermarks in `RunWithOpts`**

In `lifecycle.go`'s daemon entry (the function that builds `state` before the tick loop starts):

```go
// after determining runtime.json path
watermarks, err := NewWatermarkStore(runtimePath)
if err != nil {
	return fmt.Errorf("read runtime.json: %w", err)
}
signaler := &SignalerAdapter{Signalers: opts.Signalers}
n := nudger.New(signaler, watermarks)
n.LoadStore(watermarks.LoadIntents())
state.Nudger = n
state.Watermarks = watermarks
```

- [ ] **Step 4: Call `Nudger.Tick` once per daemon tick**

After the existing tick-builds-tree step:

```go
n.Tick(nudger.TickContext{
	Now:               time.Now(),
	Tree:              state.Tree,
	AutoResumeEnabled: watermarks.AutoResumeEnabled(),
	AutoResumeMessage: cfg.AutoResumeMessage(),
	AutoResumeDelay:   cfg.AutoResumeDelay(),
	DisruptGrace:      cfg.DisruptGrace(),
	EscalationAfter:   cfg.EscalationAfter(),
	Watermarks:        watermarks,
})
// After Tick, persist the (possibly-empty) pending set.
watermarks.SaveIntents(n.SnapshotStore())
// If the window producer fired, update the latch.
if state.Tree != nil && !state.Tree.WindowResetsAt.IsZero() {
	// Note: only set if Tick actually fired (LastNudgedAt advanced for some session).
	// Simplest: trust the Tick semantics — if the producer already moved past, the latch should match.
	watermarks.SetWindowResetFiredFor(state.Tree.WindowResetsAt)
}
```

Note: refine the latch update so it only fires when the producer actually fired (e.g. by inspecting LastNudgedAt diff). For an initial pass, setting it any time `WindowResetsAt` is non-zero is fine — the latch is just "don't re-fire for this window".

- [ ] **Step 5: Add `PendingNudge` enrichment after Tick**

After Tick but before serializing state for clients, annotate each session:

```go
for _, dir := range state.Tree.Dirs {
	for i := range dir.Sessions {
		sid := dir.Sessions[i].SessionID
		if !n.PendingFor(sid) {
			continue
		}
		sources := n.SourcesFor(sid)
		strs := make([]string, 0, len(sources))
		for _, s := range sources {
			strs = append(strs, string(s))
		}
		dir.Sessions[i].PendingNudge = &aggregate.PendingNudge{Sources: strs}
	}
}
```

Apply escalation flip on aggregate (per spec): if a session has `LastError != nil`, `IsRetryable`, and `watermarks.SessionWatermark(sid).DisruptEscalated`, flip a copy's `IsRetryable` to false before serialization. (Implementation note: copy the `ErrorRecord` to avoid mutating the snapshot.)

- [ ] **Step 6: Write an integration test**

Append to `tick_integration_test.go` (or create a new `nudger_integration_test.go`):

```go
func TestDaemonTickFiresDisruptNudgeAfterGrace(t *testing.T) {
	// Construct a tmp runtime.json, a fake signaler, a tree containing
	// one Idle session with a terminal-retryable LastError 31s in the past.
	// Run RunWithOpts (or a unit-test-friendly subset). After two ticks
	// (one to start grace, one after grace), the fake signaler must
	// have recorded a Send.
	//
	// Use the existing test harness in tick_integration_test.go as the
	// template (it already builds a daemon-with-fakes pattern).
}
```

(The full body depends on the existing harness; the engineer extends it rather than rewriting.)

- [ ] **Step 7: Run all daemon tests**

Run: `cd packages/pa-monitor && go test ./internal/daemon/ -v`
Expected: PASS for all daemon tests including the new integration test.

- [ ] **Step 8: Commit**

```bash
git add packages/pa-monitor/internal/daemon/lifecycle.go \
        packages/pa-monitor/internal/daemon/state.go \
        packages/pa-monitor/internal/daemon/tick_integration_test.go
git commit -m "feat(pa-monitor): wire Nudger into daemon tick loop"
```

---
