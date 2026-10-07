package pr

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

type fakeReviewProvider struct {
	fakeProvider
	got ReviewSubmitRequest
	res ReviewSubmitResult
}

func (f *fakeReviewProvider) SubmitReview(_ context.Context, req ReviewSubmitRequest) (ReviewSubmitResult, error) {
	f.got = req
	return f.res, nil
}

func TestReviewSubmit_DispatchRoundTrip(t *testing.T) {
	p := &fakeReviewProvider{res: ReviewSubmitResult{
		ReviewID: "r1", State: "pending", HeadSHA: "abc", AsOf: "2026-01-01T00:00:00Z",
		Status: StatusAppend, Added: 2, Body: BodyWritten,
	}}
	table := NewDispatchTable(p)
	entry, ok := table["review_submit"]
	if !ok {
		t.Fatal("review_submit missing for capable provider")
	}
	args := `{"id":"pr-1","head_sha":"abc","body":"hi","comments":[{"path":"a.go","line":3,"side":"RIGHT","body":"c"},{"thread_id":"PRRT_1","body":"r"}],"supersede_pending":true}`
	out, err := entry.Handle(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	want := ReviewSubmitRequest{
		ID: "pr-1", HeadSHA: "abc", Body: "hi", SupersedePending: true,
		Comments: []ReviewComment{{Path: "a.go", Line: 3, Side: "RIGHT", Body: "c"}, {ThreadID: "PRRT_1", Body: "r"}},
	}
	if p.got.ID != want.ID || p.got.HeadSHA != want.HeadSHA || p.got.Body != want.Body ||
		!p.got.SupersedePending || len(p.got.Comments) != 2 || p.got.Comments[0] != want.Comments[0] || p.got.Comments[1] != want.Comments[1] {
		t.Fatalf("request = %#v, want %#v", p.got, want)
	}
	raw, _ := json.Marshal(out)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"review_id", "state", "head_sha", "head_moved", "live_head_sha", "as_of", "status", "added", "already_present", "dismissed", "body", "extra_pending_reviews"} {
		if _, ok := m[k]; !ok {
			t.Errorf("output missing key %q: %s", k, raw)
		}
	}
	if m["review_id"] != "r1" || m["state"] != "pending" {
		t.Errorf("output = %s", raw)
	}
}

// TestReviewSubmit_ResultWireShape pins the result's JSON names: the counters
// and body are always present, and url and last_append are omitted when unset.
// The retired supersede-era fields never appear.
func TestReviewSubmit_ResultWireShape(t *testing.T) {
	raw, _ := json.Marshal(ReviewSubmitResult{State: StateNone, Status: StatusNoChange, Body: BodyAbsent})
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"review_id", "state", "head_sha", "head_moved", "live_head_sha", "as_of", "status", "added", "already_present", "dismissed", "body", "extra_pending_reviews"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing %q: %s", k, raw)
		}
	}
	for _, k := range []string{"url", "last_append", "pending_review", "reason", "message", "superseded", "supersede"} {
		if _, ok := m[k]; ok {
			t.Errorf("%q must be absent: %s", k, raw)
		}
	}
	if m["state"] != "none" || m["status"] != "no_change" || m["body"] != "absent" {
		t.Errorf("result = %s", raw)
	}

	raw, _ = json.Marshal(ReviewSubmitResult{
		ReviewID: "r", State: "pending", Status: StatusPosted, URL: "u",
		LastAppend: &LastAppend{At: "t", Added: 2, Head: "h"},
	})
	m = map[string]any{}
	_ = json.Unmarshal(raw, &m)
	la, _ := m["last_append"].(map[string]any)
	if m["url"] != "u" || la["at"] != "t" || la["added"] != float64(2) || la["head"] != "h" {
		t.Errorf("url/last_append = %s", raw)
	}
}

// TestReviewSubmit_ReplyWireShape: a reply carries only thread_id and body, a
// new point only path, line, side and body.
func TestReviewSubmit_ReplyWireShape(t *testing.T) {
	raw, _ := json.Marshal(ReviewComment{ThreadID: "PRRT_1", Body: "b"})
	if string(raw) != `{"thread_id":"PRRT_1","body":"b"}` {
		t.Errorf("reply = %s", raw)
	}
	raw, _ = json.Marshal(ReviewComment{Path: "a.go", Line: 3, Body: "b"})
	if string(raw) != `{"path":"a.go","line":3,"body":"b"}` {
		t.Errorf("point = %s", raw)
	}
}

func TestReviewSubmit_DecodeFailureIsInvalidArgument(t *testing.T) {
	table := NewDispatchTable(&fakeReviewProvider{})
	_, err := table["review_submit"].Handle(context.Background(), json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestReviewSubmit_CapabilitiesGating(t *testing.T) {
	cases := []struct {
		name string
		p    Provider
		want bool
	}{
		{"capable", &fakeReviewProvider{}, true},
		{"incapable", &fakeProvider{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			table := NewDispatchTable(c.p)
			_, inTable := table["review_submit"]
			table = scriptout.AddCapabilities(table, 1, scriptout.CapabilitiesResponse{})
			inOps := slices.Contains(table.Ops(), "review_submit")
			if inTable != c.want || inOps != c.want {
				t.Fatalf("inTable=%v inOps=%v want %v", inTable, inOps, c.want)
			}
		})
	}
}
