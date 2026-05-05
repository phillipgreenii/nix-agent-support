# claude-agents-tui Tweaks Batch 2 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix nine issues in `claude-agents-tui`: directory token totals, session sort order, burn rate, branch display, XML prompt stripping, awaiting-input indicator, PID-correlated session lifetime, ccusage startup UX, and directory column alignment.

**Architecture:** All changes are in Go packages under `packages/claude-agents-tui/internal/`. The data flow is: `poller.Snapshot()` reads sessions + transcripts → populates `aggregate.SessionEnrichment` → `aggregate.Build()` assembles the `Tree` → `render` package turns it into terminal output. Most bugs are missing population steps in `poller.Snapshot()`.

**Tech Stack:** Go 1.24, bubbletea TUI, lipgloss styling, JSONL transcript parsing.

---

## File Map

| File                                                       | What changes                                                                                               |
| ---------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| `internal/transcript/context.go`                           | Add `TotalTokens` to `ContextSnapshot`; accumulate `output_tokens`                                         |
| `internal/transcript/context_test.go`                      | Assert `TotalTokens` from fixture                                                                          |
| `internal/transcript/first_prompt.go`                      | Add `local-command-caveat` tag; retry-on-XML fallback                                                      |
| `internal/transcript/first_prompt_test.go`                 | New tests for caveat tag and XML fallback                                                                  |
| `internal/transcript/awaiting.go`                          | New: `IsAwaitingInput()`                                                                                   |
| `internal/transcript/awaiting_test.go`                     | New: tests for `IsAwaitingInput()`                                                                         |
| `internal/aggregate/tree.go`                               | Add `CCUsageProbed`, `CCUsageErr`, `AwaitingInput` to enrichment                                           |
| `internal/aggregate/aggregate.go`                          | Sort sessions by `StartedAt` desc within each directory                                                    |
| `internal/aggregate/aggregate_test.go`                     | Assert sort order                                                                                          |
| `internal/session/session.go`                              | Add `Branch string` field                                                                                  |
| `internal/session/git.go`                                  | New: `GitBranch(dir string) string`                                                                        |
| `internal/session/git_test.go`                             | New: tests for `GitBranch()`                                                                               |
| `internal/ccusage/cached.go`                               | Add `Probed()` and `LastErr()` methods                                                                     |
| `internal/poller/poller.go`                                | Wire `SessionTokens`, `SubshellCount`, burn rate buffers, branch, awaiting-input, PID clamp, ccusage state |
| `internal/poller/poller_test.go`                           | `TestSnapshotEnrichmentFields`                                                                             |
| `internal/render/tree.go`                                  | `?` symbol, branch display, right-aligned directory rows                                                   |
| `internal/render/header.go`                                | Three-state ccusage: loading / unavailable / no active block                                               |
| `internal/tui/view.go`                                     | Update legend to include `? awaiting input`                                                                |
| `cmd/claude-agents-tui/main.go`                            | Wire `BurnWindowShort`, `BurnWindowLong`, `CCUsageStateFn`                                                 |
| `tests/fixtures/claude-home/projects/-tmp-x/abc-def.jsonl` | Add content so enrichment tests have data                                                                  |

---

## Task 1: Add `TotalTokens` to `ContextSnapshot`

Directory token totals are 0 because `SessionTokens` is never set. The fix starts by extending `LatestContext` to also sum `output_tokens` across all assistant events.

**Files:**

- Modify: `internal/transcript/context.go`
- Modify: `internal/transcript/context_test.go`

- [ ] **Step 1: Update the failing test first**

  In `internal/transcript/context_test.go`, add to `TestLatestContextFromFixture`:

  ```go
  func TestLatestContextFromFixture(t *testing.T) {
  	res, err := LatestContext("../../tests/fixtures/transcripts/basic.jsonl")
  	if err != nil {
  		t.Fatal(err)
  	}
  	if res.Model != "claude-opus-4-7" {
  		t.Errorf("Model = %q", res.Model)
  	}
  	// second assistant message usage: 5 + 0 + 700 = 705
  	if res.ContextTokens != 705 {
  		t.Errorf("ContextTokens = %d, want 705", res.ContextTokens)
  	}
  	// output_tokens: 50 (first assistant) + 20 (second) = 70
  	if res.TotalTokens != 70 {
  		t.Errorf("TotalTokens = %d, want 70", res.TotalTokens)
  	}
  }
  ```

- [ ] **Step 2: Run test, verify it fails**

  ```bash
  cd packages/claude-agents-tui && go test ./internal/transcript/... -run TestLatestContextFromFixture -v
  ```

  Expected: `FAIL` — `res.TotalTokens` field does not exist yet.

- [ ] **Step 3: Add `TotalTokens` to `ContextSnapshot` and accumulate in scan**

  Replace `internal/transcript/context.go` entirely:

  ```go
  package transcript

  import (
  	"bufio"
  	"encoding/json"
  	"os"
  )

  type ContextSnapshot struct {
  	Model         string
  	ContextTokens int
  	TotalTokens   int // cumulative output_tokens across all assistant events
  }

  // LatestContext returns the Model, ContextTokens, and TotalTokens from the
  // transcript at path. ContextTokens is the input context size from the last
  // assistant event with a non-zero usage payload. TotalTokens is the sum of
  // output_tokens across all qualifying assistant events.
  func LatestContext(path string) (ContextSnapshot, error) {
  	f, err := os.Open(path)
  	if err != nil {
  		return ContextSnapshot{}, err
  	}
  	defer f.Close()
  	scanner := bufio.NewScanner(f)
  	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
  	var last ContextSnapshot
  	var totalOut int
  	for scanner.Scan() {
  		var ev Event
  		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
  			continue
  		}
  		if ev.Type != "assistant" {
  			continue
  		}
  		u := ev.Message.Usage
  		contextTotal := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
  		if contextTotal == 0 {
  			continue
  		}
  		totalOut += u.OutputTokens
  		last = ContextSnapshot{Model: ev.Message.Model, ContextTokens: contextTotal}
  	}
  	last.TotalTokens = totalOut
  	return last, scanner.Err()
  }
  ```

- [ ] **Step 4: Run tests, verify they pass**

  ```bash
  cd packages/claude-agents-tui && go test ./internal/transcript/... -v
  ```

  Expected: all `transcript` tests PASS.

- [ ] **Step 5: Commit**

  ```bash
  cd packages/claude-agents-tui && git add internal/transcript/context.go internal/transcript/context_test.go
  git commit -m "feat(transcript): add TotalTokens to ContextSnapshot"
  ```

---

## Task 2: Populate fixture and wire `SessionTokens` + `SubshellCount`

With `TotalTokens` available, wire it into the poller. Also wire the existing (but unused) `subshell.Counter`.

**Files:**

- Modify: `tests/fixtures/claude-home/projects/-tmp-x/abc-def.jsonl`
- Modify: `internal/poller/poller.go`
- Modify: `internal/poller/poller_test.go`

- [ ] **Step 1: Add content to the fixture transcript**

  The file `tests/fixtures/claude-home/projects/-tmp-x/abc-def.jsonl` is currently empty. Write this content (two JSONL lines):

  ```
  {"type":"user","uuid":"u1","timestamp":"2026-04-23T10:00:00Z","message":{"role":"user","content":"do the thing"}}
  {"type":"assistant","uuid":"a1","timestamp":"2026-04-23T10:00:05Z","message":{"model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":100,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":42}}}
  ```

  `ContextTokens` = 100, `TotalTokens` = 42, `Model` = `"claude-sonnet-4-6"`.

- [ ] **Step 2: Write the failing enrichment test**

  Replace `internal/poller/poller_test.go`:

  ```go
  package poller

  import (
  	"context"
  	"testing"
  	"time"
  )

  func TestSnapshotProducesTree(t *testing.T) {
  	p := &Poller{
  		SessionsDir: "../../tests/fixtures/sessions",
  		ClaudeHome:  "../../tests/fixtures/claude-home",
  		PidAlive:    func(int) bool { return true },
  		Now:         func() time.Time { return time.Now() },
  	}
  	p.CCUsageFn = func(ctx context.Context) ([]byte, error) { return []byte(`{"blocks":[]}`), nil }
  	tree, _, err := p.Snapshot(context.Background())
  	if err != nil {
  		t.Fatal(err)
  	}
  	if tree == nil {
  		t.Fatal("nil tree")
  	}
  }

  func TestSnapshotEnrichmentFields(t *testing.T) {
  	p := &Poller{
  		SessionsDir: "../../tests/fixtures/sessions",
  		ClaudeHome:  "../../tests/fixtures/claude-home",
  		PidAlive:    func(int) bool { return true },
  		Now:         func() time.Time { return time.Now() },
  	}
  	p.CCUsageFn = func(ctx context.Context) ([]byte, error) { return []byte(`{"blocks":[]}`), nil }
  	tree, _, err := p.Snapshot(context.Background())
  	if err != nil {
  		t.Fatal(err)
  	}

  	// Find the session with cwd "/tmp/x" (abc-def)
  	var found *struct {
  		sessionTokens int
  		contextTokens int
  		model         string
  	}
  	for _, d := range tree.Dirs {
  		if d.Path != "/tmp/x" {
  			continue
  		}
  		for _, s := range d.Sessions {
  			found = &struct {
  				sessionTokens int
  				contextTokens int
  				model         string
  			}{
  				sessionTokens: s.SessionEnrichment.SessionTokens,
  				contextTokens: s.SessionEnrichment.ContextTokens,
  				model:         s.SessionEnrichment.Model,
  			}
  		}
  	}
  	if found == nil {
  		t.Fatal("session for /tmp/x not found in tree")
  	}
  	if found.sessionTokens != 42 {
  		t.Errorf("SessionTokens = %d, want 42", found.sessionTokens)
  	}
  	if found.contextTokens != 100 {
  		t.Errorf("ContextTokens = %d, want 100", found.contextTokens)
  	}
  	if found.model != "claude-sonnet-4-6" {
  		t.Errorf("Model = %q, want claude-sonnet-4-6", found.model)
  	}
  	// Directory token total must equal session total (only one session in /tmp/x)
  	for _, d := range tree.Dirs {
  		if d.Path == "/tmp/x" && d.TotalTokens != 42 {
  			t.Errorf("Directory /tmp/x TotalTokens = %d, want 42", d.TotalTokens)
  		}
  	}
  }
  ```

- [ ] **Step 3: Run test, verify it fails**

  ```bash
  cd packages/claude-agents-tui && go test ./internal/poller/... -run TestSnapshotEnrichmentFields -v
  ```

  Expected: `FAIL` — `SessionTokens = 0, want 42`.

- [ ] **Step 4: Update poller to set `SessionTokens` and `SubshellCount`**

  In `internal/poller/poller.go`, update the enrichment block in `Snapshot()`. Replace lines 47–55 (the transcript calls and enrichment assignment):

  ```go
  		fp, _ := transcript.FirstPrompt(path)
  		ctxSnap, _ := transcript.LatestContext(path)
  		subs, _ := transcript.OpenSubagents(path)
  		waiting, _ := transcript.IsAwaitingInput(path)
  		shells, _ := subshellCounter.Count(s.PID)
  		enriched[s.SessionID] = aggregate.SessionEnrichment{
  			ContextTokens: ctxSnap.ContextTokens,
  			SessionTokens: ctxSnap.TotalTokens,
  			Model:         ctxSnap.Model,
  			FirstPrompt:   fp,
  			SubagentCount: subs,
  			SubshellCount: shells,
  			AwaitingInput: waiting,
  		}
  ```

  Add the import and counter to the `Poller` struct and `Snapshot()` preamble. The full updated `internal/poller/poller.go`:

  ```go
  package poller

  import (
  	"context"
  	"time"

  	"github.com/phillipgreenii/claude-agents-tui/internal/aggregate"
  	"github.com/phillipgreenii/claude-agents-tui/internal/burnrate"
  	"github.com/phillipgreenii/claude-agents-tui/internal/ccusage"
  	"github.com/phillipgreenii/claude-agents-tui/internal/session"
  	"github.com/phillipgreenii/claude-agents-tui/internal/subshell"
  	"github.com/phillipgreenii/claude-agents-tui/internal/transcript"
  )

  type Poller struct {
  	SessionsDir      string
  	ClaudeHome       string
  	PidAlive         func(int) bool
  	PlanTier         string
  	WorkingThreshold time.Duration
  	IdleThreshold    time.Duration
  	BurnWindowShort  time.Duration
  	BurnWindowLong   time.Duration
  	Now              func() time.Time
  	CCUsageFn        func(ctx context.Context) ([]byte, error)
  	CCUsageStateFn   func() (probed bool, err error)

  	burnShort       map[string]*burnrate.Buffer
  	burnLong        map[string]*burnrate.Buffer
  	prevTotalTokens map[string]int
  }

  func (p *Poller) Snapshot(ctx context.Context) (*aggregate.Tree, bool, error) {
  	now := p.Now()
  	disc := &session.Discoverer{SessionsDir: p.SessionsDir, PidAlive: p.PidAlive}
  	sessions, err := disc.Discover()
  	if err != nil {
  		return nil, false, err
  	}

  	subshellCounter := &subshell.Counter{}

  	// Lazy-init stateful maps.
  	if p.burnShort == nil {
  		p.burnShort = make(map[string]*burnrate.Buffer)
  		p.burnLong = make(map[string]*burnrate.Buffer)
  		p.prevTotalTokens = make(map[string]int)
  	}

  	enriched := map[string]aggregate.SessionEnrichment{}
  	anyWorking := false

  	for _, s := range sessions {
  		path, mtime, ok := session.ResolveTranscript(p.ClaudeHome, s)
  		if ok {
  			s.TranscriptMTime = mtime
  		}
  		s.Status = session.Classify(now, s.TranscriptMTime, p.WorkingThreshold, p.IdleThreshold)
  		s.Branch = session.GitBranch(s.Cwd)
  		if s.Status == session.Working {
  			anyWorking = true
  		}

  		fp, _ := transcript.FirstPrompt(path)
  		ctxSnap, _ := transcript.LatestContext(path)
  		subs, _ := transcript.OpenSubagents(path)
  		waiting, _ := transcript.IsAwaitingInput(path)
  		shells, _ := subshellCounter.Count(s.PID)

  		// Burn rate: add delta (tokens generated since last poll) to ring buffers.
  		prev := p.prevTotalTokens[s.SessionID]
  		delta := ctxSnap.TotalTokens - prev
  		if delta < 0 {
  			delta = 0
  		}
  		p.prevTotalTokens[s.SessionID] = ctxSnap.TotalTokens

  		winShort := p.BurnWindowShort
  		if winShort == 0 {
  			winShort = 60 * time.Second
  		}
  		winLong := p.BurnWindowLong
  		if winLong == 0 {
  			winLong = 300 * time.Second
  		}
  		if _, ok := p.burnShort[s.SessionID]; !ok {
  			p.burnShort[s.SessionID] = burnrate.New(winShort)
  			p.burnLong[s.SessionID] = burnrate.New(winLong)
  		}
  		p.burnShort[s.SessionID].Add(now, delta)
  		p.burnLong[s.SessionID].Add(now, delta)

  		enriched[s.SessionID] = aggregate.SessionEnrichment{
  			ContextTokens: ctxSnap.ContextTokens,
  			SessionTokens: ctxSnap.TotalTokens,
  			Model:         ctxSnap.Model,
  			FirstPrompt:   fp,
  			SubagentCount: subs,
  			SubshellCount: shells,
  			AwaitingInput: waiting,
  			BurnRateShort: p.burnShort[s.SessionID].Rate(now),
  			BurnRateLong:  p.burnLong[s.SessionID].Rate(now),
  		}
  	}

  	// Prune stale burn buffers (sessions that are no longer alive).
  	activeIDs := make(map[string]bool, len(sessions))
  	for _, s := range sessions {
  		activeIDs[s.SessionID] = true
  	}
  	for id := range p.burnShort {
  		if !activeIDs[id] {
  			delete(p.burnShort, id)
  			delete(p.burnLong, id)
  			delete(p.prevTotalTokens, id)
  		}
  	}

  	// PID clamp: if a PID is alive and this session has the freshest transcript
  	// for that PID, clamp Dormant → Idle. Sessions superseded by /resume stay Dormant.
  	pidActiveSID := make(map[int]string)
  	for _, s := range sessions {
  		cur, ok := pidActiveSID[s.PID]
  		if !ok || s.TranscriptMTime.After(sessions[indexByID(sessions, cur)].TranscriptMTime) {
  			pidActiveSID[s.PID] = s.SessionID
  		}
  	}
  	for _, s := range sessions {
  		if s.Status == session.Dormant && p.PidAlive != nil && p.PidAlive(s.PID) && pidActiveSID[s.PID] == s.SessionID {
  			s.Status = session.Idle
  		}
  	}

  	var block *ccusage.Block
  	if p.CCUsageFn != nil {
  		if body, err := p.CCUsageFn(ctx); err == nil && body != nil {
  			block, _ = ccusage.ParseActiveBlock(body)
  		}
  	}

  	var ccUsageProbed bool
  	var ccUsageErr error
  	if p.CCUsageStateFn != nil {
  		ccUsageProbed, ccUsageErr = p.CCUsageStateFn()
  	}

  	tree := aggregate.Build(sessions, enriched, block, p.PlanTier)
  	tree.CCUsageProbed = ccUsageProbed
  	tree.CCUsageErr = ccUsageErr
  	return tree, anyWorking, nil
  }

  // indexByID returns the index of the session with the given sessionID, or -1.
  func indexByID(sessions []*session.Session, id string) int {
  	for i, s := range sessions {
  		if s.SessionID == id {
  			return i
  		}
  	}
  	return 0 // fallback; only reached when id is absent (shouldn't happen)
  }
  ```

  Note: `AwaitingInput`, `IsAwaitingInput`, `GitBranch`, `Branch` on Session — these are defined in later tasks. The code above references them; they will compile after Tasks 3–7 are complete. If building incrementally, stub them temporarily (see Step 5 note).

- [ ] **Step 5: Verify tests pass (enrichment fields)**

  ```bash
  cd packages/claude-agents-tui && go test ./internal/poller/... -v
  ```

  Expected: both poller tests PASS. (If later tasks aren't done yet, the code may not compile — complete Tasks 3–7 first, then verify.)

- [ ] **Step 6: Commit**

  ```bash
  cd packages/claude-agents-tui && git add \
    tests/fixtures/claude-home/projects/-tmp-x/abc-def.jsonl \
    internal/poller/poller.go \
    internal/poller/poller_test.go
  git commit -m "feat(poller): wire SessionTokens, SubshellCount, burn rate, branch, awaiting-input, PID clamp, ccusage state"
  ```

---

## Task 3: Session sort order

Sessions within a directory should be sorted by creation time, newest first.

**Files:**

- Modify: `internal/aggregate/aggregate.go`
- Modify: `internal/aggregate/aggregate_test.go`

- [ ] **Step 1: Write failing test**

  In `internal/aggregate/aggregate_test.go`, add:

  ```go
  func TestBuildSessionsSortedByStartedAtDesc(t *testing.T) {
  	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
  	sessions := []*session.Session{
  		{SessionID: "old", Cwd: "/p", StartedAt: base.Add(1 * time.Minute)},
  		{SessionID: "mid", Cwd: "/p", StartedAt: base.Add(3 * time.Minute)},
  		{SessionID: "new", Cwd: "/p", StartedAt: base.Add(5 * time.Minute)},
  	}
  	enriched := map[string]SessionEnrichment{
  		"old": {SessionTokens: 10},
  		"mid": {SessionTokens: 20},
  		"new": {SessionTokens: 30},
  	}
  	tree := Build(sessions, enriched, nil, "max_5x")
  	if len(tree.Dirs) != 1 {
  		t.Fatalf("want 1 dir, got %d", len(tree.Dirs))
  	}
  	got := tree.Dirs[0].Sessions
  	if len(got) != 3 {
  		t.Fatalf("want 3 sessions, got %d", len(got))
  	}
  	want := []string{"new", "mid", "old"}
  	for i, s := range got {
  		if s.SessionID != want[i] {
  			t.Errorf("sessions[%d].SessionID = %q, want %q", i, s.SessionID, want[i])
  		}
  	}
  }
  ```

- [ ] **Step 2: Run test, verify it fails**

  ```bash
  cd packages/claude-agents-tui && go test ./internal/aggregate/... -run TestBuildSessionsSortedByStartedAtDesc -v
  ```

  Expected: `FAIL` — order is undefined.

- [ ] **Step 3: Add sort in `Build()`**

  In `internal/aggregate/aggregate.go`, after the loop that builds `byDir` and before constructing the `tree`, add:

  ```go
  	// Sort sessions within each directory newest-first (stable across polls).
  	for _, d := range byDir {
  		sort.Slice(d.Sessions, func(i, j int) bool {
  			return d.Sessions[i].StartedAt.After(d.Sessions[j].StartedAt)
  		})
  	}
  ```

  Place this immediately before the `grandTokens` loop (after the `for _, s := range sessions` loop closes).

- [ ] **Step 4: Run tests, verify they pass**

  ```bash
  cd packages/claude-agents-tui && go test ./internal/aggregate/... -v
  ```

  Expected: all aggregate tests PASS.

- [ ] **Step 5: Commit**

  ```bash
  cd packages/claude-agents-tui && git add internal/aggregate/aggregate.go internal/aggregate/aggregate_test.go
  git commit -m "feat(aggregate): sort sessions by StartedAt descending"
  ```

---

## Task 4: Git branch info

Add `Branch string` to `Session` and a helper that reads `.git/HEAD` directly (no subprocess).

**Files:**

- Create: `internal/session/git.go`
- Create: `internal/session/git_test.go`
- Modify: `internal/session/session.go`

- [ ] **Step 1: Write failing tests for `GitBranch`**

  Create `internal/session/git_test.go`:

  ```go
  package session

  import (
  	"os"
  	"path/filepath"
  	"testing"
  )

  func TestGitBranchNamedBranch(t *testing.T) {
  	dir := t.TempDir()
  	gitDir := filepath.Join(dir, ".git")
  	if err := os.MkdirAll(gitDir, 0o755); err != nil {
  		t.Fatal(err)
  	}
  	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/my-feature\n"), 0o644); err != nil {
  		t.Fatal(err)
  	}
  	got := GitBranch(dir)
  	if got != "my-feature" {
  		t.Errorf("GitBranch = %q, want \"my-feature\"", got)
  	}
  }

  func TestGitBranchDetachedHead(t *testing.T) {
  	dir := t.TempDir()
  	gitDir := filepath.Join(dir, ".git")
  	if err := os.MkdirAll(gitDir, 0o755); err != nil {
  		t.Fatal(err)
  	}
  	sha := "abc1234def5678901234567890123456789012ab"
  	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(sha+"\n"), 0o644); err != nil {
  		t.Fatal(err)
  	}
  	got := GitBranch(dir)
  	if got != "abc1234" {
  		t.Errorf("GitBranch = %q, want \"abc1234\"", got)
  	}
  }

  func TestGitBranchNoRepo(t *testing.T) {
  	dir := t.TempDir()
  	got := GitBranch(dir)
  	if got != "" {
  		t.Errorf("GitBranch = %q, want \"\"", got)
  	}
  }
  ```

- [ ] **Step 2: Run tests, verify they fail**

  ```bash
  cd packages/claude-agents-tui && go test ./internal/session/... -run TestGitBranch -v
  ```

  Expected: compile error — `GitBranch` undefined.

- [ ] **Step 3: Create `internal/session/git.go`**

  ```go
  package session

  import (
  	"os"
  	"strings"
  )

  // GitBranch reads .git/HEAD in dir and returns the current branch name.
  // Returns "" when dir is not a git repo or HEAD is unreadable.
  func GitBranch(dir string) string {
  	data, err := os.ReadFile(dir + "/.git/HEAD")
  	if err != nil {
  		return ""
  	}
  	line := strings.TrimSpace(string(data))
  	const prefix = "ref: refs/heads/"
  	if strings.HasPrefix(line, prefix) {
  		return strings.TrimPrefix(line, prefix)
  	}
  	// Detached HEAD — return short hash.
  	if len(line) >= 7 {
  		return line[:7]
  	}
  	return line
  }
  ```

- [ ] **Step 4: Add `Branch` field to `Session` struct**

  In `internal/session/session.go`, add `Branch string` to the `Session` struct:

  ```go
  type Session struct {
  	PID             int
  	SessionID       string
  	Cwd             string
  	Kind            string
  	Entrypoint      string
  	Name            string
  	Branch          string
  	StartedAt       time.Time
  	TranscriptMTime time.Time
  	Status          Status
  }
  ```

- [ ] **Step 5: Run tests, verify they pass**

  ```bash
  cd packages/claude-agents-tui && go test ./internal/session/... -v
  ```

  Expected: all session tests PASS.

- [ ] **Step 6: Commit**

  ```bash
  cd packages/claude-agents-tui && git add internal/session/git.go internal/session/git_test.go internal/session/session.go
  git commit -m "feat(session): add GitBranch helper and Branch field"
  ```

---

## Task 5: `AwaitingInput` transcript function

Detect whether the session is blocking on `AskUserQuestion`.

**Files:**

- Create: `internal/transcript/awaiting.go`
- Create: `internal/transcript/awaiting_test.go`

- [ ] **Step 1: Write failing tests**

  Create `internal/transcript/awaiting_test.go`:

  ```go
  package transcript

  import "testing"

  func TestIsAwaitingInputTrue(t *testing.T) {
  	path := t.TempDir() + "/t.jsonl"
  	body := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"auq_1","name":"AskUserQuestion","input":{}}]}}` + "\n"
  	if err := writeTestFile(path, body); err != nil {
  		t.Fatal(err)
  	}
  	got, err := IsAwaitingInput(path)
  	if err != nil {
  		t.Fatal(err)
  	}
  	if !got {
  		t.Error("IsAwaitingInput = false, want true")
  	}
  }

  func TestIsAwaitingInputFalseAfterResult(t *testing.T) {
  	path := t.TempDir() + "/t.jsonl"
  	body := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"auq_1","name":"AskUserQuestion","input":{}}]}}` + "\n" +
  		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"auq_1","content":"user answered"}]}}` + "\n"
  	if err := writeTestFile(path, body); err != nil {
  		t.Fatal(err)
  	}
  	got, err := IsAwaitingInput(path)
  	if err != nil {
  		t.Fatal(err)
  	}
  	if got {
  		t.Error("IsAwaitingInput = true, want false (result already received)")
  	}
  }

  func TestIsAwaitingInputFalseNoAUQ(t *testing.T) {
  	path := t.TempDir() + "/t.jsonl"
  	body := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}` + "\n"
  	if err := writeTestFile(path, body); err != nil {
  		t.Fatal(err)
  	}
  	got, err := IsAwaitingInput(path)
  	if err != nil {
  		t.Fatal(err)
  	}
  	if got {
  		t.Error("IsAwaitingInput = true, want false (no AskUserQuestion)")
  	}
  }
  ```

- [ ] **Step 2: Run tests, verify they fail**

  ```bash
  cd packages/claude-agents-tui && go test ./internal/transcript/... -run TestIsAwaitingInput -v
  ```

  Expected: compile error — `IsAwaitingInput` undefined.

- [ ] **Step 3: Create `internal/transcript/awaiting.go`**

  ```go
  package transcript

  import (
  	"bufio"
  	"encoding/json"
  	"os"
  )

  // IsAwaitingInput returns true if the last assistant turn in the transcript
  // contains an AskUserQuestion tool_use with no matching tool_result yet.
  func IsAwaitingInput(path string) (bool, error) {
  	f, err := os.Open(path)
  	if err != nil {
  		return false, err
  	}
  	defer f.Close()

  	var events []Event
  	scanner := bufio.NewScanner(f)
  	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
  	for scanner.Scan() {
  		var ev Event
  		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
  			continue
  		}
  		events = append(events, ev)
  	}
  	if err := scanner.Err(); err != nil {
  		return false, err
  	}

  	// Walk events: reset pending set on each assistant turn, resolve on tool_result.
  	pending := make(map[string]bool)
  	for _, ev := range events {
  		switch ev.Type {
  		case "assistant":
  			pending = make(map[string]bool)
  			for _, b := range ev.Message.Content {
  				if b.Type == "tool_use" && b.Name == "AskUserQuestion" && b.ID != "" {
  					pending[b.ID] = true
  				}
  			}
  		case "user":
  			for _, b := range ev.Message.Content {
  				if b.Type == "tool_result" && b.ToolUseID != "" {
  					delete(pending, b.ToolUseID)
  				}
  			}
  		}
  	}
  	return len(pending) > 0, nil
  }
  ```

- [ ] **Step 4: Run tests, verify they pass**

  ```bash
  cd packages/claude-agents-tui && go test ./internal/transcript/... -run TestIsAwaitingInput -v
  ```

  Expected: all three tests PASS.

- [ ] **Step 5: Commit**

  ```bash
  cd packages/claude-agents-tui && git add internal/transcript/awaiting.go internal/transcript/awaiting_test.go
  git commit -m "feat(transcript): add IsAwaitingInput"
  ```

---

## Task 6: Add `AwaitingInput` to `SessionEnrichment` and `Tree`

Add the field before the poller wires it.

**Files:**

- Modify: `internal/aggregate/tree.go`

- [ ] **Step 1: Add `AwaitingInput`, `CCUsageProbed`, `CCUsageErr` to types**

  Replace `internal/aggregate/tree.go`:

  ```go
  package aggregate

  import (
  	"time"

  	"github.com/phillipgreenii/claude-agents-tui/internal/ccusage"
  	"github.com/phillipgreenii/claude-agents-tui/internal/session"
  )

  type SessionEnrichment struct {
  	ContextTokens int
  	Model         string
  	FirstPrompt   string
  	SubagentCount int
  	SubshellCount int
  	SessionTokens int     // cumulative output_tokens across session
  	BurnRateShort float64 // tokens/min, short window
  	BurnRateLong  float64 // tokens/min, long window
  	CostUSD       float64 // estimated share, filled by Build
  	AwaitingInput bool    // true when last assistant turn contains unresolved AskUserQuestion
  }

  type Directory struct {
  	Path         string
  	Sessions     []*SessionView
  	WorkingN     int
  	IdleN        int
  	DormantN     int
  	TotalTokens  int
  	TotalCostUSD float64
  }

  type SessionView struct {
  	*session.Session
  	SessionEnrichment
  }

  type Tree struct {
  	Dirs          []*Directory
  	ActiveBlock   *ccusage.Block
  	PlanCapUSD    float64
  	GeneratedAt   time.Time
  	CCUsageProbed bool  // true once the first ccusage probe has run
  	CCUsageErr    error // non-nil if ccusage exec failed
  }

  // TopupShouldDisplay returns true when the current 5h block's actual cost has
  // reached or exceeded the plan cap — meaning the user is consuming top-up tokens.
  func (t *Tree) TopupShouldDisplay() bool {
  	if t.ActiveBlock == nil || t.PlanCapUSD <= 0 {
  		return false
  	}
  	return t.ActiveBlock.CostUSD >= t.PlanCapUSD
  }
  ```

- [ ] **Step 2: Verify compilation**

  ```bash
  cd packages/claude-agents-tui && go build ./...
  ```

  Expected: no errors.

- [ ] **Step 3: Commit**

  ```bash
  cd packages/claude-agents-tui && git add internal/aggregate/tree.go
  git commit -m "feat(aggregate): add AwaitingInput, CCUsageProbed, CCUsageErr fields"
  ```

---

## Task 7: ccusage startup state — `CachedRunner` + main wiring

Expose whether ccusage has been probed and whether it errored, so the header can show "loading…" on startup instead of "unavailable."

**Files:**

- Modify: `internal/ccusage/cached.go`
- Modify: `cmd/claude-agents-tui/main.go`

- [ ] **Step 1: Add `Probed()` and `LastErr()` to `CachedRunner`**

  In `internal/ccusage/cached.go`, add these two methods after the `Get` method:

  ```go
  // Probed returns true once the first background refresh has completed
  // (successfully or with an error).
  func (c *CachedRunner) Probed() bool {
  	c.mu.Lock()
  	defer c.mu.Unlock()
  	return !c.lastOK.IsZero() || c.lastErr != nil
  }

  // LastErr returns the most recent error from the background refresh, or nil.
  func (c *CachedRunner) LastErr() error {
  	c.mu.Lock()
  	defer c.mu.Unlock()
  	return c.lastErr
  }
  ```

- [ ] **Step 2: Wire `BurnWindowShort`, `BurnWindowLong`, and `CCUsageStateFn` in `main.go`**

  In `cmd/claude-agents-tui/main.go`, update the `&poller.Poller{...}` initializer:

  ```go
  	p := &poller.Poller{
  		SessionsDir:      session.DefaultSessionsDir(),
  		ClaudeHome:       filepath.Join(home, ".claude"),
  		PidAlive:         session.DefaultPidAlive,
  		PlanTier:         cfg.PlanTier,
  		WorkingThreshold: cfg.WorkingThreshold,
  		IdleThreshold:    cfg.IdleThreshold,
  		BurnWindowShort:  cfg.BurnWindowShort,
  		BurnWindowLong:   cfg.BurnWindowLong,
  		Now:              time.Now,
  		CCUsageFn:        ccusageCache.Get,
  		CCUsageStateFn: func() (bool, error) {
  			return ccusageCache.Probed(), ccusageCache.LastErr()
  		},
  	}
  ```

- [ ] **Step 3: Verify build**

  ```bash
  cd packages/claude-agents-tui && go build ./...
  ```

  Expected: no errors.

- [ ] **Step 4: Commit**

  ```bash
  cd packages/claude-agents-tui && git add internal/ccusage/cached.go cmd/claude-agents-tui/main.go
  git commit -m "feat(ccusage): expose Probed/LastErr; wire burn windows and ccusage state in main"
  ```

---

## Task 8: Fix XML prompt stripping

Add the missing `local-command-caveat` tag and a retry-on-XML fallback.

**Files:**

- Modify: `internal/transcript/first_prompt.go`
- Modify: `internal/transcript/first_prompt_test.go`

- [ ] **Step 1: Write failing tests**

  In `internal/transcript/first_prompt_test.go`, add:

  ```go
  func TestCleanPromptTextStripsLocalCommandCaveat(t *testing.T) {
  	in := "<local-command-caveat>Caveat: The messages below were generated by the user while in a local shell.</local-command-caveat>actual prompt"
  	want := "actual prompt"
  	if got := cleanPromptText(in); got != want {
  		t.Errorf("cleanPromptText = %q, want %q", got, want)
  	}
  }

  func TestFirstPromptSkipsXMLOnlyEvents(t *testing.T) {
  	path := t.TempDir() + "/t.jsonl"
  	// First user event is raw XML that survives stripping; second is clean.
  	body := `{"type":"user","message":{"role":"user","content":"<unknown-injected-tag>internal stuff</unknown-injected-tag>"}}` + "\n" +
  		`{"type":"user","message":{"role":"user","content":"real prompt"}}` + "\n"
  	if err := writeTestFile(path, body); err != nil {
  		t.Fatal(err)
  	}
  	got, err := FirstPrompt(path)
  	if err != nil {
  		t.Fatal(err)
  	}
  	if got != "real prompt" {
  		t.Errorf("FirstPrompt = %q, want \"real prompt\"", got)
  	}
  }
  ```

- [ ] **Step 2: Run tests, verify they fail**

  ```bash
  cd packages/claude-agents-tui && go test ./internal/transcript/... -run "TestCleanPromptTextStripsLocalCommandCaveat|TestFirstPromptSkipsXMLOnlyEvents" -v
  ```

  Expected: `FAIL` — caveat tag not stripped, XML fallback not implemented.

- [ ] **Step 3: Add `local-command-caveat` and XML fallback**

  In `internal/transcript/first_prompt.go`:
  1. Add `"local-command-caveat"` to `envelopeTagNames`:

     ```go
     var envelopeTagNames = []string{
     	"command-message",
     	"command-name",
     	"command-args",
     	"system-reminder",
     	"local-command-stdout",
     	"local-command-stderr",
     	"local-command-caveat",
     	"user-prompt-submit-hook",
     	"caveman-message",
     }
     ```

  2. Update `FirstPrompt` to skip cleaned results that still start with `<`:

     ```go
     func FirstPrompt(path string) (string, error) {
     	f, err := os.Open(path)
     	if err != nil {
     		return "", err
     	}
     	defer f.Close()
     	scanner := bufio.NewScanner(f)
     	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
     	for scanner.Scan() {
     		var ev Event
     		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
     			continue
     		}
     		if ev.Type != "user" {
     			continue
     		}
     		text := plainUserText(ev.Message.Content)
     		if text == "" {
     			continue
     		}
     		cleaned := cleanPromptText(text)
     		if cleaned == "" || strings.HasPrefix(cleaned, "<") {
     			continue // unrecognised XML envelope — try next user event
     		}
     		return cleaned, nil
     	}
     	return "", scanner.Err()
     }
     ```

     Add `"strings"` to the import block if not already present.

- [ ] **Step 4: Run all transcript tests, verify they pass**

  ```bash
  cd packages/claude-agents-tui && go test ./internal/transcript/... -v
  ```

  Expected: all transcript tests PASS.

- [ ] **Step 5: Commit**

  ```bash
  cd packages/claude-agents-tui && git add internal/transcript/first_prompt.go internal/transcript/first_prompt_test.go
  git commit -m "fix(transcript): strip local-command-caveat tag; skip XML-only user events"
  ```

---

## Task 9: Header — three-state ccusage display

Use `CCUsageProbed` and `CCUsageErr` to show loading/unavailable/no-active-block correctly.

**Files:**

- Modify: `internal/render/header.go`

- [ ] **Step 1: Update the switch in `Header()`**

  In `internal/render/header.go`, replace the existing `switch` block:

  ````go
  	switch {
  	case tree.ActiveBlock == nil:
  		sb.WriteString("5h Block   (unavailable — `ccusage` not found on PATH)\n")
  	```

  with:

  ```go
  	switch {
  	case !tree.CCUsageProbed:
  		sb.WriteString("5h Block   loading…\n")
  	case tree.CCUsageErr != nil:
  		sb.WriteString("5h Block   (unavailable — `ccusage` not found on PATH)\n")
  	case tree.ActiveBlock == nil:
  		sb.WriteString("5h Block   no active block\n")
  ````

  The remaining `case tree.PlanCapUSD <= 0:` and `default:` cases are unchanged.

- [ ] **Step 2: Verify build**

  ```bash
  cd packages/claude-agents-tui && go build ./...
  ```

  Expected: no errors.

- [ ] **Step 3: Commit**

  ```bash
  cd packages/claude-agents-tui && git add internal/render/header.go
  git commit -m "fix(render): show loading/unavailable/no-block correctly for ccusage state"
  ```

---

## Task 10: Render — `?` symbol, branch display, right-aligned directory rows

**Files:**

- Modify: `internal/render/tree.go`
- Modify: `internal/tui/view.go`

- [ ] **Step 1: Update `symbol()` to handle `AwaitingInput`**

  `symbol()` currently takes only `session.Status`. Change the signature and call site:

  In `internal/render/tree.go`, replace `symbol`:

  ```go
  func symbol(st session.Status, awaitingInput bool) string {
  	switch st {
  	case session.Working:
  		return "●"
  	case session.Idle:
  		if awaitingInput {
  			return "?"
  		}
  		return "○"
  	default:
  		return "✕"
  	}
  }
  ```

  Update the call in `renderSession`:

  ```go
  sym := symbol(s.Status, s.SessionEnrichment.AwaitingInput)
  ```

- [ ] **Step 2: Add branch to label**

  In `renderSession`, update the label line:

  ```go
  label := fmt.Sprintf("%s %s", sym, s.Label(opts.ForceID))
  if s.Branch != "" {
  	label = fmt.Sprintf("%s [%s]", label, s.Branch)
  }
  ```

  Note: `s.Branch` is accessible via the embedded `*session.Session` in `SessionView`.

- [ ] **Step 3: Right-align directory row**

  Replace `dirRow` rendering in `Tree()`. Currently:

  ```go
  		rollup := dirRollup(d, opts)
  		sb.WriteString(fmt.Sprintf("%s   %s\n", d.Path, rollup))
  ```

  Replace with:

  ```go
  		rollup := dirRollup(d, opts)
  		sb.WriteString(renderDirRow(d.Path, rollup, opts.Width))
  ```

  Add the new function:

  ```go
  func renderDirRow(path, rollup string, termWidth int) string {
  	labelW := minLabelWidth
  	if termWidth > 0 {
  		if dyn := termWidth - prefixCols - statsBlockCols; dyn > labelW {
  			labelW = dyn
  		}
  	}
  	pathStyle := lipgloss.NewStyle().Width(prefixCols + labelW).Align(lipgloss.Left)
  	statsStyle := lipgloss.NewStyle().Width(statsBlockCols).Align(lipgloss.Right)
  	return pathStyle.Render(path) + statsStyle.Render(rollup) + "\n"
  }
  ```

- [ ] **Step 4: Update legend in `view.go`**

  In `internal/tui/view.go`, update the legend string:

  ```go
  	legend := "● working  ○ idle  ? awaiting input  ✕ dormant   🤖 subagents  🐚 shells       [↑↓] nav [enter] details [q] quit"
  ```

- [ ] **Step 5: Verify build and tests**

  ```bash
  cd packages/claude-agents-tui && go build ./... && go test ./...
  ```

  Expected: all tests PASS.

- [ ] **Step 6: Commit**

  ```bash
  cd packages/claude-agents-tui && git add internal/render/tree.go internal/tui/view.go
  git commit -m "feat(render): awaiting-input symbol, branch label, right-aligned directory rows"
  ```

---

## Task 11: Full integration — final build and test

- [ ] **Step 1: Run all tests**

  ```bash
  cd packages/claude-agents-tui && go test ./... -v 2>&1 | tail -40
  ```

  Expected: all packages PASS.

- [ ] **Step 2: Build the binary**

  ```bash
  cd packages/claude-agents-tui && go build -o /tmp/claude-agents-tui ./cmd/claude-agents-tui/
  ```

  Expected: binary at `/tmp/claude-agents-tui`, no errors.

- [ ] **Step 3: Smoke-test the binary**

  ```bash
  /tmp/claude-agents-tui --version
  ```

  Expected: prints version.

- [ ] **Step 4: Run nix flake check (if available)**

  ```bash
  cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps && nix flake check 2>&1 | tail -20
  ```

  Expected: PASS or known unrelated failures.

- [ ] **Step 5: Push**

  ```bash
  cd /Users/phillipg/phillipg_mbp && git pull --rebase && bd dolt push && git push
  ```

---

## Self-Review Checklist

- **Spec §1 (PID clamp):** Covered in Task 2 poller code — `pidActiveSID` map + clamp loop. ✓
- **Spec §2 (ccusage startup):** Tasks 6 (Tree fields), 7 (CachedRunner + main wiring), 9 (header render). ✓
- **Spec §3 (XML prompts):** Task 8 — `local-command-caveat` + fallback. ✓
- **Spec §4 (awaiting input):** Tasks 5 (`IsAwaitingInput`), 6 (`AwaitingInput` field), 2 (poller wiring), 10 (render). ✓
- **Spec §7 (directory totals):** Task 1 (`TotalTokens`), Task 2 (`SessionTokens` wired). ✓
- **Spec §7 (SubshellCount):** Task 2 poller wiring. ✓
- **Spec §7 (burn rate):** Task 2 poller — stateful `burnShort`/`burnLong` buffers. ✓
- **Spec §7 (test gap):** Task 2 — `TestSnapshotEnrichmentFields`. ✓
- **Spec §8 (dir alignment):** Task 10 — `renderDirRow`. ✓
- **Spec §9 (sort order):** Task 3. ✓
- **Spec §10 (branch):** Task 4 (`GitBranch`), Task 2 (poller wires `s.Branch`), Task 10 (render). ✓
