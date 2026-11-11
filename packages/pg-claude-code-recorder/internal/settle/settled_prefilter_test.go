package settle

import (
	"context"
	"fmt"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// settledFixture settles n sessions of one file each; every third session
// changes after its settle, and every fifth is live.
func settledFixture(e *csEnv, n int) (groups map[string]*conv, live map[string]bool) {
	groups, live = map[string]*conv{}, map[string]bool{}
	for i := 0; i < n; i++ {
		session := fmt.Sprintf("s%03d", i)
		f := e.appendFile(1, 10)
		e.settle(session, mf(f, 1, 10))
		if err := e.st.Commit(&store.Rec{T: store.TStage, At: 30, Session: session, State: store.StageSettled}); err != nil {
			e.t.Fatal(err)
		}
		if i%3 == 0 {
			f.LastSize = 99
			// A generation advance is what "changed since settle" means for an append file.
			f.CurGen = 2
		}
		groups[session] = &conv{session: session, files: []store.File{f}}
		if i%5 == 0 {
			live[session] = true
		}
	}
	return groups, live
}

func TestSettledPrefilterMatchesPerStatementEvaluation(t *testing.T) {
	e := newCSEnv(t)
	groups, live := settledFixture(e, 30)
	before, unchanged := e.s.settledPrefilter(context.Background(), groups, live)
	stages, _ := e.st.Stages()
	if len(before) != len(stages) {
		t.Fatalf("before has %d stages, store %d", len(before), len(stages))
	}
	for session, c := range groups {
		// What evaluate would conclude for a settled row without a live process.
		want := !live[session] && !e.s.changedSinceSettle(c)
		if unchanged[session] != want {
			t.Errorf("%s: prefilter %v, per-statement %v", session, unchanged[session], want)
		}
	}
	if len(unchanged) == 0 || len(unchanged) == len(groups) {
		t.Fatalf("fixture is not mixed: %d of %d unchanged", len(unchanged), len(groups))
	}
}

func TestSettledPrefilterUsesOneReadTransaction(t *testing.T) {
	e := newCSEnv(t)
	groups, live := settledFixture(e, 40)
	e.cnt.Reset()
	for session, c := range groups {
		if !live[session] {
			e.s.changedSinceSettle(c)
		}
	}
	e.st.Stages() //nolint:errcheck // counted only
	perStatementTx, perStatementQ := e.cnt.Transactions(), e.cnt.Queries()

	e.cnt.Reset()
	e.s.settledPrefilter(context.Background(), groups, live)
	batchedTx, batchedQ := e.cnt.Transactions(), e.cnt.Queries()
	t.Logf("settle pass over %d conversations: per statement %d transactions for %d statements; prefilter %d transactions for %d statements",
		len(groups), perStatementTx, perStatementQ, batchedTx, batchedQ)
	if batchedTx != 1 {
		t.Fatalf("prefilter took %d transactions, want 1", batchedTx)
	}
	if perStatementTx < 50 || batchedQ > perStatementQ {
		t.Fatalf("per-statement %d tx/%d q, batched %d q", perStatementTx, perStatementQ, batchedQ)
	}
}

// The prefilter's statement count does not grow with the number of
// Conversations or files: Stages, the settled rows and the copy offsets are
// each one statement.
func TestSettledPrefilterStatementCountIsConstant(t *testing.T) {
	count := func(n int) int64 {
		e := newCSEnv(t)
		groups, live := settledFixture(e, n)
		e.cnt.Reset()
		e.s.settledPrefilter(context.Background(), groups, live)
		return e.cnt.Queries()
	}
	small, large := count(6), count(18)
	t.Logf("prefilter statements: %d conversations %d, %d conversations %d", 6, small, 18, large)
	if small != large || large > 4 {
		t.Fatalf("statements grow with the data: %d for 6 conversations, %d for 18", small, large)
	}
}
