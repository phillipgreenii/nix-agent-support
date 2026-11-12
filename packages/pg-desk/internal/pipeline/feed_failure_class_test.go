package pipeline

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
)

// Each case's message deliberately names a DIFFERENT class than its type, so a
// classifier that read the text would flag or retry the wrong entries.
func TestFeedFailureClassDecidesRetryByTypeNotText(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantClass string
		flagged   bool
	}{
		{"not_found, message says timeout", gather.NewWireError("not_found", "timed out, unavailable"), "not_found", true},
		{"invalid_argument, message says unavailable", gather.NewWireError("invalid_argument", "backend unavailable"), "invalid_argument", true},
		{"unavailable, message says not found", gather.NewWireError("unavailable", "issue not found"), "unavailable", false},
		{"local timeout", fmt.Errorf("exec pg-connector: timed out after 30s: %w", gather.ErrUnavailable), "unavailable", false},
		{"unauthenticated is retried", gather.NewWireError("unauthenticated", "not_found"), "unauthenticated", false},
		{"version_mismatch is retried", gather.NewWireError("version_mismatch", "invalid_argument"), "version_mismatch", false},
		{"unknown_op is retried", gather.NewWireError("unknown_op", "not_found"), "unknown_op", false},
		{"untyped error that says not_found is retried", errors.New("not_found: gone"), "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, g := frPipeline(t)
			g.err = c.err
			if r := frIngest(t, p, frRow(1, "i-1", "title_changed")); !r.Failed {
				t.Fatalf("result = %+v", r)
			}
			fails, _ := p.store.ListIngestFailures(frInstance)
			if len(fails) != 1 || fails[0].FailureClass != c.wantClass || fails[0].Flagged() != c.flagged {
				t.Fatalf("entry = %+v, want class %q flagged %v", fails, c.wantClass, c.flagged)
			}
			before := g.calls
			retried, still, err := p.RetryIngestFailures(context.Background(), frInstance)
			if err != nil {
				t.Fatal(err)
			}
			wantCalls, wantRetried, wantStill := before+1, 1, 1
			if c.flagged {
				wantCalls, wantRetried, wantStill = before, 0, 0
			}
			if g.calls != wantCalls || retried != wantRetried || still != wantStill {
				t.Fatalf("retry: calls %d retried %d still %d, want %d %d %d", g.calls, retried, still, wantCalls, wantRetried, wantStill)
			}
			if n, _ := p.store.CountIngestFailures(frInstance); n != 1 {
				t.Fatalf("entry dropped: %d", n)
			}
		})
	}
}

func TestFeedNotFoundWithoutRemovedIsFlaggedAndNotRetried(t *testing.T) {
	p, g := frPipeline(t)
	g.result = gather.GatherResult{RemovedState: "not_found"}
	frIngest(t, p, frRow(1, "i-1", "title_changed"))
	fails, _ := p.store.ListIngestFailures(frInstance)
	if len(fails) != 1 || !fails[0].Flagged() || fails[0].FailureClass != "not_found" {
		t.Fatalf("entry = %+v", fails)
	}
	calls := g.calls
	if retried, _, _ := p.RetryIngestFailures(context.Background(), frInstance); retried != 0 || g.calls != calls {
		t.Fatalf("flagged entry retried: retried %d calls %d->%d", retried, calls, g.calls)
	}
}

func TestFeedLaterSuccessForTheEntityClearsItsFlaggedEntry(t *testing.T) {
	p, g := frPipeline(t)
	healthy := g.result
	g.err = gather.NewWireError("not_found", "gone")
	frIngest(t, p, frRow(1, "i-1", "title_changed"))
	frIngest(t, p, frRow(2, "i-2", "title_changed")) // another entity, also flagged
	if n, _ := p.store.CountIngestFailures(frInstance); n != 2 {
		t.Fatalf("entries = %d, want 2", n)
	}
	g.err, g.result = nil, healthy
	if r := frIngest(t, p, frRow(3, "i-1", "title_changed")); !r.Written {
		t.Fatalf("result = %+v", r)
	}
	fails, _ := p.store.ListIngestFailures(frInstance)
	if len(fails) != 1 || fails[0].EntityID != "i-2" || !fails[0].Flagged() {
		t.Fatalf("entries after success = %+v, want only the flagged i-2", fails)
	}
}

func TestFeedRetryThatTurnsPermanentFlagsTheEntry(t *testing.T) {
	p, g := frPipeline(t)
	g.err = gather.NewWireError("unavailable", "down")
	frIngest(t, p, frRow(1, "i-1", "title_changed"))
	g.err = gather.NewWireError("not_found", "gone")
	if _, still, err := p.RetryIngestFailures(context.Background(), frInstance); err != nil || still != 1 {
		t.Fatalf("retry = %d %v", still, err)
	}
	fails, _ := p.store.ListIngestFailures(frInstance)
	if len(fails) != 1 || !fails[0].Flagged() || fails[0].FailureClass != "not_found" {
		t.Fatalf("entry = %+v", fails)
	}
}
