package internal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func rangeCtx(cfg string) context.Context {
	return scriptout.WithConfig(context.Background(), json.RawMessage(cfg))
}

func TestWithUpdatedQualifier(t *testing.T) {
	since := time.Date(2026, 9, 28, 13, 30, 0, 0, time.UTC)
	before := time.Date(2026, 10, 5, 2, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		q    string
		r    scriptout.TimeRange
		want string
	}{
		{"unbounded unchanged", "is:open author:@me", scriptout.TimeRange{}, "is:open author:@me"},
		{"both", "is:open", scriptout.TimeRange{Since: since, Before: before}, "is:open updated:2026-09-28..2026-10-05"},
		{"since only", "is:open", scriptout.TimeRange{Since: since}, "is:open updated:>=2026-09-28"},
		{"before only", "is:open", scriptout.TimeRange{Before: before}, "is:open updated:<=2026-10-05"},
		{"non-UTC instant rendered as its UTC date", "q", scriptout.TimeRange{Since: time.Date(2026, 9, 28, 23, 0, 0, 0, time.FixedZone("x", -2*3600))}, "q updated:>=2026-09-29"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := withUpdatedQualifier(tt.q, tt.r); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPRInRange(t *testing.T) {
	r := scriptout.TimeRange{
		Since:  time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		Before: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
	}
	tests := []struct {
		name          string
		updatedAt     string
		rng           scriptout.TimeRange
		keep, imprecs bool
	}{
		{"inside", "2026-10-03T00:00:00Z", r, true, false},
		{"same day but before since", "2026-10-01T11:59:59Z", r, false, false},
		{"since inclusive", "2026-10-01T12:00:00Z", r, true, false},
		{"before exclusive", "2026-10-05T12:00:00Z", r, false, false},
		{"missing timestamp kept but imprecise", "", r, true, true},
		{"garbage timestamp kept but imprecise", "nope", r, true, true},
		{"unbounded never imprecise", "", scriptout.TimeRange{}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keep, imp := prInRange(tt.updatedAt, tt.rng)
			if keep != tt.keep || imp != tt.imprecs {
				t.Fatalf("got keep=%v imprecise=%v, want %v/%v", keep, imp, tt.keep, tt.imprecs)
			}
		})
	}
}

func TestBackend_List_Ranged_QualifierAndPreciseFilter(t *testing.T) {
	var gotQuery string
	gh := &fakeGH{
		searchEnrichedFn: func(_ context.Context, query string) ([]api.PR, error) {
			gotQuery = query
			return []api.PR{
				{Repo: "o/r", Number: 1, UpdatedAt: "2026-10-03T00:00:00Z"},
				// Same UTC day as --since but earlier than it: the day-widened
				// qualifier lets it through, the precise filter must cut it.
				{Repo: "o/r", Number: 2, UpdatedAt: "2026-10-01T01:00:00Z"},
			}, nil
		},
	}
	b := newTestBackend(t, gh)

	got, err := b.List(rangeCtx(`{"list_since":"2026-10-01T12:00:00Z"}`), []string{"is:open"}, false, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if gotQuery != "is:open updated:>=2026-10-01" {
		t.Fatalf("query = %q", gotQuery)
	}
	if len(got.Entities) != 1 || got.Entities[0].ID != "o/r#1" {
		t.Fatalf("Entities = %+v, want only o/r#1", got.Entities)
	}
	if len(got.PresentIDs) != 1 || got.PresentIDs[0] != "o/r#1" {
		t.Fatalf("PresentIDs = %+v, want equal to the returned ids", got.PresentIDs)
	}
	if got.Truncated {
		t.Fatal("Truncated = true, want false: every PR could be judged precisely")
	}
}

func TestBackend_List_Ranged_IDsOnly(t *testing.T) {
	var gotQuery string
	gh := &fakeGH{
		searchFn: func(_ context.Context, query string) ([]api.PR, error) {
			gotQuery = query
			return []api.PR{
				{Repo: "o/r", Number: 1, UpdatedAt: "2026-10-03T00:00:00Z"},
				{Repo: "o/r", Number: 2, UpdatedAt: "2026-10-06T00:00:00Z"},
			}, nil
		},
	}
	b := newTestBackend(t, gh)

	got, err := b.List(rangeCtx(`{"list_since":"2026-10-01T00:00:00Z","list_before":"2026-10-05T00:00:00Z"}`), []string{"is:open"}, true, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if gotQuery != "is:open updated:2026-10-01..2026-10-05" {
		t.Fatalf("query = %q", gotQuery)
	}
	if len(got.PresentIDs) != 1 || got.PresentIDs[0] != "o/r#1" || len(got.Entities) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestBackend_List_Ranged_UnjudgeablePRSetsTruncated(t *testing.T) {
	gh := &fakeGH{
		searchEnrichedFn: func(_ context.Context, _ string) ([]api.PR, error) {
			return []api.PR{{Repo: "o/r", Number: 1}}, nil // no UpdatedAt
		},
	}
	b := newTestBackend(t, gh)
	got, err := b.List(rangeCtx(`{"list_since":"2026-10-01T00:00:00Z"}`), []string{"is:open"}, false, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !got.Truncated {
		t.Fatal("Truncated = false, want true: a bound that could not be honored precisely must say so")
	}
	if len(got.PresentIDs) != 1 {
		t.Fatalf("PresentIDs = %+v, want the unjudgeable PR kept", got.PresentIDs)
	}
}

func TestBackend_List_Unbounded_QueryUnchangedAndNotTruncated(t *testing.T) {
	var gotQuery string
	gh := &fakeGH{
		searchEnrichedFn: func(_ context.Context, query string) ([]api.PR, error) {
			gotQuery = query
			return []api.PR{{Repo: "o/r", Number: 1}}, nil // no UpdatedAt, but no bound either
		},
	}
	b := newTestBackend(t, gh)
	got, err := b.List(rangeCtx(`{"queries":{}}`), []string{"is:open"}, false, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if gotQuery != "is:open" || got.Truncated || len(got.PresentIDs) != 1 {
		t.Fatalf("query=%q truncated=%v ids=%v; unbounded call must be unchanged", gotQuery, got.Truncated, got.PresentIDs)
	}
}

func TestBackend_List_Ranged_MalformedBoundIsInvalidArgument(t *testing.T) {
	gh := &fakeGH{
		searchEnrichedFn: func(_ context.Context, _ string) ([]api.PR, error) {
			t.Fatal("no search may run with a malformed bound")
			return nil, nil
		},
	}
	b := newTestBackend(t, gh)
	_, err := b.List(rangeCtx(`{"list_since":"7d"}`), []string{"is:open"}, false, nil)
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
}
