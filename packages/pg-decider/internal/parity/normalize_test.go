package parity

import (
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/action"
)

func ptr(s string) *string { return &s }

func TestNormalizeOldMapsObservedRowKinds(t *testing.T) {
	fx := &Fixture{Name: "x", Entities: []string{"acme/api#1"}}
	old := OldResult{Rows: map[string][]PlannedRow{
		"acme/api#1": {
			{Kind: "review-request", ContentHash: "h3"},
			{Kind: "anchor", ContentHash: "h1"},
			{Kind: "feedback-cycle", ContentHash: "h2"},
		},
	}}
	got, err := NormalizeOld(fx, old)
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{"acme/api#1", "anchor", OpCreate},
		{"acme/api#1", "process-feedback", OpCreate},
		{"acme/api#1", "review-pr", OpCreate},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNormalizeOldRejectsUnknownRowKind(t *testing.T) {
	fx := &Fixture{Name: "x", Entities: []string{"acme/api#1"}}
	old := OldResult{Rows: map[string][]PlannedRow{"acme/api#1": {{Kind: "mystery"}}}}
	if _, err := NormalizeOld(fx, old); err == nil || !strings.Contains(err.Error(), "mystery") {
		t.Fatalf("err = %v, want one naming the unknown kind", err)
	}
}

func TestNormalizeOldDerivesAdoptionFromFixtureBeadShapes(t *testing.T) {
	fx := &Fixture{
		Name:     "x",
		Entities: []string{"acme/api#7"},
		Beads: []BeadFixture{
			{ID: "a", Title: "acme/api#7: Some title"},
			{ID: "r", Title: "review-pr: acme/api#7", State: "closed"},
			{ID: "f", Title: "process-feedback: acme/api#7"},
			{ID: "o", Title: "an unrelated bead"},
			{ID: "p", Title: "review-pr: acme/api#8"},
		},
	}
	got, err := NormalizeOld(fx, OldResult{Rows: map[string][]PlannedRow{"acme/api#7": nil}})
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{"acme/api#7", "anchor", OpAdopt},
		{"acme/api#7", "process-feedback", OpAdopt},
		{"acme/api#7", "review-pr", OpAdopt},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNormalizeNewMapsEveryOpAndKind(t *testing.T) {
	plan := action.PlanResult{Actions: []action.Action{
		{Op: action.OpCreate, Kind: "anchor", Rule: "anchor.lazy"},
		{Op: action.OpCreate, Kind: "review-pr", Rule: "review.head-advanced"},
		{Op: action.OpCreate, Kind: "process-feedback", Rule: "feedback.digest-changed"},
		{Op: action.OpCreate, Kind: "fix-ci", Rule: "fixci.failing-on-head"},
		{Op: action.OpCreate, Kind: "resolve-conflict", Rule: "conflict.present"},
		{Op: action.OpCreate, Kind: "focus-item", Rule: "focus.item"},
		{Op: action.OpClose, Kind: "anchor", Target: ptr("b1"), Rule: "all.closed"},
		{Op: action.OpReopen, Kind: "anchor", Target: ptr("b1"), Rule: "all.reopened"},
		{Op: action.OpUpdate, Kind: "anchor", Target: ptr("b1"), Rule: "anchor.backfill"},
		{Op: action.OpAnnotate, Target: ptr("ready_to_land"), Rule: "land.ready"},
	}}
	got, err := NormalizeNew(NewResult{Plans: map[string]action.PlanResult{"acme/api#1": plan}})
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{"acme/api#1", "annotation:ready_to_land", OpAnnotate},
		{"acme/api#1", "anchor", OpClose},
		{"acme/api#1", "anchor", OpCreate},
		{"acme/api#1", "anchor", OpReopen},
		{"acme/api#1", "anchor", OpUpdate},
		{"acme/api#1", "fix-ci", OpCreate},
		{"acme/api#1", "focus-item", OpCreate},
		{"acme/api#1", "process-feedback", OpCreate},
		{"acme/api#1", "resolve-conflict", OpCreate},
		{"acme/api#1", "review-pr", OpCreate},
	}
	sortEntries(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestNormalizeNewSplitsAdoptionIntoLinkAndKeyWrite(t *testing.T) {
	plan := action.PlanResult{Actions: []action.Action{
		{Op: action.OpUpdate, Kind: "review-pr", Target: ptr("b1"), Rule: "adoption"},
		{Op: action.OpUpdate, Kind: "anchor", Target: ptr("b2"), Rule: "adoption.node-id"},
	}}
	got, err := NormalizeNew(NewResult{Plans: map[string]action.PlanResult{"acme/api#1": plan}})
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{"acme/api#1", "anchor", OpKeyRewrite},
		{"acme/api#1", "review-pr", OpAdopt},
		{"acme/api#1", "review-pr", OpKeyWrite},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNormalizeNewKeepsEntitiesWithNoActions(t *testing.T) {
	got, err := NormalizeNew(NewResult{Plans: map[string]action.PlanResult{"acme/api#1": {}}})
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want no entries and no error", got, err)
	}
}

func TestNormalizeNewRejectsUnknownKindAndOp(t *testing.T) {
	for name, a := range map[string]action.Action{
		"kind": {Op: action.OpCreate, Kind: "teleport", Rule: "r"},
		"op":   {Op: action.Op("explode"), Kind: "anchor", Rule: "r"},
	} {
		_, err := NormalizeNew(NewResult{Plans: map[string]action.PlanResult{"acme/api#1": {Actions: []action.Action{a}}}})
		if err == nil {
			t.Errorf("%s: want an error, got none", name)
		}
	}
}
