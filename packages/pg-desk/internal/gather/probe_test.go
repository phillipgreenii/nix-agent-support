package gather

import (
	"context"
	"strings"
	"testing"
)

func TestProbeQueryUsesTheReadOnlyListingVerb(t *testing.T) {
	rec := entityFactory(t, `pr list=0:{"entities":[]}`)
	g := NewGatherer(testConfig(""), nil)
	if err := g.ProbeQuery(context.Background(), "pr", "mine"); err != nil {
		t.Fatalf("ProbeQuery: %v", err)
	}
	calls := readCalls(t, rec)
	if len(calls) != 1 || !strings.HasSuffix(calls[0], "pr list --query mine --ids-only --output json") {
		t.Errorf("exec args = %v, want the read-only list verb (never changes)", calls)
	}
}

func TestProbeQueryExit2IsPartialAndPasses(t *testing.T) {
	entityFactory(t, `issue list=2:{"entities":[]}`)
	g := NewGatherer(testConfig(""), nil)
	if err := g.ProbeQuery(context.Background(), "issue", "work"); err != nil {
		t.Fatalf("exit 2 (partial degradation) must pass, got %v", err)
	}
}

func TestProbeQueryUnknownQueryFails(t *testing.T) {
	entityFactory(t, `pr list=1:{"error":{"code":"invalid_argument","message":"query_not_recognized"}}`)
	g := NewGatherer(testConfig(""), nil)
	err := g.ProbeQuery(context.Background(), "pr", "nope")
	if err == nil {
		t.Fatal("want an error for a query pg-connector does not recognize")
	}
	if !strings.Contains(err.Error(), "invalid_argument") {
		t.Errorf("error %q does not carry pg-connector's own reason", err)
	}
}

func TestProbeQueryTotalFailureAndCannotStart(t *testing.T) {
	entityFactory(t, `pr list=3:{"error":{"code":"backend_down","message":"all down"}}`)
	g := NewGatherer(testConfig(""), nil)
	if err := g.ProbeQuery(context.Background(), "pr", "mine"); err == nil {
		t.Fatal("exit 3 must be an error")
	}
	t.Setenv("PATH", t.TempDir())
	if err := g.ProbeQuery(context.Background(), "pr", "mine"); err == nil {
		t.Fatal("a pg-connector that cannot start must be an error")
	}
}

func TestProbeQueryRequiresArguments(t *testing.T) {
	g := NewGatherer(testConfig(""), nil)
	for _, c := range [][2]string{{"", "q"}, {"pr", ""}} {
		if err := g.ProbeQuery(context.Background(), c[0], c[1]); err == nil {
			t.Errorf("%v: want an error", c)
		}
	}
}
