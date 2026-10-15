package internal

import (
	"context"
	"errors"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// summaryPR is a summary-level api.PR as the batched nodes document returns it.
func summaryPR(repo string, number int, nodeID string) api.PR {
	return api.PR{Repo: repo, Number: number, Title: "t", State: "open", NodeID: nodeID, HeadSHA: "abc", ChecksRollup: "success", UpdatedAt: "2026-10-01T00:00:00Z"}
}

func TestShowMany_ReturnsSummaryEntitiesAndReportsNullNodesAsMissing(t *testing.T) {
	gh := &fakeGH{nodesFn: func(_ context.Context, ids []string) (map[string]api.PR, error) {
		// PR_b resolves to nothing (deleted or inaccessible): absent from the map.
		return map[string]api.PR{"PR_a": summaryPR("o/r", 1, "PR_a"), "PR_c": summaryPR("o/r", 3, "PR_c")}, nil
	}}
	b := newTestBackend(t, gh)

	got, err := b.ShowMany(context.Background(), pr.ShowManyRequest{Refs: []pr.ShowManyRef{
		{ID: "o/r#1", NodeID: "PR_a"}, {ID: "o/r#2", NodeID: "PR_b"}, {ID: "o/r#3", NodeID: "PR_c"},
	}})
	if err != nil {
		t.Fatalf("ShowMany: %v", err)
	}
	if len(got.Entities) != 2 || got.Entities[0].ID != "o/r#1" || got.Entities[1].ID != "o/r#3" {
		t.Fatalf("Entities = %+v, want o/r#1 and o/r#3 in request order", got.Entities)
	}
	if len(got.Missing) != 1 || got.Missing[0] != "o/r#2" {
		t.Fatalf("Missing = %v, want [o/r#2]: a null node is a per-id answer, not an error", got.Missing)
	}
	e := got.Entities[0]
	if e.NodeID != "PR_a" || e.HeadSHA != "abc" || e.ChecksRollup != "success" || e.AsOf == "" || e.Stale {
		t.Fatalf("entity = %+v, want the summary field set with as_of set and stale false", e)
	}
	if len(e.Comments) != 0 || len(e.Reviews) != 0 {
		t.Fatalf("entity = %+v, want no comment or review bodies (summary level)", e)
	}
}

func TestShowMany_AnEntityNamingADifferentPRThanTheRefIsMissing(t *testing.T) {
	// The node now resolves to o/renamed#1: the ref said o/r#1, so the caller
	// must confirm with show rather than be handed an entity under another id.
	gh := &fakeGH{nodesFn: func(context.Context, []string) (map[string]api.PR, error) {
		return map[string]api.PR{"PR_a": summaryPR("o/renamed", 1, "PR_a")}, nil
	}}
	got, err := newTestBackend(t, gh).ShowMany(context.Background(), pr.ShowManyRequest{Refs: []pr.ShowManyRef{{ID: "o/r#1", NodeID: "PR_a"}}})
	if err != nil {
		t.Fatalf("ShowMany: %v", err)
	}
	if len(got.Entities) != 0 || len(got.Missing) != 1 {
		t.Fatalf("got %+v, want the mismatched ref reported missing", got)
	}
}

func TestShowMany_DuplicateRefsAreFetchedOnce(t *testing.T) {
	gh := &fakeGH{nodesFn: func(context.Context, []string) (map[string]api.PR, error) {
		return map[string]api.PR{"PR_a": summaryPR("o/r", 1, "PR_a")}, nil
	}}
	got, err := newTestBackend(t, gh).ShowMany(context.Background(), pr.ShowManyRequest{Refs: []pr.ShowManyRef{
		{ID: "o/r#1", NodeID: "PR_a"}, {ID: "o/r#1", NodeID: "PR_a"},
	}})
	if err != nil || len(got.Entities) != 1 {
		t.Fatalf("got %+v, %v, want one entity", got, err)
	}
	if len(gh.nodesCalls) != 1 || len(gh.nodesCalls[0]) != 1 {
		t.Fatalf("nodesCalls = %v, want one call naming one node id", gh.nodesCalls)
	}
}

func TestShowMany_EmptyRequestMakesNoOriginCall(t *testing.T) {
	gh := &fakeGH{}
	got, err := newTestBackend(t, gh).ShowMany(context.Background(), pr.ShowManyRequest{})
	if err != nil || len(got.Entities) != 0 || len(got.Missing) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if len(gh.nodesCalls) != 0 {
		t.Fatalf("nodesCalls = %v, want none", gh.nodesCalls)
	}
}

func TestShowMany_BadRefsAreInvalidArgumentBeforeAnyOriginCall(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  pr.ShowManyRef
	}{
		{"id not owner/repo#n", pr.ShowManyRef{ID: "nonsense", NodeID: "PR_a"}},
		{"no node id", pr.ShowManyRef{ID: "o/r#1"}},
		{"node id with document syntax", pr.ShowManyRef{ID: "o/r#1", NodeID: `PR_a"]) { x } #`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gh := &fakeGH{}
			_, err := newTestBackend(t, gh).ShowMany(context.Background(), pr.ShowManyRequest{Refs: []pr.ShowManyRef{tc.ref}})
			if !errors.Is(err, scriptout.ErrInvalidArgument) {
				t.Fatalf("err = %v, want invalid_argument", err)
			}
			if len(gh.nodesCalls) != 0 {
				t.Fatalf("nodesCalls = %v, want none", gh.nodesCalls)
			}
		})
	}
}

// A failed fetch fails every id in the request: an error, and no partial
// entities.
func TestShowMany_AFailedFetchFailsTheWholeRequest(t *testing.T) {
	gh := &fakeGH{nodesFn: func(context.Context, []string) (map[string]api.PR, error) {
		return nil, github.ErrGHAuthInvalid
	}}
	got, err := newTestBackend(t, gh).ShowMany(context.Background(), pr.ShowManyRequest{Refs: []pr.ShowManyRef{
		{ID: "o/r#1", NodeID: "PR_a"}, {ID: "o/r#2", NodeID: "PR_b"},
	}})
	if !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Fatalf("err = %v, want unauthenticated (classified like every other read)", err)
	}
	if got != nil {
		t.Fatalf("got %+v, want no result", got)
	}
}

// The reserve is judged from the fetch's own reading: below it the call is
// unavailable and its results are discarded.
func TestShowMany_BelowTheRateReserveIsUnavailable(t *testing.T) {
	gh := &fakeGH{rateLimit: 500, nodesFn: func(context.Context, []string) (map[string]api.PR, error) {
		return map[string]api.PR{"PR_a": summaryPR("o/r", 1, "PR_a")}, nil
	}}
	ctx := scriptout.WithConfig(context.Background(), []byte(`{"rate_reserve_points":1000}`))
	got, err := newTestBackend(t, gh).ShowMany(ctx, pr.ShowManyRequest{Refs: []pr.ShowManyRef{{ID: "o/r#1", NodeID: "PR_a"}}})
	if !errors.Is(err, scriptout.ErrUnavailable) || got != nil {
		t.Fatalf("got %+v, %v, want unavailable and no result", got, err)
	}
	if gh.nodesGateCalls != 1 || gh.readRateLimitCalls != 0 {
		t.Fatalf("gate calls = %d, probe calls = %d, want the folded reading and no separate probe", gh.nodesGateCalls, gh.readRateLimitCalls)
	}
}

// The ids-only list reports node ids for the ids that carry one, so the
// refresher can ask show_many without a lookup; with none reported the key is
// absent.
func TestList_IDsOnlyReportsNodeIDs(t *testing.T) {
	gh := &fakeGH{searchFn: func(context.Context, string) ([]api.PR, error) {
		return []api.PR{
			{Repo: "o/r", Number: 1, NodeID: "PR_a"},
			{Repo: "o/r", Number: 2}, // no node id reported
		}, nil
	}}
	got, err := newTestBackend(t, gh).List(context.Background(), []string{"is:open"}, true, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.NodeIDs) != 1 || got.NodeIDs["o/r#1"] != "PR_a" {
		t.Fatalf("NodeIDs = %v, want only o/r#1 -> PR_a", got.NodeIDs)
	}

	gh = &fakeGH{searchFn: func(context.Context, string) ([]api.PR, error) { return []api.PR{{Repo: "o/r", Number: 2}}, nil }}
	got, err = newTestBackend(t, gh).List(context.Background(), []string{"is:open"}, true, nil)
	if err != nil || got.NodeIDs != nil {
		t.Fatalf("NodeIDs = %v, %v, want nil when the search reported none", got.NodeIDs, err)
	}
}
