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

func TestRangedJQL(t *testing.T) {
	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	before := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		jql  string
		r    scriptout.TimeRange
		want string
	}{
		{"unbounded unchanged", "assignee = currentUser()", scriptout.TimeRange{}, "assignee = currentUser()"},
		{
			"both widened", "assignee = currentUser()",
			scriptout.TimeRange{Since: since, Before: before},
			`(assignee = currentUser()) AND updated >= "2026-09-30" AND updated < "2026-10-07"`,
		},
		{"since only", "project = X", scriptout.TimeRange{Since: since}, `(project = X) AND updated >= "2026-09-30"`},
		{"before only", "project = X", scriptout.TimeRange{Before: before}, `(project = X) AND updated < "2026-10-07"`},
		{
			"order by stays last", "project = X ORDER BY created DESC",
			scriptout.TimeRange{Since: since},
			`(project = X) AND updated >= "2026-09-30" ORDER BY created DESC`,
		},
		{
			"lowercase order by", "project = X order by created",
			scriptout.TimeRange{Since: since},
			`(project = X) AND updated >= "2026-09-30" order by created`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rangedJQL(tt.jql, tt.r)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIssueInRange(t *testing.T) {
	r := scriptout.TimeRange{
		Since:  time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		Before: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
	}
	tests := []struct {
		name          string
		updated       string
		rng           scriptout.TimeRange
		keep, imprecs bool
	}{
		{"rfc3339 inside", "2026-10-03T00:00:00Z", r, true, false},
		{"jira offset form inside", "2026-10-03T10:11:12.000+0000", r, true, false},
		{"jira offset form without millis", "2026-10-03T10:11:12+0000", r, true, false},
		{"jira offset converted before comparing", "2026-10-01T13:00:00.000+0200", r, false, false}, // = 11:00Z, before since
		{"after before", "2026-10-06T00:00:00.000+0000", r, false, false},
		{"missing kept but imprecise", "", r, true, true},
		{"garbage kept but imprecise", "last tuesday", r, true, true},
		{"unbounded never imprecise", "", scriptout.TimeRange{}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keep, imp := issueInRange(tt.updated, tt.rng)
			if keep != tt.keep || imp != tt.imprecs {
				t.Fatalf("got keep=%v imprecise=%v, want %v/%v", keep, imp, tt.keep, tt.imprecs)
			}
		})
	}
}

func TestBackend_List_Ranged_WidensJQLAndFiltersPrecisely(t *testing.T) {
	var jqls []string
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		jqls = append(jqls, jqlArg(args))
		return `{"items":[
			{"key":"PROJ-1","summary":"in","status":"To Do","updated":"2026-10-03T10:00:00.000+0000"},
			{"key":"PROJ-2","summary":"same day, before since","status":"To Do","updated":"2026-10-01T01:00:00.000+0000"}
		],"truncated":false}`, nil
	}}
	got, err := New(fr).List(listRangeCtx(`{"list_since":"2026-10-01T12:00:00Z"}`), []string{"assignee = currentUser()"}, false, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jqls) != 2 {
		t.Fatalf("searches = %v, want 2 (entities + present-ids)", jqls)
	}
	for _, jql := range jqls {
		if jql != `(assignee = currentUser()) AND updated >= "2026-09-30"` {
			t.Fatalf("jql = %q, want the day-widened bound on BOTH searches", jql)
		}
	}
	if len(got.Entities) != 1 || got.Entities[0].ID != "PROJ-1" {
		t.Fatalf("Entities = %+v, want only PROJ-1", got.Entities)
	}
	if len(got.PresentIDs) != 1 || got.PresentIDs[0] != "PROJ-1" {
		t.Fatalf("PresentIDs = %+v, want the bounded match set", got.PresentIDs)
	}
	if got.Truncated {
		t.Fatal("Truncated = true, want false: every issue could be judged precisely")
	}
}

func TestBackend_List_Ranged_UnjudgeableIssueSetsTruncated(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		return `{"items":[{"key":"PROJ-1","summary":"a","status":"To Do"}],"truncated":false}`, nil
	}}
	got, err := New(fr).List(listRangeCtx(`{"list_before":"2026-10-05T00:00:00Z"}`), []string{"project = X"}, false, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !got.Truncated || len(got.PresentIDs) != 1 {
		t.Fatalf("truncated=%v ids=%v; want kept + truncated", got.Truncated, got.PresentIDs)
	}
}

func TestBackend_List_NoRangeKeys_UnchangedAndNotTruncated(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		if jql := jqlArg(args); jql != "project = X" {
			t.Fatalf("jql = %q, want unmodified", jql)
		}
		return `{"items":[{"key":"PROJ-1","summary":"a","status":"To Do"}],"truncated":false}`, nil
	}}
	got, err := New(fr).List(listRangeCtx(`{"queries":{}}`), []string{"project = X"}, false, nil)
	if err != nil || got.Truncated || len(got.PresentIDs) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestBackend_List_Ranged_MalformedBoundIsInvalidArgument(t *testing.T) {
	fr := &fakeRunner{handle: func(args []string) (string, error) {
		t.Fatal("no search may run with a malformed bound")
		return "", nil
	}}
	_, err := New(fr).List(listRangeCtx(`{"list_before":"yesterday"}`), []string{"project = X"}, false, nil)
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
}
