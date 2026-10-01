package interpret

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
)

var entityClock = FixedClock(time.Date(2026, 9, 16, 1, 2, 3, 0, time.UTC))

func issueResult(t *testing.T, show string, degraded string) gather.GatherResult {
	t.Helper()
	b, err := json.Marshal(gather.IssueFacts{IssueShow: json.RawMessage(show)})
	if err != nil {
		t.Fatal(err)
	}
	return gather.GatherResult{Payload: b, Degraded: degraded}
}

func TestEntityInterpreters_Registry(t *testing.T) {
	m := EntityInterpreters()
	if len(m) != 2 || m["pr"] == nil || m["issue"] == nil {
		t.Fatalf("registry: %v", m)
	}
	if _, ok := m["thread"]; ok {
		t.Fatal("unexpected interpreter")
	}
}

func TestInterpretIssue_OwnershipAndCategory(t *testing.T) {
	cfg := &config.Config{SelfIssueOwner: "me"}
	cases := []struct {
		name, show, want string
	}{
		{"owner", `{"id":"b","owner":"me","assignee":"z","issue_type":"bug"}`, "mine"},
		{"assignee", `{"id":"b","owner":"o","assignee":"me","issue_type":"bug"}`, "co-owned"},
		{"team", `{"id":"b","owner":"o","assignee":"z","issue_type":"bug"}`, "team"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := InterpretIssue(issueResult(t, tc.show, "note"), entityClock, cfg)
			if err != nil {
				t.Fatal(err)
			}
			want := Interpretation{Ownership: tc.want, Category: "bug", Degraded: "note", AsOf: "2026-09-16T01:02:03Z"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v want %+v", got, want)
			}
		})
	}
}

func TestInterpretIssue_NilConfigIsTeam(t *testing.T) {
	got, err := InterpretIssue(issueResult(t, `{"owner":"me","issue_type":"task"}`, ""), entityClock, nil)
	if err != nil || got.Ownership != "team" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestInterpretIssue_EarlyReturns(t *testing.T) {
	want := Interpretation{Degraded: "d", AsOf: "2026-09-16T01:02:03Z"}
	// Empty payload: length check happens before decode.
	got, err := InterpretIssue(gather.GatherResult{Degraded: "d", RemovedState: "not_found"}, entityClock, &config.Config{})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("empty payload: %+v %v", got, err)
	}
	// Payload present, IssueShow empty.
	got, err = InterpretIssue(gather.GatherResult{Payload: json.RawMessage(`{}`), Degraded: "d"}, entityClock, &config.Config{})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("empty show: %+v %v", got, err)
	}
	// Malformed payload is an error.
	if _, err := InterpretIssue(gather.GatherResult{Payload: json.RawMessage(`nope`)}, entityClock, &config.Config{}); err == nil {
		t.Fatal("want decode error")
	}
}

// TestPRPathParityFixtures: InterpretPR equals Interpret on the same facts.
func TestPRPathParityFixtures(t *testing.T) {
	cfg := &config.Config{SelfLogin: "me"}
	facts := []gather.Facts{
		{PRShow: json.RawMessage(`{"author":"me","state":"open","title":"t"}`), AsOf: "a"},
		{RemovedState: "not_found", Degraded: "x"},
	}
	for i, f := range facts {
		want, werr := Interpret(f, entityClock, cfg)
		b, _ := json.Marshal(f)
		got, gerr := InterpretPR(gather.GatherResult{Payload: b, AsOf: f.AsOf, Degraded: f.Degraded, RemovedState: f.RemovedState}, entityClock, cfg)
		if (werr == nil) != (gerr == nil) {
			t.Fatalf("case %d errs: %v %v", i, werr, gerr)
		}
		wb, _ := json.Marshal(want)
		gb, _ := json.Marshal(got)
		if string(wb) != string(gb) {
			t.Fatalf("case %d:\n%s\n%s", i, gb, wb)
		}
	}
}
