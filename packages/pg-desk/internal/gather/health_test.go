package gather

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

const healthWire = `{"sources":[{"source":"jira-a","status":"succeeded","count":1}],"backends":[{"backend":"jira-a","status":{"health":"degraded","queries":[` +
	`{"name":"mine","baseline_at":"2026-10-01T00:00:00Z"},{"name":"team","baseline_open_age_seconds":930},{"name":"watch","baseline_open_age_seconds":90000}]}}]}`

func TestBaselineOpenAgesReturnsOnlyQueriesWithoutAStoredBaseline(t *testing.T) {
	rec := entityFactory(t, "issue health=0:"+healthWire)
	g := NewGatherer(testConfig(""), nil)
	got, err := g.BaselineOpenAges(context.Background(), "issue", "jira-a")
	if err != nil {
		t.Fatal(err)
	}
	if calls := readCalls(t, rec); len(calls) != 1 || calls[0] != "issue health --backend jira-a --output json" {
		t.Fatalf("argv = %v", calls)
	}
	if want := map[string]int64{"team": 930, "watch": 90000}; !reflect.DeepEqual(got, want) {
		t.Errorf("ages = %v, want %v (a stored baseline has no entry)", got, want)
	}
}

func TestBaselineOpenAgesPartialExitIsAnAnswer(t *testing.T) {
	entityFactory(t, "issue health=2:"+healthWire)
	g := NewGatherer(testConfig(""), nil)
	if _, err := g.BaselineOpenAges(context.Background(), "issue", "jira-a"); err != nil {
		t.Fatalf("exit 2 with the backend's document is an answer: %v", err)
	}
}

func TestBaselineOpenAgesFailuresAreErrors(t *testing.T) {
	cases := map[string]string{
		"total failure":      `issue health=3:{"sources":[{"source":"jira-a","status":"degraded","reason":"boom"}],"backends":[]}`,
		"no status document": `issue health=2:{"sources":[{"source":"jira-a","status":"degraded","reason":"status exploded"}],"backends":[]}`,
		"another backend":    `issue health=0:{"sources":[],"backends":[{"backend":"other","status":{"queries":[]}}]}`,
		"unparseable":        `issue health=0:not json`,
	}
	for name, behavior := range cases {
		t.Run(name, func(t *testing.T) {
			entityFactory(t, behavior)
			g := NewGatherer(testConfig(""), nil)
			ages, err := g.BaselineOpenAges(context.Background(), "issue", "jira-a")
			if err == nil {
				t.Fatalf("want an error (an absent entry must never read as a stored baseline), got %v", ages)
			}
		})
	}
}

func TestBaselineOpenAgesUnavailableWrapsErrUnavailable(t *testing.T) {
	entityFactory(t, `issue health=1:{"error":{"code":"unavailable","message":"daemon down"}}`)
	g := NewGatherer(testConfig(""), nil)
	_, err := g.BaselineOpenAges(context.Background(), "issue", "jira-a")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if _, err := g.BaselineOpenAges(context.Background(), "issue", ""); err == nil {
		t.Error("an empty backend must be refused")
	}
}
