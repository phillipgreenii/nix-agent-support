package gather

import (
	"context"
	"testing"
)

// Exit 5 is the degraded answer of every fan-out verb and exit 2 is the
// shared Usage code (nothing was attempted): no pg-connector verb emits 2 for
// a degraded answer, so each raw-exit path must read 5 as an answer and 2 as
// an error, even when stdout holds a parseable document.
func TestDegradedExitMappingPerPath(t *testing.T) {
	const listDoc = `{"entities":[],"sources":[{"source":"b","status":"degraded","count":0}],"truncated":false}`
	const capDoc = `{"sources":[{"source":"jira-a","status":"degraded","reason":"x"}],"backends":[]}`
	const feedDoc = `{"sources":[{"backend":"jira","status":"degraded","reason":"x"}],"changes":[]}`
	paths := []struct {
		name     string
		verb     string
		doc      string
		callable func(g *Gatherer) error
	}{
		{"health", "issue health", healthWire, func(g *Gatherer) error {
			_, err := g.BaselineOpenAges(context.Background(), "issue", "jira-a")
			return err
		}},
		{"capabilities", "issue capabilities", capDoc, func(g *Gatherer) error {
			_, err := g.Capabilities(context.Background(), "issue", "jira-a")
			return err
		}},
		{"changes", "pr changes", changesWirePR, func(g *Gatherer) error {
			_, err := g.ListChanges(context.Background(), "pr", "mine", "pg-desk")
			return err
		}},
		{"list fingerprints", "issue list", listDoc, func(g *Gatherer) error {
			_, err := g.ListFingerprints(context.Background(), "issue", "open")
			return err
		}},
		{"probe", "issue list", listDoc, func(g *Gatherer) error {
			return g.ProbeQuery(context.Background(), "issue", "work")
		}},
		{"feed", "issue changes", feedDoc, func(g *Gatherer) error {
			_, err := g.ListFeed(context.Background(), "issue", "", "pg-desk", "jira-a", false)
			return err
		}},
		{"query members", "issue list", listDoc, func(g *Gatherer) error {
			_, err := g.ListQueryMembers(context.Background(), "work", "jira-a")
			return err
		}},
	}
	for _, p := range paths {
		t.Run(p.name+" exit 5 is an answer", func(t *testing.T) {
			entityFactory(t, p.verb+"=5:"+p.doc)
			if err := p.callable(NewGatherer(testConfig(""), nil)); err != nil {
				t.Fatalf("exit 5 with a document must be an answer: %v", err)
			}
		})
		t.Run(p.name+" exit 2 is a usage error", func(t *testing.T) {
			entityFactory(t, p.verb+"=2:"+p.doc)
			if err := p.callable(NewGatherer(testConfig(""), nil)); err == nil {
				t.Fatal("exit 2 is the shared Usage code and must be an error, not a degraded answer")
			}
		})
	}
}
