package gather

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestListOpenIDsReadsPresentIDsFromTheIDsOnlyListing(t *testing.T) {
	rec := entityFactory(t, `pr list=0:{"entities":[],"present_ids":["acme/widgets#1","acme/widgets#2"],"sources":[]}`)
	g := NewGatherer(testConfig(""), nil)
	ids, err := g.ListOpenIDs(context.Background(), "pr", "mine")
	if err != nil {
		t.Fatalf("ListOpenIDs: %v", err)
	}
	if want := []string{"acme/widgets#1", "acme/widgets#2"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
	calls := readCalls(t, rec)
	if len(calls) != 1 || !strings.HasSuffix(calls[0], "pr list --query mine --ids-only --output json") {
		t.Errorf("exec args = %v, want the ids-only list verb", calls)
	}
}

func TestListOpenIDsExit2IsPartialAndKeepsTheIDs(t *testing.T) {
	entityFactory(t, `pr list=2:{"present_ids":["7"]}`)
	g := NewGatherer(testConfig(""), nil)
	ids, err := g.ListOpenIDs(context.Background(), "pr", "mine")
	if err != nil || !reflect.DeepEqual(ids, []string{"7"}) {
		t.Fatalf("ids=%v err=%v, want [7] nil", ids, err)
	}
}

func TestListOpenIDsFailures(t *testing.T) {
	entityFactory(t, `pr list=3:{"error":{"code":"unavailable","message":"all down"}}`)
	g := NewGatherer(testConfig(""), nil)
	if _, err := g.ListOpenIDs(context.Background(), "pr", "mine"); err == nil {
		t.Fatal("exit 3 must be an error")
	}
	entityFactory(t, `pr list=0:not json`)
	if _, err := g.ListOpenIDs(context.Background(), "pr", "mine"); err == nil {
		t.Fatal("undecodable stdout must be an error")
	}
	if _, err := g.ListOpenIDs(context.Background(), "pr", ""); err == nil {
		t.Fatal("an empty query must be an error")
	}
}
