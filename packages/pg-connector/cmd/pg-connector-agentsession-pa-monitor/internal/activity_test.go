package internal

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeSessionsRunner returns a canned `pa-monitor sessions` body and records
// the range it was called with.
type fakeSessionsRunner struct {
	fakeRunner
	body                string
	err                 error
	gotSince, gotBefore time.Time
	calls               int
}

func (f *fakeSessionsRunner) Sessions(_ context.Context, since, before time.Time) ([]byte, error) {
	f.calls++
	f.gotSince, f.gotBefore = since, before
	return []byte(f.body), f.err
}

var (
	testSince  = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	testBefore = time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
)

func listActivity(t *testing.T, r Runner, cfg string) (*schema.ActivityListResult, error) {
	t.Helper()
	ctx := context.Background()
	if cfg != "" {
		ctx = scriptout.WithConfig(ctx, json.RawMessage(cfg))
	}
	return New(r).ListActivity(ctx, testSince, testBefore)
}

const twoSessions = `{"sessions":[
 {"session_id":"s1","cwd":"/work/proj","branch":"feat","model":"m","started_at":"2026-10-01T10:00:00Z","ended_at":"2026-10-01T11:00:00Z","user_turns":3,"assistant_turns":4,"first_prompt":"fix the bug","tokens":{"input":1,"output":2,"cache_read":3,"cache_write":4},"cost_usd":0.5},
 {"session_id":"s2","cwd":"/work/other","branch":"main","started_at":"2026-10-01T12:00:00Z","ended_at":"2026-10-01T12:01:00Z","user_turns":0,"assistant_turns":0,"first_prompt":"","tokens":{"input":0,"output":0,"cache_read":0,"cache_write":0}}
]}`

func TestListActivity_OneItemPerSession_DefaultOmitsZeroTurns(t *testing.T) {
	r := &fakeSessionsRunner{body: twoSessions}
	res, err := listActivity(t, r, "")
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1 (zero-user-turn session omitted by default)", len(res.Items))
	}
	it := res.Items[0]
	if it.ID != "session:s1" || it.Kind != "session" || it.EntityType != "agentsession" || it.EntityID != "s1" {
		t.Errorf("identity fields = %+v", it)
	}
	if it.OccurredAt != "2026-10-01T10:00:00Z" {
		t.Errorf("occurred_at = %q, want the record's started_at", it.OccurredAt)
	}
	if it.Summary != "fix the bug (proj, feat)" {
		t.Errorf("summary = %q", it.Summary)
	}
	if want := []string{"cwd:proj", "branch:feat"}; !reflect.DeepEqual(it.Labels, want) {
		t.Errorf("labels = %v, want %v", it.Labels, want)
	}
	if it.Stale || it.AsOf == "" {
		t.Errorf("as_of/stale = %q/%v", it.AsOf, it.Stale)
	}
	if res.Truncated {
		t.Error("truncated must be false: this backend imposes no cap")
	}
	if !r.gotSince.Equal(testSince) || !r.gotBefore.Equal(testBefore) {
		t.Errorf("runner got range [%v, %v)", r.gotSince, r.gotBefore)
	}
}

func TestListActivity_FieldsIsTheWholeRecord(t *testing.T) {
	r := &fakeSessionsRunner{body: twoSessions}
	res, err := listActivity(t, r, "")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Sessions []json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(twoSessions), &doc); err != nil {
		t.Fatal(err)
	}
	var want, got any
	_ = json.Unmarshal(doc.Sessions[0], &want)
	if err := json.Unmarshal(res.Items[0].Fields, &got); err != nil {
		t.Fatalf("fields not JSON: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fields = %v, want the record %v", got, want)
	}
}

func TestListActivity_IDsStableAcrossCalls(t *testing.T) {
	r := &fakeSessionsRunner{body: twoSessions}
	a, _ := listActivity(t, r, "")
	b, _ := listActivity(t, r, "")
	if a.Items[0].ID != b.Items[0].ID {
		t.Errorf("ids differ across calls: %q vs %q", a.Items[0].ID, b.Items[0].ID)
	}
}

func TestListActivity_MinUserTurns(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		want []string
	}{
		{"default 1", "", []string{"session:s1"}},
		{"empty object", `{}`, []string{"session:s1"}},
		{"zero includes all", `{"min_user_turns":0}`, []string{"session:s1", "session:s2"}},
		{"at threshold", `{"min_user_turns":3}`, []string{"session:s1"}},
		{"above threshold", `{"min_user_turns":4}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := listActivity(t, &fakeSessionsRunner{body: twoSessions}, tt.cfg)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, it := range res.Items {
				got = append(got, it.ID)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ids = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestListActivity_InvalidConfig(t *testing.T) {
	for _, cfg := range []string{`{"min_user_turns":"2"}`, `{"min_user_turns":1.5}`, `{"min_user_turns":-1}`, `{"min_user_turns":true}`, `[1]`} {
		r := &fakeSessionsRunner{body: twoSessions}
		_, err := listActivity(t, r, cfg)
		if !errors.Is(err, scriptout.ErrInvalidArgument) {
			t.Errorf("config %s: err = %v, want ErrInvalidArgument", cfg, err)
		}
		if r.calls != 0 {
			t.Errorf("config %s: runner must not be exec'd on a bad config", cfg)
		}
	}
}

func TestListActivity_NoPromptAndDispatched(t *testing.T) {
	body := `{"sessions":[
 {"session_id":"s1","cwd":"/a/b/","branch":"","started_at":"2026-10-01T10:00:00Z","user_turns":0,"first_prompt":"  \n ","dispatched":true}]}`
	res, err := listActivity(t, &fakeSessionsRunner{body: body}, `{"min_user_turns":0}`)
	if err != nil {
		t.Fatal(err)
	}
	it := res.Items[0]
	if it.Summary != "(no prompt) (b)" {
		t.Errorf("summary = %q", it.Summary)
	}
	if want := []string{"cwd:b", "agent:dispatched"}; !reflect.DeepEqual(it.Labels, want) {
		t.Errorf("labels = %v, want %v (no empty branch label)", it.Labels, want)
	}
}

func TestListActivity_NoDispatchedLabelWhenAbsentOrFalse(t *testing.T) {
	body := `{"sessions":[
 {"session_id":"s1","cwd":"/a/b","branch":"x","started_at":"2026-10-01T10:00:00Z","user_turns":1,"first_prompt":"p"},
 {"session_id":"s2","cwd":"/a/b","branch":"x","started_at":"2026-10-01T10:00:00Z","user_turns":1,"first_prompt":"p","dispatched":false}]}`
	res, err := listActivity(t, &fakeSessionsRunner{body: body}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range res.Items {
		for _, l := range it.Labels {
			if l == "agent:dispatched" {
				t.Errorf("%s carries agent:dispatched without dispatched:true", it.ID)
			}
		}
	}
}

func TestListActivity_PromptOneLineAndTruncatedTo80Runes(t *testing.T) {
	long := strings.Repeat("é", 100)
	body, _ := json.Marshal(map[string]any{"sessions": []any{
		map[string]any{"session_id": "s1", "cwd": "/a/b", "branch": "x", "started_at": "2026-10-01T10:00:00Z", "user_turns": 1, "first_prompt": long},
		map[string]any{"session_id": "s2", "cwd": "/a/b", "branch": "x", "started_at": "2026-10-01T10:00:00Z", "user_turns": 1, "first_prompt": "line one\n\n  line\ttwo"},
		map[string]any{"session_id": "s3", "cwd": "/a/b", "branch": "x", "started_at": "2026-10-01T10:00:00Z", "user_turns": 1, "first_prompt": strings.Repeat("a", 80)},
	}})
	res, err := listActivity(t, &fakeSessionsRunner{body: string(body)}, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Repeat("é", 80) + "... (b, x)"; res.Items[0].Summary != want {
		t.Errorf("long summary = %q, want %q", res.Items[0].Summary, want)
	}
	if want := "line one line two (b, x)"; res.Items[1].Summary != want {
		t.Errorf("multi-line summary = %q, want %q", res.Items[1].Summary, want)
	}
	if want := strings.Repeat("a", 80) + " (b, x)"; res.Items[2].Summary != want {
		t.Errorf("exactly-80 summary = %q, want uncut %q", res.Items[2].Summary, want)
	}
}

func TestListActivity_SkipsUndatableOrIDlessRecords(t *testing.T) {
	body := `{"sessions":[
 {"session_id":"","cwd":"/a","started_at":"2026-10-01T10:00:00Z","user_turns":1,"first_prompt":"p"},
 {"session_id":"nodate","cwd":"/a","user_turns":1,"first_prompt":"p"},
 {"session_id":"baddate","cwd":"/a","started_at":"yesterday","ended_at":"2026-10-01T10:00:00Z","user_turns":1,"first_prompt":"p"},
 {"session_id":"ok","cwd":"/a","started_at":"2026-10-01T10:00:00Z","user_turns":1,"first_prompt":"p"}]}`
	res, err := listActivity(t, &fakeSessionsRunner{body: body}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].ID != "session:ok" {
		t.Errorf("items = %+v, want only session:ok", res.Items)
	}
}

func TestListActivity_EmptyIsNonNullSlice(t *testing.T) {
	res, err := New(&fakeSessionsRunner{body: `{"sessions":[]}`}).ListActivity(context.Background(), time.Time{}, testBefore)
	if err != nil {
		t.Fatal(err)
	}
	if res.Items == nil {
		t.Error("Items must be [] not nil")
	}
}

func TestListActivity_RunnerFailureIsUnavailable(t *testing.T) {
	for _, e := range []error{exec.ErrNotFound, errors.New("boom")} {
		_, err := listActivity(t, &fakeSessionsRunner{err: e}, "")
		if !errors.Is(err, scriptout.ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
	}
	_, err := listActivity(t, &fakeSessionsRunner{body: "not json"}, "")
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Errorf("bad body err = %v, want ErrUnavailable", err)
	}
}

func TestSessionsArgs(t *testing.T) {
	since := time.Date(2026, 10, 1, 2, 0, 0, 0, time.FixedZone("x", 3600))
	before := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		since, before time.Time
		want          []string
	}{
		{"both as RFC3339 UTC", since, before, []string{"sessions", "--json", "--since", "2026-10-01T01:00:00Z", "--before", "2026-10-05T00:00:00Z"}},
		{"zero since omitted", time.Time{}, before, []string{"sessions", "--json", "--before", "2026-10-05T00:00:00Z"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionsArgs(tt.since, tt.before); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestActivityKinds(t *testing.T) {
	if !reflect.DeepEqual(ActivityKinds, []string{"session"}) {
		t.Errorf("ActivityKinds = %v", ActivityKinds)
	}
}
