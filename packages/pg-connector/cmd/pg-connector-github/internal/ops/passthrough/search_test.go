package passthrough

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestSearch_MapsGHDataToSearchResult(t *testing.T) {
	gh := &fakeGH{searchFn: func(ctx context.Context, query string) ([]api.PR, error) {
		if query != "repo:owner/repo is:pr is:open" {
			t.Fatalf("query = %q", query)
		}
		return []api.PR{{Repo: "owner/repo", Number: 7, Title: "Add feature", URL: "https://example.invalid/owner/repo/pull/7"}}, nil
	}}

	got, err := newBackend(gh).Search(context.Background(), "repo:owner/repo is:pr is:open", nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search results = %+v, want exactly 1", got)
	}
	want := schema.SearchResult{Type: "pr", ID: "owner/repo#7", Title: "Add feature", URL: "https://example.invalid/owner/repo/pull/7", Source: "pg-connector-pr-github"}
	if got[0].Type != want.Type || got[0].ID != want.ID || got[0].Title != want.Title || got[0].URL != want.URL || got[0].Source != want.Source || len(got[0].Attributes) != 0 {
		t.Fatalf("Search()[0] = %+v, want %+v", got[0], want)
	}
}

func TestSearch_QueryIsTrimmed(t *testing.T) {
	var seen string
	gh := &fakeGH{searchFn: func(_ context.Context, q string) ([]api.PR, error) { seen = q; return nil, nil }}
	got, err := newBackend(gh).Search(context.Background(), "  is:open \n", nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want an empty, non-error answer", got, err)
	}
	if seen != "is:open" {
		t.Errorf("query = %q, want the trimmed text", seen)
	}
}

func TestSearch_EmptyQuery_IsInvalidArgumentAndRunsNothing(t *testing.T) {
	called := false
	gh := &fakeGH{searchFn: func(context.Context, string) ([]api.PR, error) { called = true; return nil, nil }}
	_, err := newBackend(gh).Search(context.Background(), "   ", nil)
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
	if called {
		t.Error("an empty query reached GitHub")
	}
}

func TestSearch_SearchError_Classified(t *testing.T) {
	gh := &fakeGH{searchFn: func(context.Context, string) ([]api.PR, error) { return nil, github.ErrGHAuthInvalid }}
	_, err := newBackend(gh).Search(context.Background(), "is:open", nil)
	if !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnauthenticated)", err)
	}
}

// A governor refusal is answered unavailable naming the cap.
func TestSearch_RefusalIsUnavailableNamingTheCap(t *testing.T) {
	gh := &fakeGH{searchFn: func(context.Context, string) ([]api.PR, error) {
		return nil, &governor.Refusal{Kind: governor.RefusedCap, Reason: "rest_search cap of 30 requests per minute is spent"}
	}}
	_, err := newBackend(gh).Search(context.Background(), "is:open", nil)
	if !errors.Is(err, scriptout.ErrUnavailable) || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("err = %v, want unavailable naming the cap", err)
	}
}
