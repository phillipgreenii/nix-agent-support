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
