package pr

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

type fakePendingProvider struct {
	fakeProvider
	got PendingReviewRequest
	res PendingReviewResult
}

func (f *fakePendingProvider) PendingReview(_ context.Context, req PendingReviewRequest) (PendingReviewResult, error) {
	f.got = req
	return f.res, nil
}

func TestReviewPending_DispatchRoundTrip(t *testing.T) {
	p := &fakePendingProvider{res: PendingReviewResult{
		Pending: true, HeadSHA: "h2", AsOf: "2026-01-01T00:00:00Z",
		Review: &PendingReview{
			ReviewID: "PRR_1", DatabaseID: 7, State: "pending", CommitSHA: "h1", Stale: true,
			Body: "b", BodyMarked: true, AllMarked: false,
			Comments: []PendingReviewComment{{ID: "C1", Path: "a.go", Line: 3, Body: "c", Marked: false}},
		},
	}}
	entry, ok := NewDispatchTable(p)["review_pending"]
	if !ok {
		t.Fatal("review_pending missing for capable provider")
	}
	out, err := entry.Handle(context.Background(), json.RawMessage(`{"id":"pr-1"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if p.got.ID != "pr-1" {
		t.Fatalf("request = %#v", p.got)
	}
	raw, _ := json.Marshal(out)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"pending", "head_sha", "as_of", "review"} {
		if _, ok := m[k]; !ok {
			t.Errorf("output missing key %q: %s", k, raw)
		}
	}
	rev := m["review"].(map[string]any)
	for _, k := range []string{"review_id", "database_id", "state", "commit_sha", "stale", "body", "body_marked", "comments", "all_marked"} {
		if _, ok := rev[k]; !ok {
			t.Errorf("review missing key %q: %s", k, raw)
		}
	}
}

func TestReviewPending_NoneOmitsReview(t *testing.T) {
	raw, _ := json.Marshal(PendingReviewResult{HeadSHA: "h", AsOf: "t"})
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m["pending"] != false {
		t.Errorf("none must carry an explicit pending:false: %s", raw)
	}
	if _, ok := m["review"]; ok {
		t.Errorf("review present on none: %s", raw)
	}
}

func TestReviewPending_DecodeFailureIsInvalidArgument(t *testing.T) {
	_, err := NewDispatchTable(&fakePendingProvider{})["review_pending"].Handle(context.Background(), json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestReviewPending_CapabilitiesGating(t *testing.T) {
	cases := []struct {
		name string
		p    Provider
		want bool
	}{
		{"capable", &fakePendingProvider{}, true},
		{"incapable", &fakeProvider{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			table := NewDispatchTable(c.p)
			_, inTable := table["review_pending"]
			table = scriptout.AddCapabilities(table, 1, scriptout.CapabilitiesResponse{})
			inOps := slices.Contains(table.Ops(), "review_pending")
			if inTable != c.want || inOps != c.want {
				t.Fatalf("inTable=%v inOps=%v want %v", inTable, inOps, c.want)
			}
		})
	}
}
