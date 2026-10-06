package internal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var (
	actSince  = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	actBefore = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
)

// bdListJSON renders a bd `list --json` envelope from raw issue objects.
func bdListJSON(issues ...string) string {
	return `{"data":[` + strings.Join(issues, ",") + `],"schema_version":1}`
}

func actBead(id, createdBy, createdAt string, labels ...string) string {
	lj, _ := json.Marshal(labels)
	return `{"id":"` + id + `","title":"title of ` + id + `","status":"open","priority":2,"issue_type":"task",` +
		`"created_by":"` + createdBy + `","created_at":"` + createdAt + `","labels":` + string(lj) + `}`
}

func actCtx(actors ...string) context.Context {
	cfg, _ := json.Marshal(map[string]any{"activity_actors": actors})
	return scriptout.WithConfig(context.Background(), cfg)
}

func listFake(issues ...string) *fakeRunner {
	return &fakeRunner{workspace: "/some/where/tracker-ws", handle: func([]string) (string, error) {
		return bdListJSON(issues...), nil
	}}
}

func TestListActivity_EmptyOrMissingActorsIsUnavailableBeforeAnyBdCall(t *testing.T) {
	cases := map[string]context.Context{
		"no config":    context.Background(),
		"empty list":   actCtx(),
		"blank actors": actCtx("", "  "),
		"wrong key":    scriptout.WithConfig(context.Background(), json.RawMessage(`{"attention_exclude":"x"}`)),
		"bad json":     scriptout.WithConfig(context.Background(), json.RawMessage(`not json`)),
	}
	for name, ctx := range cases {
		t.Run(name, func(t *testing.T) {
			fr := listFake(actBead("tp-1", "me", "2026-09-05T00:00:00Z"))
			_, err := New(fr).ListActivity(ctx, actSince, actBefore)
			if !errors.Is(err, scriptout.ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
			if !strings.Contains(err.Error(), "activity_actors") {
				t.Errorf("err = %q, want it to name activity_actors", err)
			}
			if len(fr.calls) != 0 {
				t.Errorf("bd calls = %v, want none", fr.calls)
			}
		})
	}
}

func TestListActivity_OnlyConfiguredActorsBeads(t *testing.T) {
	fr := listFake(
		actBead("tp-1", "me", "2026-09-05T00:00:00Z"),
		actBead("tp-2", "someone-else", "2026-09-06T00:00:00Z"),
		actBead("tp-3", "my-agent", "2026-09-07T00:00:00Z"),
		actBead("tp-4", "another-human", "2026-09-08T00:00:00Z"),
		actBead("tp-5", "", "2026-09-09T00:00:00Z"),
	)
	got, err := New(fr).ListActivity(actCtx("me", "my-agent"), actSince, actBefore)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	var ids []string
	for _, it := range got.Items {
		ids = append(ids, it.ID)
	}
	want := []string{"tp-1#issue.created", "tp-3#issue.created"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	if got.Truncated {
		t.Error("truncated = true, want false (-n 0 is unbounded)")
	}
}

func TestListActivity_ItemShapeAndFieldsKeys(t *testing.T) {
	fr := listFake(actBead("tp-1", "me", "2026-09-05T01:02:03Z", "alpha", "beta"))
	got, err := New(fr).ListActivity(actCtx("me"), actSince, actBefore)
	if err != nil || len(got.Items) != 1 {
		t.Fatalf("ListActivity = %+v, %v", got, err)
	}
	it := got.Items[0]
	if it.ID != "tp-1#issue.created" || it.Kind != "issue.created" || it.EntityType != "issue" || it.EntityID != "tp-1" {
		t.Errorf("identity fields = %+v", it)
	}
	if it.OccurredAt != "2026-09-05T01:02:03Z" {
		t.Errorf("occurred_at = %q, want the bead's own created_at", it.OccurredAt)
	}
	if it.Approximate || it.Stale || it.Summary == "" {
		t.Errorf("approximate/stale/summary = %v/%v/%q", it.Approximate, it.Stale, it.Summary)
	}
	if _, err := time.Parse(time.RFC3339, it.AsOf); err != nil {
		t.Errorf("as_of %q: %v", it.AsOf, err)
	}
	wantLabels := []string{"workspace:tracker-ws", "tracker:beads", "bead-label:alpha", "bead-label:beta"}
	if strings.Join(it.Labels, "|") != strings.Join(wantLabels, "|") {
		t.Errorf("labels = %v, want %v", it.Labels, wantLabels)
	}
	// The fields keys are pinned: issue.created documents exactly these.
	var fields map[string]any
	if err := json.Unmarshal(it.Fields, &fields); err != nil {
		t.Fatalf("fields is not an object: %v (%s)", err, it.Fields)
	}
	wantFields := map[string]any{
		"title": "title of tp-1", "created_by": "me", "issue_type": "task", "status": "open", "priority": float64(2),
	}
	if len(fields) != len(wantFields) {
		t.Errorf("fields = %v, want exactly keys of %v", fields, wantFields)
	}
	for k, v := range wantFields {
		if fields[k] != v {
			t.Errorf("fields[%q] = %v, want %v", k, fields[k], v)
		}
	}
}

func TestListActivity_IDStableAcrossRanges(t *testing.T) {
	fr := listFake(actBead("tp-1", "me", "2026-09-05T00:00:00Z"))
	b := New(fr)
	first, err := b.ListActivity(actCtx("me"), actSince, actBefore)
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.ListActivity(actCtx("me"), actSince.AddDate(0, 0, 3), actBefore.AddDate(0, 1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || len(second.Items) != 1 || first.Items[0].ID != second.Items[0].ID {
		t.Fatalf("ids differ across ranges: %+v vs %+v", first.Items, second.Items)
	}
}

func TestListActivity_ExactRangeBoundaries(t *testing.T) {
	fr := listFake(
		actBead("tp-before-since", "me", "2026-08-31T23:59:59Z"),
		actBead("tp-at-since", "me", "2026-09-01T00:00:00Z"),
		actBead("tp-inside", "me", "2026-09-15T12:00:00+02:00"),
		actBead("tp-just-before-end", "me", "2026-09-30T23:59:59Z"),
		actBead("tp-at-before", "me", "2026-10-01T00:00:00Z"),
		actBead("tp-after", "me", "2026-10-02T00:00:00Z"),
		actBead("tp-no-date", "me", ""),
		actBead("tp-bad-date", "me", "yesterday"),
	)
	got, err := New(fr).ListActivity(actCtx("me"), actSince, actBefore)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, it := range got.Items {
		ids = append(ids, it.EntityID)
	}
	want := "tp-at-since,tp-inside,tp-just-before-end"
	if strings.Join(ids, ",") != want {
		t.Fatalf("entity ids = %v, want %s", ids, want)
	}
}

func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func TestListActivity_BdArgvIsBoundedAndWidened(t *testing.T) {
	fr := listFake()
	if _, err := New(fr).ListActivity(actCtx("me"), actSince, actBefore); err != nil {
		t.Fatal(err)
	}
	if len(fr.calls) != 1 {
		t.Fatalf("bd calls = %v, want exactly one", fr.calls)
	}
	args := fr.calls[0]
	if args[0] != "list" || !containsArg(args, "--json") || !containsArg(args, "--all") {
		t.Errorf("argv = %v, want list --all --json", args)
	}
	if n, ok := argValue(args, "-n"); !ok || n != "0" {
		t.Errorf("argv = %v, want -n 0 (unbounded)", args)
	}
	after, ok := argValue(args, "--created-after")
	if !ok {
		t.Fatalf("argv = %v, want --created-after", args)
	}
	afterT, err := time.Parse(time.RFC3339, after)
	if err != nil || afterT.After(actSince.Add(-24*time.Hour)) {
		t.Errorf("--created-after %q (%v) is tighter than since minus one day", after, err)
	}
	beforeV, ok := argValue(args, "--created-before")
	if !ok {
		t.Fatalf("argv = %v, want --created-before", args)
	}
	beforeT, err := time.Parse(time.RFC3339, beforeV)
	if err != nil || beforeT.Before(actBefore.Add(24*time.Hour)) {
		t.Errorf("--created-before %q (%v) is tighter than before plus one day", beforeV, err)
	}
}

func TestListActivity_SinceOmittedHasNoCreatedAfterAndReturnsFullRecord(t *testing.T) {
	fr := listFake(
		actBead("tp-old", "me", "2001-01-01T00:00:00Z"),
		actBead("tp-new", "me", "2026-09-05T00:00:00Z"),
	)
	got, err := New(fr).ListActivity(actCtx("me"), time.Time{}, actBefore)
	if err != nil {
		t.Fatal(err)
	}
	if containsArg(fr.calls[0], "--created-after") {
		t.Errorf("argv = %v, want no --created-after", fr.calls[0])
	}
	if len(got.Items) != 2 {
		t.Errorf("items = %d, want the full record (2)", len(got.Items))
	}
}

func TestListActivity_NoInRangeBeadsSerializesItemsAsEmptyArray(t *testing.T) {
	cases := map[string]*fakeRunner{
		"bd returned none":       listFake(),
		"only others' beads":     listFake(actBead("tp-1", "someone-else", "2026-09-05T00:00:00Z")),
		"only out-of-range ones": listFake(actBead("tp-1", "me", "2026-12-05T00:00:00Z")),
	}
	for name, fr := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := New(fr).ListActivity(actCtx("me"), actSince, actBefore)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(got)
			if !strings.Contains(string(raw), `"items":[]`) {
				t.Errorf("result = %s, want items serialized as []", raw)
			}
		})
	}
}

func TestListActivity_BdFailureIsPassedThrough(t *testing.T) {
	fr := &fakeRunner{handle: func([]string) (string, error) {
		return "", errors.New("bd exploded")
	}}
	if _, err := New(fr).ListActivity(actCtx("me"), actSince, actBefore); err == nil {
		t.Fatal("want an error")
	}
}

func TestListActivity_DuplicateBdRowsEmitOneItem(t *testing.T) {
	b := actBead("tp-1", "me", "2026-09-05T00:00:00Z")
	got, err := New(listFake(b, b)).ListActivity(actCtx("me"), actSince, actBefore)
	if err != nil || len(got.Items) != 1 {
		t.Fatalf("items = %+v, err = %v, want exactly one", got, err)
	}
}

func TestNewActivityItem_FieldsAlwaysAnObject(t *testing.T) {
	for _, f := range []any{nil, map[string]any{"a": 1}, []string{"not", "an", "object"}} {
		it := newActivityItem("tp-1", "issue.created", "2026-09-05T00:00:00Z", "s", nil, f)
		if len(it.Fields) == 0 || it.Fields[0] != '{' {
			t.Errorf("fields %v -> %s, want an object", f, it.Fields)
		}
		if it.EntityType != "issue" || it.Stale || it.ID != "" {
			t.Errorf("item = %+v: entity_type issue, stale false, id left to the caller", it)
		}
	}
}
