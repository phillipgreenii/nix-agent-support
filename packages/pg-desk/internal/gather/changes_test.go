package gather

import (
	"context"
	"strings"
	"testing"
)

const changesWirePR = `{"sources":[{"backend":"gh","status":"succeeded"},{"backend":"gh2","status":"degraded","reason":"rate_limited"}],"changes":[` +
	`{"change":"added","source":"gh","entity":{"id":"acme/api#1","title":"First"}},` +
	`{"change":"changed","source":"gh","entity":{"id":"acme/api#2","title":"Second"}},` +
	`{"change":"removed","source":"gh","entity":{"id":"acme/api#3"}}]}`

func TestListChangesDecodesSourcesAndChanges(t *testing.T) {
	rec := entityFactory(t, "pr changes=0:"+changesWirePR)
	g := NewGatherer(testConfig(""), nil)
	got, err := g.ListChanges(context.Background(), "pr", "mine", "pg-desk")
	if err != nil {
		t.Fatalf("ListChanges: %v", err)
	}
	if len(got.Sources) != 2 || got.Sources[1] != (ListChangesSource{Backend: "gh2", Status: "degraded", Reason: "rate_limited"}) {
		t.Errorf("sources = %+v", got.Sources)
	}
	want := []ListedChange{
		{Change: ChangeAdded, EntityID: "acme/api#1", Title: "First"},
		{Change: ChangeChanged, EntityID: "acme/api#2", Title: "Second"},
		{Change: ChangeRemoved, EntityID: "acme/api#3"},
	}
	if len(got.Changes) != len(want) {
		t.Fatalf("changes = %+v", got.Changes)
	}
	for i := range want {
		if got.Changes[i] != want[i] {
			t.Errorf("change %d = %+v, want %+v", i, got.Changes[i], want[i])
		}
	}
	calls := readCalls(t, rec)
	if len(calls) != 1 || !strings.HasSuffix(calls[0], "pr changes --query mine --consumer pg-desk --output json") {
		t.Errorf("exec args = %v", calls)
	}
}

func TestListChangesThreadTitleIsFirstNonEmptyLineTruncated(t *testing.T) {
	long := strings.Repeat("x", 100)
	wire := `{"sources":[],"changes":[{"change":"added","source":"s","entity":{"id":"C1/1.2","text":"\n  \n  hello there\nsecond"}},` +
		`{"change":"added","source":"s","entity":{"id":"C1/1.3","text":"` + long + `"}}]}`
	entityFactory(t, "thread changes=0:"+wire)
	g := NewGatherer(testConfig(""), nil)
	got, err := g.ListChanges(context.Background(), "thread", "mentions", "pg-desk")
	if err != nil {
		t.Fatalf("ListChanges: %v", err)
	}
	if got.Changes[0].Title != "hello there" {
		t.Errorf("title 0 = %q", got.Changes[0].Title)
	}
	if got.Changes[1].Title != strings.Repeat("x", 80) {
		t.Errorf("title 1 = %q (len %d)", got.Changes[1].Title, len(got.Changes[1].Title))
	}
}

func TestListChangesExit2ReturnsPartialResult(t *testing.T) {
	entityFactory(t, "pr changes=2:"+changesWirePR)
	g := NewGatherer(testConfig(""), nil)
	got, err := g.ListChanges(context.Background(), "pr", "mine", "pg-desk")
	if err != nil {
		t.Fatalf("exit 2 must return the partial result, got error %v", err)
	}
	if len(got.Changes) != 3 {
		t.Errorf("changes = %+v", got.Changes)
	}
}

func TestListChangesErrors(t *testing.T) {
	for name, behavior := range map[string]string{
		"exit 3 total failure": `pr changes=3:{"error":{"code":"backend_down","message":"all down"}}`,
		"exit 1 bad query":     `pr changes=1:{"error":{"code":"query_not_recognized","message":"no such query"}}`,
		"undecodable stdout":   `pr changes=0:not json`,
		"entity without id":    `pr changes=0:{"sources":[],"changes":[{"change":"added","source":"s","entity":{"title":"t"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			entityFactory(t, behavior)
			g := NewGatherer(testConfig(""), nil)
			if _, err := g.ListChanges(context.Background(), "pr", "mine", "pg-desk"); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestListChangesCannotStartIsAnError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	g := NewGatherer(testConfig(""), nil)
	if _, err := g.ListChanges(context.Background(), "pr", "mine", "pg-desk"); err == nil {
		t.Fatal("want an error when pg-connector cannot be started")
	}
}

func TestListChangesRequiresArguments(t *testing.T) {
	g := NewGatherer(testConfig(""), nil)
	for _, c := range [][3]string{{"", "q", "c"}, {"pr", "", "c"}, {"pr", "q", ""}} {
		if _, err := g.ListChanges(context.Background(), c[0], c[1], c[2]); err == nil {
			t.Errorf("%v: want an error", c)
		}
	}
}
