package internal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
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
	if len(fr.calls) != 3 {
		t.Fatalf("bd calls = %v, want exactly three (created, closed, in-progress)", fr.calls)
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

// --- issue.started / issue.closed ---

// actWork renders a bead with the fields issue.started/issue.closed read.
// startedAt/closedAt "" omit the key (bd omits started_at when unset and
// reports closed_at null/absent when not closed).
func actWork(id, assignee, owner, status, startedAt, closedAt string) string {
	s := `{"id":"` + id + `","title":"title of ` + id + `","status":"` + status + `","priority":1,"issue_type":"task",` +
		`"created_by":"nobody","created_at":"2020-01-01T00:00:00Z","assignee":"` + assignee + `","owner":"` + owner + `"`
	if startedAt != "" {
		s += `,"started_at":"` + startedAt + `"`
	}
	if closedAt != "" {
		s += `,"closed_at":"` + closedAt + `"`
	} else {
		s += `,"closed_at":null`
	}
	return s + `}`
}

// workFake answers each of the three list calls from its own row set, telling
// them apart by their flags.
func workFake(closed, inProgress []string) *fakeRunner {
	return &fakeRunner{workspace: "/some/where/tracker-ws", handle: func(args []string) (string, error) {
		switch {
		case containsArg(args, "--closed-before"):
			return bdListJSON(closed...), nil
		case containsArg(args, "--status"):
			return bdListJSON(inProgress...), nil
		}
		return bdListJSON(), nil
	}}
}

func itemsOfKind(items []schema.ActivityItem, kind string) []string {
	var out []string
	for _, it := range items {
		if it.Kind == kind {
			out = append(out, it.ID)
		}
	}
	return out
}

func TestListActivity_ClosedAndStartedOnlyConfiguredActorsAssigneeOrOwner(t *testing.T) {
	fr := workFake(
		[]string{
			actWork("tp-a", "me", "", "closed", "2026-09-02T00:00:00Z", "2026-09-03T00:00:00Z"),
			actWork("tp-b", "", "my-agent", "closed", "", "2026-09-04T00:00:00Z"),
			actWork("tp-c", "stranger", "other", "closed", "2026-09-02T00:00:00Z", "2026-09-05T00:00:00Z"),
			actWork("tp-d", "", "", "closed", "2026-09-02T00:00:00Z", "2026-09-05T00:00:00Z"),
		},
		[]string{
			actWork("tp-e", "me", "", "in_progress", "2026-09-06T00:00:00Z", ""),
			actWork("tp-f", "stranger", "", "in_progress", "2026-09-06T00:00:00Z", ""),
		},
	)
	got, err := New(fr).ListActivity(actCtx("me", "my-agent"), actSince, actBefore)
	if err != nil {
		t.Fatal(err)
	}
	closed := strings.Join(itemsOfKind(got.Items, "issue.closed"), ",")
	if want := "tp-a#issue.closed#2026-09-03T00:00:00Z,tp-b#issue.closed#2026-09-04T00:00:00Z"; closed != want {
		t.Errorf("closed ids = %s, want %s", closed, want)
	}
	started := strings.Join(itemsOfKind(got.Items, "issue.started"), ",")
	if want := "tp-e#issue.started#2026-09-06T00:00:00Z,tp-a#issue.started#2026-09-02T00:00:00Z"; started != want {
		t.Errorf("started ids = %s, want %s", started, want)
	}
}

func TestListActivity_ClosedStartedItemShape(t *testing.T) {
	fr := workFake([]string{actWork("tp-a", "me", "my-agent", "closed", "2026-09-02T00:00:00Z", "2026-09-03T00:00:00Z")}, nil)
	got, err := New(fr).ListActivity(actCtx("me", "my-agent"), actSince, actBefore)
	if err != nil {
		t.Fatal(err)
	}
	// Assignee and owner are both configured actors: one item per happening.
	if len(got.Items) != 2 {
		t.Fatalf("items = %+v, want one started and one closed", got.Items)
	}
	for _, it := range got.Items {
		if it.EntityType != "issue" || it.EntityID != "tp-a" || it.Approximate || it.Stale {
			t.Errorf("item = %+v", it)
		}
		var f map[string]any
		if err := json.Unmarshal(it.Fields, &f); err != nil {
			t.Fatalf("fields: %v", err)
		}
		if f["attribution"] != "assignee" {
			t.Errorf("%s fields.attribution = %v, want assignee", it.Kind, f["attribution"])
		}
		for _, k := range []string{"title", "assignee", "owner", "issue_type", "status", "priority"} {
			if _, ok := f[k]; !ok {
				t.Errorf("%s fields missing %q: %v", it.Kind, k, f)
			}
		}
		wantLabels := "workspace:tracker-ws|tracker:beads"
		if strings.Join(it.Labels, "|") != wantLabels {
			t.Errorf("labels = %v", it.Labels)
		}
	}
	for _, it := range got.Items {
		want := map[string]string{"issue.started": "2026-09-02T00:00:00Z", "issue.closed": "2026-09-03T00:00:00Z"}[it.Kind]
		if it.OccurredAt != want {
			t.Errorf("%s occurred_at = %q, want %q (the bead's own timestamp)", it.Kind, it.OccurredAt, want)
		}
	}
}

func TestListActivity_ClosedIDStabilityAndReopen(t *testing.T) {
	b1 := actWork("tp-a", "me", "", "closed", "", "2026-09-05T00:00:00Z")
	b1offset := actWork("tp-a", "me", "", "closed", "", "2026-09-05T02:00:00+02:00") // same instant
	b2 := actWork("tp-a", "me", "", "closed", "", "2026-09-20T00:00:00Z")            // re-closed later

	ids := func(row string, since, before time.Time) []string {
		got, err := New(workFake([]string{row}, nil)).ListActivity(actCtx("me"), since, before)
		if err != nil {
			t.Fatal(err)
		}
		return itemsOfKind(got.Items, "issue.closed")
	}
	first := ids(b1, actSince, actBefore)
	overlap := ids(b1, actSince.AddDate(0, 0, 2), actBefore.AddDate(0, 1, 0))
	if len(first) != 1 || strings.Join(first, "") != strings.Join(overlap, "") {
		t.Errorf("same closed_at over overlapping ranges: %v vs %v, want identical ids", first, overlap)
	}
	if off := ids(b1offset, actSince, actBefore); strings.Join(off, "") != strings.Join(first, "") || !strings.HasSuffix(off[0], "#2026-09-05T00:00:00Z") {
		t.Errorf("offset closed_at id = %v, want UTC-rendered %v", off, first)
	}
	if later := ids(b2, actSince, actBefore); len(later) != 1 || later[0] == first[0] {
		t.Errorf("re-close ids = %v vs %v, want a different id", later, first)
	}
	// One closed_at per row: a prior close bd no longer reports is not re-emitted.
	if got := ids(b2, actSince, actBefore); len(got) != 1 || strings.Contains(strings.Join(got, ""), "09-05") {
		t.Errorf("ids = %v, want only the reported close", got)
	}
}

func TestListActivity_ClosedStartedRangeBoundaries(t *testing.T) {
	fr := workFake([]string{
		actWork("tp-before-since", "me", "", "closed", "", "2026-08-31T23:59:59Z"),
		actWork("tp-at-since", "me", "", "closed", "", "2026-09-01T00:00:00Z"),
		actWork("tp-at-before", "me", "", "closed", "", "2026-10-01T00:00:00Z"),
		actWork("tp-no-date", "me", "", "closed", "", ""),
		actWork("tp-bad-date", "me", "", "closed", "", "last tuesday"),
		actWork("tp-updated-only", "me", "", "closed", "", ""),
	}, []string{
		actWork("tp-s-at-since", "me", "", "in_progress", "2026-09-01T00:00:00Z", ""),
		actWork("tp-s-at-before", "me", "", "in_progress", "2026-10-01T00:00:00Z", ""),
		actWork("tp-s-old", "me", "", "in_progress", "2026-08-01T00:00:00Z", ""),
		actWork("tp-s-missing", "me", "", "in_progress", "", ""),
		actWork("tp-s-bad", "me", "", "in_progress", "soon", ""),
	})
	got, err := New(fr).ListActivity(actCtx("me"), actSince, actBefore)
	if err != nil {
		t.Fatal(err)
	}
	var ents []string
	for _, it := range got.Items {
		ents = append(ents, it.Kind+":"+it.EntityID)
	}
	if want := "issue.closed:tp-at-since,issue.started:tp-s-at-since"; strings.Join(ents, ",") != want {
		t.Errorf("items = %v, want %s", ents, want)
	}
}

func TestListActivity_InProgressYieldsStartedOnlyClosedYieldsBoth(t *testing.T) {
	fr := workFake(
		[]string{
			actWork("tp-closed", "me", "", "closed", "2026-09-02T00:00:00Z", "2026-09-03T00:00:00Z"),
			actWork("tp-closed-old-start", "me", "", "closed", "2026-07-02T00:00:00Z", "2026-09-03T00:00:00Z"),
		},
		[]string{actWork("tp-wip", "me", "", "in_progress", "2026-09-06T00:00:00Z", "")},
	)
	got, err := New(fr).ListActivity(actCtx("me"), actSince, actBefore)
	if err != nil {
		t.Fatal(err)
	}
	var ents []string
	for _, it := range got.Items {
		ents = append(ents, it.Kind+":"+it.EntityID)
	}
	want := "issue.closed:tp-closed,issue.closed:tp-closed-old-start,issue.started:tp-wip,issue.started:tp-closed"
	if strings.Join(ents, ",") != want {
		t.Errorf("items = %v, want %s", ents, want)
	}
}

func TestListActivity_ExactlyThreeListCallsAndNoneWithoutActors(t *testing.T) {
	fr := workFake(nil, nil)
	if _, err := New(fr).ListActivity(actCtx("me"), actSince, actBefore); err != nil {
		t.Fatal(err)
	}
	if len(fr.calls) != 3 {
		t.Fatalf("bd calls = %v, want exactly 3", fr.calls)
	}
	for _, c := range fr.calls {
		if c[0] != "list" || !containsArg(c, "--json") {
			t.Errorf("call %v is not a bd list --json", c)
		}
		if n, ok := argValue(c, "-n"); !ok || n != "0" {
			t.Errorf("call %v lacks -n 0", c)
		}
	}
	var inProgress int
	for _, c := range fr.calls {
		if v, ok := argValue(c, "--status"); ok && v == "in_progress" {
			inProgress++
		}
	}
	if inProgress != 1 {
		t.Errorf("in-progress calls = %d, want 1", inProgress)
	}

	none := workFake(nil, nil)
	if _, err := New(none).ListActivity(actCtx(), actSince, actBefore); !errors.Is(err, scriptout.ErrUnavailable) || len(none.calls) != 0 {
		t.Errorf("empty actors: err = %v, calls = %v", err, none.calls)
	}
}

func TestListActivity_ClosedArgvWidenedAndSinceOmitted(t *testing.T) {
	fr := workFake(nil, nil)
	if _, err := New(fr).ListActivity(actCtx("me"), actSince, actBefore); err != nil {
		t.Fatal(err)
	}
	var closedArgs []string
	for _, c := range fr.calls {
		if containsArg(c, "--closed-before") {
			closedArgs = c
		}
	}
	if closedArgs == nil || !containsArg(closedArgs, "--all") {
		t.Fatalf("calls = %v, want a closed-range call with --all", fr.calls)
	}
	after, _ := argValue(closedArgs, "--closed-after")
	afterT, err := time.Parse(time.RFC3339, after)
	if err != nil || afterT.After(actSince.Add(-24*time.Hour)) {
		t.Errorf("--closed-after %q (%v) is tighter than since minus one day", after, err)
	}
	beforeV, _ := argValue(closedArgs, "--closed-before")
	beforeT, err := time.Parse(time.RFC3339, beforeV)
	if err != nil || beforeT.Before(actBefore.Add(24*time.Hour)) {
		t.Errorf("--closed-before %q (%v) is tighter than before plus one day", beforeV, err)
	}

	// since omitted: no --closed-after, and the full record comes back.
	fr = workFake([]string{
		actWork("tp-old", "me", "", "closed", "", "2001-01-01T00:00:00Z"),
		actWork("tp-new", "me", "", "closed", "", "2026-09-05T00:00:00Z"),
	}, nil)
	got, err := New(fr).ListActivity(actCtx("me"), time.Time{}, actBefore)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range fr.calls {
		if containsArg(c, "--closed-after") {
			t.Errorf("argv %v carries --closed-after with since omitted", c)
		}
	}
	if n := len(itemsOfKind(got.Items, "issue.closed")); n != 2 {
		t.Errorf("closed items = %d, want the full record (2)", n)
	}
}

func TestListActivity_StartedDuplicateAcrossInProgressAndClosedEmitsOnce(t *testing.T) {
	row := actWork("tp-a", "me", "", "closed", "2026-09-02T00:00:00Z", "2026-09-03T00:00:00Z")
	fr := workFake([]string{row}, []string{row})
	got, err := New(fr).ListActivity(actCtx("me"), actSince, actBefore)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(itemsOfKind(got.Items, "issue.started")); n != 1 {
		t.Errorf("started items = %d, want 1", n)
	}
}

func TestActivityKinds_ListsAllThree(t *testing.T) {
	if got := strings.Join(ActivityKinds, ","); got != "issue.created,issue.started,issue.closed" {
		t.Errorf("ActivityKinds = %s", got)
	}
}
