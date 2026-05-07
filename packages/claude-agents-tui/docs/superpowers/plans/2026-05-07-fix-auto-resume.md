# Fix Auto-Resume Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect rate-limit pause from the synthetic-assistant transcript shape (Claude Code ≥ 2.1.126) so the pause glyph + countdown render, and add an [M] key that fires the auto-resume message immediately to all non-Working sessions.

**Architecture:** Two narrow, independent changes.

1. `internal/transcript`: extend the JSONL scanner to recognize a new rate-limit event shape (`type=assistant`, top-level `error="rate_limit"`, `isApiErrorMessage=true`, reset time encoded only in message text). Add a small text parser that resolves `H:MMam/pm (IANA-TZ)` against the event timestamp.
2. `internal/tui`: add an `M` key that fires `m.autoResumeMessage` to every non-Working session via the existing `signal.Signaler` plumbing, independent of `m.autoResume`.

**Tech Stack:** Go, `bubbletea` TUI, JSONL transcripts.

**Spec:** `docs/superpowers/specs/2026-05-07-fix-auto-resume-design.md`

**Run all tests from:** `packages/claude-agents-tui/` (paths below assume that CWD).

---

## File Structure

| File | Status | Responsibility |
|------|--------|----------------|
| `internal/transcript/ratelimit.go` | modify | Add `parseLimitResetText`. Extend `RateLimitPause` to detect synthetic-assistant shape. |
| `internal/transcript/ratelimit_test.go` | modify | Add tests for `parseLimitResetText` and synthetic-shape detection. |
| `internal/transcript/snapshot.go` | modify | In `Scan`, detect synthetic shape via auxiliary unmarshal; share text-parsing logic. |
| `internal/transcript/snapshot_test.go` | modify | Add Scan test for synthetic shape. |
| `internal/tui/update.go` | modify | Handle `M` key: send to all non-Working sessions. |
| `internal/tui/model_test.go` | modify | Test `M` key fires regardless of `autoResume` toggle. |
| `internal/render/header.go` | modify | Append `[M] resume now` hint to controls line. |
| `internal/render/header_test.go` | modify | Assert `[M]` hint present in toggle test. |

---

## Task 1: Add `parseLimitResetText` (TDD)

**Files:**
- Modify: `internal/transcript/ratelimit.go`
- Test: `internal/transcript/ratelimit_test.go`

The parser takes the synthetic message text and the event timestamp; returns the next occurrence of the parsed clock-time in the named TZ at or after the event timestamp. Returns `(zero, false)` when the text doesn't match.

Examples it must handle:
- `"You've hit your limit · resets 3:30pm (America/New_York)"`
- `"You've hit your limit · resets 7:10pm (America/New_York)"`
- `"You've hit your limit · resets 12:05am (America/Los_Angeles)"`
- `"You've hit your limit · resets 1:00am (UTC)"`

- [ ] **Step 1: Write failing tests**

Append to `internal/transcript/ratelimit_test.go`:

```go
func TestParseLimitResetTextStandard(t *testing.T) {
	// Event at 2026-05-05 17:12:37 UTC → 13:12:37 EDT.
	// "3:30pm (America/New_York)" → 19:30 UTC same day.
	ev := time.Date(2026, 5, 5, 17, 12, 37, 0, time.UTC)
	got, ok := parseLimitResetText("You've hit your limit · resets 3:30pm (America/New_York)", ev)
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := time.Date(2026, 5, 5, 19, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got.UTC(), want)
	}
}

func TestParseLimitResetTextDayRollover(t *testing.T) {
	// Event at 2026-05-05 23:00:00 UTC → 19:00 EDT.
	// "1:00am (America/New_York)" parsed for 2026-05-05 = 05:00 UTC same day, which
	// is BEFORE the event time. Expect rollover to 2026-05-06 05:00 UTC.
	ev := time.Date(2026, 5, 5, 23, 0, 0, 0, time.UTC)
	got, ok := parseLimitResetText("You've hit your limit · resets 1:00am (America/New_York)", ev)
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := time.Date(2026, 5, 6, 5, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got.UTC(), want)
	}
}

func TestParseLimitResetTextTwelveHour(t *testing.T) {
	ev := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Time
	}{
		{"resets 12:00am (UTC)", time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)},
		{"resets 12:30am (UTC)", time.Date(2026, 5, 5, 0, 30, 0, 0, time.UTC)},
		{"resets 12:00pm (UTC)", time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)},
		{"resets 12:30pm (UTC)", time.Date(2026, 5, 5, 12, 30, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		got, ok := parseLimitResetText(c.in, ev)
		if !ok {
			t.Errorf("%q: ok=false, want true", c.in)
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("%q: got %v, want %v", c.in, got.UTC(), c.want)
		}
	}
}

func TestParseLimitResetTextRejectsUnknownText(t *testing.T) {
	ev := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	cases := []string{
		"unrelated text",
		"resets soon",
		"resets 3:30pm",                       // no TZ
		"resets 25:00am (UTC)",                // bad hour
		"resets 3:60pm (UTC)",                 // bad minute
		"resets 3:30pm (Not/A_Real_Zone_Foo)", // bad TZ
	}
	for _, c := range cases {
		if _, ok := parseLimitResetText(c, ev); ok {
			t.Errorf("%q: ok=true, want false", c)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/transcript/ -run 'TestParseLimitResetText'`
Expected: FAIL with `undefined: parseLimitResetText`.

- [ ] **Step 3: Implement parser**

Add to `internal/transcript/ratelimit.go` (above `RateLimitPause`):

```go
import (
	"regexp"
	"strconv"
	"strings"
)

// limitResetRe captures hour, minute, am/pm marker, and IANA TZ id.
// Matches strings like: "resets 3:30pm (America/New_York)".
var limitResetRe = regexp.MustCompile(`resets\s+(\d{1,2}):(\d{2})(am|pm)\s+\(([^)]+)\)`)

// parseLimitResetText resolves the next occurrence of the clock time + IANA TZ
// in the message at or after eventTime. Returns (zero, false) on any parse
// failure (bad clock time, unknown TZ, regex miss).
func parseLimitResetText(text string, eventTime time.Time) (time.Time, bool) {
	m := limitResetRe.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}, false
	}
	hour, err := strconv.Atoi(m[1])
	if err != nil || hour < 1 || hour > 12 {
		return time.Time{}, false
	}
	minute, err := strconv.Atoi(m[2])
	if err != nil || minute < 0 || minute > 59 {
		return time.Time{}, false
	}
	switch strings.ToLower(m[3]) {
	case "am":
		if hour == 12 {
			hour = 0
		}
	case "pm":
		if hour != 12 {
			hour += 12
		}
	}
	loc, err := time.LoadLocation(m[4])
	if err != nil {
		return time.Time{}, false
	}
	evLocal := eventTime.In(loc)
	candidate := time.Date(evLocal.Year(), evLocal.Month(), evLocal.Day(), hour, minute, 0, 0, loc)
	if !candidate.After(eventTime) {
		candidate = candidate.Add(24 * time.Hour)
	}
	return candidate.UTC(), true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/transcript/ -run 'TestParseLimitResetText' -v`
Expected: PASS for all four tests.

- [ ] **Step 5: Commit**

```bash
git add internal/transcript/ratelimit.go internal/transcript/ratelimit_test.go
git commit -m "feat(transcript): parseLimitResetText resolves H:MMam/pm + IANA TZ"
```

---

## Task 2: Detect synthetic-assistant rate-limit shape in `RateLimitPause` (TDD)

**Files:**
- Modify: `internal/transcript/ratelimit.go`
- Test: `internal/transcript/ratelimit_test.go`

`RateLimitPause` currently scans for `system/api_error/rate_limit_error` only. Extend it to also recognize the synthetic shape and use the message text to compute the reset time.

- [ ] **Step 1: Write failing tests**

Add to `internal/transcript/ratelimit_test.go`:

```go
// syntheticRateLimitEvent returns a JSONL line for the new rate-limit shape.
func syntheticRateLimitEvent(ts time.Time, text string) string {
	return `{"type":"assistant","timestamp":"` + ts.UTC().Format(time.RFC3339Nano) +
		`","message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"` +
		text + `"}]},"error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429}`
}

func TestRateLimitPauseDetectsSyntheticAssistant(t *testing.T) {
	ts := time.Date(2026, 5, 5, 17, 12, 37, 0, time.UTC) // 13:12 EDT
	path := t.TempDir() + "/t.jsonl"
	body := syntheticRateLimitEvent(ts, "You've hit your limit · resets 3:30pm (America/New_York)") + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, err := RateLimitPause(path)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := time.Date(2026, 5, 5, 19, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got.UTC(), want)
	}
}

func TestRateLimitPauseSyntheticClearedByLaterUser(t *testing.T) {
	ts := time.Date(2026, 5, 5, 17, 12, 37, 0, time.UTC)
	path := t.TempDir() + "/t.jsonl"
	body := syntheticRateLimitEvent(ts, "You've hit your limit · resets 3:30pm (America/New_York)") + "\n" +
		`{"type":"user","timestamp":"2026-05-05T19:35:00Z","message":{"role":"user","content":"continue"}}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, err := RateLimitPause(path)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !got.IsZero() {
		t.Errorf("got %v, want zero (user resumed after rate-limit)", got)
	}
}

func TestRateLimitPauseSyntheticIgnoredWhenTextUnparseable(t *testing.T) {
	ts := time.Date(2026, 5, 5, 17, 12, 37, 0, time.UTC)
	path := t.TempDir() + "/t.jsonl"
	body := syntheticRateLimitEvent(ts, "You've hit your limit · come back tomorrow") + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	got, err := RateLimitPause(path)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !got.IsZero() {
		t.Errorf("got %v, want zero (text not parseable)", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/transcript/ -run 'TestRateLimitPauseDetectsSyntheticAssistant|TestRateLimitPauseSyntheticClearedByLaterUser|TestRateLimitPauseSyntheticIgnoredWhenTextUnparseable' -v`
Expected: FAIL — current code returns zero for the synthetic shape.

- [ ] **Step 3: Extend `RateLimitPause` to handle the synthetic shape**

Replace the body of `RateLimitPause` in `internal/transcript/ratelimit.go` with:

```go
func RateLimitPause(path string) (resetsAt time.Time, err error) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()

	type rateLimitScan struct {
		Type      string    `json:"type"`
		Subtype   string    `json:"subtype"`
		Timestamp time.Time `json:"timestamp"`
		RetryInMs int64     `json:"retryInMs"`
		Error     struct {
			Error struct {
				Error struct {
					Type string `json:"type"`
				} `json:"error"`
			} `json:"error"`
		} `json:"error"`
	}
	type syntheticScan struct {
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
		Type string `json:"type"`
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
		return time.Time{}, sc.Err()
	}

	// Find index of last rate-limit event (either shape) and compute its absolute reset time.
	lastIdx := -1
	var lastResetsAt time.Time
	for i, line := range lines {
		// Old shape: system/api_error/rate_limit_error/retryInMs.
		var ev rateLimitScan
		if err := json.Unmarshal(line, &ev); err == nil &&
			ev.Type == "system" && ev.Subtype == "api_error" &&
			ev.Error.Error.Error.Type == "rate_limit_error" && ev.RetryInMs > 0 {
			lastIdx = i
			lastResetsAt = ev.Timestamp.Add(time.Duration(ev.RetryInMs) * time.Millisecond)
			continue
		}
		// New synthetic-assistant shape: error="rate_limit" + isApiErrorMessage.
		var s syntheticScan
		if err := json.Unmarshal(line, &s); err == nil &&
			s.Type == "assistant" && s.Error == "rate_limit" && s.IsApiErrorMessage {
			var text string
			for _, b := range s.Message.Content {
				if b.Type == "text" {
					text = b.Text
					break
				}
			}
			if t, ok := parseLimitResetText(text, s.Timestamp); ok {
				lastIdx = i
				lastResetsAt = t
			}
		}
	}
	if lastIdx < 0 {
		return time.Time{}, nil
	}

	// If a *non-synthetic* user or assistant event follows the rate-limit, the session resumed.
	for _, line := range lines[lastIdx+1:] {
		var ev typeOnly
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type != "user" && ev.Type != "assistant" {
			continue
		}
		// A synthetic rate-limit assistant must NOT count as a resume. Re-parse to check.
		var s syntheticScan
		if json.Unmarshal(line, &s) == nil &&
			s.Type == "assistant" && s.Error == "rate_limit" && s.IsApiErrorMessage {
			continue
		}
		return time.Time{}, nil
	}

	return lastResetsAt, nil
}
```

- [ ] **Step 4: Run all `ratelimit_test.go` tests to verify they pass**

Run: `go test ./internal/transcript/ -run 'TestRateLimitPause|TestParseLimitResetText' -v`
Expected: PASS for all (including the pre-existing five `TestRateLimitPause*` tests).

- [ ] **Step 5: Commit**

```bash
git add internal/transcript/ratelimit.go internal/transcript/ratelimit_test.go
git commit -m "fix(transcript): detect rate-limit pause from synthetic-assistant shape"
```

---

## Task 3: Mirror synthetic-shape detection in `Scan` (TDD)

**Files:**
- Modify: `internal/transcript/snapshot.go`
- Test: `internal/transcript/snapshot_test.go`

`Scan` is the single-pass enrichment used by the poller. It must populate `RateLimitResetsAt` from the synthetic shape too.

- [ ] **Step 1: Write failing test**

Add to `internal/transcript/snapshot_test.go`:

```go
func TestScanSyntheticRateLimit(t *testing.T) {
	ts := time.Date(2026, 5, 5, 17, 12, 37, 0, time.UTC)
	path := t.TempDir() + "/synth.jsonl"
	body := `{"type":"assistant","timestamp":"` + ts.UTC().Format(time.RFC3339Nano) +
		`","message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"You've hit your limit · resets 3:30pm (America/New_York)"}]},"error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	snap, err := Scan(path)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := time.Date(2026, 5, 5, 19, 30, 0, 0, time.UTC)
	if !snap.RateLimitResetsAt.Equal(want) {
		t.Errorf("RateLimitResetsAt = %v, want %v", snap.RateLimitResetsAt.UTC(), want)
	}
}

func TestScanSyntheticRateLimitClearedByLaterUser(t *testing.T) {
	ts := time.Date(2026, 5, 5, 17, 12, 37, 0, time.UTC)
	path := t.TempDir() + "/synth_cleared.jsonl"
	body := `{"type":"assistant","timestamp":"` + ts.UTC().Format(time.RFC3339Nano) +
		`","message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"You've hit your limit · resets 3:30pm (America/New_York)"}]},"error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429}` + "\n" +
		`{"type":"user","timestamp":"2026-05-05T19:35:00Z","message":{"role":"user","content":"continue"}}` + "\n"
	if err := writeTestFile(path, body); err != nil {
		t.Fatal(err)
	}
	snap, err := Scan(path)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !snap.RateLimitResetsAt.IsZero() {
		t.Errorf("RateLimitResetsAt = %v, want zero (user resumed)", snap.RateLimitResetsAt)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/transcript/ -run 'TestScanSyntheticRateLimit' -v`
Expected: FAIL — `Scan` currently misses the synthetic shape.

- [ ] **Step 3: Extend `Scan` to detect the synthetic shape**

Edit `internal/transcript/snapshot.go`. Inside the `for sc.Scan()` loop, BEFORE the existing `for _, b := range ev.Message.Content { ... }` block (so the auxiliary fields are read once per line), add:

```go
		// Auxiliary parse: only the synthetic-assistant rate-limit shape sets
		// these top-level fields. Failure leaves all zero values (old shape).
		var aux struct {
			Error             string `json:"error"`
			IsApiErrorMessage bool   `json:"isApiErrorMessage"`
		}
		_ = json.Unmarshal(sc.Bytes(), &aux)
		isSyntheticRateLimit := ev.Type == "assistant" && aux.Error == "rate_limit" && aux.IsApiErrorMessage
```

The auxiliary parse fails for the *old* shape (where `error` is an object), and `json.Unmarshal` leaves `aux` zero — that path is fine.

Then change the `case "assistant":` block to handle synthetic events specially. Replace:

```go
		case "assistant":
			u := ev.Message.Usage
			ctx := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
			if ctx > 0 {
				lastCtxTotal = ctx
				lastCtxModel = ev.Message.Model
			}
			totalOut += u.OutputTokens
			pendingAUQ = make(map[string]bool)
			for _, b := range ev.Message.Content {
				if b.Type == "tool_use" && b.ID != "" {
					switch b.Name {
					case "Task":
						openTasks[b.ID] = true
					case "AskUserQuestion":
						pendingAUQ[b.ID] = true
					}
				}
			}
			if hasAPIErr {
				resumedAfterAPIErr = true
			}
```

with:

```go
		case "assistant":
			if isSyntheticRateLimit {
				// Synthetic rate-limit message has zero usage and is NOT a user/assistant
				// resume. Read the reset time from the text and record it.
				var text string
				for _, b := range ev.Message.Content {
					if b.Type == "text" {
						text = b.Text
						break
					}
				}
				if t, ok := parseLimitResetText(text, ev.Timestamp); ok {
					lastAPIErrTime = t
					lastAPIErrRetry = 0 // sentinel: lastAPIErrTime is absolute
					hasAPIErr = true
					resumedAfterAPIErr = false
				}
				break
			}
			u := ev.Message.Usage
			ctx := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
			if ctx > 0 {
				lastCtxTotal = ctx
				lastCtxModel = ev.Message.Model
			}
			totalOut += u.OutputTokens
			pendingAUQ = make(map[string]bool)
			for _, b := range ev.Message.Content {
				if b.Type == "tool_use" && b.ID != "" {
					switch b.Name {
					case "Task":
						openTasks[b.ID] = true
					case "AskUserQuestion":
						pendingAUQ[b.ID] = true
					}
				}
			}
			if hasAPIErr {
				resumedAfterAPIErr = true
			}
```

Then change the post-loop assignment. Replace:

```go
	if hasAPIErr && !resumedAfterAPIErr {
		snap.RateLimitResetsAt = lastAPIErrTime.Add(time.Duration(lastAPIErrRetry) * time.Millisecond)
	}
```

with:

```go
	if hasAPIErr && !resumedAfterAPIErr {
		if lastAPIErrRetry == 0 {
			// Synthetic shape: lastAPIErrTime is already the absolute reset time.
			snap.RateLimitResetsAt = lastAPIErrTime
		} else {
			snap.RateLimitResetsAt = lastAPIErrTime.Add(time.Duration(lastAPIErrRetry) * time.Millisecond)
		}
	}
```

- [ ] **Step 4: Run snapshot tests**

Run: `go test ./internal/transcript/ -run 'TestScan' -v`
Expected: PASS for all (`TestScanSyntheticRateLimit`, `TestScanSyntheticRateLimitClearedByLaterUser`, and pre-existing `TestScan*`).

- [ ] **Step 5: Run the full transcript package**

Run: `go test ./internal/transcript/ -v`
Expected: PASS, no regressions.

- [ ] **Step 6: Commit**

```bash
git add internal/transcript/snapshot.go internal/transcript/snapshot_test.go
git commit -m "fix(transcript): Scan picks up synthetic-assistant rate-limit shape"
```

---

## Task 4: Add `M` key — manual fire (TDD)

**Files:**
- Modify: `internal/tui/update.go`
- Test: `internal/tui/model_test.go`

The `M` key sends `m.autoResumeMessage` to every non-Working session via the existing signaler. It is independent of `m.autoResume` and `m.autoResumeFired`. It does not mutate the tree.

- [ ] **Step 1: Write failing tests**

Append to `internal/tui/model_test.go`:

```go
func TestManualFireSendsToAllNonWorkingRegardlessOfAutoResume(t *testing.T) {
	mock := &mockSignaler{name: "mock", detect: true}
	tree := &aggregate.Tree{
		Dirs: []*aggregate.Directory{{
			Path: "/p",
			Sessions: []*aggregate.SessionView{
				{Session: &session.Session{PID: 11, Status: session.Idle}},
				{Session: &session.Session{PID: 22, Status: session.Working}},
				{Session: &session.Session{PID: 33, Status: session.Idle}},
			},
		}},
	}
	m := NewModel(Options{
		Tree:              tree,
		Signalers:         []signal.Signaler{mock},
		AutoResumeMessage: "go",
	})
	// autoResume left false on purpose — manual fire MUST work without the toggle.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'M'}})
	if len(mock.sent) != 2 {
		t.Fatalf("sent = %v, want 2 entries (PIDs 11 and 33)", mock.sent)
	}
	want := map[string]bool{"11:go": false, "33:go": false}
	for _, s := range mock.sent {
		if _, ok := want[s]; !ok {
			t.Errorf("unexpected send %q", s)
		}
		want[s] = true
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("missing send %q", k)
		}
	}
}

func TestManualFireDoesNotMutateTree(t *testing.T) {
	mock := &mockSignaler{name: "mock", detect: true}
	resetsAt := time.Now().Add(30 * time.Minute)
	tree := &aggregate.Tree{
		WindowResetsAt: resetsAt,
		Dirs: []*aggregate.Directory{{
			Path:     "/p",
			Sessions: []*aggregate.SessionView{{Session: &session.Session{PID: 7, Status: session.Idle}}},
		}},
	}
	m := NewModel(Options{
		Tree: tree, Signalers: []signal.Signaler{mock}, AutoResumeMessage: "go",
	})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'M'}})
	if !m.tree.WindowResetsAt.Equal(resetsAt) {
		t.Errorf("WindowResetsAt mutated; got %v, want %v", m.tree.WindowResetsAt, resetsAt)
	}
	if m.autoResumeFired {
		t.Error("autoResumeFired must not be set by manual fire")
	}
}

func TestManualFireNilTreeNoCrash(t *testing.T) {
	mock := &mockSignaler{name: "mock", detect: true}
	m := NewModel(Options{Tree: nil, Signalers: []signal.Signaler{mock}, AutoResumeMessage: "go"})
	// Defensive: must not panic, must not send anything.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'M'}})
	if len(mock.sent) != 0 {
		t.Errorf("sent = %v, want empty (nil tree)", mock.sent)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/ -run 'TestManualFire' -v`
Expected: FAIL — `M` is unhandled, sends nothing.

- [ ] **Step 3: Add the `M` key handler**

Edit `internal/tui/update.go`. In the `tea.KeyMsg` switch, add a case after the existing `case "R":` block (and before `case "down", "j":`):

```go
		case "M":
			if m.tree == nil {
				return m, nil
			}
			for _, d := range m.tree.Dirs {
				for _, sv := range d.Sessions {
					if sv.Status == session.Working {
						continue
					}
					sig := signal.ResolveSignaler(m.signalers, sv.PID)
					if sig == nil {
						fmt.Fprintf(os.Stderr, "manual-resume: no signaler for pid %d\n", sv.PID)
						continue
					}
					if err := sig.Send(sv.PID, m.autoResumeMessage); err != nil {
						fmt.Fprintf(os.Stderr, "manual-resume: send failed pid %d: %v\n", sv.PID, err)
					}
				}
			}
```

(All imports — `fmt`, `os`, `session`, `signal` — are already present in this file; no import changes needed.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui/ -run 'TestManualFire' -v`
Expected: PASS for all three.

- [ ] **Step 5: Run the full tui package**

Run: `go test ./internal/tui/ -v`
Expected: PASS, no regressions.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/update.go internal/tui/model_test.go
git commit -m "feat(tui): [M] key fires auto-resume message to non-working sessions"
```

---

## Task 5: Add `[M] resume now` hint to header (TDD)

**Files:**
- Modify: `internal/render/header.go`
- Test: `internal/render/header_test.go`

- [ ] **Step 1: Add a failing assertion**

Edit `internal/render/header_test.go`. Extend the `wantAll` slice in both subcases of `TestHeaderToggleBothOptionsPresent`:

Find:

```go
			{
				name:    "defaults",
				opts:    HeaderOpts{},
				wantAll: []string{"tokens", "cost", "active", "all", "name", "id"},
			},
			{
				name:    "CostMode+ShowAll+ForceID",
				opts:    HeaderOpts{CostMode: true, ShowAll: true, ForceID: true},
				wantAll: []string{"tokens", "cost", "active", "all", "name", "id"},
			},
```

Replace with:

```go
			{
				name:    "defaults",
				opts:    HeaderOpts{},
				wantAll: []string{"tokens", "cost", "active", "all", "name", "id", "[M] resume now"},
			},
			{
				name:    "CostMode+ShowAll+ForceID",
				opts:    HeaderOpts{CostMode: true, ShowAll: true, ForceID: true},
				wantAll: []string{"tokens", "cost", "active", "all", "name", "id", "[M] resume now"},
			},
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/render/ -run 'TestHeaderToggleBothOptionsPresent' -v`
Expected: FAIL — `[M] resume now` is not in the header.

- [ ] **Step 3: Add the hint to the controls line**

Edit `internal/render/header.go`. Find the `Fprintf` that formats the controls line:

```go
	fmt.Fprintf(&sb, "[C]affeinate: %s  |  [t] %s · %s  |  [a] %s · %s  |  [n] %s · %s  |  [R] auto-resume: %s  |  [q]\n\n",
		caff, tokLabel, costLabel, activeLabel, allLabel, nameLabel, idLabel, autoResumeLabel)
```

Replace with:

```go
	fmt.Fprintf(&sb, "[C]affeinate: %s  |  [t] %s · %s  |  [a] %s · %s  |  [n] %s · %s  |  [R] auto-resume: %s  |  [M] resume now  |  [q]\n\n",
		caff, tokLabel, costLabel, activeLabel, allLabel, nameLabel, idLabel, autoResumeLabel)
```

- [ ] **Step 4: Run header tests**

Run: `go test ./internal/render/ -run 'TestHeader' -v`
Expected: PASS, all header tests including the modified toggle test.

- [ ] **Step 5: Commit**

```bash
git add internal/render/header.go internal/render/header_test.go
git commit -m "feat(render): show [M] resume now hint in header controls line"
```

---

## Task 6: Whole-package verification

**Files:** none (verification only).

- [ ] **Step 1: Run the whole package test suite**

Run: `go test ./...`
Expected: PASS for every package.

- [ ] **Step 2: Run `go vet`**

Run: `go vet ./...`
Expected: clean.

- [ ] **Step 3: If `pre-commit` config exists, run it**

Run: `test -f .pre-commit-config.yaml && (prek run --all-files 2>/dev/null || pre-commit run --all-files) || echo "no pre-commit config"`
Expected: PASS if config present; otherwise the literal text "no pre-commit config".

- [ ] **Step 4: If `flake.nix` exists at repo root, run `nix flake check`**

Run: `cd ../.. && test -f flake.nix && nix flake check || echo "no flake at root"`
Expected: PASS if a flake exists; otherwise the literal text "no flake at root".

- [ ] **Step 5: Final commit if any tooling produced changes**

Run: `git status`
If clean: skip. Otherwise inspect changes, stage targeted files, and commit:

```bash
git add <files>
git commit -m "chore: tooling-driven cleanup post auto-resume fix"
```

---

## Self-Review Notes

- **Spec coverage:** Task 1 covers `parseLimitResetText`. Task 2 covers `RateLimitPause` synthetic detection (incl. resume clearing). Task 3 covers `Scan` synthetic detection (incl. resume clearing). Task 4 covers `[M]` manual fire (all-non-working scope, independent of toggle, no tree mutation). Task 5 covers header hint. Task 6 covers full-package verification.
- **Placeholder scan:** none.
- **Type consistency:** `parseLimitResetText(text string, eventTime time.Time) (time.Time, bool)` referenced identically in Tasks 1, 2, and 3. `lastAPIErrRetry == 0` sentinel introduced in Task 3 only; legacy callers untouched.
