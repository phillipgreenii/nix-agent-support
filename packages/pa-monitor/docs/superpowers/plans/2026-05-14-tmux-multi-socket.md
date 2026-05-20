# TmuxSignaler Multi-Socket + Help-Popup Log Path Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `TmuxSignaler` work for users running multiple tmux servers (one server per `-L <name>` socket), and surface the path to the signal-error log inside the `?` help modal.

**Architecture:** Mirror the cmux Phase 1.5 design — `TmuxSignaler` becomes pid-aware via a TTL-cached enumeration. Discovery walks `ps -A -o pid,comm,args`, filters rows where `comm == "tmux"`, parses `-L <name>` (defaulting to `default`), and runs `tmux -L <name> list-panes -a -F "..."` per discovered socket. Detect and Send share one `cachedPanes()` helper with 2s TTL. Help-modal change adds a single optional footer parameter to the `Modal`/`HelpModal` renderers; `view.go` passes `Signal errors logged to: <path>` when `cacheDir` is non-empty.

**Tech Stack:** Go standard library (`context`, `os/exec`, `strings`, `strconv`, `sync`, `time`). `tmux` CLI. `ps` CLI. No new dependencies.

**Working directory:** Go from `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui`; git from `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support`. No git remote — do not push.

**Reference spec:** `docs/superpowers/specs/2026-05-14-tmux-multi-socket-design.md`.

---

## Task 1: Beads issue + commit spec/plan

- [ ] **Step 1: Create bd issue**

```bash
bd create \
  --title="TmuxSignaler multi-socket + help log path (Phase 3)" \
  --description="Make TmuxSignaler work across multiple tmux servers (-L sockets) by enumerating via ps + per-socket list-panes; pid-aware Detect with 2s TTL cache mirroring CmuxSignaler. Plus: one-line log path footer in the ? help modal. Spec: docs/superpowers/specs/2026-05-14-tmux-multi-socket-design.md" \
  --type=feature --priority=2
```

Note the returned id (referenced as `BD_ID3` below). Claim:

```bash
bd update <id> --claim
```

- [ ] **Step 2: Commit spec + plan**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/docs/superpowers/specs/2026-05-14-tmux-multi-socket-design.md
git add packages/claude-agents-tui/docs/superpowers/plans/2026-05-14-tmux-multi-socket.md
git commit -m "docs(claude-agents-tui): tmux multi-socket spec and plan"
```

Capture SHA.

---

## Task 2: Scaffold new TmuxSignaler struct fields (no behavior change)

**Files:**

- Modify: `internal/signal/tmux.go`

Goal: Add `cacheMu`, `cacheAt`, `cacheLocs`, `cacheErr`, and the `paneLoc` type to the struct. Keep `Detect` and `Send` working as today (single-socket) so existing tests pass. Subsequent tasks will wire the new fields in.

- [ ] **Step 1: Replace the contents of `internal/signal/tmux.go` with the scaffold**

The whole file becomes:

```go
package signal

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// tmuxCacheTTL is how long an enumeration result stays fresh. Mirrors
// CmuxSignaler's surfaceCacheTTL so a single signalNonWorking pass over N
// non-Working sessions runs ps + per-socket list-panes once, not N times.
const tmuxCacheTTL = 2 * time.Second

// paneLoc identifies a tmux pane by the socket name (`-L`) of its server and
// the canonical pane target string (`<session>:<window>.<pane>`).
type paneLoc struct {
	socketName string
	paneID     string
}

// TmuxSignaler sends keys to the tmux pane hosting a process. Multi-socket
// aware: discovers running tmux servers via ps, enumerates panes per socket,
// caches the result for tmuxCacheTTL.
// RunCmd is injectable for tests; nil defaults to exec.CommandContext.
type TmuxSignaler struct {
	RunCmd func(ctx context.Context, name string, args ...string) ([]byte, error)

	cacheMu   sync.Mutex
	cacheAt   time.Time
	cacheLocs map[int]paneLoc
	cacheErr  error
}

func (t *TmuxSignaler) Name() string { return "tmux" }

func (t *TmuxSignaler) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if t.RunCmd != nil {
		return t.RunCmd(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).Output()
}

// Detect returns true if any ancestor process of pid is named "tmux".
// Will be replaced in Task 5 with a pid-aware implementation.
func (t *TmuxSignaler) Detect(pid int) bool {
	seen := map[int]bool{}
	for {
		if pid < 1 || seen[pid] {
			return false
		}
		seen[pid] = true
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := t.run(ctx, "ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid))
		cancel()
		if err != nil {
			return false
		}
		fields := strings.Fields(strings.TrimSpace(string(out)))
		if len(fields) < 2 {
			return false
		}
		if fields[1] == "tmux" {
			return true
		}
		ppid, err := strconv.Atoi(fields[0])
		if err != nil || ppid <= 1 {
			return false
		}
		pid = ppid
	}
}

// Send injects text + Enter into the tmux pane that contains pid.
// Will be replaced in Task 6 with a multi-socket implementation.
func (t *TmuxSignaler) Send(pid int, text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := t.run(ctx, "tmux", "list-panes", "-a", "-F",
		"#{pane_pid} #{session_name}:#{window_index}.#{pane_index}")
	if err != nil {
		return fmt.Errorf("tmux list-panes: %w", err)
	}
	paneID := t.findPaneForPID(ctx, string(out), pid)
	if paneID == "" {
		return fmt.Errorf("signal: no tmux pane found for pid %d", pid)
	}
	_, err = t.run(ctx, "tmux", "send-keys", "-t", paneID, text, "Enter")
	return err
}

// findPaneForPID walks up the process tree from targetPID until it finds a pid
// that matches a tmux pane's shell pid from listOutput. Will be replaced in
// Task 6 with a multi-socket-aware walker.
func (t *TmuxSignaler) findPaneForPID(ctx context.Context, listOutput string, targetPID int) string {
	panePIDs := map[int]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(listOutput), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ppid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		panePIDs[ppid] = fields[1]
	}
	seen := map[int]bool{}
	pid := targetPID
	for {
		if pid < 1 || seen[pid] {
			return ""
		}
		seen[pid] = true
		if paneID, ok := panePIDs[pid]; ok {
			return paneID
		}
		out, err := t.run(ctx, "ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid))
		if err != nil {
			return ""
		}
		fields := strings.Fields(strings.TrimSpace(string(out)))
		if len(fields) < 1 {
			return ""
		}
		ppid, err := strconv.Atoi(fields[0])
		if err != nil || ppid < 1 {
			return ""
		}
		pid = ppid
	}
}
```

The new bits are `tmuxCacheTTL` constant, `paneLoc` type, `cacheMu`/`cacheAt`/`cacheLocs`/`cacheErr` struct fields, and the `sync` import. Behavior is unchanged.

- [ ] **Step 2: Run existing tests, confirm green**

```bash
go test ./internal/signal/... -v
```

Expected: every test in signal_test.go passes including the three existing tmux tests (`TestTmuxDetectReturnsTrueWhenTmuxIsAncestor`, `TestTmuxDetectReturnsFalseWhenNoTmuxAncestor`, `TestTmuxSendKeysFindsPaneByAncestor`, `TestTmuxSendErrorsWhenNoPaneFound`, `TestTmuxDetectReturnsFalseForLookalikeComm`).

- [ ] **Step 3: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/signal/tmux.go
git commit -m "refactor(claude-agents-tui): scaffold TmuxSignaler cache fields"
```

Capture SHA.

---

## Task 3: TDD enumeratePanes — ps + per-socket list-panes

**Files:**

- Modify: `internal/signal/tmux.go`
- Modify: `internal/signal/signal_test.go`

Goal: Add a method `(t *TmuxSignaler) enumeratePanes(ctx context.Context) (map[int]paneLoc, error)` that runs `ps -A -o pid,comm,args`, filters comm == "tmux", parses `-L <name>` (defaulting to `default`), dedupes socket names, runs `tmux -L <name> list-panes -a -F "#{pane_pid} #{session_name}:#{window_index}.#{pane_index}"` per socket, and merges into a single map keyed by pane pid.

- [ ] **Step 1: Replace the existing `fakeRun` helper with a multi-socket-aware version**

The current `fakeRun` (in `internal/signal/signal_test.go`) supports `ps` and a single `tmux` socket via a single `paneList` argument. Replace it with:

```go
// fakeMultiSocketRun supports the new multi-socket TmuxSignaler. It serves:
//
//   - `ps -A -o pid,comm,args`            -> psListAll output
//   - `ps -o ppid=,comm= -p <pid>`        -> processTree[<pid>]
//   - `tmux -L <name> list-panes -a -F …` -> panesBySocket[<name>] (empty string = error)
//   - `tmux -L <name> send-keys …`        -> records to sentKeys (always succeeds)
//
// processTree maps pid -> [ppid, comm]. panesBySocket maps socket name to the
// stdout body of list-panes (one line per pane: "<pane_pid> <pane_id>"). When
// panesBySocket has no entry for a socket name we return an error to simulate
// a dead server.
func fakeMultiSocketRun(
	psListAll string,
	processTree map[int][2]string,
	panesBySocket map[string]string,
	sentKeys *[]string,
) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "ps":
			// Multi-socket survey: ps -A -o pid,comm,args
			if len(args) >= 1 && args[0] == "-A" {
				return []byte(psListAll), nil
			}
			// Ancestry walk: ps -o ppid=,comm= -p <pid>
			pidStr := args[len(args)-1]
			pid, _ := strconv.Atoi(pidStr)
			if entry, ok := processTree[pid]; ok {
				return []byte(entry[0] + " " + entry[1]), nil
			}
			return nil, fmt.Errorf("ps: no such pid %d", pid)
		case "tmux":
			if len(args) >= 3 && args[0] == "-L" && args[2] == "list-panes" {
				body, ok := panesBySocket[args[1]]
				if !ok {
					return nil, fmt.Errorf("tmux -L %s: no server", args[1])
				}
				return []byte(body), nil
			}
			if len(args) >= 3 && args[0] == "-L" && args[2] == "send-keys" {
				if sentKeys != nil {
					*sentKeys = append(*sentKeys, "tmux "+strings.Join(args, " "))
				}
				return []byte(""), nil
			}
			return nil, fmt.Errorf("tmux: unexpected args %v", args)
		}
		return nil, fmt.Errorf("unexpected command: %s", name)
	}
}
```

Imports for the test file: already has `context`, `fmt`, `strconv`, `strings`, `testing`. Verify and add anything missing.

The OLD `fakeRun` function may still be needed by tests that haven't yet migrated. Keep both for now — Task 7 prunes the obsolete one.

- [ ] **Step 2: Append a failing test for enumeratePanes**

```go
// psSampleSingleServer is a minimal `ps -A -o pid,comm,args` excerpt with one
// tmux server using -L gc. Real ps output is multi-line and noisy; we use a
// trimmed excerpt because enumeratePanes only filters on `comm == "tmux"` then
// parses argv.
//
// Format (matches real ps output): "<pid> <comm> <argv...>" per line.
// enumeratePanes uses strings.Fields which collapses whitespace.
const psSampleSingleServer = `28346 tmux tmux -u -L gc new-session -d -s mayor
12345 zsh -zsh
67890 claude /usr/bin/claude
`

func TestTmuxEnumerateSingleServer(t *testing.T) {
	processTree := map[int][2]string{}
	panes := map[string]string{
		"gc": "100 mayor:0.0\n200 mayor:0.1\n",
	}
	sig := &signal.TmuxSignaler{
		RunCmd: fakeMultiSocketRun(psSampleSingleServer, processTree, panes, nil),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locs, err := signal.EnumeratePanesForTest(sig, ctx)
	if err != nil {
		t.Fatalf("EnumeratePanes: %v", err)
	}
	if loc, ok := locs[100]; !ok || loc.SocketName != "gc" || loc.PaneID != "mayor:0.0" {
		t.Errorf("locs[100] = %+v, want {gc, mayor:0.0}", loc)
	}
	if loc, ok := locs[200]; !ok || loc.SocketName != "gc" || loc.PaneID != "mayor:0.1" {
		t.Errorf("locs[200] = %+v, want {gc, mayor:0.1}", loc)
	}
}
```

Add `time` to imports in signal_test.go if missing.

The test references `signal.EnumeratePanesForTest` and `paneLoc` fields by their exported aliases. Add a small whitebox export in a new file `internal/signal/export_test.go`:

```go
package signal

import "context"

// EnumeratePanesForTest exposes enumeratePanes for whitebox testing.
func EnumeratePanesForTest(t *TmuxSignaler, ctx context.Context) (map[int]PaneLocForTest, error) {
	locs, err := t.enumeratePanes(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int]PaneLocForTest, len(locs))
	for pid, loc := range locs {
		out[pid] = PaneLocForTest{SocketName: loc.socketName, PaneID: loc.paneID}
	}
	return out, nil
}

// PaneLocForTest is the test-visible view of paneLoc.
type PaneLocForTest struct {
	SocketName string
	PaneID     string
}
```

Run, expect compile FAIL (enumeratePanes doesn't exist yet, EnumeratePanesForTest references it):

```bash
go test ./internal/signal/... -run TestTmuxEnumerate -v
```

- [ ] **Step 3: Implement enumeratePanes**

Append to `internal/signal/tmux.go` (alongside the other helpers):

```go
// enumeratePanes discovers running tmux servers via `ps -A -o pid,comm,args`
// (filtering comm == "tmux", parsing argv for `-L <name>`, defaulting absent
// `-L` to "default"), then runs `tmux -L <name> list-panes -a -F "..."` per
// deduplicated socket name and merges results into a single map keyed by
// pane shell pid.
//
// Per-socket errors (e.g. server died between ps and list-panes) are silently
// skipped — partial discovery is the normal case for transient process churn.
// A failure of the `ps` call itself returns an error.
func (t *TmuxSignaler) enumeratePanes(ctx context.Context) (map[int]paneLoc, error) {
	psOut, err := t.run(ctx, "ps", "-A", "-o", "pid,comm,args")
	if err != nil {
		return nil, fmt.Errorf("ps -A: %w", err)
	}
	socketNames := parseTmuxSocketNames(string(psOut))
	result := map[int]paneLoc{}
	for _, name := range socketNames {
		out, err := t.run(ctx, "tmux", "-L", name, "list-panes", "-a", "-F",
			"#{pane_pid} #{session_name}:#{window_index}.#{pane_index}")
		if err != nil {
			continue
		}
		for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			pid, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			result[pid] = paneLoc{socketName: name, paneID: fields[1]}
		}
	}
	return result, nil
}

// parseTmuxSocketNames takes `ps -A -o pid,comm,args` stdout and returns the
// deduplicated list of `-L <name>` values found across rows where the second
// field (comm) is exactly "tmux". Rows without an explicit `-L` flag yield
// the name "default".
func parseTmuxSocketNames(psOut string) []string {
	seen := map[string]bool{}
	var names []string
	for line := range strings.SplitSeq(strings.TrimSpace(psOut), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != "tmux" {
			continue
		}
		name := "default"
		for i := 2; i < len(fields)-1; i++ {
			if fields[i] == "-L" {
				name = fields[i+1]
				break
			}
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}
```

- [ ] **Step 4: Run, expect green**

```bash
go test ./internal/signal/... -run TestTmuxEnumerate -v
```

Expected: PASS.

- [ ] **Step 5: Append a second test — multiple servers**

```go
const psSampleTwoServers = `28346 tmux tmux -u -L gc new-session -d -s mayor
36990 tmux tmux -u -L work attach
99999 bash -bash
`

func TestTmuxEnumerateTwoServers(t *testing.T) {
	processTree := map[int][2]string{}
	panes := map[string]string{
		"gc":   "100 mayor:0.0\n",
		"work": "300 dev:0.0\n",
	}
	sig := &signal.TmuxSignaler{
		RunCmd: fakeMultiSocketRun(psSampleTwoServers, processTree, panes, nil),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locs, err := signal.EnumeratePanesForTest(sig, ctx)
	if err != nil {
		t.Fatalf("EnumeratePanes: %v", err)
	}
	if locs[100].SocketName != "gc" || locs[100].PaneID != "mayor:0.0" {
		t.Errorf("locs[100] = %+v, want {gc, mayor:0.0}", locs[100])
	}
	if locs[300].SocketName != "work" || locs[300].PaneID != "dev:0.0" {
		t.Errorf("locs[300] = %+v, want {work, dev:0.0}", locs[300])
	}
}

func TestTmuxEnumerationSkipsDeadSocket(t *testing.T) {
	processTree := map[int][2]string{}
	// `work` socket has no entry in panesBySocket → fakeMultiSocketRun returns
	// an error for that socket. Enumeration should still surface `gc`.
	panes := map[string]string{
		"gc": "100 mayor:0.0\n",
	}
	sig := &signal.TmuxSignaler{
		RunCmd: fakeMultiSocketRun(psSampleTwoServers, processTree, panes, nil),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locs, err := signal.EnumeratePanesForTest(sig, ctx)
	if err != nil {
		t.Fatalf("EnumeratePanes: %v", err)
	}
	if locs[100].SocketName != "gc" {
		t.Errorf("locs[100] = %+v, want gc despite work failing", locs[100])
	}
	if _, hasWork := locs[300]; hasWork {
		t.Errorf("locs[300] present despite work socket failing")
	}
}

func TestTmuxEnumerateDefaultSocketWhenNoDashL(t *testing.T) {
	const psNoDashL = `28346 tmux tmux new-session -d -s default
`
	panes := map[string]string{
		"default": "500 mysession:0.0\n",
	}
	sig := &signal.TmuxSignaler{
		RunCmd: fakeMultiSocketRun(psNoDashL, nil, panes, nil),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locs, err := signal.EnumeratePanesForTest(sig, ctx)
	if err != nil {
		t.Fatalf("EnumeratePanes: %v", err)
	}
	if locs[500].SocketName != "default" {
		t.Errorf("locs[500] = %+v, want default socket", locs[500])
	}
}
```

Run, expect all three green:

```bash
go test ./internal/signal/... -run TestTmuxEnumerate -v
```

- [ ] **Step 6: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/signal/
git commit -m "feat(claude-agents-tui): TmuxSignaler enumerates multi-socket panes via ps + -L"
```

Capture SHA.

---

## Task 4: TDD cachedPanes — 2s TTL wrapper

**Files:**

- Modify: `internal/signal/tmux.go`
- Modify: `internal/signal/signal_test.go`
- Modify: `internal/signal/export_test.go`

- [ ] **Step 1: Append a whitebox helper for the cache**

In `internal/signal/export_test.go`, append:

```go
// CachedPanesForTest exposes cachedPanes for whitebox testing.
func CachedPanesForTest(t *TmuxSignaler) (map[int]PaneLocForTest, error) {
	locs, err := t.cachedPanes()
	if err != nil {
		return nil, err
	}
	out := make(map[int]PaneLocForTest, len(locs))
	for pid, loc := range locs {
		out[pid] = PaneLocForTest{SocketName: loc.socketName, PaneID: loc.paneID}
	}
	return out, nil
}
```

- [ ] **Step 2: Append failing test**

In `internal/signal/signal_test.go`:

```go
func TestTmuxCachedPanesCachesAcrossCalls(t *testing.T) {
	psCalls := 0
	listCalls := 0
	processTree := map[int][2]string{}
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "ps" && len(args) >= 1 && args[0] == "-A":
			psCalls++
			return []byte(psSampleSingleServer), nil
		case name == "tmux" && len(args) >= 3 && args[0] == "-L" && args[2] == "list-panes":
			listCalls++
			return []byte("100 mayor:0.0\n"), nil
		}
		return nil, fmt.Errorf("unexpected: %s %v", name, args)
	}
	_ = processTree
	sig := &signal.TmuxSignaler{RunCmd: run}
	for i := 0; i < 5; i++ {
		if _, err := signal.CachedPanesForTest(sig); err != nil {
			t.Fatalf("CachedPanes #%d: %v", i, err)
		}
	}
	if psCalls != 1 {
		t.Errorf("ps -A ran %d times; want 1 (cache should coalesce)", psCalls)
	}
	if listCalls != 1 {
		t.Errorf("tmux list-panes ran %d times; want 1 (cache should coalesce)", listCalls)
	}
}
```

Run, expect compile FAIL (cachedPanes doesn't exist):

```bash
go test ./internal/signal/... -run TestTmuxCachedPanes -v
```

- [ ] **Step 3: Implement cachedPanes**

Append to `internal/signal/tmux.go`:

```go
// cachedPanes returns the pane map, caching for tmuxCacheTTL. Mirrors
// CmuxSignaler.cachedSurfaces — a single signalNonWorking pass over N
// non-Working sessions runs ps + per-socket list-panes once, not N times.
// Errors are cached for the same window so a transient ps failure doesn't
// fan out into N error reports.
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

- [ ] **Step 4: Run, expect green**

```bash
go test ./internal/signal/... -run TestTmuxCachedPanes -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/signal/
git commit -m "feat(claude-agents-tui): TmuxSignaler 2s TTL cache wrapper"
```

Capture SHA.

---

## Task 5: TDD pid-aware Detect

**Files:**

- Modify: `internal/signal/tmux.go`
- Modify: `internal/signal/signal_test.go`

- [ ] **Step 1: Append failing tests**

```go
func TestTmuxDetectReturnsTrueOnlyWhenPidInPane(t *testing.T) {
	// Process tree: agent 1000 -> bash 500 -> shell 100 (which is pane gc:mayor:0.0)
	tree := map[int][2]string{
		1000: {"500", "claude"},
		500:  {"100", "bash"},
	}
	panes := map[string]string{
		"gc": "100 mayor:0.0\n",
	}
	sig := &signal.TmuxSignaler{
		RunCmd: fakeMultiSocketRun(psSampleSingleServer, tree, panes, nil),
	}
	if !sig.Detect(1000) {
		t.Error("Detect(1000) = false, want true (pid in pane gc:mayor:0.0 via ancestor 100)")
	}
}

func TestTmuxDetectReturnsFalseWhenPidNotInAnyPane(t *testing.T) {
	// Process tree: agent 1000 -> bash 500 -> tmux 200 (but server 200's panes
	// have shell pid 999, not 500). Pid 1000's ancestry never reaches 999.
	tree := map[int][2]string{
		1000: {"500", "claude"},
		500:  {"200", "bash"},
		200:  {"1", "tmux"},
	}
	panes := map[string]string{
		"gc": "999 mayor:0.0\n",
	}
	sig := &signal.TmuxSignaler{
		RunCmd: fakeMultiSocketRun(psSampleSingleServer, tree, panes, nil),
	}
	if sig.Detect(1000) {
		t.Error("Detect(1000) = true, want false (no pane has 1000's ancestors)")
	}
}
```

Run, expect FAIL — current Detect is comm-based, returns true based on ancestor name alone:

```bash
go test ./internal/signal/... -run TestTmuxDetectReturnsTrueOnlyWhenPidInPane -v
```

Actually the first test may already PASS because the current Detect checks ancestor comm == "tmux", but our tree has no "tmux" ancestor — only bash chain. So `TestTmuxDetectReturnsTrueOnlyWhenPidInPane` will FAIL because the current Detect can't see the pane match.

The second test will FAIL because the current Detect sees the "tmux" ancestor and returns true unconditionally.

- [ ] **Step 2: Replace Detect**

In `internal/signal/tmux.go`, REPLACE the entire `Detect` method body with:

```go
// Detect returns true iff a known tmux pane lists pid (or one of its
// ancestors) as its shell pid. Pid-aware via cachedPanes — comm-only matching
// (pre-Phase-3 behavior) is dropped because it produced false positives when
// a tmuxinator-like ancestor existed without a matching pane.
func (t *TmuxSignaler) Detect(pid int) bool {
	locs, err := t.cachedPanes()
	if err != nil {
		return false
	}
	return t.findPaneLocForPID(locs, pid) != nil
}

// findPaneLocForPID walks up the process tree from targetPID until it finds a
// pid that exists in the locs map, or runs out of ancestors. Returns *paneLoc
// (not just paneID) so Send can use the socketName too.
func (t *TmuxSignaler) findPaneLocForPID(locs map[int]paneLoc, targetPID int) *paneLoc {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seen := map[int]bool{}
	pid := targetPID
	for {
		if pid < 1 || seen[pid] {
			return nil
		}
		seen[pid] = true
		if loc, ok := locs[pid]; ok {
			return &loc
		}
		out, err := t.run(ctx, "ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid))
		if err != nil {
			return nil
		}
		fields := strings.Fields(strings.TrimSpace(string(out)))
		if len(fields) < 1 {
			return nil
		}
		ppid, err := strconv.Atoi(fields[0])
		if err != nil || ppid < 1 {
			return nil
		}
		pid = ppid
	}
}
```

This shadows the old `findPaneForPID` (which is still on the type — it'll be removed in Task 6 along with the old Send).

- [ ] **Step 3: Run, expect green**

```bash
go test ./internal/signal/... -run TestTmuxDetect -v
```

Note: existing tests `TestTmuxDetectReturnsTrueWhenTmuxIsAncestor` and `TestTmuxDetectReturnsFalseWhenNoTmuxAncestor` may now FAIL because:

- `TestTmuxDetectReturnsTrueWhenTmuxIsAncestor` constructs a tree where the ancestor is named tmux but the test's `paneList` arg to `fakeRun` is empty. With pid-aware Detect, empty panes = false. The test's intent (ancestor-based detect) is obsolete.
- `TestTmuxDetectReturnsFalseWhenNoTmuxAncestor` still returns false but for the new reason (no pane match).

DROP `TestTmuxDetectReturnsTrueWhenTmuxIsAncestor` from `signal_test.go`. Update `TestTmuxDetectReturnsFalseWhenNoTmuxAncestor` to use the new `fakeMultiSocketRun` if needed (or remove if redundant with the new `TestTmuxDetectReturnsFalseWhenPidNotInAnyPane`).

`TestTmuxDetectReturnsFalseForLookalikeComm` (Phase 1.5) should still pass — `tmuxinator` ancestor with empty `panesBySocket` yields false. But if it uses the old `fakeRun` shape, migrate to `fakeMultiSocketRun` and pass empty `panesBySocket{}`.

After the cleanup, re-run:

```bash
go test ./internal/signal/...
```

Expected: PASS for everything.

- [ ] **Step 4: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/signal/
git commit -m "feat(claude-agents-tui): pid-aware TmuxSignaler.Detect via cachedPanes"
```

Capture SHA.

---

## Task 6: TDD multi-socket Send

**Files:**

- Modify: `internal/signal/tmux.go`
- Modify: `internal/signal/signal_test.go`

- [ ] **Step 1: Append failing test**

```go
func TestTmuxSendFindsPaneOnNonDefaultSocket(t *testing.T) {
	tree := map[int][2]string{
		2000: {"600", "claude"},
		600:  {"300", "bash"},
	}
	panes := map[string]string{
		"gc":   "100 mayor:0.0\n",
		"work": "300 dev:0.0\n",
	}
	var sent []string
	sig := &signal.TmuxSignaler{
		RunCmd: fakeMultiSocketRun(psSampleTwoServers, tree, panes, &sent),
	}
	if err := sig.Send(2000, "continue"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("expected 1 send-keys call, got %d: %v", len(sent), sent)
	}
	if !strings.Contains(sent[0], "tmux -L work send-keys -t dev:0.0 continue Enter") {
		t.Errorf("send call = %q, want -L work + -t dev:0.0 with Enter", sent[0])
	}
}
```

Run, expect FAIL (current Send doesn't use `-L`):

```bash
go test ./internal/signal/... -run TestTmuxSendFindsPaneOnNonDefaultSocket -v
```

- [ ] **Step 2: Replace Send and remove the old findPaneForPID**

In `internal/signal/tmux.go`:

- DELETE the old `Send` method body.
- DELETE the old `findPaneForPID` method.
- ADD the new `Send`:

```go
// Send injects text + Enter into the tmux pane that contains pid. Uses
// cachedPanes for socket+pane discovery, so signalNonWorking over N sessions
// pays the enumeration cost once per cache TTL window. The matched pane's
// socket name is threaded through the send-keys call via -L.
func (t *TmuxSignaler) Send(pid int, text string) error {
	locs, err := t.cachedPanes()
	if err != nil {
		return fmt.Errorf("tmux enumerate: %w", err)
	}
	loc := t.findPaneLocForPID(locs, pid)
	if loc == nil {
		return fmt.Errorf("signal: no tmux pane found for pid %d", pid)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = t.run(ctx, "tmux", "-L", loc.socketName, "send-keys", "-t", loc.paneID, text, "Enter")
	return err
}
```

- [ ] **Step 3: Migrate the existing TestTmuxSendKeysFindsPaneByAncestor + TestTmuxSendErrorsWhenNoPaneFound to the new fake**

In `internal/signal/signal_test.go`, locate these two tests. They currently use `fakeRun` and a single-socket `paneList`. Update them to use `fakeMultiSocketRun`:

```go
func TestTmuxSendKeysFindsPaneByAncestor(t *testing.T) {
	tree := map[int][2]string{
		1000: {"500", "claude"},
		500:  {"100", "bash"},
	}
	panes := map[string]string{
		"default": "100 main:0.0\n200 main:0.1\n",
	}
	var sent []string
	sig := &signal.TmuxSignaler{
		RunCmd: fakeMultiSocketRun(psSampleDefaultOnly, tree, panes, &sent),
	}
	if err := sig.Send(1000, "continue"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("expected 1 send-keys call, got %d: %v", len(sent), sent)
	}
	if !strings.Contains(sent[0], "tmux -L default send-keys -t main:0.0 continue Enter") {
		t.Errorf("send call = %q, want -L default + -t main:0.0", sent[0])
	}
}

func TestTmuxSendErrorsWhenNoPaneFound(t *testing.T) {
	tree := map[int][2]string{
		1000: {"500", "claude"},
		500:  {"1", "bash"},
	}
	panes := map[string]string{
		"default": "999 other:0.0\n",
	}
	sig := &signal.TmuxSignaler{
		RunCmd: fakeMultiSocketRun(psSampleDefaultOnly, tree, panes, nil),
	}
	err := sig.Send(1000, "continue")
	if err == nil {
		t.Error("Send should return error when no pane found for PID")
	}
}
```

Add a single-server-no-dashL ps fixture at the top of the test file:

```go
const psSampleDefaultOnly = `28346 tmux tmux new-session -d -s main
`
```

- [ ] **Step 4: Run all signal tests, expect green**

```bash
go test ./internal/signal/... -v
```

Expected: PASS for everything.

- [ ] **Step 5: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/signal/
git commit -m "feat(claude-agents-tui): multi-socket TmuxSignaler.Send via -L name"
```

Capture SHA.

---

## Task 7: Prune obsolete test helper and run full suite

**Files:**

- Modify: `internal/signal/signal_test.go`

- [ ] **Step 1: Remove the old `fakeRun` helper**

If `fakeRun` (single-socket) is still in `signal_test.go` and no test references it anymore (Tasks 5-6 should have migrated everything), delete it. Confirm:

```bash
grep -n "fakeRun\b" packages/claude-agents-tui/internal/signal/signal_test.go
```

If references remain, migrate them to `fakeMultiSocketRun` first. Once clean, delete the `fakeRun` definition.

- [ ] **Step 2: Run all tests + vet**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./...
go vet ./...
```

Expected: PASS / no vet output.

- [ ] **Step 3: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/signal/signal_test.go
git commit -m "test(claude-agents-tui): drop obsolete single-socket fakeRun helper"
```

Capture SHA.

If there were no actual changes (fakeRun already gone), skip the commit and proceed to Task 8.

---

## Task 8: Renderer — extraFooter parameter for Modal + HelpModal

**Files:**

- Modify: `internal/render/modals.go`
- Modify: `internal/render/modals_test.go` (or wherever modal tests live)
- Modify: `internal/tui/view.go` (compile-fix: pass `""` to maintain behavior; real wiring in Task 9)

- [ ] **Step 1: Confirm modals_test.go exists; if not, create it**

```bash
ls packages/claude-agents-tui/internal/render/modals_test.go 2>&1
```

If absent, the new test file will be created in step 2.

- [ ] **Step 2: Write failing tests for extraFooter**

Add to `internal/render/modals_test.go`:

```go
package render_test

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-agents-tui/internal/render"
)

func TestHelpModalRendersExtraFooter(t *testing.T) {
	rows := []render.HelpRow{{Keys: "q", Description: "quit"}}
	out := render.HelpModal(rows, "Signal errors logged to: /tmp/foo.log", 100, 20, 0)
	if !strings.Contains(out, "Signal errors logged to: /tmp/foo.log") {
		t.Errorf("output missing extraFooter line")
	}
}

func TestHelpModalOmitsExtraFooterWhenEmpty(t *testing.T) {
	rows := []render.HelpRow{{Keys: "q", Description: "quit"}}
	out := render.HelpModal(rows, "", 100, 20, 0)
	if strings.Contains(out, "Signal errors logged to") {
		t.Errorf("output unexpectedly contains the footer pattern")
	}
}
```

Run, expect compile FAIL (HelpModal signature mismatch):

```bash
go test ./internal/render/... -run TestHelpModal -v
```

- [ ] **Step 3: Add the parameter to Modal and HelpModal**

In `internal/render/modals.go`:

Change `Modal` signature:

```go
func Modal(title string, rows []ModalRow, extraFooter string, width, height, scroll int) string {
```

In the body of `Modal`, after the `for _, r := range visibleRows { ... }` loop and BEFORE `content.WriteString(footerHint)`, add:

```go
if extraFooter != "" {
    if lipgloss.Width(extraFooter) > contentWidth {
        extraFooter = extraFooter[:contentWidth]
    }
    content.WriteString(extraFooter)
    content.WriteString("\n")
}
```

Change `HelpModal` signature:

```go
func HelpModal(rows []HelpRow, extraFooter string, width, height, scroll int) string {
    mrows := make([]ModalRow, len(rows))
    for i, r := range rows {
        mrows[i] = ModalRow{Left: r.Keys, Right: r.Description}
    }
    return Modal("Help — keybindings", mrows, extraFooter, width, height, scroll)
}
```

Change `LegendModal` signature (it calls `Modal`) — its body becomes:

```go
func LegendModal(width, height, scroll int) string {
    return Modal("Legend — symbols", legendRows, "", width, height, scroll)
}
```

- [ ] **Step 4: Update view.go call site (compile-fix only)**

In `internal/tui/view.go:120`, change:

```go
return render.HelpModal(bindingsToHelpRows(), m.width, m.height, m.modalScrollOffset)
```

to:

```go
return render.HelpModal(bindingsToHelpRows(), "", m.width, m.height, m.modalScrollOffset)
```

(Task 9 replaces the `""` with the computed log path.)

`LegendModal` call site doesn't change since it takes `width, height, scroll` only.

- [ ] **Step 5: Run tests + build**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go build ./...
go test ./...
```

Expected: build clean, all tests PASS including the two new render tests.

If any existing test asserts on the exact output of `Modal` or `HelpModal`, update the call signature there too.

- [ ] **Step 6: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/render/ packages/claude-agents-tui/internal/tui/view.go
git commit -m "feat(claude-agents-tui): Modal accepts extraFooter line"
```

Capture SHA.

---

## Task 9: View wiring — pass signal log path to HelpModal

**Files:**

- Modify: `internal/tui/view.go`
- Modify: `internal/tui/view_test.go` (or appropriate test file)

- [ ] **Step 1: Append failing test**

Read `internal/tui/view_test.go` to see test scaffolding for view rendering. Append:

```go
func TestViewHelpModalIncludesSignalLogPathWhenCacheDirSet(t *testing.T) {
	tmp := t.TempDir()
	m := tui.NewModel(tui.Options{
		CacheDir: tmp,
	})
	tui.SetActiveModalForTest(m, tui.ModalHelp)
	tui.SetSizeForTest(m, 120, 40)
	out := m.View()
	want := "Signal errors logged to: " + tmp + "/signal-errors.log"
	if !strings.Contains(out, want) {
		t.Errorf("View output missing %q\n--- output ---\n%s", want, out)
	}
}

func TestViewHelpModalOmitsLogPathWhenCacheDirEmpty(t *testing.T) {
	m := tui.NewModel(tui.Options{CacheDir: ""})
	tui.SetActiveModalForTest(m, tui.ModalHelp)
	tui.SetSizeForTest(m, 120, 40)
	out := m.View()
	if strings.Contains(out, "Signal errors logged to") {
		t.Errorf("View output unexpectedly contains the footer pattern")
	}
}
```

In `internal/tui/export_test.go`, add the test hooks if missing:

```go
func SetActiveModalForTest(m *Model, kind ModalKind) {
	m.activeModal = kind
}

func SetSizeForTest(m *Model, w, h int) {
	m.width = w
	m.height = h
}
```

Add `path/filepath`, `strings`, `testing` imports to view_test.go as needed.

Run, expect FAIL (path isn't wired):

```bash
go test ./internal/tui/... -run TestViewHelpModal -v
```

- [ ] **Step 2: Wire view.go to compute and pass the path**

In `internal/tui/view.go`, find the help-modal case (currently `case ModalHelp:` or similar). Replace:

```go
return render.HelpModal(bindingsToHelpRows(), "", m.width, m.height, m.modalScrollOffset)
```

with:

```go
extra := ""
if m.cacheDir != "" {
    extra = "Signal errors logged to: " + filepath.Join(m.cacheDir, "signal-errors.log")
}
return render.HelpModal(bindingsToHelpRows(), extra, m.width, m.height, m.modalScrollOffset)
```

Add `"path/filepath"` to view.go imports if missing.

Note: use `m.cacheDir` directly (not `m.errorLogger.CacheDir`) since cacheDir is the canonical source — `errorLogger` is constructed from it.

- [ ] **Step 3: Run, expect green**

```bash
go test ./internal/tui/... -v
```

Expected: PASS for everything, including the two new tests.

Full sweep:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./...
go vet ./...
```

Expected: PASS / no vet output.

- [ ] **Step 4: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add packages/claude-agents-tui/internal/tui/view.go packages/claude-agents-tui/internal/tui/view_test.go packages/claude-agents-tui/internal/tui/export_test.go
git commit -m "feat(claude-agents-tui): help modal shows signal-errors.log path"
```

Capture SHA.

---

## Task 10: Manual smoke test

**Goal:** Validate inside the user's actual multi-tmux environment.

- [ ] **Step 1: Build**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go build -o /tmp/claude-agents-tui ./cmd/claude-agents-tui
```

- [ ] **Step 2: Verify the help modal**

Run `/tmp/claude-agents-tui` (in any host — cmux or plain terminal). Press `?`. Confirm at the bottom of the modal, above the `[esc] close` line:

```
Signal errors logged to: /Users/<you>/.cache/claude-agents-tui/signal-errors.log
```

(Exact path depends on the user's `cacheDir` setting.)

Press `esc` to close. Confirm the modal closes cleanly.

- [ ] **Step 3: Verify multi-socket signaling**

Trigger a manual-resume. If the user has Claude sessions running across multiple tmux sockets (e.g. `-L gc` and one other), the manual-resume should now:

- Reach Claude sessions on every tmux socket, not just the default one.
- Skip sessions whose process ancestry doesn't lead to any pane (e.g. VS Code Claude, plain terminal Claude).

Watch `~/.cache/claude-agents-tui/signal-errors.log` while triggering:

```bash
# In a side surface/terminal:
tail -f ~/.cache/claude-agents-tui/signal-errors.log
```

Expected log entries:

- `manual-resume: no signaler for pid <pid>` — for sessions outside any signaler's reach (VS Code, plain terminal). Acceptable.
- NO `manual-resume: send failed pid <pid>: tmux list-panes: exit status 1` — that error is the bug this phase fixes. If you see it, capture the failing pid and inspect via:

```bash
ps -o pid,ppid,comm,args -p <pid>
# walk ancestry and verify tmux server with the right -L is running
```

- [ ] **Step 4: Record outcome**

```bash
bd update BD_ID3 --notes "Smoke test passed: help modal shows log path; multi-socket signaler reaches Claude sessions across all tmux servers; no list-panes failures in log."
```

If anything fails: do NOT close BD_ID3. Adjust the relevant Task and re-run.

---

## Task 11: Final verification + close

- [ ] **Step 1: Full test suite + vet**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/packages/claude-agents-tui
go test ./...
go vet ./...
```

Expected: PASS / no vet output.

- [ ] **Step 2: `nix flake check`**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
nix flake check
```

If the only failures are pre-existing breakage from Phase 1's smoke test (statix lint on `darwin/services/beads-web/default.nix`, treefmt failures unrelated to claude-agents-tui), ignore. Real regressions are anything new — fix or report.

- [ ] **Step 3: Diff inspection**

```bash
git log --oneline <pre-task-2-SHA>^..HEAD
git diff --stat <pre-task-2-SHA>^..HEAD -- packages/claude-agents-tui/
```

Confirm files changed are limited to:

- `internal/signal/tmux.go`
- `internal/signal/signal_test.go`
- `internal/signal/export_test.go`
- `internal/render/modals.go`
- `internal/render/modals_test.go`
- `internal/tui/view.go`
- `internal/tui/view_test.go`
- `internal/tui/export_test.go`
- `docs/superpowers/{specs,plans}/2026-05-14-tmux-multi-socket*.md`

No accidental edits to `cmd/`, `internal/cmuxstatus/`, `internal/poller/`, `internal/signal/cmux.go`, `go.mod`, `go.sum`, `default.nix`.

- [ ] **Step 4: Close bd issue**

```bash
bd close BD_ID3 --reason "TmuxSignaler is multi-socket aware; help modal shows signal-errors.log path."
```

- [ ] **Step 5: Save insight**

```bash
bd remember "TmuxSignaler multi-socket: discover sockets via 'ps -A -o pid,comm,args' filtered by comm=='tmux', parse argv for '-L <name>' (default 'default'). Per-socket: tmux -L <name> list-panes -a -F '...'. Send: tmux -L <name> send-keys -t <pane> <text> Enter. 2s TTL cache mirrors CmuxSignaler. Pid-aware Detect via cachedPanes — drops comm-based detect because it false-positives on tmuxinator-like ancestors."
```

---

## Self-review notes (record-keeping only)

- **Spec coverage:** Detect → Task 5. Send → Task 6. enumeratePanes → Task 3. cachedPanes → Task 4. Help modal → Tasks 8-9. Test surface from spec is realized across Tasks 3-6, 8-9.
- **Placeholder scan:** Task 5 step 3 has the only "if X then drop test Y" instruction, which is necessary because Task 5 changes Detect semantics; the conditional is concrete.
- **Type consistency:** `paneLoc` defined Task 2, used Tasks 3-6. `PaneLocForTest` exported in Task 3. `cachedPanes()` defined Task 4, called from Tasks 5-6.
