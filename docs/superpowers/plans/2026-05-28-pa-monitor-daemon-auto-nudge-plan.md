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
