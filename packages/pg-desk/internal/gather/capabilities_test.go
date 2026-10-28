package gather

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

const capsWire = `{"sources":[{"source":"jira-a","status":"succeeded","count":1}],"backends":[{"backend":"jira-a","protocolVersion":1,` +
	`"schemaVersions":{"issue":1},"ops":["capabilities"],"vocabulary":{"owns_changes":true},` +
	`"queries":{"mine":{"watched":true},"team":{"watched":false}}}]}`

func TestCapabilitiesDecodesBackendsAndPinsTheBackend(t *testing.T) {
	rec := entityFactory(t, "issue capabilities=0:"+capsWire)
	g := NewGatherer(testConfig(""), nil)
	got, err := g.Capabilities(context.Background(), "issue", "jira-a")
	if err != nil {
		t.Fatal(err)
	}
	if calls := readCalls(t, rec); len(calls) != 1 || calls[0] != "issue capabilities --backend jira-a --output json" {
		t.Fatalf("argv = %v", calls)
	}
	b, ok := got.Backend("jira-a")
	if !ok || !b.OwnsChanges() {
		t.Fatalf("backend = %+v ok=%v, want owns_changes true", b, ok)
	}
	want := map[string]CapQuery{"mine": {Watched: true}, "team": {Watched: false}}
	if !reflect.DeepEqual(b.Queries, want) {
		t.Errorf("queries = %v, want %v", b.Queries, want)
	}
	if _, ok := got.Backend("other"); ok {
		t.Error("Backend(other) found a row that is not there")
	}
}

func TestCapabilitiesWithoutABackendPassesNoFlag(t *testing.T) {
	rec := entityFactory(t, "issue capabilities=0:"+capsWire)
	g := NewGatherer(testConfig(""), nil)
	if _, err := g.Capabilities(context.Background(), "issue", ""); err != nil {
		t.Fatal(err)
	}
	if calls := readCalls(t, rec); len(calls) != 1 || calls[0] != "issue capabilities --output json" {
		t.Fatalf("argv = %v", calls)
	}
}

func TestCapabilitiesDegradedSourceHasNoBackendRowAndKeepsItsReason(t *testing.T) {
	entityFactory(t, `issue capabilities=3:{"sources":[{"source":"jira-a","status":"degraded","reason":"spawn failed"}],"backends":[]}`)
	g := NewGatherer(testConfig(""), nil)
	got, err := g.Capabilities(context.Background(), "issue", "jira-a")
	if err != nil {
		t.Fatalf("a parseable total-failure document is an answer: %v", err)
	}
	if _, ok := got.Backend("jira-a"); ok {
		t.Error("a degraded source must not yield a backend row")
	}
	if r := got.Reason("jira-a"); r != "spawn failed" {
		t.Errorf("reason = %q", r)
	}
}

func TestCapabilitiesFailuresAreErrors(t *testing.T) {
	cases := map[string]string{
		"unparseable": `issue capabilities=0:not json`,
		"odd exit":    `issue capabilities=9:{}`,
	}
	for name, behavior := range cases {
		t.Run(name, func(t *testing.T) {
			entityFactory(t, behavior)
			g := NewGatherer(testConfig(""), nil)
			if _, err := g.Capabilities(context.Background(), "issue", "jira-a"); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestCapabilitiesUnavailableWrapsErrUnavailable(t *testing.T) {
	entityFactory(t, `issue capabilities=1:{"error":{"code":"unavailable","message":"daemon down"}}`)
	g := NewGatherer(testConfig(""), nil)
	_, err := g.Capabilities(context.Background(), "issue", "jira-a")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if _, err := g.Capabilities(context.Background(), "", "jira-a"); err == nil {
		t.Error("an empty type must be refused")
	}
}
