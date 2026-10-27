package gather

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/cmdrun"
)

// entityLog is every argv (joined with spaces) the entity double saw, in order.
type entityLog struct {
	calls []string
}

// entityFactory installs an in-process fake pg-connector as the runner seam
// and returns its call log. behavior is a "|"-separated list of
// "<verb>=<exit>:<stdout>" entries, e.g. "issue show=0:{...}"; every
// invocation appends its argv to the log, and a verb with no entry answers
// exit 99 ("unexpected verb"). Nothing here starts a process.
func entityFactory(t *testing.T, behavior string) *entityLog {
	t.Helper()
	log := &entityLog{}
	orig := runner
	runner = func(_ context.Context, c cmdrun.Call) cmdrun.Result {
		log.calls = append(log.calls, strings.Join(c.Args, " "))
		verb := ""
		if len(c.Args) >= 2 {
			verb = c.Args[0] + " " + c.Args[1]
		}
		for _, ent := range strings.Split(behavior, "|") {
			k, rest, ok := strings.Cut(ent, "=")
			if !ok || k != verb {
				continue
			}
			code, out, _ := strings.Cut(rest, ":")
			n := 0
			for _, c := range code {
				n = n*10 + int(c-'0')
			}
			return cmdrun.Result{Stdout: out, Code: n}
		}
		return cmdrun.Result{Stderr: "unexpected verb: " + verb, Code: 99}
	}
	t.Cleanup(func() { runner = orig })
	return log
}

func readCalls(t *testing.T, rec *entityLog) []string {
	t.Helper()
	if len(rec.calls) == 0 {
		return nil
	}
	return append([]string(nil), rec.calls...)
}

const issueShowWire = `{"protocolVersion":1,"schemaVersion":4,"result":{"id":"bd-1","owner":"me","assignee":"x","issue_type":"task","as_of":"2026-09-16T00:00:00Z"}}`

func TestEntityGatherers_RegistryKeys(t *testing.T) {
	g := NewGatherer(testConfig(""), nil)
	m := g.EntityGatherers()
	if len(m) != 3 || m["pr"] == nil || m["issue"] == nil || m["thread"] == nil {
		t.Fatalf("registry keys: %v", m)
	}
}

func TestIssueAdapter_NotFoundIsRemoved(t *testing.T) {
	entityFactory(t, `issue show=4:{"error":{"code":"not_found"}}`)
	g := NewGatherer(testConfig(""), nil)
	res, err := g.EntityGatherers()["issue"].GatherEntity(context.Background(), "bd-1", ChangeChanged)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.RemovedState != "not_found" || len(res.Payload) != 0 {
		t.Fatalf("res: %+v", res)
	}
}

func TestIssueAdapter_HardError(t *testing.T) {
	entityFactory(t, `issue show=1:boom`)
	g := NewGatherer(testConfig(""), nil)
	if _, err := g.EntityGatherers()["issue"].GatherEntity(context.Background(), "bd-1", ChangeChanged); err == nil {
		t.Fatal("want error")
	}
}

func TestIssueAdapter_ShowOnlyByDefault(t *testing.T) {
	rec := entityFactory(t, `issue show=0:`+issueShowWire)
	g := NewGatherer(testConfig("/tmp/beads"), nil)
	res, err := g.EntityGatherers()["issue"].GatherEntity(context.Background(), "bd-1", ChangeAdded)
	if err != nil {
		t.Fatal(err)
	}
	if res.AsOf != "2026-09-16T00:00:00Z" {
		t.Fatalf("asof %q", res.AsOf)
	}
	var f IssueFacts
	if err := json.Unmarshal(res.Payload, &f); err != nil || len(f.IssueShow) == 0 || len(f.IssueDeps) != 0 {
		t.Fatalf("facts %+v err %v", f, err)
	}
	calls := readCalls(t, rec)
	if len(calls) != 1 || calls[0] != "issue show bd-1 --fresh" {
		t.Fatalf("calls %v", calls)
	}
}

func TestIssueAdapter_DepsOnlyWhenSwitched(t *testing.T) {
	rec := entityFactory(t, `issue show=0:`+issueShowWire+`|issue deps=0:{"result":{"deps":[]}}`)
	g := NewGatherer(testConfig(""), nil)
	g.SetReadIssueDeps(true)
	res, err := g.EntityGatherers()["issue"].GatherEntity(context.Background(), "bd-1", ChangeChanged)
	if err != nil {
		t.Fatal(err)
	}
	var f IssueFacts
	if err := json.Unmarshal(res.Payload, &f); err != nil || len(f.IssueDeps) == 0 {
		t.Fatalf("facts %+v err %v", f, err)
	}
	calls := readCalls(t, rec)
	if len(calls) != 2 || !strings.HasPrefix(calls[1], "issue deps bd-1") {
		t.Fatalf("calls %v", calls)
	}
}

func TestIssueAdapter_DepsFailureIsError(t *testing.T) {
	entityFactory(t, `issue show=0:`+issueShowWire+`|issue deps=1:x`)
	g := NewGatherer(testConfig(""), nil)
	g.SetReadIssueDeps(true)
	if _, err := g.EntityGatherers()["issue"].GatherEntity(context.Background(), "bd-1", ChangeChanged); err == nil {
		t.Fatal("want error on failed deps read")
	}
}

const threadShowWire = `{"protocolVersion":1,"schemaVersion":4,"result":{"text":"root text","reply_count":2,"as_of":"2026-09-16T00:00:00Z","messages":[{"text":"hello"}]}}`

func TestThreadGatherAdapterHydratesMessages(t *testing.T) {
	rec := entityFactory(t, `thread show=0:`+threadShowWire)
	g := NewGatherer(testConfig(""), nil)
	res, err := g.EntityGatherers()["thread"].GatherEntity(context.Background(), "t-1", ChangeChanged)
	if err != nil {
		t.Fatal(err)
	}
	if res.AsOf != "2026-09-16T00:00:00Z" {
		t.Fatalf("asof %q", res.AsOf)
	}
	var f ThreadFacts
	if err := json.Unmarshal(res.Payload, &f); err != nil || len(f.ThreadShow) == 0 {
		t.Fatalf("facts %+v err %v", f, err)
	}
	var got struct {
		Text     string            `json:"text"`
		AsOf     string            `json:"as_of"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(f.ThreadShow, &got); err != nil {
		t.Fatal(err)
	}
	if got.Text != "root text" || got.AsOf != "2026-09-16T00:00:00Z" || len(got.Messages) != 1 || string(got.Messages[0]) != `{"text":"hello"}` {
		t.Fatalf("payload round-trip: %+v", got)
	}
	if calls := readCalls(t, rec); len(calls) != 1 || calls[0] != "thread show t-1" {
		t.Fatalf("calls %v", calls)
	}
}

func TestThreadAdapter_NotFoundIsRemoved(t *testing.T) {
	entityFactory(t, `thread show=4:{"error":{"code":"not_found"}}`)
	g := NewGatherer(testConfig(""), nil)
	res, err := g.EntityGatherers()["thread"].GatherEntity(context.Background(), "t-1", ChangeChanged)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.RemovedState != "not_found" || len(res.Payload) != 0 {
		t.Fatalf("res: %+v", res)
	}
}

func TestThreadAdapter_HardErrorAndEmptyID(t *testing.T) {
	entityFactory(t, `thread show=1:boom`)
	g := NewGatherer(testConfig(""), nil)
	if _, err := g.EntityGatherers()["thread"].GatherEntity(context.Background(), "t-1", ChangeChanged); err == nil {
		t.Fatal("want error")
	}
	if _, err := g.EntityGatherers()["thread"].GatherEntity(context.Background(), "", ChangeChanged); err == nil {
		t.Fatal("want error on empty id")
	}
}

// TestPRPathParityFixtures: the pr adapter's payload is json.Marshal of the
// direct Gather result, for every change kind.
func TestPRPathParityFixtures(t *testing.T) {
	cases := []struct {
		behavior string
		change   ChangeKind
	}{
		{"happy", ChangeAdded},
		{"happy", ChangeChanged},
		{"removed_open", ChangeRemoved},
		{"removed_merged", ChangeRemoved},
		{"removed_not_found", ChangeRemoved},
	}
	for _, tc := range cases {
		t.Run(tc.behavior+"/"+string(tc.change), func(t *testing.T) {
			orig := runner
			t.Cleanup(func() { runner = orig })
			runner = helperRunner(tc.behavior, nil)
			direct, derr := NewGatherer(testConfig(""), nil).Gather(context.Background(), "pr", fixturePRKey, tc.change)
			res, aerr := NewGatherer(testConfig(""), nil).EntityGatherers()["pr"].GatherEntity(context.Background(), fixturePRKey, tc.change)
			if (derr == nil) != (aerr == nil) {
				t.Fatalf("errs differ: %v vs %v", derr, aerr)
			}
			if derr != nil {
				return
			}
			want, _ := json.Marshal(direct)
			// The pr adapter adds exactly the pending-review state
			// (review_pending) to the direct result; drop
			// it and the rest must be byte-identical.
			got := res.Payload
			if tc.change != ChangeRemoved {
				var m map[string]json.RawMessage
				if err := json.Unmarshal(got, &m); err != nil {
					t.Fatal(err)
				}
				if m["review_pending"] == nil {
					t.Fatalf("adapter payload lacks the review state: %s", got)
				}
				delete(m, "review_pending")
				var derr2 error
				if got, derr2 = json.Marshal(m); derr2 != nil {
					t.Fatal(derr2)
				}
				var dm map[string]json.RawMessage
				_ = json.Unmarshal(want, &dm)
				want, _ = json.Marshal(dm) // same key order (sorted) as got
			}
			if string(got) != string(want) {
				t.Fatalf("payload mismatch:\n%s\n%s", res.Payload, want)
			}
			if res.AsOf != direct.AsOf || res.Degraded != direct.Degraded || res.RemovedState != direct.RemovedState {
				t.Fatalf("meta mismatch: %+v vs %+v", res, direct)
			}
		})
	}
}
