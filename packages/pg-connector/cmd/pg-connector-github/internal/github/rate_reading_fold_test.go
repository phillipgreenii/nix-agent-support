package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// readingPage is one files/commits connection page whose own rateLimit carries
// cost 1 and the given remaining (bead pg2-msgvt: the reading folded into the
// page's own document).
func readingPage(field string, remaining int, hasNext bool, cursor string) []byte {
	nodes := ""
	if hasNext {
		nodes = "{}"
	}
	return []byte(fmt.Sprintf(`{"data":{"rateLimit":{"cost":1,"remaining":%d,"resetAt":"2026-10-08T01:00:00Z"},"repository":{"pullRequest":{%q:{"totalCount":0,"pageInfo":{"hasNextPage":%t,"endCursor":%q},"nodes":[%s]}}}}}`,
		remaining, field, hasNext, cursor, nodes))
}

// The files and commits documents select the whole reading the gate needs,
// not only the cost.
func TestFilesAndCommitsDocuments_SelectTheWholeRateLimitReading(t *testing.T) {
	for name, q := range map[string]string{"files": prFilesPageQuery, "commits": prCommitsPageQuery} {
		if !strings.Contains(q, "rateLimit { cost remaining resetAt }") {
			t.Errorf("%s document does not select rateLimit { cost remaining resetAt }", name)
		}
	}
}

type gatedRead struct {
	name string
	run  func(p *Provider, gate RateGate) error
}

var gatedReads = []gatedRead{
	{"files", func(p *Provider, gate RateGate) error {
		_, err := p.GetFilesGated(context.Background(), "o/r", 1, gate)
		return err
	}},
	{"commits", func(p *Provider, gate RateGate) error {
		_, err := p.GetCommitsGated(context.Background(), "o/r", 1, gate)
		return err
	}},
}

// Every page's own reading is offered to the gate, in order, and the read
// takes no request beyond the pages themselves (no separate probe).
func TestGetFilesAndCommitsGated_GateEveryPageWithNoSeparateProbe(t *testing.T) {
	for _, g := range gatedReads {
		t.Run(g.name, func(t *testing.T) {
			gh := &sequencedGH{responses: [][]byte{
				readingPage(g.name, 4000, true, "C1"),
				readingPage(g.name, 3999, false, ""),
			}}
			var seen []RateLimit
			err := g.run(NewWithRunner(gh), func(rl RateLimit) error {
				seen = append(seen, rl)
				return nil
			})
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if len(seen) != 2 || seen[0].Remaining != 4000 || seen[1].Remaining != 3999 || seen[0].ResetAt != "2026-10-08T01:00:00Z" {
				t.Errorf("gate readings = %+v, want both pages' own readings in order", seen)
			}
			if len(gh.calls) != 2 {
				t.Errorf("gh calls = %d, want 2 (one per page, no rateLimit probe)", len(gh.calls))
			}
		})
	}
}

// A refusal on the first page ends the read: its page is unused and no further
// page is requested.
func TestGetFilesAndCommitsGated_RefusalOnFirstPageStopsTheRead(t *testing.T) {
	refused := errors.New("below the reserve")
	for _, g := range gatedReads {
		t.Run(g.name, func(t *testing.T) {
			gh := &sequencedGH{responses: [][]byte{readingPage(g.name, 10, true, "C1")}}
			err := g.run(NewWithRunner(gh), func(RateLimit) error { return refused })
			if !errors.Is(err, refused) {
				t.Fatalf("err = %v, want the gate's refusal as-is", err)
			}
			if len(gh.calls) != 1 {
				t.Errorf("gh calls = %d, want 1 (a refusal must not page on)", len(gh.calls))
			}
		})
	}
}

// A reading that drops below the reserve on a LATER page still refuses, and the
// pages already read are discarded rather than returned as a partial result.
func TestGetFilesGated_RefusalOnLaterPageReturnsNoPartialResult(t *testing.T) {
	refused := errors.New("below the reserve")
	gh := &sequencedGH{responses: [][]byte{
		readingPage("files", 4000, true, "C1"),
		readingPage("files", 10, false, ""),
	}}
	calls := 0
	files, err := NewWithRunner(gh).GetFilesGated(context.Background(), "o/r", 1, func(rl RateLimit) error {
		calls++
		if calls == 2 {
			return refused
		}
		return nil
	})
	if !errors.Is(err, refused) || files != nil {
		t.Fatalf("files = %+v, err = %v; want no result and the refusal", files, err)
	}
}

// A response with no rateLimit object is an error when a gate is installed
// (fail closed, never a reading of 0), and is fine without one.
func TestGetFilesAndCommitsGated_MissingReadingFailsClosed(t *testing.T) {
	for _, g := range gatedReads {
		t.Run(g.name, func(t *testing.T) {
			noReading := []byte(fmt.Sprintf(`{"data":{"repository":{"pullRequest":{%q:{"totalCount":0,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}}`, g.name))
			gated := false
			err := g.run(NewWithRunner(&sequencedGH{responses: [][]byte{noReading}}), func(RateLimit) error { gated = true; return nil })
			if err == nil || gated {
				t.Fatalf("err = %v, gate called = %v; want an error without judging a made-up reading", err, gated)
			}
			if err := g.run(NewWithRunner(&sequencedGH{responses: [][]byte{noReading}}), nil); err != nil {
				t.Fatalf("nil gate: err = %v, want success", err)
			}
		})
	}
}

// A refused call has still spent its one page, and the row says so.
func TestGetFilesGated_RefusedCallLogsTheCostOfThePageThatCarriedTheReading(t *testing.T) {
	gh := &sequencedGH{responses: [][]byte{readingPage("files", 10, true, "C1")}}
	ev, line := instrumentedOp(t, "files", func(ctx context.Context) error {
		_, err := NewWithRunner(gh).GetFilesGated(ctx, "o/r", 1, func(RateLimit) error { return errors.New("below the reserve") })
		return err
	})
	requireCost(t, ev, line, 1)
}
