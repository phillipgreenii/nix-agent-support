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
		Supersede: &SupersedeOutcome{Attempted: true, Deleted: false, Error: "boom"},
	}}
	table := NewDispatchTable(p)
	entry, ok := table["review_submit"]
	if !ok {
		t.Fatal("review_submit missing for capable provider")
	}
	args := `{"id":"pr-1","head_sha":"abc","body":"hi","comments":[{"path":"a.go","line":3,"side":"RIGHT","body":"c"}],"supersede_pending":true}`
	out, err := entry.Handle(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	want := ReviewSubmitRequest{
		ID: "pr-1", HeadSHA: "abc", Body: "hi", SupersedePending: true,
		Comments: []ReviewComment{{Path: "a.go", Line: 3, Side: "RIGHT", Body: "c"}},
	}
	if p.got.ID != want.ID || p.got.HeadSHA != want.HeadSHA || p.got.Body != want.Body ||
		!p.got.SupersedePending || len(p.got.Comments) != 1 || p.got.Comments[0] != want.Comments[0] {
		t.Fatalf("request = %#v, want %#v", p.got, want)
	}
	raw, _ := json.Marshal(out)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"review_id", "state", "head_sha", "as_of", "supersede"} {
		if _, ok := m[k]; !ok {
			t.Errorf("output missing key %q: %s", k, raw)
		}
	}
	if m["review_id"] != "r1" || m["state"] != "pending" {
		t.Errorf("output = %s", raw)
	}
}

func TestReviewSubmit_SupersedeOmittedWhenNil(t *testing.T) {
	raw, _ := json.Marshal(ReviewSubmitResult{ReviewID: "r", State: "pending"})
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if _, ok := m["supersede"]; ok {
		t.Errorf("supersede present: %s", raw)
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

// TestReviewSubmit_StatusFieldsWireShape pins the JSON names of the guarded
// supersede's output fields (contract 9.1): status, reason, message,
// pending_review, superseded, and the omission of the optional ones when unset.
func TestReviewSubmit_StatusFieldsWireShape(t *testing.T) {
	res := ReviewSubmitResult{
		HeadSHA: "abc", AsOf: "t", Status: StatusBlockedHumanPending, Reason: ReasonHumanEdited, Message: "m",
		PendingReview: &PendingReviewRef{ReviewID: "r1", DatabaseID: 5, URL: "u", CommitSHA: "old"},
	}
	raw, _ := json.Marshal(res)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for k, want := range map[string]any{"status": "blocked_human_pending", "reason": "human_edited", "message": "m", "review_id": ""} {
		if m[k] != want {
			t.Errorf("%s = %v, want %v: %s", k, m[k], want, raw)
		}
	}
	ref, _ := m["pending_review"].(map[string]any)
	if ref["review_id"] != "r1" || ref["database_id"] != float64(5) || ref["url"] != "u" || ref["commit_sha"] != "old" {
		t.Errorf("pending_review = %v", ref)
	}
	if _, ok := m["superseded"]; ok {
		t.Errorf("superseded must be omitted unless replaced: %s", raw)
	}

	rep := ReviewSubmitResult{Status: StatusReplaced, Superseded: &SupersededReview{
		PendingReviewRef: PendingReviewRef{ReviewID: "r1", DatabaseID: 5, CommitSHA: "old"},
		ArchivePath:      "/a.json", Body: "b", Comments: []PendingReviewComment{{ID: "c", Body: "x", Marked: true}},
	}}
	raw, _ = json.Marshal(rep)
	m = map[string]any{}
	_ = json.Unmarshal(raw, &m)
	sup, _ := m["superseded"].(map[string]any)
	if sup["review_id"] != "r1" || sup["archive_path"] != "/a.json" || sup["body"] != "b" {
		t.Errorf("superseded flattens the old review's ref plus content: %v", sup)
	}
	if cs, _ := sup["comments"].([]any); len(cs) != 1 {
		t.Errorf("superseded.comments = %v", sup["comments"])
	}
}
