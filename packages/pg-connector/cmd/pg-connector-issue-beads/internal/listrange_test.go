package internal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func listRangeCtx(cfg string) context.Context {
	return scriptout.WithConfig(context.Background(), json.RawMessage(cfg))
}

func TestIssueInRange(t *testing.T) {
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
		{"fractional seconds inside", "2026-10-03T00:00:00.123456789Z", r, true, false},
		{"offset converted before comparing", "2026-10-01T13:00:00+02:00", r, false, false}, // = 11:00Z
		{"since inclusive", "2026-10-01T12:00:00Z", r, true, false},
		{"before exclusive", "2026-10-05T12:00:00Z", r, false, false},
		{"missing kept but imprecise", "", r, true, true},
		{"garbage kept but imprecise", "x", r, true, true},
		{"unbounded never imprecise", "", scriptout.TimeRange{}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keep, imp := issueInRange(tt.updatedAt, tt.rng)
			if keep != tt.keep || imp != tt.imprecs {
				t.Fatalf("got keep=%v imprecise=%v, want %v/%v", keep, imp, tt.keep, tt.imprecs)
			}
		})
	}
}

const rangedListJSON = `{"data":[
 {"id":"tp-1","title":"in window","status":"open","priority":1,"updated_at":"2026-10-03T00:00:00Z"},
 {"id":"tp-2","title":"too old","status":"open","priority":1,"updated_at":"2026-09-01T00:00:00Z"},
 {"id":"tp-3","title":"too new","status":"open","priority":1,"updated_at":"2026-10-09T00:00:00Z"}
],"schema_version":1}`

func TestBackend_List_Ranged_FiltersByUpdatedAt(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if containsArg(args, "list_since") || containsArg(args, "--since") {
			t.Fatalf("args = %v: the bound must not leak into the bd argv", args)
		}
		return rangedListJSON, nil
	}}
	got, err := New(fr).List(listRangeCtx(`{"list_since":"2026-10-01T00:00:00Z","list_before":"2026-10-05T00:00:00Z"}`), []string{"list"}, false, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Entities) != 1 || got.Entities[0].ID != "tp-1" {
		t.Fatalf("Entities = %+v, want only tp-1", got.Entities)
	}
	if len(got.PresentIDs) != 1 || got.PresentIDs[0] != "tp-1" {
		t.Fatalf("PresentIDs = %+v, want equal to the returned ids", got.PresentIDs)
	}
	if got.Truncated {
		t.Fatal("Truncated = true, want false")
	}
}

func TestBackend_List_Ranged_IDsOnly(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) { return rangedListJSON, nil }}
	got, err := New(fr).List(listRangeCtx(`{"list_since":"2026-10-01T00:00:00Z"}`), []string{"list"}, true, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Entities) != 0 || len(got.PresentIDs) != 2 {
		t.Fatalf("got entities=%d ids=%v, want 0 entities and tp-1+tp-3", len(got.Entities), got.PresentIDs)
	}
}

func TestBackend_List_Ranged_UnjudgeableIssueSetsTruncated(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return `{"data":[{"id":"tp-1","title":"a","status":"open","priority":1}],"schema_version":1}`, nil
	}}
	got, err := New(fr).List(listRangeCtx(`{"list_since":"2026-10-01T00:00:00Z"}`), []string{"list"}, false, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !got.Truncated || len(got.PresentIDs) != 1 {
		t.Fatalf("truncated=%v ids=%v; want kept + truncated", got.Truncated, got.PresentIDs)
	}
}

func TestBackend_List_NoRangeKeys_ReturnsEverythingNotTruncated(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) { return rangedListJSON, nil }}
	got, err := New(fr).List(listRangeCtx(`{"queries":{}}`), []string{"list"}, false, nil)
	if err != nil || len(got.PresentIDs) != 3 || got.Truncated {
		t.Fatalf("got %+v, %v; want all 3 ids and not truncated", got, err)
	}
}

func TestBackend_List_Ranged_MalformedBoundIsInvalidArgument(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		t.Fatal("bd must not run with a malformed bound")
		return "", nil
	}}
	_, err := New(fr).List(listRangeCtx(`{"list_since":"7d"}`), []string{"list"}, false, nil)
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
}
