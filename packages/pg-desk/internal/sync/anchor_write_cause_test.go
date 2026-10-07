package sync

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// These tests guard bead pg2-n6d8y items 2 and 4.
//
// Item 2: every anchor bead write logs WHY it happened, so the residual
// desk-issue echo (bead pg2-jj0ym) can be attributed from the serve/run log
// instead of inferred from code and data.
//
// Item 4: an anchor whose work-beads read was degraded (no anchor entity) and
// whose mergeability read is UNKNOWN must hold the conflict episode the ledger
// last recorded, instead of reading it as "no conflict" and costing a
// metadata-only write.

// logLines returns the captured log lines containing needle.
func logLines(buf *bytes.Buffer, needle string) []string {
	var out []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, needle) {
			out = append(out, l)
		}
	}
	return out
}

func newLoggedSyncer(t *testing.T) (*Syncer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	st := store.OpenForTest(t)
	return New(testConfig(ModeApply), st, WithClock(fixedClock), WithLogger(logger)), &buf
}

func TestSync_Anchor_WriteCauseIsLogged(t *testing.T) {
	s, buf := newLoggedSyncer(t)
	withFactory(t)
	interp := interpFor("mine", nil)
	ctx := context.Background()

	// Run 1: anchor created.
	if err := s.Sync(ctx, fixtureRepo, fixtureEntity, gather.ChangeAdded,
		conflictFacts("MERGEABLE", "CLEAN", "", nil, ""), interp); err != nil {
		t.Fatalf("Sync 1: %v", err)
	}
	if lines := logLines(buf, "anchor write"); len(lines) != 1 || !strings.Contains(lines[0], "cause=created") {
		t.Fatalf("anchor creation not logged with cause=created: %q", lines)
	}
	anchor, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)

	// Run 2: unchanged -> no bead write, so no log line.
	buf.Reset()
	if err := s.Sync(ctx, fixtureRepo, fixtureEntity, gather.ChangeChanged,
		conflictFacts("MERGEABLE", "CLEAN", anchor.BeadID, nil, "P2"), interp); err != nil {
		t.Fatalf("Sync 2: %v", err)
	}
	if lines := logLines(buf, "anchor write"); len(lines) != 0 {
		t.Fatalf("a quiet anchor check logged a write: %q", lines)
	}

	// Run 3: the PR starts conflicting; the only thing that differs from the
	// last recorded state is the conflict episode.
	if err := s.Sync(ctx, fixtureRepo, fixtureEntity, gather.ChangeChanged,
		conflictFacts("CONFLICTING", "DIRTY", anchor.BeadID, nil, "P2"), interp); err != nil {
		t.Fatalf("Sync 3: %v", err)
	}
	lines := logLines(buf, "anchor write")
	if len(lines) != 1 {
		t.Fatalf("want one anchor write log line for the conflict flip, got %q", lines)
	}
	for _, want := range []string{"cause=conflict-flip", "conflict=true", "bead=" + anchor.BeadID, "anchor_entity=true"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("conflict-flip log line missing %q: %s", want, lines[0])
		}
	}

	// Run 4: a real PR content change (branch rename) while still conflicting.
	buf.Reset()
	f := conflictFacts("CONFLICTING", "DIRTY", anchor.BeadID, []string{"pbase:2"}, "P1")
	f.PRShow = prFixture(map[string]any{"mergeable": "CONFLICTING", "merge_state_status": "DIRTY", "branch": "renamed"})
	if err := s.Sync(ctx, fixtureRepo, fixtureEntity, gather.ChangeChanged, f, interp); err != nil {
		t.Fatalf("Sync 4: %v", err)
	}
	lines = logLines(buf, "anchor write")
	if len(lines) != 1 || !strings.Contains(lines[0], "cause=pr-content-change") {
		t.Fatalf("a PR content change was not logged as pr-content-change: %q", lines)
	}
}

func TestSync_Anchor_DegradedReadUnknownMergeabilityHoldsLedgerEpisode(t *testing.T) {
	s, buf := newLoggedSyncer(t)
	recordFile := withFactory(t)
	interp := interpFor("mine", nil)
	ctx := context.Background()

	// The anchor is created during a conflict episode.
	if err := s.Sync(ctx, fixtureRepo, fixtureEntity, gather.ChangeAdded,
		conflictFacts("CONFLICTING", "DIRTY", "", nil, ""), interp); err != nil {
		t.Fatalf("Sync 1: %v", err)
	}
	anchor, _, _ := s.store.GetLedger(fixtureRepo, "pr", fixtureEntity, KindAnchor)

	// Degraded work-beads read (no anchor entity in the facts) plus an UNKNOWN
	// mergeability read: nothing is known to have changed, so no write.
	buf.Reset()
	before := len(readCallRecords(t, recordFile))
	if err := s.Sync(ctx, fixtureRepo, fixtureEntity, gather.ChangeChanged,
		conflictFacts("UNKNOWN", "UNKNOWN", "", nil, ""), interp); err != nil {
		t.Fatalf("Sync degraded UNKNOWN: %v", err)
	}
	if calls := anchorCalls(t, recordFile, anchor.BeadID, before); len(calls) != 0 {
		t.Fatalf("a degraded read with UNKNOWN mergeability wrote the anchor: %+v", calls)
	}
	if lines := logLines(buf, "anchor write"); len(lines) != 0 {
		t.Fatalf("unexpected anchor write log: %q", lines)
	}

	// A DEFINITE clean read still ends the episode, degraded or not: the
	// hold only applies to an UNKNOWN read.
	before = len(readCallRecords(t, recordFile))
	if err := s.Sync(ctx, fixtureRepo, fixtureEntity, gather.ChangeChanged,
		conflictFacts("MERGEABLE", "CLEAN", "", nil, ""), interp); err != nil {
		t.Fatalf("Sync degraded clean: %v", err)
	}
	if calls := anchorCalls(t, recordFile, anchor.BeadID, before); len(calls) != 1 {
		t.Fatalf("a definite clean read after a conflict must write once, got %+v", calls)
	}
}
