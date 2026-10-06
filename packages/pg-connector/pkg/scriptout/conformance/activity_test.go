package conformance

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/activity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var (
	actSince  = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	actBefore = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
)

// goodItem builds an item that satisfies every generic invariant.
func goodItem(id string) schema.ActivityItem {
	return schema.ActivityItem{
		ID:         id,
		Kind:       "thing.done",
		EntityType: "thing",
		EntityID:   "T-1",
		OccurredAt: "2026-09-15T10:00:00Z",
		Summary:    "did a thing",
		Fields:     json.RawMessage(`{}`),
		AsOf:       "2026-10-01T00:00:00Z",
	}
}

// fakeActivityProvider is a configurable activity.Provider.
type fakeActivityProvider struct {
	mu    sync.Mutex
	calls int
	items func(call int, since, before time.Time) []schema.ActivityItem
}

func (f *fakeActivityProvider) ListActivity(_ context.Context, since, before time.Time) (*schema.ActivityListResult, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()
	items := f.items(call, since, before)
	if items == nil {
		items = []schema.ActivityItem{}
	}
	return &schema.ActivityListResult{Items: items}, nil
}

func providerBackend(items func(call int, since, before time.Time) []schema.ActivityItem) Backend {
	return TableBackend{Table: activity.NewDispatchTable(&fakeActivityProvider{items: items})}
}

func staticItems(items ...schema.ActivityItem) func(int, time.Time, time.Time) []schema.ActivityItem {
	return func(int, time.Time, time.Time) []schema.ActivityItem { return items }
}

// laxTable is a hand-built list_activity table that validates only what its
// flags say, standing in for a backend that skips range validation.
func laxTable(rejectMissingBefore, rejectReversed bool) scriptout.DispatchTable {
	return scriptout.DispatchTable{
		"list_activity": {
			SchemaVersion: 1,
			Handle: func(_ context.Context, args json.RawMessage) (any, error) {
				var a schema.ActivityListArgs
				_ = json.Unmarshal(args, &a)
				if rejectMissingBefore && a.Before == "" {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "before required")
				}
				if rejectReversed && a.Since != "" && a.Before != "" {
					s, _ := time.Parse(time.RFC3339, a.Since)
					b, _ := time.Parse(time.RFC3339, a.Before)
					if !s.Before(b) {
						return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "reversed")
					}
				}
				return &schema.ActivityListResult{Items: []schema.ActivityItem{}}, nil
			},
		},
	}
}

func resultsByName(t *testing.T, rs []Result) map[string]Result {
	t.Helper()
	m := map[string]Result{}
	for _, r := range rs {
		if !strings.HasPrefix(r.Name, "list_activity/") {
			t.Fatalf("result name %q lacks the list_activity/ prefix", r.Name)
		}
		m[r.Name] = r
	}
	if len(m) != len(rs) {
		t.Fatalf("duplicate result names in %v", rs)
	}
	return m
}

var allActivitySubcases = []string{
	"list_activity/valid-range",
	"list_activity/item-invariants",
	"list_activity/id-stability",
	"list_activity/reversed-range",
	"list_activity/missing-before",
}

// expectOnlyFailing asserts exactly the named sub-case fails and the rest pass.
func expectOnlyFailing(t *testing.T, rs []Result, failing string) {
	t.Helper()
	m := resultsByName(t, rs)
	for _, name := range allActivitySubcases {
		r, ok := m[name]
		if !ok {
			t.Fatalf("missing sub-case %q in %v", name, rs)
		}
		switch {
		case name == failing && r.Err == nil:
			t.Errorf("%s passed, want a failure", name)
		case name != failing && (r.Err != nil || r.Skipped):
			t.Errorf("%s: err=%v skipped=%v, want a clean pass", name, r.Err, r.Skipped)
		}
	}
}

func TestRunListActivityCase_CompliantPasses(t *testing.T) {
	backend := providerBackend(staticItems(goodItem("a"), goodItem("b")))
	rs := RunListActivityCase(context.Background(), backend, actSince, actBefore)
	expectOnlyFailing(t, rs, "")
}

func TestRunListActivityCase_EmptyItemsPasses(t *testing.T) {
	backend := providerBackend(staticItems())
	rs := RunListActivityCase(context.Background(), backend, actSince, actBefore)
	expectOnlyFailing(t, rs, "")
}

func TestRunListActivityCase_OpenEndedSincePasses(t *testing.T) {
	backend := providerBackend(staticItems(goodItem("a")))
	rs := RunListActivityCase(context.Background(), backend, time.Time{}, actBefore)
	expectOnlyFailing(t, rs, "")
}

func TestRunListActivityCase_BoundaryInclusivity(t *testing.T) {
	atSince := goodItem("at-since")
	atSince.OccurredAt = actSince.Format(time.RFC3339)
	backend := providerBackend(staticItems(atSince))
	expectOnlyFailing(t, RunListActivityCase(context.Background(), backend, actSince, actBefore), "")

	atBefore := goodItem("at-before")
	atBefore.OccurredAt = actBefore.Format(time.RFC3339)
	backend = providerBackend(staticItems(atBefore))
	expectOnlyFailing(t, RunListActivityCase(context.Background(), backend, actSince, actBefore), "list_activity/item-invariants")
}

func TestRunListActivityCase_NonUTCOffsetPasses(t *testing.T) {
	it := goodItem("offset")
	it.OccurredAt = "2026-09-15T10:00:00-07:00"
	it.AsOf = "2026-10-01T00:00:00+02:00"
	expectOnlyFailing(t, RunListActivityCase(context.Background(), providerBackend(staticItems(it)), actSince, actBefore), "")
}

func TestRunListActivityCase_ItemInvariantViolations(t *testing.T) {
	cases := map[string]func(*schema.ActivityItem){
		"zone-less occurred_at": func(it *schema.ActivityItem) { it.OccurredAt = "2026-09-15T10:00:00" },
		"zone-less as_of":       func(it *schema.ActivityItem) { it.AsOf = "2026-10-01T00:00:00" },
		"before the range":      func(it *schema.ActivityItem) { it.OccurredAt = "2026-08-31T23:59:59Z" },
		"after the range":       func(it *schema.ActivityItem) { it.OccurredAt = "2026-10-02T00:00:00Z" },
		"empty id":              func(it *schema.ActivityItem) { it.ID = "" },
		"empty kind":            func(it *schema.ActivityItem) { it.Kind = "" },
		"empty entity_type":     func(it *schema.ActivityItem) { it.EntityType = "" },
		"empty entity_id":       func(it *schema.ActivityItem) { it.EntityID = "" },
		"empty summary":         func(it *schema.ActivityItem) { it.Summary = "" },
		"fields not an object":  func(it *schema.ActivityItem) { it.Fields = json.RawMessage(`[1]`) },
		"fields null":           func(it *schema.ActivityItem) { it.Fields = json.RawMessage(`null`) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			it := goodItem("x")
			mutate(&it)
			// A wire-level encode of a nil/absent Fields is covered by "fields null".
			rs := RunListActivityCase(context.Background(), providerBackend(staticItems(it)), actSince, actBefore)
			expectOnlyFailing(t, rs, "list_activity/item-invariants")
		})
	}
}

func TestRunListActivityCase_DuplicateIDsFailIDStability(t *testing.T) {
	backend := providerBackend(staticItems(goodItem("dup"), goodItem("dup")))
	expectOnlyFailing(t, RunListActivityCase(context.Background(), backend, actSince, actBefore), "list_activity/id-stability")
}

func TestRunListActivityCase_UnstableIDsFailIDStability(t *testing.T) {
	backend := providerBackend(func(call int, _, _ time.Time) []schema.ActivityItem {
		return []schema.ActivityItem{goodItem("id-" + string(rune('a'+call)))}
	})
	expectOnlyFailing(t, RunListActivityCase(context.Background(), backend, actSince, actBefore), "list_activity/id-stability")
}

func TestRunListActivityCase_AcceptedReversedRangeFails(t *testing.T) {
	backend := TableBackend{Table: laxTable(true, false)}
	expectOnlyFailing(t, RunListActivityCase(context.Background(), backend, actSince, actBefore), "list_activity/reversed-range")
}

func TestRunListActivityCase_AcceptedMissingBeforeFails(t *testing.T) {
	backend := TableBackend{Table: laxTable(false, true)}
	expectOnlyFailing(t, RunListActivityCase(context.Background(), backend, actSince, actBefore), "list_activity/missing-before")
}

func TestRunListActivityCase_ValidRangeErrorSkipsDependents(t *testing.T) {
	table := scriptout.DispatchTable{
		"list_activity": {
			SchemaVersion: 1,
			Handle: func(context.Context, json.RawMessage) (any, error) {
				return nil, scriptout.WrapError(scriptout.ErrUnavailable, "down")
			},
		},
	}
	m := resultsByName(t, RunListActivityCase(context.Background(), TableBackend{Table: table}, actSince, actBefore))
	if m["list_activity/valid-range"].Err == nil {
		t.Error("valid-range passed against an erroring backend")
	}
	for _, n := range []string{"list_activity/item-invariants", "list_activity/id-stability"} {
		if !m[n].Skipped || m[n].Err != nil {
			t.Errorf("%s: skipped=%v err=%v, want skipped", n, m[n].Skipped, m[n].Err)
		}
	}
}

func TestListActivityRequest_OmitsEmptyArgs(t *testing.T) {
	cases := []struct {
		since, before string
		want          string
	}{
		{"2026-09-01T00:00:00Z", "2026-10-01T00:00:00Z", `{"op":"list_activity","args":{"before":"2026-10-01T00:00:00Z","since":"2026-09-01T00:00:00Z"}}`},
		{"", "2026-10-01T00:00:00Z", `{"op":"list_activity","args":{"before":"2026-10-01T00:00:00Z"}}`},
		{"2026-09-01T00:00:00Z", "", `{"op":"list_activity","args":{"since":"2026-09-01T00:00:00Z"}}`},
		{"", "", `{"op":"list_activity","args":{}}`},
	}
	for _, c := range cases {
		if got := string(ListActivityRequest(c.since, c.before)); got != c.want {
			t.Errorf("ListActivityRequest(%q, %q) = %s, want %s", c.since, c.before, got, c.want)
		}
	}
}

func TestInvokeListActivity_RoundTrip(t *testing.T) {
	backend := providerBackend(staticItems(goodItem("a")))
	res, err := InvokeListActivity(context.Background(), backend, "2026-09-01T00:00:00Z", "2026-10-01T00:00:00Z")
	if err != nil {
		t.Fatalf("InvokeListActivity: %v", err)
	}
	if res.ErrorCode != "" || res.ExitCode != 0 {
		t.Fatalf("ErrorCode=%q ExitCode=%d, want success", res.ErrorCode, res.ExitCode)
	}
	var decoded schema.ActivityListResult
	if err := json.Unmarshal(res.Result, &decoded); err != nil || len(decoded.Items) != 1 {
		t.Fatalf("decode: err=%v items=%d", err, len(decoded.Items))
	}

	res, err = InvokeListActivity(context.Background(), backend, "", "")
	if err != nil {
		t.Fatalf("InvokeListActivity (no before): %v", err)
	}
	if res.ErrorCode != "invalid_argument" {
		t.Fatalf("ErrorCode = %q, want invalid_argument", res.ErrorCode)
	}
}
