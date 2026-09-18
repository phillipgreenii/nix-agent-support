# agentsession Connector Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a new `agentsession` capability to pg-connector, backed by pa-monitor, so agent
sessions (liveness, status, model, token/cost usage) are queryable, participate in the existing
`attention` (escalations) and `search` capabilities, and are reachable by every existing pg-connector
consumer with no consumer-side code changes.

**Architecture:** pa-monitor gains two `--json` output flags (`status`, `info`) on existing
subcommands plus one new `search` subcommand — no new daemon RPCs, no new domain logic. pg-connector
gains a new Tier-2 backend (`pg-connector-agentsession-pa-monitor`) that execs those three pa-monitor
CLI invocations, and a new Tier-1 capability (`pkg/schema/agentsession.go` +
`pkg/provider/agentsession`) with its own CLI verb group, wired through every mechanical
registration point the existing capabilities (`pr`/`issue`/`ci`/`scm`/`thread`) already go through.

**Tech Stack:** Go (pa-monitor, pg-connector, claude-transcript modules — three separate Go modules
in this repo, each with its own `go.mod`/`gomod2nix.toml`), gRPC (pa-monitor's existing daemon
protocol, unchanged), cobra (pg-connector's CLI), Nix (`mkGoApp`, home-manager module).

**Spec:** `phillipgreenii-nix-agent-support/docs/superpowers/specs/2026-09-18-agentsession-connector-design.md`

## Revision note (2026-09-18)

An independent adversarial review of an earlier draft of this plan found several confirmed defects
— a nonexistent pa-monitor proto type (`pb.GetStateResponse`; the real type is `pb.DaemonState`), a
`pg-connector` CLI output call that didn't match the real `writeTargetedResult` signature, a
`search` subcommand that never actually parsed its own `--json` flag, a wrong `flake.nix` path, a
fabricated "thread" registration precedent in ziprecruiter's machine config that doesn't exist, and
an under-populated `capabilities.SchemaVersions` map. This revision fixes all of them — see each
task below and the Self-Review Notes at the end. A second pass (this same revision) also found that
Task 11's originally-planned "wire into a check" step fought an already-settled repo convention
(contract tests are deliberately `nix run`-only, never `checks.*`) — fixed by discovering there is
already a `pg-connector-contract` umbrella app that will pick up this backend's contract test with
**no new nix wiring at all**.

## Global Constraints

- No new pa-monitor domain logic or gRPC calls in Phase 1 — `status --json`/`info --json` reformat
  data the CLI already fetches; `search` is new pure-Go code layered on the existing
  `session.ResolveTranscript` + the same session-enumeration RPC `status` already uses.
- `agentsession` is the capability token everywhere (package name, schema file, CLI verb, wire
  `connector.` key) — never hyphenated, matching the existing `pr`/`issue`/`ci`/`scm`/`thread`/
  `attention`/`search` convention.
- The `agentsession.Session` schema carries NO transcript path or content field. Transcript access
  only ever happens through the `search` capability, executed by pa-monitor — the backend has no
  filesystem/Claude-domain knowledge of its own (operator decision, 2026-09-18: pa-monitor's
  `ResolveTranscript` is non-trivial and must not be duplicated).
- `agentsession.Provider` is read-only (`Show`/`List` only, no write ops) — mirrors
  `thread.Provider`'s shape exactly. `List` has NO caller-facing named-query concept today (see
  Task 6) — it always returns pa-monitor's own default session scope.
- Every backend error is classified through `pkg/scriptout`'s closed error taxonomy
  (`scriptout.WrapError(scriptout.Err*, msg)`) — never a bare Go error returned to the dispatch
  table.
- A `--json`-style flag MUST be parsed by scanning `args` for the token anywhere in the list, never
  by assuming a fixed position relative to a positional argument (Task 2/3/4 all follow this).
- Naive/unindexed transcript search only — no index, no cross-history optimization (explicitly
  deferred per the spec). `--since`/`--before` (Task 1/4) narrow the scan; they do not index it.
- `pa-monitor search`'s `--since`/`--before` filter is reachable only via that direct CLI
  invocation. `pkg/provider/search.Provider`'s shared interface (every search backend implements
  it, not just this one) has no time-bound parameter, so `pg-connector search <query>`'s generic
  fan-out cannot reach this filter yet — extending the shared interface is tracked separately as
  bead `pg2-emmut`, not decided inside this plan.
- Out of scope (do not implement): Jira/beads/Slack entity cross-linking, new "hung" detection
  beyond pa-monitor's existing `Status`/`Blocker`/`LongIdle`, the `agent-transcript` capability
  split, exposing the store-layer Active/All `Filter` choice over gRPC.

---

## Phase 1 — pa-monitor changes

### Task 1: `claude-transcript.Search` primitive

**Files:**

- Create: `packages/claude-transcript/search.go`
- Create: `packages/claude-transcript/search_test.go`
- Create: `packages/claude-transcript/testdata/search-basic.jsonl`

**Interfaces:**

- Produces: `type Match struct { Role string; Line int; Snippet string; Timestamp time.Time }` and
  `func Search(path, query string, since, before time.Time) ([]Match, error)` — exported from
  package `claudetranscript`. `since`/`before` are zero-value `time.Time` when unbounded. Phase 1
  Task 4 (pa-monitor's `search` subcommand) and Phase 2 Task 10 (the agentsession backend's own
  tests, indirectly) both depend on this exact signature. `Event.Timestamp time.Time` already
  exists (`claude-transcript/events.go:8`, confirmed by direct inspection) — no new parsing is
  needed to bound by time, only a comparison against a field already decoded.

- [ ] **Step 1: Write the failing test**

Create `packages/claude-transcript/testdata/search-basic.jsonl` (each line now carries a distinct
`timestamp`, needed for the since/before test cases below):

```jsonl
{"type":"user","timestamp":"2026-09-18T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"can you look at the flaky test in payments?"}]}}
{"type":"assistant","timestamp":"2026-09-18T10:05:00Z","message":{"role":"assistant","content":[{"type":"text","text":"I looked at the payments test — it is flaky because of a race in the retry loop."}]}}
{"type":"assistant","timestamp":"2026-09-18T11:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"unrelated turn about docs"}]}}
```

Create `packages/claude-transcript/search_test.go`:

```go
package claudetranscript

import (
	"testing"
	"time"
)

func TestSearch_MatchesCaseInsensitiveSubstring(t *testing.T) {
	matches, err := Search("testdata/search-basic.jsonl", "flaky", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("got %d matches, want 2: %+v", len(matches), matches)
	}
	if matches[0].Role != "user" || matches[0].Line != 1 {
		t.Errorf("match[0] = %+v, want role=user line=1", matches[0])
	}
	if matches[1].Role != "assistant" || matches[1].Line != 2 {
		t.Errorf("match[1] = %+v, want role=assistant line=2", matches[1])
	}
}

func TestSearch_NoMatches(t *testing.T) {
	matches, err := Search("testdata/search-basic.jsonl", "nonexistent-token", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("got %d matches, want 0", len(matches))
	}
}

func TestSearch_MissingFile(t *testing.T) {
	if _, err := Search("testdata/does-not-exist.jsonl", "x", time.Time{}, time.Time{}); err == nil {
		t.Fatal("expected an error for a missing file, got nil")
	}
}

func TestSearch_SinceExcludesEarlierMatches(t *testing.T) {
	// "flaky" matches lines 1 (10:00) and 2 (10:05). since=10:03 must keep
	// only line 2.
	since := time.Date(2026, 9, 18, 10, 3, 0, 0, time.UTC)
	matches, err := Search("testdata/search-basic.jsonl", "flaky", since, time.Time{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 1 || matches[0].Line != 2 {
		t.Fatalf("got %+v, want exactly line 2", matches)
	}
}

func TestSearch_BeforeExcludesLaterMatches(t *testing.T) {
	before := time.Date(2026, 9, 18, 10, 3, 0, 0, time.UTC)
	matches, err := Search("testdata/search-basic.jsonl", "flaky", time.Time{}, before)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 1 || matches[0].Line != 1 {
		t.Fatalf("got %+v, want exactly line 1", matches)
	}
}

func TestSearch_SinceAndBeforeNarrowToOneWindow(t *testing.T) {
	since := time.Date(2026, 9, 18, 10, 1, 0, 0, time.UTC)
	before := time.Date(2026, 9, 18, 10, 30, 0, 0, time.UTC)
	matches, err := Search("testdata/search-basic.jsonl", "flaky", since, before)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 1 || matches[0].Line != 2 {
		t.Fatalf("got %+v, want exactly line 2", matches)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/claude-transcript && go test ./... -run TestSearch -v`
Expected: FAIL — `Search` (and `Match`) undefined, or signature mismatch.

- [ ] **Step 3: Write minimal implementation**

Create `packages/claude-transcript/search.go`:

```go
// search.go: a naive, unindexed per-call scan of one transcript's text
// content for a substring query, optionally bounded by time — the
// concrete backing for pg-connector's agentsession search.Provider
// implementation. Deliberately no index: an efficient/cross-history
// search is explicitly deferred (design decision, 2026-09-18). The time
// bound narrows the scan; it does not index it.
package claudetranscript

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

// Match is one line of one transcript that matched a Search query.
type Match struct {
	// Role is the matching event's message role ("user" or "assistant").
	Role string
	// Line is the 1-indexed line number within the transcript file.
	Line int
	// Snippet is the matching text block's full text (callers needing a
	// bounded-length preview truncate it themselves).
	Snippet string
	// Timestamp is the matching event's own Timestamp, as already parsed
	// from the transcript (Event.Timestamp) — not separately computed.
	Timestamp time.Time
}

// Search scans path (a transcript .jsonl file) for query as a
// case-insensitive substring match against every user/assistant text
// block whose own Timestamp falls within [since, before] (either bound
// may be the zero time.Time, meaning unbounded on that side), returning
// one Match per matching event. An empty result (nil, nil) is a
// well-formed "no matches," not an error.
func Search(path, query string, since, before time.Time) ([]Match, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	lower := strings.ToLower(query)
	var matches []Match
	scanner := newTranscriptScanner(f)
	line := 0
	for scanner.Scan() {
		line++
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue // tolerate non-event lines, mirrors LastAssistantText
		}
		if ev.Type != "user" && ev.Type != "assistant" {
			continue
		}
		if !since.IsZero() && ev.Timestamp.Before(since) {
			continue
		}
		if !before.IsZero() && ev.Timestamp.After(before) {
			continue
		}
		var b strings.Builder
		for _, blk := range ev.Message.Content {
			if blk.Type == "text" {
				b.WriteString(blk.Text)
			}
		}
		text := b.String()
		if text == "" {
			continue
		}
		if strings.Contains(strings.ToLower(text), lower) {
			matches = append(matches, Match{Role: ev.Type, Line: line, Snippet: text, Timestamp: ev.Timestamp})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return matches, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/claude-transcript && go test ./... -run TestSearch -v`
Expected: PASS (all six subtests).

- [ ] **Step 5: Commit**

```bash
git add packages/claude-transcript/search.go packages/claude-transcript/search_test.go packages/claude-transcript/testdata/search-basic.jsonl
git commit -m "claude-transcript: add naive Search primitive"
```

---

### Task 2: `pa-monitor status --json`

**Files:**

- Modify: `packages/pa-monitor/cmd/pa-monitor/cli.go` (`runStatus`, plus new shared helpers)
- Create: `packages/pa-monitor/cmd/pa-monitor/status_json.go`
- Create: `packages/pa-monitor/cmd/pa-monitor/status_json_test.go`

**Interfaces:**

- Produces: `type sessionJSON struct{...}`, `func statusJSON(state *pb.DaemonState, details
[]*pb.SessionDetail, now time.Time) statusJSONDoc`, and the shared helpers `stripJSONFlag(args
[]string) (rest []string, jsonMode bool)`, `contextWithTimeout() (context.Context,
context.CancelFunc)`, `dialOrExit(ctx context.Context) (*rpcclient.Client, error)`,
  `getStateOrExit(ctx context.Context, client *rpcclient.Client) (*pb.DaemonState, error)` — Task 3
  reuses `sessionJSON`/`stripJSONFlag`; Task 4 reuses all four helpers; Phase 2 Task 9 unmarshals
  this exact wire shape, so field names here are load-bearing across modules.

**Verified wire shape** (`internal/proto/pa_monitor.pb.go`, confirmed by direct inspection — the RPC
is `GetState(GetStateRequest) returns (DaemonState)`, and every real call site
(`cli.go:26`/`auto_resume.go:48`/`cmux_bridge.go:495`) types the result `*pb.DaemonState`, never
`GetStateResponse`, which does not exist anywhere in this module). `SessionView` already carries
`SessionId`, `Pid` (uint32; `0` means dead — there is no separate liveness bool), `Cwd`, `Name`,
`Model`, `Status`, `Blocker`, `Branch`, `TerminalHost`, `StartedAt`, `TranscriptMtime`,
`SessionTokens`, `CostUsd`. `Block`/`Week` already carry `Id`, `CostUsd`, `CapHitAt`
(`*timestamppb.Timestamp`, nil when not hit). `long_idle` is not on the wire — compute it via
`session.IsLongIdle(now, transcriptMtime, session.LongIdleThreshold)` (exported from
`internal/core/session`, same module, importable from `cmd/pa-monitor`).

- [ ] **Step 1: Write the failing test**

Create `packages/pa-monitor/cmd/pa-monitor/status_json_test.go`:

```go
package main

import (
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

func TestStatusJSON_SessionFields(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	state := &pb.DaemonState{
		Dirs: []*pb.Directory{{
			Sessions: []*pb.SessionView{{
				SessionId:     "s1",
				Pid:           4567,
				Cwd:           "/repo",
				Model:         "claude-sonnet-5",
				Status:        "blocked",
				Blocker:       "usage_limit",
				SessionTokens: 12345,
				CostUsd:       1.23,
				StartedAt:     timestamppb.New(now.Add(-time.Hour)),
			}},
		}},
		ActiveBlock: &pb.Block{Id: "b1", CostUsd: 4.5},
	}
	details := []*pb.SessionDetail{{View: state.Dirs[0].Sessions[0]}}

	doc := statusJSON(state, details, now)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed struct {
		Sessions []struct {
			SessionID     string  `json:"session_id"`
			Pid           int     `json:"pid"`
			Status        string  `json:"status"`
			Blocker       string  `json:"blocker"`
			SessionTokens uint64  `json:"session_tokens"`
			CostUSD       float64 `json:"cost_usd"`
			LongIdle      bool    `json:"long_idle"`
		} `json:"sessions"`
		ActiveBlock *struct {
			ID      string  `json:"id"`
			CostUSD float64 `json:"cost_usd"`
		} `json:"active_block"`
		ActiveWeek *struct{} `json:"active_week"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(parsed.Sessions))
	}
	s := parsed.Sessions[0]
	if s.SessionID != "s1" || s.Pid != 4567 || s.Status != "blocked" || s.Blocker != "usage_limit" {
		t.Errorf("session fields wrong: %+v", s)
	}
	if s.SessionTokens != 12345 || s.CostUSD != 1.23 {
		t.Errorf("usage fields wrong: %+v", s)
	}
	if parsed.ActiveBlock == nil || parsed.ActiveBlock.ID != "b1" {
		t.Errorf("active_block wrong: %+v", parsed.ActiveBlock)
	}
	if parsed.ActiveWeek != nil {
		t.Errorf("active_week should be nil, got %+v", parsed.ActiveWeek)
	}
}

func TestStatusJSON_DeadPidOmitted(t *testing.T) {
	now := time.Now().UTC()
	state := &pb.DaemonState{
		Dirs: []*pb.Directory{{Sessions: []*pb.SessionView{{SessionId: "s2", Pid: 0}}}},
	}
	doc := statusJSON(state, nil, now)
	raw, _ := json.Marshal(doc)
	var parsed struct {
		Sessions []map[string]any `json:"sessions"`
	}
	_ = json.Unmarshal(raw, &parsed)
	if _, ok := parsed.Sessions[0]["pid"]; ok {
		t.Errorf("pid should be omitted for a dead session, got %v", parsed.Sessions[0]["pid"])
	}
}

func TestStripJSONFlag(t *testing.T) {
	rest, jsonMode := stripJSONFlag([]string{"session:s1", "--json"})
	if !jsonMode || len(rest) != 1 || rest[0] != "session:s1" {
		t.Errorf("got rest=%v jsonMode=%v", rest, jsonMode)
	}
	rest, jsonMode = stripJSONFlag([]string{"--json", "session:s1"})
	if !jsonMode || len(rest) != 1 || rest[0] != "session:s1" {
		t.Errorf("--json before the positional arg: got rest=%v jsonMode=%v", rest, jsonMode)
	}
	rest, jsonMode = stripJSONFlag([]string{"session:s1"})
	if jsonMode || len(rest) != 1 {
		t.Errorf("no flag present: got rest=%v jsonMode=%v", rest, jsonMode)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pa-monitor && go test ./cmd/pa-monitor/... -run 'TestStatusJSON|TestStripJSONFlag' -v`
Expected: FAIL — `statusJSON`/`stripJSONFlag` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `packages/pa-monitor/cmd/pa-monitor/status_json.go`:

```go
// status_json.go: the --json sibling of runStatus's text output, plus the
// small helpers status/info/search all share for flag parsing and
// dial/GetState boilerplate. Emits exactly the data runStatus already
// gathers (state.GetDirs() sessions + the per-session GetSessionInfo
// details it collects for the annotation table, plus
// ActiveBlock/ActiveWeek) as one JSON document. No new gRPC calls.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/rpcclient"
)

// stripJSONFlag removes a "--json" token from args (wherever it appears —
// never assumed to be in a fixed position relative to a positional
// selector/query) and reports whether it was present, returning the
// remaining args in their original relative order. Shared by
// status/info/search.
func stripJSONFlag(args []string) (rest []string, jsonMode bool) {
	for _, a := range args {
		if a == "--json" {
			jsonMode = true
			continue
		}
		rest = append(rest, a)
	}
	return rest, jsonMode
}

// contextWithTimeout returns the standard 3s-timeout context every
// dial-the-daemon subcommand uses (matches runStatus/runInfo/
// runCaffeinate's existing inline 3*time.Second calls).
func contextWithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 3*time.Second)
}

// dialOrExit dials the daemon, printing the standard unreachable message
// and exiting 2 on failure (matches runStatus's original inline block).
// The returned error is always nil on return — os.Exit already terminated
// the process on failure — callers still check it defensively to satisfy
// Go's control-flow expectations, mirroring this file's own
// getStateOrExit below.
func dialOrExit(ctx context.Context) (*rpcclient.Client, error) {
	client, err := rpcclient.Dial(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, rpcclient.DaemonUnavailableMessage("<unknown>"))
		os.Exit(2)
	}
	return client, nil
}

// getStateOrExit calls GetState, printing a diagnostic and exiting 2 on
// failure (matches runStatus's original inline block).
func getStateOrExit(ctx context.Context, client *rpcclient.Client) (*pb.DaemonState, error) {
	state, err := client.C.GetState(ctx, &pb.GetStateRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: GetState: %v\n", err)
		os.Exit(2)
	}
	return state, nil
}

// sessionJSON is the --json wire shape for one session — reused verbatim
// by `info --json` (Task 3) for a session selector, and unmarshaled
// verbatim by pg-connector-agentsession-pa-monitor (Phase 2). Field names
// here are the wire contract; changing one is a breaking change for both
// consumers.
type sessionJSON struct {
	SessionID     string  `json:"session_id"`
	Pid           *int    `json:"pid,omitempty"`
	Cwd           string  `json:"cwd"`
	Name          string  `json:"name,omitempty"`
	Model         string  `json:"model"`
	Status        string  `json:"status"`
	Blocker       string  `json:"blocker,omitempty"`
	Branch        string  `json:"branch,omitempty"`
	TerminalHost  string  `json:"terminal_host,omitempty"`
	StartedAt     string  `json:"started_at,omitempty"`
	SessionTokens uint64  `json:"session_tokens"`
	CostUSD       float64 `json:"cost_usd"`
	LongIdle      bool    `json:"long_idle"`
}

type usageWindowJSON struct {
	ID       string  `json:"id"`
	CostUSD  float64 `json:"cost_usd"`
	CapHitAt *string `json:"cap_hit_at,omitempty"`
}

type statusJSONDoc struct {
	Sessions    []sessionJSON    `json:"sessions"`
	ActiveBlock *usageWindowJSON `json:"active_block,omitempty"`
	ActiveWeek  *usageWindowJSON `json:"active_week,omitempty"`
}

// toSessionJSON converts one SessionView into the wire shape. now is
// injected for testability.
func toSessionJSON(v *pb.SessionView, now time.Time) sessionJSON {
	sj := sessionJSON{
		SessionID:     v.GetSessionId(),
		Cwd:           v.GetCwd(),
		Name:          v.GetName(),
		Model:         v.GetModel(),
		Status:        v.GetStatus(),
		Blocker:       v.GetBlocker(),
		Branch:        v.GetBranch(),
		TerminalHost:  v.GetTerminalHost(),
		SessionTokens: v.GetSessionTokens(),
		CostUSD:       v.GetCostUsd(),
	}
	if pid := v.GetPid(); pid != 0 {
		p := int(pid)
		sj.Pid = &p
	}
	if ts := v.GetStartedAt(); ts != nil {
		sj.StartedAt = ts.AsTime().UTC().Format(time.RFC3339)
	}
	if ts := v.GetTranscriptMtime(); ts != nil {
		sj.LongIdle = session.IsLongIdle(now, ts.AsTime(), session.LongIdleThreshold)
	}
	return sj
}

func toUsageWindowJSON(id string, costUSD float64, capHitAt *timestamppb.Timestamp) *usageWindowJSON {
	uw := &usageWindowJSON{ID: id, CostUSD: costUSD}
	if capHitAt != nil {
		s := capHitAt.AsTime().UTC().Format(time.RFC3339)
		uw.CapHitAt = &s
	}
	return uw
}

// statusJSON builds the full --json document from state (as GetState
// returned it) and now (injected for testability; production callers pass
// time.Now().UTC()). details is currently unused by the JSON path (the
// text path's LastError/PendingNudge annotations have no --json
// equivalent yet — out of scope for this task) but is accepted so a
// future extension does not need to change this function's signature.
func statusJSON(state *pb.DaemonState, details []*pb.SessionDetail, now time.Time) statusJSONDoc {
	var doc statusJSONDoc
	for _, d := range state.GetDirs() {
		for _, v := range d.GetSessions() {
			if v.GetSessionId() == "" {
				continue
			}
			doc.Sessions = append(doc.Sessions, toSessionJSON(v, now))
		}
	}
	if b := state.GetActiveBlock(); b != nil {
		doc.ActiveBlock = toUsageWindowJSON(b.GetId(), b.GetCostUsd(), b.GetCapHitAt())
	}
	if w := state.GetActiveWeek(); w != nil {
		doc.ActiveWeek = toUsageWindowJSON(w.GetId(), w.GetCostUsd(), w.GetCapHitAt())
	}
	return doc
}

func writeStatusJSON(w io.Writer, state *pb.DaemonState, details []*pb.SessionDetail) error {
	enc := json.NewEncoder(w)
	return enc.Encode(statusJSON(state, details, time.Now().UTC()))
}
```

Note: `toUsageWindowJSON`'s `capHitAt` parameter type is `*timestamppb.Timestamp` — add the
`"google.golang.org/protobuf/types/known/timestamppb"` import.

Modify `packages/pa-monitor/cmd/pa-monitor/cli.go`'s `runStatus`:

1. At the top, add `args, jsonMode := stripJSONFlag(args)` (the `args` result is unused by
   `runStatus` itself today since it takes no positional args, but keep the assignment so a future
   positional flag added to `status` composes correctly — use `_ = args` if the linter complains).
2. Replace the existing inline `context.WithTimeout(context.Background(), 3*time.Second)` /
   `rpcclient.Dial(ctx)` / `client.C.GetState(ctx, &pb.GetStateRequest{})` block with calls to
   `contextWithTimeout()`/`dialOrExit(ctx)`/`getStateOrExit(ctx, client)` — a pure extract-method
   refactor, no behavior change.
3. Right after `details` is built (the existing per-session `GetSessionInfo` loop) and before any
   text-formatting call: `if jsonMode { if err := writeStatusJSON(os.Stdout, state, details); err
!= nil { fmt.Fprintf(os.Stderr, "status: %v\n", err); os.Exit(2) }; return }`.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pa-monitor && go test ./cmd/pa-monitor/... -v`
Expected: PASS — including every pre-existing test in this package (`cli_format_test.go`,
`control_test.go`, `daemon_test.go`, etc.); the `runStatus` extract-method refactor in step 3 must
not change any existing test's outcome.

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/cmd/pa-monitor/status_json.go packages/pa-monitor/cmd/pa-monitor/status_json_test.go packages/pa-monitor/cmd/pa-monitor/cli.go
git commit -m "pa-monitor: add --json to status"
```

---

### Task 3: `pa-monitor info <selector> --json`

**Files:**

- Modify: `packages/pa-monitor/cmd/pa-monitor/control.go` (`runInfo`)
- Create: `packages/pa-monitor/cmd/pa-monitor/info_json_test.go`

**Interfaces:**

- Consumes: `sessionJSON`, `toSessionJSON`, `stripJSONFlag` from Task 2.
- Produces: for a `session:<id>` selector, `--json` emits exactly one `sessionJSON` object (not
  wrapped in an array) — Phase 2 Task 8's `Show` unmarshals this shape directly.

- [ ] **Step 1: Write the failing test**

Create `packages/pa-monitor/cmd/pa-monitor/info_json_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

func TestWriteSessionInfoJSON(t *testing.T) {
	v := &pb.SessionView{SessionId: "s1", Status: "working", Model: "claude-sonnet-5"}
	var buf bytes.Buffer
	if err := writeSessionInfoJSON(&buf, v, time.Now().UTC()); err != nil {
		t.Fatalf("writeSessionInfoJSON: %v", err)
	}
	var got sessionJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.SessionID != "s1" || got.Status != "working" || got.Model != "claude-sonnet-5" {
		t.Errorf("got %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pa-monitor && go test ./cmd/pa-monitor/... -run TestWriteSessionInfoJSON -v`
Expected: FAIL — `writeSessionInfoJSON` undefined.

- [ ] **Step 3: Write minimal implementation**

Add to `packages/pa-monitor/cmd/pa-monitor/control.go` (near `runInfo`), with `"encoding/json"` and
`"time"` added to its imports:

```go
// writeSessionInfoJSON writes v as one sessionJSON object (Task 2's shape)
// — the --json sibling of formatSessionInfoHeader/formatSessionInfo's text
// output for a session: selector.
func writeSessionInfoJSON(w io.Writer, v *pb.SessionView, now time.Time) error {
	enc := json.NewEncoder(w)
	return enc.Encode(toSessionJSON(v, now))
}
```

Modify `runInfo`: at the top, `args, jsonMode := stripJSONFlag(args)` — this correctly handles
`--json` appearing either before or after the selector token (`pa-monitor info --json
session:s1` and `pa-monitor info session:s1 --json` both work identically), unlike assuming a fixed
position. In the `sel.GetSessionId() != ""`-style branch (after `GetSessionInfo` succeeds and
before the existing `formatSessionInfoHeader`/`formatSessionInfo` text calls), add: `if jsonMode {
if err := writeSessionInfoJSON(os.Stdout, resp.GetView(), time.Now().UTC()); err != nil { ... exit
2 ... }; return }`. The `sel.GetPath() != ""` (directory-rollup) branch stays text-only for this
task — path-selector JSON is not needed by Phase 2 and is out of scope here.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pa-monitor && go test ./cmd/pa-monitor/... -run TestWriteSessionInfoJSON -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/cmd/pa-monitor/control.go packages/pa-monitor/cmd/pa-monitor/info_json_test.go
git commit -m "pa-monitor: add --json to info"
```

---

### Task 4: `pa-monitor search` subcommand

**Files:**

- Create: `packages/pa-monitor/cmd/pa-monitor/search.go`
- Create: `packages/pa-monitor/cmd/pa-monitor/search_test.go`
- Modify: `packages/pa-monitor/cmd/pa-monitor/main.go` (register the subcommand, `subcommandNames`)
- Modify: `packages/pa-monitor/cmd/pa-monitor/main_help_test.go` (`allSubcommandNames` — this file
  keeps its own separate hardcoded list specifically to catch help-text/dispatch drift; omitting
  `search` here defeats that guard's purpose even though no test would fail today)
- Modify: `packages/pa-monitor/go.mod` / `packages/pa-monitor/gomod2nix.toml` (add the
  `claude-transcript` local module dependency — see `phillipg-nix-repo-base` ADR 0008's Pattern B
  for the exact `replace => ../claude-transcript` + `modRoot` shape; `pg-connector`'s own
  `go.mod`/`gomod2nix.toml` is the sibling precedent to copy from, since it already depends on
  this same module for other backends).

**Interfaces:**

- Consumes: `claudetranscript.Search(path, query string, since, before time.Time) ([]Match, error)`
  (Task 1), `session.ResolveTranscript(claudeHome, s *session.Session) (path string, mtime
time.Time, ok bool)` (existing, `internal/core/session/transcript.go:76`), `stripJSONFlag`/
  `contextWithTimeout`/`dialOrExit`/`getStateOrExit` (Task 2).
- Produces: JSON on stdout: `{"query": "...", "matches":
[{"session_id","role","line","snippet","timestamp"}]}` — Phase 2 Task 10 (the agentsession
  backend's `search.Provider`) unmarshals this exact shape. `--since <bound>`/`--before <bound>`
  each accept either a `time.ParseDuration` string (interpreted as "this long ago" relative to
  now) or an absolute RFC3339 timestamp.

**Before writing new code, verify the refactor has a safety net.** Task 2/3 extracted
`contextWithTimeout`/`dialOrExit`/`getStateOrExit` out of `runStatus`'s inline logic. Read
`packages/pa-monitor/cmd/pa-monitor/cli_format_test.go` (formatting helpers only — confirmed by
name) and grep the package for any existing test that actually exercises `runStatus`/`runInfo`
against a live or fake daemon. If none exists (as of this plan's writing, no `cli_test.go` exists
in this package at all), the extract-method refactor in Task 2 has no automated regression net
beyond "the package still compiles and `cli_format_test.go`'s formatter-level tests still pass" —
mitigate by keeping that refactor mechanically pure (no logic change, confirmed by re-reading the
diff before committing Task 2) and, if a real pa-monitor daemon is reachable locally, manually
diffing `pa-monitor status` text output before and after the refactor.

- [ ] **Step 1: Write the failing test**

Create `packages/pa-monitor/cmd/pa-monitor/search_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

func TestParseSearchArgs(t *testing.T) {
	cases := []struct {
		name          string
		args          []string
		wantQuery     string
		wantSessionID string
		wantErr       bool
	}{
		{"bare query", []string{"flaky"}, "flaky", "", false},
		{"json before query", []string{"--json", "flaky"}, "flaky", "", false},
		{"json after query", []string{"flaky", "--json"}, "flaky", "", false},
		{"with session", []string{"flaky", "--session", "s1"}, "flaky", "s1", false},
		{"session before query", []string{"--session", "s1", "flaky"}, "flaky", "s1", false},
		{"with since duration", []string{"flaky", "--since", "24h"}, "flaky", "", false},
		{"with before rfc3339", []string{"flaky", "--before", "2026-09-18T00:00:00Z"}, "flaky", "", false},
		{"no query", []string{}, "", "", true},
		{"two positional", []string{"flaky", "other"}, "", "", true},
		{"dangling --session", []string{"flaky", "--session"}, "", "", true},
		{"invalid --since", []string{"flaky", "--since", "not-a-time"}, "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			query, sessionID, _, _, err := parseSearchArgs(c.args, time.Now())
			if c.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSearchArgs: %v", err)
			}
			if query != c.wantQuery || sessionID != c.wantSessionID {
				t.Errorf("got query=%q sessionID=%q, want query=%q sessionID=%q", query, sessionID, c.wantQuery, c.wantSessionID)
			}
		})
	}
}

func TestParseTimeBound(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	got, err := parseTimeBound("24h", now)
	if err != nil {
		t.Fatalf("parseTimeBound(24h): %v", err)
	}
	if want := now.Add(-24 * time.Hour); !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}

	got, err = parseTimeBound("2026-09-17T00:00:00Z", now)
	if err != nil {
		t.Fatalf("parseTimeBound(rfc3339): %v", err)
	}
	want := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}

	if _, err := parseTimeBound("not-a-time", now); err == nil {
		t.Fatal("expected an error for an unparseable bound")
	}
}

func TestSearchSessions_FindsMatchInOneTranscript(t *testing.T) {
	home := t.TempDir()
	projDir := filepath.Join(home, "projects", "-repo")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(projDir, "s1.jsonl")
	line := `{"type":"assistant","timestamp":"2026-09-18T10:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"the payments test is flaky"}]}}` + "\n"
	if err := os.WriteFile(transcript, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	sessions := []*session.Session{{SessionID: "s1", Cwd: "/repo"}}
	var buf bytes.Buffer
	if err := searchSessions(&buf, home, sessions, "flaky", "", time.Time{}, time.Time{}); err != nil {
		t.Fatalf("searchSessions: %v", err)
	}

	var doc searchJSONDoc
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Query != "flaky" || len(doc.Matches) != 1 {
		t.Fatalf("got %+v", doc)
	}
	if doc.Matches[0].SessionID != "s1" || doc.Matches[0].Role != "assistant" || doc.Matches[0].Timestamp == "" {
		t.Errorf("match wrong: %+v", doc.Matches[0])
	}
}

func TestSearchSessions_ScopedToOneSession(t *testing.T) {
	home := t.TempDir()
	sessions := []*session.Session{{SessionID: "s1", Cwd: "/repo"}, {SessionID: "s2", Cwd: "/repo"}}
	var buf bytes.Buffer
	// Neither session has a real transcript on disk; --session s2 must not
	// error just because s1 (unresolvable) is in the broader session list.
	if err := searchSessions(&buf, home, sessions, "x", "s2", time.Time{}, time.Time{}); err != nil {
		t.Fatalf("searchSessions: %v", err)
	}
	var doc searchJSONDoc
	_ = json.Unmarshal(buf.Bytes(), &doc)
	if len(doc.Matches) != 0 {
		t.Errorf("expected no matches for an unresolvable transcript, got %+v", doc.Matches)
	}
}

func TestSearchSessions_SinceExcludesEarlierMatch(t *testing.T) {
	home := t.TempDir()
	projDir := filepath.Join(home, "projects", "-repo")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(projDir, "s1.jsonl")
	line := `{"type":"assistant","timestamp":"2026-09-18T10:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"the payments test is flaky"}]}}` + "\n"
	if err := os.WriteFile(transcript, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	sessions := []*session.Session{{SessionID: "s1", Cwd: "/repo"}}
	since := time.Date(2026, 9, 18, 11, 0, 0, 0, time.UTC)
	var buf bytes.Buffer
	if err := searchSessions(&buf, home, sessions, "flaky", "", since, time.Time{}); err != nil {
		t.Fatalf("searchSessions: %v", err)
	}
	var doc searchJSONDoc
	_ = json.Unmarshal(buf.Bytes(), &doc)
	if len(doc.Matches) != 0 {
		t.Errorf("expected the match to be excluded by --since, got %+v", doc.Matches)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pa-monitor && go test ./cmd/pa-monitor/... -run 'TestParseSearchArgs|TestSearchSessions' -v`
Expected: FAIL — `parseSearchArgs`/`searchSessions`/`searchJSONDoc` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `packages/pa-monitor/cmd/pa-monitor/search.go`:

```go
// search.go: the `search` subcommand — resolves each in-scope session's
// transcript via session.ResolveTranscript, then scans it with
// claude-transcript's naive Search primitive, optionally bounded by
// --since/--before. No index, no daemon RPC of its own beyond whatever
// runSearch used to build the session list.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	claudetranscript "github.com/phillipgreenii/claude-transcript"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

type searchMatchJSON struct {
	SessionID string `json:"session_id"`
	Role      string `json:"role"`
	Line      int    `json:"line"`
	Snippet   string `json:"snippet"`
	Timestamp string `json:"timestamp,omitempty"`
}

type searchJSONDoc struct {
	Query   string            `json:"query"`
	Matches []searchMatchJSON `json:"matches"`
}

// parseTimeBound parses a --since/--before value as either a
// time.ParseDuration string (interpreted as "this long ago" relative to
// now — mirrors this repo's existing attention.perBackend.threshold
// convention, e.g. "24h") or an absolute RFC3339 timestamp. The two forms
// never collide syntactically (a duration string has no "-"/":"/"T"
// punctuation), so duration is tried first with no ambiguity.
func parseTimeBound(s string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid time bound %q: not a duration (e.g. \"24h\") or RFC3339 timestamp", s)
	}
	return t, nil
}

// parseSearchArgs parses `search`'s own argv: exactly one positional query
// token, an optional "--json" flag (accepted but not required — search has
// no text-output mode, so this flag is a no-op kept only for command-line
// symmetry with status/info), and optional "--session <id>"/"--since
// <bound>"/"--before <bound>" value flags — any of these MAY appear in any
// order. now is injected for testability; production callers pass
// time.Now().UTC().
func parseSearchArgs(args []string, now time.Time) (query, sessionID string, since, before time.Time, err error) {
	rest, _ := stripJSONFlag(args)
	var positional []string
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--session":
			if i+1 >= len(rest) {
				return "", "", time.Time{}, time.Time{}, errors.New("search: --session requires a value")
			}
			sessionID = rest[i+1]
			i++
		case "--since":
			if i+1 >= len(rest) {
				return "", "", time.Time{}, time.Time{}, errors.New("search: --since requires a value")
			}
			since, err = parseTimeBound(rest[i+1], now)
			if err != nil {
				return "", "", time.Time{}, time.Time{}, fmt.Errorf("search: --since: %w", err)
			}
			i++
		case "--before":
			if i+1 >= len(rest) {
				return "", "", time.Time{}, time.Time{}, errors.New("search: --before requires a value")
			}
			before, err = parseTimeBound(rest[i+1], now)
			if err != nil {
				return "", "", time.Time{}, time.Time{}, fmt.Errorf("search: --before: %w", err)
			}
			i++
		default:
			positional = append(positional, rest[i])
		}
	}
	if len(positional) != 1 {
		return "", "", time.Time{}, time.Time{}, fmt.Errorf("search: expected exactly one query argument, got %d", len(positional))
	}
	return positional[0], sessionID, since, before, nil
}

// searchSessions scans every session in sessions (or only sessionID, when
// non-empty) for query within [since, before] (either may be the zero
// time.Time, meaning unbounded), writing the result as JSON to w. A
// session whose transcript cannot be resolved (ResolveTranscript's ok ==
// false) is silently skipped — not an error, mirroring how a dead/cold
// session is silently skipped elsewhere in this CLI (e.g. runStatus's
// per-session GetSessionInfo loop).
func searchSessions(w io.Writer, claudeHome string, sessions []*session.Session, query, sessionID string, since, before time.Time) error {
	doc := searchJSONDoc{Query: query}
	for _, s := range sessions {
		if sessionID != "" && s.SessionID != sessionID {
			continue
		}
		path, _, ok := session.ResolveTranscript(claudeHome, s)
		if !ok {
			continue
		}
		matches, err := claudetranscript.Search(path, query, since, before)
		if err != nil {
			continue // unreadable transcript: skip, don't fail the whole search
		}
		for _, m := range matches {
			mj := searchMatchJSON{SessionID: s.SessionID, Role: m.Role, Line: m.Line, Snippet: m.Snippet}
			if !m.Timestamp.IsZero() {
				mj.Timestamp = m.Timestamp.UTC().Format(time.RFC3339)
			}
			doc.Matches = append(doc.Matches, mj)
		}
	}
	enc := json.NewEncoder(w)
	return enc.Encode(doc)
}

// runSearch implements the `search` subcommand: `pa-monitor search
// [--json] <query> [--session <id>] [--since <bound>] [--before <bound>]`.
// It dials the daemon the same way runStatus does (via Task 2's shared
// helpers), converts the returned SessionViews into session.Session
// values (only the fields ResolveTranscript needs: SessionID, Cwd, Name),
// and delegates to searchSessions.
func runSearch(args []string) {
	query, sessionID, since, before, err := parseSearchArgs(args, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(3)
	}

	ctx, cancel := contextWithTimeout()
	defer cancel()
	client, err := dialOrExit(ctx)
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()

	state, err := getStateOrExit(ctx, client)
	if err != nil {
		return
	}

	var sessions []*session.Session
	for _, d := range state.GetDirs() {
		for _, v := range d.GetSessions() {
			if v.GetSessionId() == "" {
				continue
			}
			sessions = append(sessions, &session.Session{
				SessionID: v.GetSessionId(), Cwd: v.GetCwd(), Name: v.GetName(),
			})
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "search: %v\n", err)
		os.Exit(2)
	}
	claudeHome := home + "/.claude"
	if err := searchSessions(os.Stdout, claudeHome, sessions, query, sessionID, since, before); err != nil {
		fmt.Fprintf(os.Stderr, "search: %v\n", err)
		os.Exit(2)
	}
}
```

Modify `packages/pa-monitor/cmd/pa-monitor/main.go`: add `"search"` to `subcommandNames`, and a
`case "search": runSearch(rest)` arm in `run`'s switch.

Modify `packages/pa-monitor/cmd/pa-monitor/main_help_test.go`: add `"search"` to
`allSubcommandNames`.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pa-monitor && go test ./cmd/pa-monitor/... -v`
Expected: PASS — including all pre-existing tests (the `runStatus` helper-sharing must not change
observable behavior).

- [ ] **Step 5: Commit**

```bash
git add packages/pa-monitor/cmd/pa-monitor/search.go packages/pa-monitor/cmd/pa-monitor/search_test.go packages/pa-monitor/cmd/pa-monitor/main.go packages/pa-monitor/cmd/pa-monitor/main_help_test.go packages/pa-monitor/go.mod packages/pa-monitor/gomod2nix.toml
git commit -m "pa-monitor: add search subcommand"
```

---

## Phase 2 — new pg-connector capability + backend

### Task 5: `pkg/schema/agentsession.go`

**Files:**

- Create: `packages/pg-connector/pkg/schema/agentsession.go`
- Create: `packages/pg-connector/pkg/schema/agentsession_test.go`
- Modify: `packages/pg-connector/pkg/schema/versions.go` (register
  `AgentSessionSchemaVersion` in `CurrentSchemaVersions`)
- Modify: `packages/pg-connector/cmd/pg-connector/entity_store_test.go`
  (`entityKindTokens` map)

**Interfaces:**

- Produces: `schema.AgentSession`, `schema.AgentSessionListResult`,
  `schema.AgentSessionSchemaVersion` — every later task in this phase imports these exact types.

- [ ] **Step 1: Write the failing test**

Create `packages/pg-connector/pkg/schema/agentsession_test.go`:

```go
package schema

import (
	"encoding/json"
	"testing"
)

func TestAgentSession_JSONFieldNames(t *testing.T) {
	pid := 4567
	s := AgentSession{
		SessionID: "s1", PID: &pid, Cwd: "/repo", Model: "claude-sonnet-5",
		Status: "blocked", Blocker: "usage_limit", Tokens: 12345, CostUSD: 1.23,
		AsOf: "2026-09-18T12:00:00Z", Stale: false,
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, key := range []string{"session_id", "pid", "cwd", "model", "status", "blocker", "tokens", "cost_usd", "as_of"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing expected JSON key %q in %s", key, raw)
		}
	}
	if _, ok := m["transcript_path"]; ok {
		t.Errorf("AgentSession must not carry a transcript path field, got %s", raw)
	}
}

func TestAgentSession_DeadPIDOmitted(t *testing.T) {
	raw, _ := json.Marshal(AgentSession{SessionID: "s1"})
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if _, ok := m["pid"]; ok {
		t.Errorf("pid should be omitted when nil, got %s", raw)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pg-connector && go test ./pkg/schema/... -run TestAgentSession -v`
Expected: FAIL — `AgentSession` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `packages/pg-connector/pkg/schema/agentsession.go`:

```go
// agentsession.go: the agentsession entity/capability's shared JSON wire
// shape [design: docs/superpowers/specs/2026-09-18-agentsession-connector-design.md].
// Every field mirrors a fact pa-monitor already tracks — nothing here is
// newly computed by this capability. Deliberately carries no transcript
// path/content field: transcript access happens only through the search
// capability (see the design's Summary for why).
package schema

// AgentSessionSchemaVersion is the agentsession capability's own schema
// version, mirroring every other capability's identical
// <Entity>SchemaVersion convention (INV-VER-1).
const AgentSessionSchemaVersion = 1

// AgentSession is one Claude Code agent session as pa-monitor reports it.
type AgentSession struct {
	SessionID    string  `json:"session_id"`
	PID          *int    `json:"pid,omitempty"` // nil when the process is dead
	Cwd          string  `json:"cwd"`
	Name         string  `json:"name,omitempty"`
	Model        string  `json:"model"`
	Status       string  `json:"status"`            // "working" | "blocked" | "idle"
	Blocker      string  `json:"blocker,omitempty"` // "human_input" | "human_authn" | "usage_limit" | "error"
	Branch       string  `json:"branch,omitempty"`
	TerminalHost string  `json:"terminal_host,omitempty"`
	StartedAt    string  `json:"started_at,omitempty"`
	Tokens       uint64  `json:"tokens"`
	CostUSD      float64 `json:"cost_usd"`
	LongIdle     bool    `json:"long_idle"`

	// AsOf/Stale: INV-ASOF-1/2, the same contract every other capability
	// carries.
	AsOf  string `json:"as_of"`
	Stale bool   `json:"stale"`
}

// AgentSessionListResult is the "list" op's wire result payload — the same
// generic shape ThreadListResult/IssueListResult document in full.
type AgentSessionListResult struct {
	Entities   []AgentSession `json:"entities"`
	PresentIDs []string       `json:"present_ids"`
	Cursor     *string        `json:"cursor"`
	Truncated  bool           `json:"truncated"`
}
```

Modify `packages/pg-connector/pkg/schema/versions.go`: add
`"agentsession": AgentSessionSchemaVersion` to `CurrentSchemaVersions`.

Modify `packages/pg-connector/cmd/pg-connector/entity_store_test.go`: add
`"agentsession": "agentsession"` to the `entityKindTokens` map (mirrors the `thread` addition's own
precedent noted in that file's doc comment).

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pg-connector && go test ./pkg/schema/... ./cmd/pg-connector/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pg-connector/pkg/schema/agentsession.go packages/pg-connector/pkg/schema/agentsession_test.go packages/pg-connector/pkg/schema/versions.go packages/pg-connector/cmd/pg-connector/entity_store_test.go
git commit -m "pg-connector: add agentsession schema"
```

---

### Task 6: `pkg/provider/agentsession` (interface + dispatch table)

**Files:**

- Create: `packages/pg-connector/pkg/provider/agentsession/iface.go`
- Create: `packages/pg-connector/pkg/provider/agentsession/dispatch.go`
- Create: `packages/pg-connector/pkg/provider/agentsession/dispatch_test.go`
- Modify: `packages/pg-connector/cmd/pg-connector/naming_convention_test.go`
  (`capabilityPackages`)

**Interfaces:**

- Consumes: `schema.AgentSession`, `schema.AgentSessionListResult`, `schema.AgentSessionSchemaVersion`
  (Task 5), `scriptout.DispatchTable`/`scriptout.Decode`/`scriptout.WrapError`/
  `scriptout.ErrInvalidArgument` (existing).
- Produces: `agentsession.Provider` interface and `agentsession.NewDispatchTable(p Provider)
scriptout.DispatchTable` — Task 8's backend implements `Provider`; Task 8's `main.go` calls
  `NewDispatchTable`.

**Deliberate divergence from thread/issue's "list" pattern:** this capability's `list` op does NOT
resolve a caller-facing named query via `schema.ResolveQuery` the way `thread`/`issue`'s dispatch
tables do. pa-monitor's `status` op returns one fixed default session scope — there is no
per-backend `config.queries` concept for it to resolve against, and adding one with nothing behind
it would be unusable dead weight (confirmed: `schema.ResolveQuery(config, "")` looks up
`cfg.Queries[""]`, which no backend config will ever declare, so passing an empty query name would
make every `list` call fail with `query_not_recognized`). `list`'s dispatch entry therefore decodes
only `{ids_only}` and calls `p.List(ctx, nil, a.IDsOnly)` directly — see Task 7 for why the CLI's
own `list` command needs no `--query` flag as a result.

- [ ] **Step 1: Write the failing test**

Create `packages/pg-connector/pkg/provider/agentsession/dispatch_test.go`:

```go
package agentsession

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

type fakeProvider struct {
	showID  string
	show    *schema.AgentSession
	showErr error
	list    *schema.AgentSessionListResult
}

func (f *fakeProvider) Show(_ context.Context, id string) (*schema.AgentSession, error) {
	f.showID = id
	return f.show, f.showErr
}
func (f *fakeProvider) List(_ context.Context, _ schema.QueryExpr, _ bool) (*schema.AgentSessionListResult, error) {
	return f.list, nil
}

func TestDispatchTable_Show(t *testing.T) {
	fp := &fakeProvider{show: &schema.AgentSession{SessionID: "s1"}}
	table := NewDispatchTable(fp)
	entry, ok := table["show"]
	if !ok {
		t.Fatal(`"show" not in dispatch table`)
	}
	args, _ := json.Marshal(map[string]string{"id": "s1"})
	result, err := entry.Handle(context.Background(), args)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.(*schema.AgentSession)
	if !ok || got.SessionID != "s1" {
		t.Errorf("got %+v", result)
	}
	if fp.showID != "s1" {
		t.Errorf("Provider.Show called with id %q, want s1", fp.showID)
	}
}

func TestDispatchTable_ShowInvalidArgs(t *testing.T) {
	table := NewDispatchTable(&fakeProvider{})
	_, err := table["show"].Handle(context.Background(), json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected an error for malformed args")
	}
}

func TestDispatchTable_List(t *testing.T) {
	fp := &fakeProvider{list: &schema.AgentSessionListResult{PresentIDs: []string{"s1"}}}
	table := NewDispatchTable(fp)
	args, _ := json.Marshal(map[string]bool{"ids_only": true})
	result, err := table["list"].Handle(context.Background(), args)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.(*schema.AgentSessionListResult)
	if !ok || len(got.PresentIDs) != 1 {
		t.Errorf("got %+v", result)
	}
}

func TestDispatchTable_HasNoWriteOps(t *testing.T) {
	table := NewDispatchTable(&fakeProvider{})
	for _, op := range []string{"create", "update", "close", "delete", "transition"} {
		if _, ok := table[op]; ok {
			t.Errorf("agentsession is read-only; unexpected write op %q registered", op)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pg-connector && go test ./pkg/provider/agentsession/... -v`
Expected: FAIL — package `agentsession` does not exist yet.

- [ ] **Step 3: Write minimal implementation**

Create `packages/pg-connector/pkg/provider/agentsession/iface.go`:

```go
// Package agentsession declares the agentsession capability's provider
// interface [design: docs/superpowers/specs/2026-09-18-agentsession-connector-design.md].
// Read-only (Show/List only, no write ops) — mirrors pkg/provider/thread's
// identical shape, since you don't create or mutate a session through
// this capability.
package agentsession

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Provider is the agentsession capability's provider interface.
type Provider interface {
	// Show returns id's current state.
	Show(ctx context.Context, id string) (*schema.AgentSession, error)

	// List returns sessions currently in scope. query is accepted for
	// interface-shape symmetry with thread/issue's List but is always nil
	// as called by this package's own dispatch table today — see
	// dispatch.go's doc comment for why.
	List(ctx context.Context, query schema.QueryExpr, idsOnly bool) (*schema.AgentSessionListResult, error)
}
```

Create `packages/pg-connector/pkg/provider/agentsession/dispatch.go`:

```go
package agentsession

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// NewDispatchTable builds the agentsession capability's op-dispatch table
// for p: show and list always; auth_status only when p also implements
// pkg/provider.AuthChecker (INV-AUTH-1).
func NewDispatchTable(p Provider) scriptout.DispatchTable {
	table := scriptout.DispatchTable{
		"show": {
			SchemaVersion: schema.AgentSessionSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					ID string `json:"id"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode show args: "+err.Error())
				}
				return p.Show(ctx, a.ID)
			},
		},
		// list decodes only ids_only — no query resolution. See this
		// file's own package-level doc comment (iface.go) for why: this
		// capability has no caller-facing named-query concept today.
		"list": {
			SchemaVersion: schema.AgentSessionSchemaVersion,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					IDsOnly bool `json:"ids_only"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode list args: "+err.Error())
				}
				return p.List(ctx, nil, a.IDsOnly)
			},
		},
	}

	if ac, ok := p.(provider.AuthChecker); ok {
		table[scriptout.OpAuthStatus] = scriptout.OpHandler{
			SchemaVersion: schema.AgentSessionSchemaVersion,
			Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
				if err := ac.CheckAuth(ctx); err != nil {
					return scriptout.AuthStatus{State: scriptout.AuthMissing, Detail: err.Error()}, nil
				}
				return scriptout.AuthStatus{State: scriptout.AuthOK}, nil
			},
		}
	}

	return table
}
```

Modify `packages/pg-connector/cmd/pg-connector/naming_convention_test.go`: add `"agentsession"` to
`capabilityPackages` (mirrors the `thread`/`calendar` precedent already documented in that
variable's own doc comment).

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pg-connector && go test ./pkg/provider/agentsession/... ./cmd/pg-connector/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pg-connector/pkg/provider/agentsession/ packages/pg-connector/cmd/pg-connector/naming_convention_test.go
git commit -m "pg-connector: add agentsession provider interface + dispatch table"
```

---

### Task 7: Tier-1 CLI verb group + registry wiring

**Files:**

- Create: `packages/pg-connector/cmd/pg-connector/agentsession.go`
- Create: `packages/pg-connector/cmd/pg-connector/agentsession_test.go`
- Modify: `packages/pg-connector/cmd/pg-connector/registry.go` (`entityTypes`)
- Modify: `packages/pg-connector/cmd/pg-connector/root.go` (`newRootCmd`)

**Verified against real source** (this was the plan's single biggest defect in an earlier draft —
every call below is checked against the actual file, not assumed): `output.go:106`/`:130` declares
`type humanizeResult func(result json.RawMessage) (string, error)` and
`func writeTargetedResult(cmd *cobra.Command, resp *scriptout.Response, err error, humanize
humanizeResult) error` — EVERY error, including a pre-dispatch registry-resolution error, is routed
through this one function with `resp` nil (never a bare `return err` to cobra — that reintroduces
bug `pg2-njx27`, empty stdout on error). `backend_flag.go:22`'s `addBackendFlag(cmd *cobra.Command,
help string) *string` registers the `--backend` flag and returns a pointer read after `cmd.RunE`
runs. `dispatch.go:82`'s `Dispatch(ctx, reg, entityType, op, args, pinned)` and `dispatch.go:122`'s
`DispatchTargeted(ctx, reg, entityType, op, args, pinned)` are the two real resolution helpers —
`DispatchTargeted` is for id-keyed ops (mirrors `issue.go`'s `show`); `Dispatch` hard-fails at N>1
registered backends with no pin (used here for `list`, which is not id-keyed).

**Interfaces:**

- Consumes: `Dispatch`, `DispatchTargeted`, `addBackendFlag`, `LoadRegistry`, `writeTargetedResult`,
  `humanizeResult` (all existing, verified above), `schema.AgentSession`/`AgentSessionListResult`
  (Task 5).
- Produces: `newAgentSessionCmd() *cobra.Command`, registered as `pg-connector agentsession
show|list`.

- [ ] **Step 1: Write the failing test**

Create `packages/pg-connector/cmd/pg-connector/agentsession_test.go`:

```go
package main

import "testing"

func TestNewAgentSessionCmd_HasShowAndListOnly(t *testing.T) {
	cmd := newAgentSessionCmd()
	names := map[string]bool{}
	for _, c := range cmd.Commands() {
		names[c.Name()] = true
	}
	if !names["show"] || !names["list"] {
		t.Fatalf("expected show and list subcommands, got %v", names)
	}
	for _, forbidden := range []string{"create", "update", "close"} {
		if names[forbidden] {
			t.Errorf("agentsession is read-only; unexpected %q subcommand", forbidden)
		}
	}
}

func TestHumanizeAgentSession_RoundTrip(t *testing.T) {
	raw := []byte(`{"session_id":"s1","status":"blocked","blocker":"usage_limit","model":"claude-sonnet-5","cwd":"/repo","tokens":100,"cost_usd":0.5}`)
	out, err := humanizeAgentSession(raw)
	if err != nil {
		t.Fatalf("humanizeAgentSession: %v", err)
	}
	if !contains(out, "s1") || !contains(out, "blocked/usage_limit") {
		t.Errorf("got %q", out)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pg-connector && go test ./cmd/pg-connector/... -run 'TestNewAgentSessionCmd|TestHumanizeAgentSession' -v`
Expected: FAIL — `newAgentSessionCmd`/`humanizeAgentSession` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `packages/pg-connector/cmd/pg-connector/agentsession.go`:

```go
// agentsession.go: the agentsession capability's Tier-1 verb group —
// show/list only (read-only). show is a targeted, id-keyed op
// (DispatchTargeted, mirroring issue.go's show); list is NOT id-keyed and
// has exactly one intended backend today, so it uses the simpler Dispatch
// helper (hard-fails at N>1 with no pin) rather than ci.go's fan-out
// Sources/outcome-struct machinery, which this capability does not need
// (YAGNI).
package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

func newAgentSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agentsession",
		Short: "Query Claude Code agent sessions (pa-monitor-backed)",
	}
	cmd.AddCommand(newAgentSessionShowCmd())
	cmd.AddCommand(newAgentSessionListCmd())
	return cmd
}

func newAgentSessionShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one agent session's current state",
		Args:  cobra.ExactArgs(1),
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend, skipping the multi-instance try-each resolution policy")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return reportAgentSessionTargetedOutcome(cmd, nil, err, humanizeAgentSession)
		}
		resp, dispatchErr := DispatchTargeted(cmd.Context(), reg, "agentsession", "show", map[string]string{"id": args[0]}, *backendFlag)
		return reportAgentSessionTargetedOutcome(cmd, resp, dispatchErr, humanizeAgentSession)
	}
	return cmd
}

func newAgentSessionListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List agent sessions from the registered pa-monitor backend",
		Args:  cobra.NoArgs,
	}
	backendFlag := addBackendFlag(cmd, "pin to exactly this backend (agentsession has one intended backend today, so this only guards against a stale/mistyped name)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, err := LoadRegistry()
		if err != nil {
			return reportAgentSessionTargetedOutcome(cmd, nil, err, humanizeAgentSessionList)
		}
		resp, dispatchErr := Dispatch(cmd.Context(), reg, "agentsession", "list", map[string]bool{"ids_only": false}, *backendFlag)
		return reportAgentSessionTargetedOutcome(cmd, resp, dispatchErr, humanizeAgentSessionList)
	}
	return cmd
}

func reportAgentSessionTargetedOutcome(cmd *cobra.Command, resp *scriptout.Response, err error, humanize humanizeResult) error {
	return writeTargetedResult(cmd, resp, err, humanize)
}

func humanizeAgentSession(raw json.RawMessage) (string, error) {
	var s schema.AgentSession
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	status := s.Status
	if s.Blocker != "" {
		status += "/" + s.Blocker
	}
	return fmt.Sprintf("session_id: %s\nstatus:     %s\nmodel:      %s\ncwd:        %s\ntokens:     %d\ncost_usd:   $%.2f\n",
		s.SessionID, status, s.Model, s.Cwd, s.Tokens, s.CostUSD), nil
}

func humanizeAgentSessionList(raw json.RawMessage) (string, error) {
	var l schema.AgentSessionListResult
	if err := json.Unmarshal(raw, &l); err != nil {
		return "", err
	}
	if len(l.Entities) == 0 {
		return "agent sessions: (none)\n", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "agent sessions (%d):\n", len(l.Entities))
	for _, s := range l.Entities {
		status := s.Status
		if s.Blocker != "" {
			status += "/" + s.Blocker
		}
		fmt.Fprintf(&b, "  %-12s %-20s %s\n", s.SessionID, status, s.Cwd)
	}
	return b.String(), nil
}
```

Modify `packages/pg-connector/cmd/pg-connector/registry.go`: add `"agentsession"` to `entityTypes`.

Modify `packages/pg-connector/cmd/pg-connector/root.go`: add
`root.AddCommand(newAgentSessionCmd())` alongside the other `AddCommand` calls in `newRootCmd`.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pg-connector && go test ./cmd/pg-connector/... -v`
Expected: PASS — including `naming_convention_test.go`, `entity_store_test.go`,
`layout_convention_test.go`, `registry_test.go` (all now see `agentsession` and must not flag it).

- [ ] **Step 5: Commit**

```bash
git add packages/pg-connector/cmd/pg-connector/agentsession.go packages/pg-connector/cmd/pg-connector/agentsession_test.go packages/pg-connector/cmd/pg-connector/registry.go packages/pg-connector/cmd/pg-connector/root.go
git commit -m "pg-connector: add agentsession Tier-1 CLI verb group"
```

---

### Task 8: Backend scaffold — `Show`/`List` against pa-monitor

**Files:**

- Create: `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/main.go`
- Create: `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal/runner.go`
- Create: `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal/backend.go`
- Create: `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal/backend_test.go`

**Interfaces:**

- Consumes: `agentsession.Provider`/`NewDispatchTable` (Task 6), the `sessionJSON` wire shape
  (Task 2/3 — same field names, re-declared here as this module cannot import pa-monitor's
  `cmd/pa-monitor` package (`internal/`-scoped, different module); duplication of the wire
  CONTRACT, not the logic, mirrors how every existing backend re-declares its own view of its
  target's wire shape (e.g. `bdIssue` in `pg-connector-issue-beads`) rather than importing the
  target's internals).
- Produces: `internal.Backend` implementing `agentsession.Provider`; `internal.Runner` interface
  (production impl execs `pa-monitor`; tests inject a fake) — Task 9/10 add methods to this same
  `Backend`, and Task 9/10 also MODIFY this task's own `main.go` (see their own Interfaces
  sections) to widen `capabilities.SchemaVersions`.

- [ ] **Step 1: Write the failing test**

Create `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal/backend_test.go`:

```go
package internal

import (
	"context"
	"errors"
	"testing"
)

type fakeRunner struct {
	statusJSON string
	infoJSON   map[string]string // selector -> json
	err        error
}

func (f *fakeRunner) Status(_ context.Context) ([]byte, error) {
	return []byte(f.statusJSON), f.err
}
func (f *fakeRunner) Info(_ context.Context, selector string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	j, ok := f.infoJSON[selector]
	if !ok {
		return nil, errors.New("pa-monitor: no directory/session matched")
	}
	return []byte(j), nil
}
func (f *fakeRunner) Search(_ context.Context, _ string, _ string) ([]byte, error) {
	return nil, errors.New("not used by this test")
}

func TestBackend_Show(t *testing.T) {
	r := &fakeRunner{infoJSON: map[string]string{
		"session:s1": `{"session_id":"s1","status":"working","model":"claude-sonnet-5","session_tokens":100,"cost_usd":0.1}`,
	}}
	b := New(r)
	got, err := b.Show(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.SessionID != "s1" || got.Status != "working" {
		t.Errorf("got %+v", got)
	}
	if got.AsOf == "" {
		t.Error("AsOf must be populated (INV-ASOF-1)")
	}
}

func TestBackend_Show_NotFound(t *testing.T) {
	r := &fakeRunner{infoJSON: map[string]string{}}
	b := New(r)
	if _, err := b.Show(context.Background(), "missing"); err == nil {
		t.Fatal("expected an error for an unknown session id")
	}
}

func TestBackend_List(t *testing.T) {
	r := &fakeRunner{statusJSON: `{"sessions":[{"session_id":"s1","status":"working"},{"session_id":"s2","status":"idle"}]}`}
	b := New(r)
	got, err := b.List(context.Background(), nil, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Entities) != 2 || got.PresentIDs[0] != "s1" || got.PresentIDs[1] != "s2" {
		t.Errorf("got %+v", got)
	}
}

func TestBackend_DaemonUnreachable(t *testing.T) {
	r := &fakeRunner{err: errors.New("daemon unreachable")}
	b := New(r)
	if _, err := b.List(context.Background(), nil, false); err == nil {
		t.Fatal("expected an error when the daemon is unreachable")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pg-connector && go test ./cmd/pg-connector-agentsession-pa-monitor/... -v`
Expected: FAIL — package does not exist yet.

- [ ] **Step 3: Write minimal implementation**

Create `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal/runner.go`:

```go
// runner.go: the exec boundary to the real `pa-monitor` CLI — mirrors
// pg-connector-issue-beads' Runner/CLIRunner split (production execs the
// real binary; tests inject a fake).
package internal

import (
	"context"
	"os/exec"
)

// Runner is this backend's exec seam.
type Runner interface {
	Status(ctx context.Context) ([]byte, error)
	Info(ctx context.Context, selector string) ([]byte, error)
	Search(ctx context.Context, query, sessionID string) ([]byte, error)
}

// CLIRunner execs the real pa-monitor binary (resolved on $PATH).
type CLIRunner struct{}

func NewCLIRunner() *CLIRunner { return &CLIRunner{} }

func (CLIRunner) Status(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "pa-monitor", "status", "--json").Output()
}

func (CLIRunner) Info(ctx context.Context, selector string) ([]byte, error) {
	return exec.CommandContext(ctx, "pa-monitor", "info", selector, "--json").Output()
}

func (CLIRunner) Search(ctx context.Context, query, sessionID string) ([]byte, error) {
	args := []string{"search", query}
	if sessionID != "" {
		args = append(args, "--session", sessionID)
	}
	return exec.CommandContext(ctx, "pa-monitor", args...).Output()
}
```

Create `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal/backend.go`:

```go
// backend.go: Backend implements pkg/provider/agentsession.Provider against
// a real pa-monitor CLI (via Runner). No AuthChecker: pa-monitor's daemon
// has no per-caller credential concept (mirrors pg-connector-scm-git's
// identical reasoning for local git).
package internal

import (
	"context"
	"encoding/json"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/agentsession"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Backend is pg-connector-agentsession-pa-monitor's concrete
// agentsession.Provider implementation.
type Backend struct {
	runner Runner
	now    func() time.Time
}

func New(r Runner) *Backend {
	return &Backend{runner: r, now: func() time.Time { return time.Now().UTC() }}
}

var _ agentsession.Provider = (*Backend)(nil)

// sessionJSON mirrors pa-monitor's own wire shape (packages/pa-monitor's
// cmd/pa-monitor/status_json.go's sessionJSON) field-for-field. Duplicated
// here deliberately — see this task's own doc comment on why importing
// pa-monitor's internal package is not an option.
type sessionJSON struct {
	SessionID     string  `json:"session_id"`
	Pid           *int    `json:"pid,omitempty"`
	Cwd           string  `json:"cwd"`
	Name          string  `json:"name,omitempty"`
	Model         string  `json:"model"`
	Status        string  `json:"status"`
	Blocker       string  `json:"blocker,omitempty"`
	Branch        string  `json:"branch,omitempty"`
	TerminalHost  string  `json:"terminal_host,omitempty"`
	StartedAt     string  `json:"started_at,omitempty"`
	SessionTokens uint64  `json:"session_tokens"`
	CostUSD       float64 `json:"cost_usd"`
	LongIdle      bool    `json:"long_idle"`
}

func (s sessionJSON) toSchema(asOf time.Time) schema.AgentSession {
	return schema.AgentSession{
		SessionID: s.SessionID, PID: s.Pid, Cwd: s.Cwd, Name: s.Name, Model: s.Model,
		Status: s.Status, Blocker: s.Blocker, Branch: s.Branch, TerminalHost: s.TerminalHost,
		StartedAt: s.StartedAt, Tokens: s.SessionTokens, CostUSD: s.CostUSD, LongIdle: s.LongIdle,
		AsOf: asOf.Format(time.RFC3339), Stale: false,
	}
}

// Show execs `pa-monitor info session:<id> --json`.
func (b *Backend) Show(ctx context.Context, id string) (*schema.AgentSession, error) {
	raw, err := b.runner.Info(ctx, "session:"+id)
	if err != nil {
		return nil, classifyPaMonitorError(err)
	}
	var sj sessionJSON
	if err := json.Unmarshal(raw, &sj); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "pa-monitor: decode info --json: "+err.Error())
	}
	result := sj.toSchema(b.now())
	return &result, nil
}

type statusJSONDoc struct {
	Sessions []sessionJSON `json:"sessions"`
}

// List execs `pa-monitor status --json`. query is always nil as called by
// this capability's own dispatch table (Task 6) — accepted here only for
// interface-shape symmetry with thread.Provider.List.
func (b *Backend) List(ctx context.Context, _ schema.QueryExpr, idsOnly bool) (*schema.AgentSessionListResult, error) {
	raw, err := b.runner.Status(ctx)
	if err != nil {
		return nil, classifyPaMonitorError(err)
	}
	var doc statusJSONDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "pa-monitor: decode status --json: "+err.Error())
	}
	asOf := b.now()
	result := &schema.AgentSessionListResult{Truncated: false}
	for _, sj := range doc.Sessions {
		result.PresentIDs = append(result.PresentIDs, sj.SessionID)
		if !idsOnly {
			result.Entities = append(result.Entities, sj.toSchema(asOf))
		}
	}
	return result, nil
}

// classifyPaMonitorError maps a Runner-level failure (typically an
// *exec.ExitError from a daemon-unreachable pa-monitor invocation) onto
// scriptout's closed taxonomy. pa-monitor has no well-formed not_found
// signal for a single missing session id today (it fails the whole
// invocation) — every failure here is ErrUnavailable, matching this
// backend's own capabilities-reports-disabled handling.
func classifyPaMonitorError(err error) error {
	return scriptout.WrapError(scriptout.ErrUnavailable, "pa-monitor: "+err.Error())
}
```

Create `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/main.go` (Tasks 9/10 will
modify `newDispatchTable` below — this is the v1 scaffold covering only `agentsession`):

```go
package main

import (
	"os"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/agentsession"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var Version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	backend := internal.New(internal.NewCLIRunner())
	return scriptout.ServeLoop(newDispatchTable(backend))
}

func newDispatchTable(backend *internal.Backend) scriptout.DispatchTable {
	table := agentsession.NewDispatchTable(backend)
	return scriptout.AddCapabilities(table, schema.AgentSessionSchemaVersion, scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions:  map[string]int{"agentsession": schema.AgentSessionSchemaVersion},
		Version:         Version,
	})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pg-connector && go test ./cmd/pg-connector-agentsession-pa-monitor/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/
git commit -m "pg-connector: scaffold pg-connector-agentsession-pa-monitor backend"
```

---

### Task 9: `attention.Provider` on the backend

**Files:**

- Create: `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal/attention.go`
- Create: `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal/attention_test.go`
- Modify: `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal/backend.go`
  (add block/week fields to `statusJSONDoc`)
- Modify: `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/main.go` (merge in
  `attention.NewDispatchTable`'s entries AND widen `SchemaVersions`)

**Interfaces:**

- Consumes: `attention.Provider`/`attention.NewDispatchTable` (existing,
  `pkg/provider/attention`), `schema.AttentionItem`/`schema.Severity`/`schema.AttentionSchemaVersion`
  (existing).
- Produces: `Backend.ListAttention(ctx) ([]schema.AttentionItem, error)`.

**Verified against real source:** `pg-connector-issue-beads/main.go` is the one real
two-capability precedent — it builds `table := issue.NewDispatchTable(backend)`, then `for op,
handler := range attention.NewDispatchTable(backend) { table[op] = handler }`, then calls
`scriptout.AddCapabilities` with `SchemaVersions: map[string]int{"issue":
schema.IssueSchemaVersion, "attention": schema.AttentionSchemaVersion}` — `AddCapabilities`
auto-computes `Ops` from the merged table but NEVER auto-populates `SchemaVersions`; every merged
capability's schema version must be added by hand or `config validate`'s schema-skew check is
silently blind to it. This task mirrors that exactly.

- [ ] **Step 1: Write the failing test**

Create `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal/attention_test.go`:

```go
package internal

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

func TestListAttention_BlockedHumanInput(t *testing.T) {
	r := &fakeRunner{statusJSON: `{"sessions":[{"session_id":"s1","status":"blocked","blocker":"human_input"}]}`}
	b := New(r)
	items, err := b.ListAttention(context.Background())
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(items) != 1 || items[0].Severity != schema.SeverityHigh || items[0].ID != "s1" {
		t.Errorf("got %+v", items)
	}
}

func TestListAttention_BlockedUsageLimitIsMedium(t *testing.T) {
	r := &fakeRunner{statusJSON: `{"sessions":[{"session_id":"s1","status":"blocked","blocker":"usage_limit"}]}`}
	b := New(r)
	items, _ := b.ListAttention(context.Background())
	if len(items) != 1 || items[0].Severity != schema.SeverityMedium {
		t.Errorf("got %+v", items)
	}
}

func TestListAttention_LongIdleIsLow(t *testing.T) {
	r := &fakeRunner{statusJSON: `{"sessions":[{"session_id":"s1","status":"idle","long_idle":true}]}`}
	b := New(r)
	items, _ := b.ListAttention(context.Background())
	if len(items) != 1 || items[0].Severity != schema.SeverityLow {
		t.Errorf("got %+v", items)
	}
}

func TestListAttention_WorkingSessionRaisesNothing(t *testing.T) {
	r := &fakeRunner{statusJSON: `{"sessions":[{"session_id":"s1","status":"working"}]}`}
	b := New(r)
	items, _ := b.ListAttention(context.Background())
	if len(items) != 0 {
		t.Errorf("got %+v, want no items", items)
	}
}

func TestListAttention_BlockCapHit(t *testing.T) {
	r := &fakeRunner{statusJSON: `{"sessions":[],"active_block":{"id":"b1","cost_usd":140,"cap_hit_at":"2026-09-18T12:00:00Z"}}`}
	b := New(r)
	items, _ := b.ListAttention(context.Background())
	if len(items) != 1 || items[0].Severity != schema.SeverityCritical || items[0].Type != "agentsession-usage-limit" {
		t.Errorf("got %+v", items)
	}
}

func TestListAttention_WeekNotHit(t *testing.T) {
	r := &fakeRunner{statusJSON: `{"sessions":[],"active_week":{"id":"w1","cost_usd":10}}`}
	b := New(r)
	items, _ := b.ListAttention(context.Background())
	if len(items) != 0 {
		t.Errorf("got %+v, want no items (cap not hit)", items)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pg-connector && go test ./cmd/pg-connector-agentsession-pa-monitor/... -run TestListAttention -v`
Expected: FAIL — `ListAttention` undefined.

- [ ] **Step 3: Write minimal implementation**

Modify `backend.go`'s `statusJSONDoc` to add the block/week fields:

```go
type usageWindowJSON struct {
	ID       string  `json:"id"`
	CostUSD  float64 `json:"cost_usd"`
	CapHitAt string  `json:"cap_hit_at,omitempty"`
}

type statusJSONDoc struct {
	Sessions    []sessionJSON    `json:"sessions"`
	ActiveBlock *usageWindowJSON `json:"active_block,omitempty"`
	ActiveWeek  *usageWindowJSON `json:"active_week,omitempty"`
}
```

Create `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal/attention.go`:

```go
// attention.go: Backend's attention.Provider implementation — per-session
// blocked/long-idle escalations, plus an account-level item when the
// active 5h block or 7-day week usage cap has been hit
// [design: docs/superpowers/specs/2026-09-18-agentsession-connector-design.md].
package internal

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

var _ attention.Provider = (*Backend)(nil)

// blockerSeverity maps a blocked session's blocker reason to its attention
// severity. human_input/human_authn need a person; usage_limit
// self-recovers at the reset time.
func blockerSeverity(blocker string) schema.Severity {
	switch blocker {
	case "human_input", "human_authn":
		return schema.SeverityHigh
	case "usage_limit":
		return schema.SeverityMedium
	default:
		return schema.SeverityMedium
	}
}

func (b *Backend) ListAttention(ctx context.Context) ([]schema.AttentionItem, error) {
	raw, err := b.runner.Status(ctx)
	if err != nil {
		return nil, classifyPaMonitorError(err)
	}
	var doc statusJSONDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, classifyPaMonitorError(err)
	}

	var items []schema.AttentionItem
	for _, sj := range doc.Sessions {
		switch {
		case sj.Status == "blocked":
			items = append(items, schema.AttentionItem{
				Type: "agentsession", ID: sj.SessionID,
				Summary:  "session " + sj.SessionID + " is blocked (" + sj.Blocker + ")",
				Severity: blockerSeverity(sj.Blocker),
			})
		case sj.LongIdle:
			items = append(items, schema.AttentionItem{
				Type: "agentsession", ID: sj.SessionID,
				Summary:  "session " + sj.SessionID + " has been idle a long time",
				Severity: schema.SeverityLow,
			})
		}
	}
	if doc.ActiveBlock != nil && doc.ActiveBlock.CapHitAt != "" {
		items = append(items, schema.AttentionItem{
			Type: "agentsession-usage-limit", ID: doc.ActiveBlock.ID,
			Summary: "5-hour usage block cap has been hit", Severity: schema.SeverityCritical,
		})
	}
	if doc.ActiveWeek != nil && doc.ActiveWeek.CapHitAt != "" {
		items = append(items, schema.AttentionItem{
			Type: "agentsession-usage-limit", ID: doc.ActiveWeek.ID,
			Summary: "7-day usage week cap has been hit", Severity: schema.SeverityCritical,
		})
	}
	return items, nil
}
```

Modify `main.go`:

```go
func newDispatchTable(backend *internal.Backend) scriptout.DispatchTable {
	table := agentsession.NewDispatchTable(backend)
	for op, handler := range attention.NewDispatchTable(backend) {
		table[op] = handler
	}
	return scriptout.AddCapabilities(table, schema.AgentSessionSchemaVersion, scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions: map[string]int{
			"agentsession": schema.AgentSessionSchemaVersion,
			"attention":    schema.AttentionSchemaVersion,
		},
		Version: Version,
	})
}
```

(add the `"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/attention"` import.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pg-connector && go test ./cmd/pg-connector-agentsession-pa-monitor/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/
git commit -m "pg-connector-agentsession-pa-monitor: implement attention.Provider"
```

---

### Task 10: `search.Provider` on the backend

**Files:**

- Create: `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal/search.go`
- Create: `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal/search_test.go`
- Modify: `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/main.go` (merge in
  `search.NewDispatchTable`'s entries AND widen `SchemaVersions` again)

**Interfaces:**

- Consumes: `search.Provider`/`search.NewDispatchTable` (existing, `pkg/provider/search`),
  `schema.SearchResult`/`schema.SearchSchemaVersion` (existing), `Runner.Search` (Task 8).
- Produces: `Backend.Search(ctx, query, fields) ([]schema.SearchResult, error)`.

- [ ] **Step 1: Write the failing test**

Create `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal/search_test.go`:

```go
package internal

import (
	"context"
	"errors"
	"testing"
)

type fakeSearchRunner struct {
	fakeRunner
	searchJSON string
	searchErr  error
}

func (f *fakeSearchRunner) Search(_ context.Context, _ string, _ string) ([]byte, error) {
	return []byte(f.searchJSON), f.searchErr
}

func TestBackend_Search(t *testing.T) {
	r := &fakeSearchRunner{searchJSON: `{"query":"flaky","matches":[{"session_id":"s1","role":"assistant","line":2,"snippet":"the payments test is flaky"}]}`}
	b := New(r)
	got, err := b.Search(context.Background(), "flaky", nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].ID != "s1" || got[0].Type != "agentsession" {
		t.Errorf("got %+v", got)
	}
}

func TestBackend_Search_NoMatches(t *testing.T) {
	r := &fakeSearchRunner{searchJSON: `{"query":"nope","matches":[]}`}
	b := New(r)
	got, err := b.Search(context.Background(), "nope", nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
}

func TestBackend_Search_RunnerError(t *testing.T) {
	r := &fakeSearchRunner{searchErr: errors.New("daemon unreachable")}
	b := New(r)
	if _, err := b.Search(context.Background(), "x", nil); err == nil {
		t.Fatal("expected an error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd packages/pg-connector && go test ./cmd/pg-connector-agentsession-pa-monitor/... -run TestBackend_Search -v`
Expected: FAIL — `Backend.Search` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal/search.go`:

```go
// search.go: Backend's search.Provider implementation — execs `pa-monitor
// search <query>` and maps matches onto schema.SearchResult.
package internal

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/search"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

var _ search.Provider = (*Backend)(nil)

type searchMatchJSON struct {
	SessionID string `json:"session_id"`
	Role      string `json:"role"`
	Line      int    `json:"line"`
	Snippet   string `json:"snippet"`
	Timestamp string `json:"timestamp,omitempty"`
}

type searchJSONDoc struct {
	Matches []searchMatchJSON `json:"matches"`
}

// Search ignores fields — pa-monitor's search subcommand has no
// attribute-selection concept; a well-behaved Provider silently ignores an
// unsupported requested attribute (pkg/provider/search.Provider's own
// documented freedom boundary). It cannot pass a time bound through to
// `pa-monitor search` here: pkg/provider/search.Provider's own signature
// (query, fields — shared by every search backend, not just this one) has
// no time-bound parameter at all, so this call is always unbounded
// regardless of pa-monitor's own --since/--before support. Extending the
// shared interface to carry one is tracked separately as bead pg2-emmut.
func (b *Backend) Search(ctx context.Context, query string, _ []string) ([]schema.SearchResult, error) {
	raw, err := b.runner.Search(ctx, query, "")
	if err != nil {
		return nil, classifyPaMonitorError(err)
	}
	var doc searchJSONDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, classifyPaMonitorError(err)
	}
	var results []schema.SearchResult
	for _, m := range doc.Matches {
		attrs := map[string]any{"role": m.Role, "line": m.Line}
		if m.Timestamp != "" {
			attrs["timestamp"] = m.Timestamp
		}
		results = append(results, schema.SearchResult{
			Type: "agentsession", ID: m.SessionID, Title: m.Snippet, Source: "pa-monitor",
			Attributes: attrs,
		})
	}
	return results, nil
}
```

Modify `main.go`:

```go
func newDispatchTable(backend *internal.Backend) scriptout.DispatchTable {
	table := agentsession.NewDispatchTable(backend)
	for op, handler := range attention.NewDispatchTable(backend) {
		table[op] = handler
	}
	for op, handler := range search.NewDispatchTable(backend) {
		table[op] = handler
	}
	return scriptout.AddCapabilities(table, schema.AgentSessionSchemaVersion, scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions: map[string]int{
			"agentsession": schema.AgentSessionSchemaVersion,
			"attention":    schema.AttentionSchemaVersion,
			"search":       schema.SearchSchemaVersion,
		},
		Version: Version,
	})
}
```

(add the `"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/search"` import.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd packages/pg-connector && go test ./cmd/pg-connector-agentsession-pa-monitor/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/
git commit -m "pg-connector-agentsession-pa-monitor: implement search.Provider"
```

---

### Task 11: Contract-test coverage (no new nix wiring)

**Files:**

- Create: `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal/realpamonitor_test.go`
- Modify: `flake.nix` (repo root — add `pa-monitor` to `pg-connector-contract`'s
  `runtimeInputs`; this is the ONLY flake.nix change this task needs)

**Corrected from an earlier draft:** this task originally proposed wiring a contract test into a
new `checks.*` flake gate, treating the fact that `cmd/pg-connector-issue-beads/internal/
realbd_test.go` runs in no gate as an oversight. It is not — `flake.nix` already documents (right
above its `pg-connector-contract` app definition) that every `//go:build contract` suite in this
repo (`ccpool-contract`/`pb-contract`/`pg-pr-contract`/`pg-connector-contract`) is DELIBERATELY
`nix run`-only, never a flake check, specifically to keep a real external dependency (here: a
running pa-monitor daemon) out of the sandboxed default check path (bead `pg2-kqft7`). Better
still: `pg-connector-contract` already runs `go test -tags contract -timeout=0 -p 1 ./...` across
the WHOLE `packages/pg-connector` module — this task's new `//go:build contract` test file is
picked up automatically by that existing `./...` glob. **No new nix app, no new check, is needed.**
The only real gap is that `pg-connector-contract`'s `runtimeInputs` (`go`, `git`, `gh`, `beads`)
does not include `pa-monitor`, so a real run of this backend's contract test would fail to find it
on PATH.

Separately, `cmd/pg-connector/e2e_contract_test.go` is a larger, pre-existing suite that builds and
drives all five (soon six) real Tier-2 binaries together, including two tests with hardcoded
per-backend lists (`TestContract_ConfigValidate_AllFourRealBackends`,
`TestContract_AuthStatus_AllFourRealBackends`, plus a binary list near that file's own top).
Extending that suite to also cover `pg-connector-agentsession-pa-monitor` is desirable but is its
own follow-on — the file is large enough (400+ lines, several hardcoded backend-name lists) that
committing to its exact edits without reading it in full first would repeat this plan's own
earlier mistake of asserting unverified code. Flag it as follow-on work, not a step here.

- [ ] **Step 1: Write the contract test**

Create `packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal/realpamonitor_test.go`:

```go
//go:build contract

package internal

import (
	"context"
	"testing"
	"time"
)

// TestRealPaMonitor_StatusAndInfoRoundTrip exercises the real pa-monitor
// binary (must be on PATH, daemon running) — skips (not fails) when
// unreachable, matching every other daemon-dependent test's convention.
// Picked up automatically by `nix run .#pg-connector-contract`'s existing
// `go test -tags contract ./...` (packages/pg-connector-wide) — no new nix
// wiring needed.
func TestRealPaMonitor_StatusAndInfoRoundTrip(t *testing.T) {
	r := NewCLIRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw, err := r.Status(ctx)
	if err != nil {
		t.Skipf("pa-monitor daemon unreachable, skipping: %v", err)
	}

	b := New(r)
	list, err := b.List(ctx, nil, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	t.Logf("pa-monitor status --json returned %d bytes, %d sessions", len(raw), len(list.Entities))

	if len(list.PresentIDs) == 0 {
		t.Skip("no live sessions to Show(); round trip for status only")
		return
	}
	got, err := b.Show(ctx, list.PresentIDs[0])
	if err != nil {
		t.Fatalf("Show(%q): %v", list.PresentIDs[0], err)
	}
	if got.SessionID != list.PresentIDs[0] {
		t.Errorf("Show returned session %q, want %q", got.SessionID, list.PresentIDs[0])
	}
}
```

- [ ] **Step 2: Run it manually (no red-then-green cycle — no new production code)**

Run: `go test -tags contract ./cmd/pg-connector-agentsession-pa-monitor/... -run TestRealPaMonitor -v`
(from `packages/pg-connector`)
Expected: either PASS or a clear SKIP naming the unreachable daemon.

- [ ] **Step 3: Add `pa-monitor` to `pg-connector-contract`'s runtime inputs**

In the repo-root `flake.nix`, find `pg-connector-contract = pkgs.writeShellApplication { ...
runtimeInputs = [ pkgs.go pkgs.git pkgs.gh (pkgs.llm-agentsPkgs.beads or ...) ]; ... };` and add
`pkgs.pa-monitor` to that list.

- [ ] **Step 4: Verify the umbrella contract run picks up the new test**

Run: `nix run .#pg-connector-contract` — Expected: the run includes
`TestRealPaMonitor_StatusAndInfoRoundTrip` in its output (PASS or SKIP), with no separate
invocation needed.

- [ ] **Step 5: Commit**

```bash
git add packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal/realpamonitor_test.go flake.nix
git commit -m "pg-connector-agentsession-pa-monitor: add real-daemon contract test"
```

---

### Task 12: Nix packaging

**Files:**

- Create: `packages/pg-connector/pg-connector-agentsession-pa-monitor.nix`
- Modify: `flake.nix` (repo root — overlay entry; NOT `packages/pg-connector/flake.nix`, which does
  not exist)
- Modify: `home/programs/pg-connector/default.nix` (`connector.agentsession` option +
  `home.packages`)

**Interfaces:**

- Consumes: `mkGoApp` (existing, from `phillipg-nix-repo-base`), the shared
  `packages/pg-connector` `gomod2nixToml`/src fileset every sibling `.nix` file already uses.

- [ ] **Step 1: Write the derivation**

Create `packages/pg-connector/pg-connector-agentsession-pa-monitor.nix`, copying
`packages/pg-connector/pg-connector-thread-slack.nix` verbatim except for the binary name /
`subPackages` path (`cmd/pg-connector-agentsession-pa-monitor`) and `meta.description`.

- [ ] **Step 2: Wire the overlay**

Modify the repo-root `flake.nix`: add an entry alongside the existing `pg-connector-thread-slack`
one (same file, same `overlays.default`/`final: prev: { ... }` block):

```nix
# pg-connector-agentsession-pa-monitor: the agentsession capability's
# Tier-2 pa-monitor backend.
pg-connector-agentsession-pa-monitor =
  final.callPackage ./packages/pg-connector/pg-connector-agentsession-pa-monitor.nix
    { };
```

- [ ] **Step 3: Build it**

Run: `nix build .#pg-connector-agentsession-pa-monitor`
Expected: builds successfully, producing a binary that runs `--help`/`capabilities` without error.

- [ ] **Step 4: Wire the home-manager module**

Modify `home/programs/pg-connector/default.nix`:

- Add an `agentsession` option to the `connector` submodule's `options`, mirroring `thread`'s exact
  shape (`listOf str`, default `[ ]`).
- Add `agentsession = if cfg.connector.agentsession == [ ] then null else
cfg.connector.agentsession;` to `renderedConnector`.
- Add `pkgs.pg-connector-agentsession-pa-monitor` to `home.packages`.

- [ ] **Step 5: Commit**

```bash
git add packages/pg-connector/pg-connector-agentsession-pa-monitor.nix flake.nix home/programs/pg-connector/default.nix
git commit -m "pg-connector: package and wire pg-connector-agentsession-pa-monitor"
```

---

## Phase 3 — downstream wiring

### Task 13: Register the backend (configuration, this repo)

**Files:**

- Modify: whichever host's home-manager config in this repo enables
  `phillipgreenii.programs.pg-connector` (if any).

- [ ] **Step 1: Confirm where registration belongs**

Run: `grep -rl "programs.pg-connector" /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support --include=*.nix`

If this repo has no host enabling the module directly (expected — this repo ships the module,
`phillipg-nix-ziprecruiter` is the actual consumer, confirmed by Task 14's own findings below),
skip to Task 14; there is nothing to change here.

- [ ] **Step 2: Commit** (only if Step 1 found something to change)

```bash
git add -A
git commit -m "pg-connector: register agentsession backend in this repo's own host config"
```

---

### Task 14: Register the backend (configuration, phillipg-nix-ziprecruiter)

**Files:**

- Modify: `phillipg-nix-ziprecruiter/machines/phillipg-mbp-02/default.nix`

**Verified against real source (corrected from an earlier draft):** the earlier draft claimed a
"thread"/`pg-connector-thread-slack` registration to mirror. There is no such entry anywhere in
this repo — confirmed by direct inspection of `machines/phillipg-mbp-02/default.nix`'s
`phillipgreenii.programs.pg-connector` block (lines ~901-947 as of this plan's writing). Its real,
current shape:

```nix
pg-connector = {
  enable = true;
  connector = {
    pr = [ "pg-connector-pr-github" ];
    issue = [
      "pg-connector-issue-jira"
      "pg-connector-issue-beads"
    ];
    ci = [ "pg-connector-ci-github-actions" ];
    scm = "pg-connector-scm-git";
  };
  attention.sources = [
    "pg-connector-issue-beads"
    "pg-connector-issue-jira"
    "pg-connector-pr-github"
  ];
  search.sources = [
    "pg-connector-pr-github"
    "pg-connector-issue-jira"
  ];
  # ... backends: block follows
};
```

- [ ] **Step 1: Re-confirm the current shape hasn't drifted**

Run: `grep -n "pg-connector = {" -A 50 /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/machines/phillipg-mbp-02/default.nix`

If the shape above no longer matches, adapt the edit below to whatever the real current shape is
— do not apply the snippet blind.

- [ ] **Step 2: Add the new backend**

Add `agentsession = [ "pg-connector-agentsession-pa-monitor" ];` to the `connector = { ... }` block
(list form, matching `pr`/`issue`/`ci` — NOT `scm`'s bare-string form), and append
`"pg-connector-agentsession-pa-monitor"` as a new element of both the existing `attention.sources`
and `search.sources` lists shown above.

- [ ] **Step 3: Build**

Run: `nix build .#darwinConfigurations.phillipg-mbp-02.system` (build-only per this workspace's
own `darwin-rebuild check` prohibition).
Expected: builds successfully.

- [ ] **Step 4: Manually verify the downstream consumers need no changes**

After the next `pn workspace apply` on that host: run `pg-connector attention list` and
`pg-connector search <some-known-transcript-substring>` directly, and separately run `df-attention`
/ `df-search` (the daily-focus wrapper scripts) and confirm an `agentsession` row appears in both
pairs of output with no code change to `df-attention`/`df-search` themselves — this is the concrete
verification of the design's "confirmed pure generic passthrough" claim (already checked against
both scripts' `.sh` content, not just their `.nix` wrappers), not an assumption left unchecked.

- [ ] **Step 5: Commit**

```bash
git add machines/phillipg-mbp-02/default.nix
git commit -m "ziprecruiter: register pg-connector-agentsession-pa-monitor backend"
```

---

## Self-Review Notes

- **Spec coverage:** every section of the design doc (pa-monitor's three additions, schema,
  provider interface, backend attention/search wiring, all ten mechanical registration points,
  downstream wiring, testing strategy, explicit non-goals) maps to at least one task above.
- **Type consistency verified:** `sessionJSON`'s field names (Task 2) are reused verbatim in Task 3
  and Task 8 (re-declared, not imported, deliberately — see Task 8's own doc comment); Task 4 is a
  separate wire shape (`searchJSONDoc`/`searchMatchJSON`) reused verbatim in Task 10;
  `schema.AgentSession`/`AgentSessionListResult` (Task 5) are the only types Tasks 6-10 construct
  against.
- **Corrections applied after an independent adversarial review** (2026-09-18, full detail in each
  task's own text above): `pb.GetStateResponse` → `pb.DaemonState` (Task 2/4 — the real proto RPC
  return type; the old name does not exist anywhere in pa-monitor); Task 7's `writeTargetedResult`/
  `addBackendFlag`/`Dispatch`/`DispatchTargeted` calls rewritten against the real, verified
  signatures (`output.go`/`backend_flag.go`/`dispatch.go`), including the `--backend` pin flag and
  proper error routing every existing verb group carries; Task 4's `search` argument parsing
  rewritten as an actual flag scanner (`parseSearchArgs`) instead of a broken fixed-position
  assumption that would have read the literal string `"--json"` as the query; Task 6/7's `list`
  simplified to drop a `--query`/`schema.ResolveQuery` mechanism that could never have resolved
  successfully (confirmed: `ResolveQuery(config, "")` never matches); Tasks 9/10 now explicitly
  widen `capabilities.SchemaVersions` for each merged-in capability, mirroring
  `pg-connector-issue-beads`'s real precedent; Task 11 rewritten from "add a new check" (which
  would have fought an already-settled, documented repo convention) to "no new nix wiring needed —
  the existing `pg-connector-contract` app already covers it, plus add `pa-monitor` to its
  `runtimeInputs`"; Task 12's file path corrected from a nonexistent
  `packages/pg-connector/flake.nix` to the real repo-root `flake.nix`; Task 14 rewritten against
  the real, current `phillipg-nix-ziprecruiter` machine config shape (no `thread` entry exists
  there to mirror, as an earlier draft claimed) instead of a fabricated precedent.
- **`--since`/`--before` time-bound filtering added (2026-09-18, user request):** Task 1
  (`claudetranscript.Search`) and Task 4 (`pa-monitor search`) now take a `since`/`before` bound,
  using each transcript event's own already-parsed `Timestamp` field — no new data needed, only a
  comparison. This filter is reachable only via `pa-monitor search` directly; the shared
  `pkg/provider/search.Provider` interface every search backend implements has no time-bound
  parameter, so extending it is out of scope here and tracked as its own exploration bead,
  `pg2-emmut`.
