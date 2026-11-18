package lifecycle

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
)

// The suspected-crash list must decide a row from its evidence before any
// transcript read: reading the transcript of every ended Conversation made
// conversation.list outlast the client timeout on a large store.
func TestSuspectedListReadsNoTranscriptForRowsItDrops(t *testing.T) {
	c := countdriver.Register("lifecycle-suspected-counting")
	st, err := store.Open(t.TempDir(), store.Options{DriverName: "lifecycle-suspected-counting"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db, err := sql.Open("sqlite", filepath.Join(st.Dir(), store.DBName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &cwdFixture{t: t, st: st, db: db}
	f.loadFixture()
	e := &Engine{St: st, Exists: func(string) bool { return true }}
	ctx := context.Background()

	// Baseline: an unrestricted list reads the transcript of every row, so the
	// counter sees the cwd query (otherwise this test proves nothing).
	c.Reset()
	all, err := e.List(ctx, ConversationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	base := c.Matching("field_names WHERE name='cwd'")
	if len(all) == 0 || base == 0 {
		t.Fatalf("baseline read %d cwd queries for %d rows", base, len(all))
	}
	for _, r := range all {
		if Suspected(r.Evidence) {
			t.Fatalf("the fixture row %s is suspected: nothing would be dropped", r.SessionID)
		}
	}

	c.Reset()
	got, err := e.List(ctx, ConversationFilter{Suspected: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("suspected list returned %d rows, want 0", len(got))
	}
	if n := c.Matching("field_names WHERE name='cwd'") + c.Matching("FROM records WHERE session"); n != 0 {
		t.Fatalf("suspected list ran %d transcript queries for dropped rows (baseline %d)", n, base)
	}
}

// A label query bounded by since must drop the rows that ended before it from
// the model's end time, without reading their transcripts.
func TestListSinceReadsNoTranscriptForRowsEndedEarlier(t *testing.T) {
	c := countdriver.Register("lifecycle-since-counting")
	st, err := store.Open(t.TempDir(), store.Options{DriverName: "lifecycle-since-counting"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db, err := sql.Open("sqlite", filepath.Join(st.Dir(), store.DBName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &cwdFixture{t: t, st: st, db: db}
	f.loadFixture()
	// Both fixture Processes exit, observed, long before the since below.
	for i, pid := range []int64{101, 107} { // the fixture's pid field is 100 + the registry record id
		if err := st.Commit(&store.Rec{T: store.TProc, Kind: "exit", Pid: pid, ProcStart: fmt.Sprint("ps", 100+i), At: int64(60 + i), Reason: store.ExitObserved}); err != nil {
			t.Fatal(err)
		}
	}
	e := &Engine{St: st, Exists: func(string) bool { return true }}
	ctx := context.Background()
	const cwdQ = "field_names WHERE name='cwd'"

	c.Reset()
	all, err := e.List(ctx, ConversationFilter{Ended: boolp(true)})
	if err != nil {
		t.Fatal(err)
	}
	base := c.Matching(cwdQ)
	if len(all) == 0 || base == 0 {
		t.Fatalf("baseline read %d cwd queries for %d ended rows", base, len(all))
	}
	for _, r := range all {
		if r.EndTime == nil {
			t.Fatalf("fixture row %s has no end time from the model: the test proves nothing", r.SessionID)
		}
	}

	c.Reset()
	got, err := e.List(ctx, ConversationFilter{Ended: boolp(true), Since: "2020-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("list returned %d rows ended before since, want 0", len(got))
	}
	if n := c.Matching(cwdQ) + c.Matching("FROM records WHERE session"); n != 0 {
		t.Fatalf("list ran %d transcript queries for rows ended before since (baseline %d)", n, base)
	}
}

func boolp(b bool) *bool { return &b }
