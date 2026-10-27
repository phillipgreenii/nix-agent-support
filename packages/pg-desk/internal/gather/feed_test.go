package gather

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const feedWire = `{"sources":[{"backend":"jira","status":"succeeded","version":"v1","truncated":false,"next_seq":9,"store_identity":"store-a"}],` +
	`"changes":[{"change":"changed","source":"jira","seq":7,"kinds":["fields_changed","commented"],"fields":{"summary":"x"},"entity":{"id":"PROJ-1","title":"t"}},` +
	`{"change":"added","source":"jira","seq":8,"kinds":["created"],"fields":{},"entity":{"id":"PROJ-2"}}],` +
	`"sources_freshness":[{"query":"mine","last_success_at":"2026-10-26T00:00:00Z"},{"query":"team","last_error":"boom"}]}`

func TestFeedListDecodesRowsSourcesAndFreshness(t *testing.T) {
	rec := entityFactory(t, "issue changes=0:"+feedWire)
	g := NewGatherer(testConfig(""), nil)
	got, err := g.ListFeed(context.Background(), "issue", "mine", "pg-desk", "jira-a", false)
	if err != nil {
		t.Fatalf("ListFeed: %v", err)
	}
	calls := readCalls(t, rec)
	if len(calls) != 1 || calls[0] != "issue changes --query mine --consumer pg-desk --backend jira-a --no-ack --output json" {
		t.Fatalf("argv = %v", calls)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("rows = %+v", got.Rows)
	}
	r := got.Rows[0]
	if r.Seq != 7 || r.EntityID != "PROJ-1" || strings.Join(r.Kinds, ",") != "fields_changed,commented" || string(r.Fields) != `{"summary":"x"}` {
		t.Errorf("row 0 = %+v", r)
	}
	s := got.Sources[0]
	if s.Backend != "jira" || s.Status != "succeeded" || s.NextSeq == nil || *s.NextSeq != 9 || s.StoreIdentity != "store-a" || s.CursorExpired {
		t.Errorf("source = %+v", s)
	}
	if len(got.Freshness) != 2 || got.Freshness[0] != (FeedFreshness{Query: "mine", LastSuccessAt: "2026-10-26T00:00:00Z"}) || got.Freshness[1].LastError != "boom" {
		t.Errorf("freshness = %+v", got.Freshness)
	}
}

func TestFeedListEmptyAnswerStillCarriesNextSeq(t *testing.T) {
	entityFactory(t, `issue changes=0:{"sources":[{"backend":"jira","status":"succeeded","next_seq":42,"store_identity":"s"}],"changes":[]}`)
	g := NewGatherer(testConfig(""), nil)
	got, err := g.ListFeed(context.Background(), "issue", "", "pg-desk", "jira-a", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 0 || len(got.Sources) != 1 || got.Sources[0].NextSeq == nil || *got.Sources[0].NextSeq != 42 {
		t.Fatalf("got %+v", got)
	}
}

func TestFeedListEmptyQueryPassesNoQueryAndResetAppends(t *testing.T) {
	rec := entityFactory(t, `issue changes=0:{"sources":[],"changes":[]}`)
	g := NewGatherer(testConfig(""), nil)
	if _, err := g.ListFeed(context.Background(), "issue", "", "pg-desk", "jira-a", true); err != nil {
		t.Fatal(err)
	}
	calls := readCalls(t, rec)
	want := "issue changes --consumer pg-desk --backend jira-a --no-ack --reset --output json"
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("argv = %v, want %q", calls, want)
	}
}

func TestFeedListCursorExpiredExit3ReturnsDocument(t *testing.T) {
	entityFactory(t, `issue changes=3:{"sources":[{"backend":"jira","status":"degraded","reason":"cursor_expired: feed key dropped"}],"changes":[]}`)
	g := NewGatherer(testConfig(""), nil)
	got, err := g.ListFeed(context.Background(), "issue", "", "pg-desk", "jira-a", false)
	if err != nil {
		t.Fatalf("exit 3 with a parseable document must be returned, got %v", err)
	}
	if len(got.Sources) != 1 || !got.Sources[0].CursorExpired {
		t.Fatalf("sources = %+v", got.Sources)
	}
}

func TestFeedListOtherDegradedReasonIsNotCursorExpired(t *testing.T) {
	entityFactory(t, `issue changes=2:{"sources":[{"backend":"jira","status":"degraded","reason":"rate_limited"}],"changes":[]}`)
	g := NewGatherer(testConfig(""), nil)
	got, err := g.ListFeed(context.Background(), "issue", "", "pg-desk", "jira-a", false)
	if err != nil || got.Sources[0].CursorExpired {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestFeedListErrors(t *testing.T) {
	for name, behavior := range map[string]string{
		"unparseable":    `issue changes=0:not json`,
		"exit 1":         `issue changes=1:{"error":{"code":"invalid_argument","message":"bad"}}`,
		"error envelope": `issue changes=3:{"error":{"code":"backend_down","message":"all down"}}`,
		"row no id":      `issue changes=0:{"sources":[],"changes":[{"seq":1,"entity":{}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			entityFactory(t, behavior)
			g := NewGatherer(testConfig(""), nil)
			_, err := g.ListFeed(context.Background(), "issue", "", "pg-desk", "jira-a", false)
			if err == nil || errors.Is(err, ErrUnavailable) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestFeedListUnavailableWrapsErrUnavailable(t *testing.T) {
	for _, exit := range []string{"1", "3", "4"} {
		entityFactory(t, `issue changes=`+exit+`:{"error":{"code":"unavailable","message":"daemon down"}}`)
		g := NewGatherer(testConfig(""), nil)
		_, err := g.ListFeed(context.Background(), "issue", "", "pg-desk", "jira-a", false)
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("exit %s: err = %v", exit, err)
		}
	}
}

func TestFeedListRequiresArguments(t *testing.T) {
	g := NewGatherer(testConfig(""), nil)
	if _, err := g.ListFeed(context.Background(), "issue", "", "", "jira-a", false); err == nil {
		t.Error("missing consumer must be an error")
	}
	if _, err := g.ListFeed(context.Background(), "issue", "", "pg-desk", "", false); err == nil {
		t.Error("missing backend must be an error")
	}
}

func TestFeedAckArgvAndAckedSeq(t *testing.T) {
	rec := entityFactory(t, `issue changes-ack=0:{"consumer":"pg-desk","query":"mine","acked_seq":9,"store_identity":"s"}`)
	g := NewGatherer(testConfig(""), nil)
	got, err := g.AckFeed(context.Background(), "issue", "mine", "pg-desk", "jira-a", 9)
	if err != nil || got != 9 {
		t.Fatalf("got %d err %v", got, err)
	}
	calls := readCalls(t, rec)
	want := "issue changes-ack --query mine --consumer pg-desk --next-seq 9 --backend jira-a --output json"
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("argv = %v, want %q", calls, want)
	}
}

func TestFeedAckEmptyQueryAndEnvelopedAnswer(t *testing.T) {
	rec := entityFactory(t, `issue changes-ack=0:{"result":{"consumer":"pg-desk","acked_seq":0}}`)
	g := NewGatherer(testConfig(""), nil)
	got, err := g.AckFeed(context.Background(), "issue", "", "pg-desk", "jira-a", 0)
	if err != nil || got != 0 {
		t.Fatalf("got %d err %v", got, err)
	}
	if calls := readCalls(t, rec); len(calls) != 1 || strings.Contains(calls[0], "--query") {
		t.Fatalf("argv = %v", calls)
	}
}

func TestFeedAckFailures(t *testing.T) {
	entityFactory(t, `issue changes-ack=1:{"error":{"code":"invalid_argument","message":"bad"}}`)
	g := NewGatherer(testConfig(""), nil)
	if _, err := g.AckFeed(context.Background(), "issue", "", "pg-desk", "jira-a", 1); err == nil || errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	entityFactory(t, `issue changes-ack=1:{"error":{"code":"unavailable","message":"down"}}`)
	if _, err := g.AckFeed(context.Background(), "issue", "", "pg-desk", "jira-a", 1); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	entityFactory(t, `issue changes-ack=0:{"consumer":"pg-desk"}`)
	if _, err := g.AckFeed(context.Background(), "issue", "", "pg-desk", "jira-a", 1); err == nil {
		t.Fatal("an answer without acked_seq must be an error")
	}
	if _, err := g.AckFeed(context.Background(), "issue", "", "pg-desk", "jira-a", -1); err == nil {
		t.Fatal("negative next seq must be an error")
	}
}

func showWire(result string) string { return `issue show=0:{"result":` + result + `}` }

func TestEntityMetaWatchedDistinguishesTrueFalseAndNotStated(t *testing.T) {
	for _, tc := range []struct {
		name, in           string
		watched, wantState bool
	}{
		{"true", `{"entity_meta":{"watched":true}}`, true, true},
		{"false", `{"entity_meta":{"watched":false}}`, false, true},
		{"meta without watched", `{"entity_meta":{}}`, false, false},
		{"no meta", `{"id":"PROJ-1"}`, false, false},
		{"null watched", `{"entity_meta":{"watched":null}}`, false, false},
		{"garbage", `not json`, false, false},
		{"empty", ``, false, false},
	} {
		w, st := EntityMetaWatched(json.RawMessage(tc.in))
		if w != tc.watched || st != tc.wantState {
			t.Errorf("%s: got (%v,%v), want (%v,%v)", tc.name, w, st, tc.watched, tc.wantState)
		}
	}
}

func TestEntityMetaHydrationBackendKeepsFreshAndFillsWatched(t *testing.T) {
	rec := entityFactory(t, showWire(`{"id":"PROJ-1","as_of":"2026-10-26T00:00:00Z","served_from":"origin","entity_meta":{"watched":true}}`))
	g := NewGatherer(testConfig(""), nil)
	bg, ok := g.EntityGatherers()["issue"].(BackendEntityGatherer)
	if !ok {
		t.Fatal("issue adapter must implement BackendEntityGatherer")
	}
	res, err := bg.GatherEntityOn(context.Background(), "PROJ-1", "jira-a", ChangeChanged)
	if err != nil {
		t.Fatal(err)
	}
	if res.Degraded != "" || res.Watched == nil || !*res.Watched || res.AsOf != "2026-10-26T00:00:00Z" {
		t.Fatalf("res = %+v", res)
	}
	if calls := readCalls(t, rec); len(calls) != 1 || calls[0] != "issue show PROJ-1 --fresh --backend jira-a" {
		t.Fatalf("argv = %v", calls)
	}
}

func TestEntityMetaHydrationWithoutBackendKeepsTodaysArgvAndNotStated(t *testing.T) {
	rec := entityFactory(t, showWire(`{"id":"bd-1"}`))
	g := NewGatherer(testConfig(""), nil)
	res, err := g.EntityGatherers()["issue"].GatherEntity(context.Background(), "bd-1", ChangeChanged)
	if err != nil || res.Watched != nil {
		t.Fatalf("res %+v err %v", res, err)
	}
	if calls := readCalls(t, rec); len(calls) != 1 || calls[0] != "issue show bd-1 --fresh" {
		t.Fatalf("argv = %v", calls)
	}
}

func TestEntityMetaGatherEntityWatchAddsWatch(t *testing.T) {
	rec := entityFactory(t, showWire(`{"id":"PROJ-1","entity_meta":{"watched":false}}`))
	g := NewGatherer(testConfig(""), nil)
	bg := g.EntityGatherers()["issue"].(BackendEntityGatherer)
	res, err := bg.GatherEntityWatch(context.Background(), "PROJ-1", "jira-a")
	if err != nil || res.Watched == nil || *res.Watched {
		t.Fatalf("res %+v err %v", res, err)
	}
	if calls := readCalls(t, rec); len(calls) != 1 || calls[0] != "issue show PROJ-1 --fresh --backend jira-a --watch" {
		t.Fatalf("argv = %v", calls)
	}
}

func TestEntityMetaDegradedRule(t *testing.T) {
	for _, tc := range []struct {
		name, result string
		degraded     bool
	}{
		{"stale true", `{"id":"PROJ-1","stale":true}`, true},
		{"served from cache", `{"id":"PROJ-1","served_from":"cache"}`, true},
		{"served from origin", `{"id":"PROJ-1","served_from":"origin","stale":false}`, false},
		{"no annotations", `{"id":"PROJ-1"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entityFactory(t, showWire(tc.result))
			g := NewGatherer(testConfig(""), nil)
			res, err := g.EntityGatherers()["issue"].(BackendEntityGatherer).GatherEntityOn(context.Background(), "PROJ-1", "jira-a", ChangeChanged)
			if err != nil {
				t.Fatal(err)
			}
			if (res.Degraded != "") != tc.degraded {
				t.Fatalf("degraded = %q, want degraded=%v", res.Degraded, tc.degraded)
			}
		})
	}
}

func TestEntityMetaUnavailableIsErrorNotRemoved(t *testing.T) {
	entityFactory(t, `issue show=1:{"error":{"code":"unavailable","message":"daemon down"}}`)
	g := NewGatherer(testConfig(""), nil)
	res, err := g.EntityGatherers()["issue"].GatherEntity(context.Background(), "PROJ-1", ChangeChanged)
	if !errors.Is(err, ErrUnavailable) || res.RemovedState != "" {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestFeedHoldsIssue(t *testing.T) {
	g := NewGatherer(testConfig(""), nil)
	rec := entityFactory(t, showWire(`{"id":"PROJ-1"}`))
	held, err := g.HoldsIssue(context.Background(), "PROJ-1", "jira-a")
	if err != nil || !held {
		t.Fatalf("held %v err %v", held, err)
	}
	if calls := readCalls(t, rec); len(calls) != 1 || calls[0] != "issue show PROJ-1 --fresh --backend jira-a" {
		t.Fatalf("argv = %v", calls)
	}
	entityFactory(t, `issue show=4:{"error":{"code":"not_found"}}`)
	held, err = g.HoldsIssue(context.Background(), "PROJ-1", "jira-a")
	if err != nil || held {
		t.Fatalf("held %v err %v", held, err)
	}
	entityFactory(t, `issue show=1:{"error":{"code":"unavailable","message":"down"}}`)
	if _, err = g.HoldsIssue(context.Background(), "PROJ-1", "jira-a"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err %v", err)
	}
	entityFactory(t, `issue show=1:boom`)
	if _, err = g.HoldsIssue(context.Background(), "PROJ-1", "jira-a"); err == nil || errors.Is(err, ErrUnavailable) {
		t.Fatalf("err %v", err)
	}
}

func TestFeedListQueryMembers(t *testing.T) {
	rec := entityFactory(t, `issue list=0:{"entities":[{"id":"PROJ-1","entity_meta":{"watched":true}},{"id":"PROJ-2","entity_meta":{"watched":false}},{"id":"PROJ-3"}],"present_ids":["PROJ-1","PROJ-2","PROJ-3"],"truncated":true}`)
	g := NewGatherer(testConfig(""), nil)
	got, err := g.ListQueryMembers(context.Background(), "mine", "jira-a")
	if err != nil {
		t.Fatal(err)
	}
	if calls := readCalls(t, rec); len(calls) != 1 || calls[0] != "issue list --query mine --backend jira-a --output json" {
		t.Fatalf("argv = %v", calls)
	}
	if !got.Truncated || len(got.Members) != 3 {
		t.Fatalf("got %+v", got)
	}
	if w := got.Members[0].Watched; w == nil || !*w {
		t.Errorf("member 0 = %+v", got.Members[0])
	}
	if w := got.Members[1].Watched; w == nil || *w {
		t.Errorf("member 1 = %+v", got.Members[1])
	}
	if got.Members[2].ID != "PROJ-3" || got.Members[2].Watched != nil {
		t.Errorf("member 2 = %+v", got.Members[2])
	}
}

func TestFeedListQueryMembersUnavailable(t *testing.T) {
	entityFactory(t, `issue list=1:{"error":{"code":"unavailable","message":"down"}}`)
	g := NewGatherer(testConfig(""), nil)
	if _, err := g.ListQueryMembers(context.Background(), "mine", "jira-a"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err %v", err)
	}
}
