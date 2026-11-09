package query_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

// lines builds n transcript lines of one session, minute-spaced from hour.
func lines(session string, hour, n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, `{"type":"user","uuid":"%s-%d-%d","sessionId":%q,"timestamp":"2026-10-29T%02d:%02d:00Z","message":{"role":"user","content":"x"}}`+"\n",
			session, hour, i, session, hour, i%60)
	}
	return sb.String()
}

func inventory(t *testing.T, k *testkit.Kit, req query.InventoryRequest) *query.InventoryAnswer {
	t.Helper()
	a, err := k.Engine.Inventory(context.Background(), req)
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	return a
}

// The inventory reads a row per file, not per record: 300 records in 3 files
// cost about 3 rows through the counting driver, and the plan of its statement
// names no scan of records.
func TestInventoryReadsFilesNotRecords(t *testing.T) {
	c := countdriver.Register("inventory-reads")
	k := testkit.NewWithStoreOptions(t, store.Options{DriverName: "inventory-reads"})
	k.Add("example-proj", "example-session-1.jsonl", lines("example-session-1", 1, 100))
	k.Add("example-proj", "example-session-2.jsonl", lines("example-session-2", 2, 100))
	k.AddAgent("example-proj", "example-session-1", "example-agent-1", lines("example-session-1", 3, 100))
	k.Parse()
	if n := k.Count(`SELECT COUNT(*) FROM records`); n < 300 {
		t.Fatalf("records %d", n)
	}

	c.Reset()
	a := inventory(t, k, query.InventoryRequest{Sources: []string{"claude-transcript"}})
	if len(a.Files) != 3 || a.Degraded || a.More {
		t.Fatalf("answer %+v", a)
	}
	if got := c.RowsRead(); got > 10 {
		t.Fatalf("inventory read %d rows for 300 records in 3 files", got)
	}
	var sub query.InventoryFile
	for _, f := range a.Files {
		if f.AgentID != "" {
			sub = f
		}
	}
	if sub.AgentID != "example-agent-1" || sub.FileSession != "example-session-1" || sub.Project != "example-proj" || sub.Last != "2026-10-29T03:59:00Z" {
		t.Fatalf("subagent file %+v", sub)
	}

	q, args := query.InventorySQL(query.InventoryRequest{Sources: []string{"x"}, Since: "2026-10-29T00:00:00Z", Sessions: []string{"s"}}, 1, 10)
	rs, err := k.Store.Read().Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rs.Close() }()
	for rs.Next() {
		var id, parent, unused int
		var detail string
		if err := rs.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(detail), "records") {
			t.Fatalf("plan scans records: %s", detail)
		}
	}
}

func TestInventoryNarrowing(t *testing.T) {
	k := testkit.New(t)
	k.Add("example-proj", "example-session-1.jsonl", lines("example-session-1", 1, 3))
	k.Add("example-proj", "example-session-2.jsonl", lines("example-session-2", 5, 3))
	k.Parse()
	if a := inventory(t, k, query.InventoryRequest{Since: "2026-10-29T04:00:00Z"}); len(a.Files) != 1 || a.Files[0].FileSession != "example-session-2" {
		t.Fatalf("since: %+v", a.Files)
	}
	if a := inventory(t, k, query.InventoryRequest{Sessions: []string{"example-session-1"}}); len(a.Files) != 1 || a.Files[0].FileSession != "example-session-1" {
		t.Fatalf("sessions: %+v", a.Files)
	}
	if a := inventory(t, k, query.InventoryRequest{Since: "2026-10-29T04:00:00Z", Sessions: []string{"example-session-1"}}); len(a.Files) != 2 {
		t.Fatalf("since or sessions: %+v", a.Files)
	}
	if a := inventory(t, k, query.InventoryRequest{Limit: 1}); len(a.Files) != 1 || !a.More {
		t.Fatalf("limit: %+v", a)
	}
	first := inventory(t, k, query.InventoryRequest{Limit: 1})
	if a := inventory(t, k, query.InventoryRequest{Limit: 1, After: first.Files[0].File}); len(a.Files) != 1 || a.More || a.Files[0].File == first.Files[0].File {
		t.Fatalf("after: %+v", a)
	}
	if a := inventory(t, k, query.InventoryRequest{Sources: []string{"claude-status"}}); len(a.Files) != 0 {
		t.Fatalf("sources: %+v", a.Files)
	}
}

func factsDump(t *testing.T, k *testkit.Kit) string {
	t.Helper()
	rs, err := k.Store.Read().Query(`SELECT file_id, COALESCE(project,''), COALESCE(file_session,''), COALESCE(agent_id,''), last_time_ns FROM file_facts ORDER BY file_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rs.Close() }()
	var sb strings.Builder
	for rs.Next() {
		var id, ns int64
		var p, s, a string
		if err := rs.Scan(&id, &p, &s, &a, &ns); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sb, "%d|%s|%s|%s|%d\n", id, p, s, a, ns)
	}
	return sb.String()
}

// The newest record time of a file is kept in the parse commit transaction: a
// later append moves it, and a fresh database that parses the same copies again
// (a rebuild) arrives at the same table.
func TestFileFactsMaintainedAndSurviveRebuild(t *testing.T) {
	k := testkit.New(t)
	id := k.Add("example-proj", "example-session-1.jsonl", lines("example-session-1", 1, 5))
	k.AddAgent("example-proj", "example-session-1", "example-agent-1", lines("example-session-1", 2, 5))
	k.Parse()
	var last int64
	if err := k.Store.Read().QueryRow(`SELECT last_time_ns FROM file_facts WHERE file_id=?`, id).Scan(&last); err != nil {
		t.Fatal(err)
	}
	k.Append(id, lines("example-session-1", 9, 2))
	k.Parse()
	var after int64
	if err := k.Store.Read().QueryRow(`SELECT last_time_ns FROM file_facts WHERE file_id=?`, id).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after <= last {
		t.Fatalf("append did not move the newest time: %d then %d", last, after)
	}
	want := k.Count(`SELECT MAX(time_ns) FROM records WHERE file_id=` + fmt.Sprint(id))
	if int64(want) != after {
		t.Fatalf("stored %d, recompute %d", after, want)
	}
	before := factsDump(t, k)

	k2 := testkit.New(t)
	k2.Add("example-proj", "example-session-1.jsonl", lines("example-session-1", 1, 5))
	k2.AddAgent("example-proj", "example-session-1", "example-agent-1", lines("example-session-1", 2, 5))
	k2.Append(id, lines("example-session-1", 9, 2))
	k2.Parse()
	if got := factsDump(t, k2); got != before {
		t.Fatalf("rebuilt table differs:\n%s\nvs\n%s", got, before)
	}
	if !k2.Store.FileFactsComplete() {
		t.Fatal("a database created empty is not marked complete")
	}
}

// A database that existed before the table is backfilled in slices and marked
// complete at the end; a restart resumes from the saved cursor.
func TestFileFactsBackfillOfAnOlderDatabase(t *testing.T) {
	k := testkit.New(t)
	k.Add("example-proj", "example-session-1.jsonl", lines("example-session-1", 1, 5))
	k.Add("example-proj", "example-session-2.jsonl", lines("example-session-2", 2, 5))
	k.AddAgent("example-proj", "example-session-2", "example-agent-1", lines("example-session-2", 3, 5))
	k.Parse()
	want := factsDump(t, k)
	if err := k.Store.WithWrite("", func(tx *sql.Tx) error {
		for _, q := range []string{`DELETE FROM file_facts`, `DELETE FROM meta WHERE key='` + store.MetaFileFactsComplete + `'`} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if a := inventory(t, k, query.InventoryRequest{}); len(a.Files) != 0 || !a.Degraded {
		t.Fatalf("an unfilled table must answer degraded: %+v", a)
	}
	ctx := context.Background()
	done, n, err := k.Parser.BackfillFileFacts(ctx, 2)
	if err != nil || done || n != 2 {
		t.Fatalf("first slice: done=%v n=%d err=%v", done, n, err)
	}
	k.Reopen()
	done, n, err = k.Parser.BackfillFileFacts(ctx, 2)
	if err != nil || !done || n != 1 {
		t.Fatalf("second slice: done=%v n=%d err=%v", done, n, err)
	}
	if got := factsDump(t, k); got != want {
		t.Fatalf("backfilled table differs:\n%s\nvs\n%s", got, want)
	}
	if !k.Store.FileFactsComplete() {
		t.Fatal("not marked complete")
	}
}

// A stored value that no longer matches a recompute over records is reported,
// and repaired when asked.
func TestFileFactsDriftIsDetectedAndRepaired(t *testing.T) {
	k := testkit.New(t)
	id := k.Add("example-proj", "example-session-1.jsonl", lines("example-session-1", 1, 5))
	k.Add("example-proj", "example-session-2.jsonl", lines("example-session-2", 2, 5))
	k.Parse()
	ctx := context.Background()
	if d, next, err := k.Parser.CheckFileFacts(ctx, 0, 10, false); err != nil || len(d) != 0 || next != 0 {
		t.Fatalf("clean check: %+v next=%d err=%v", d, next, err)
	}
	if err := k.Store.WithWrite("", func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE file_facts SET last_time_ns=1 WHERE file_id=?`, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	d, _, err := k.Parser.CheckFileFacts(ctx, 0, 10, false)
	if err != nil || len(d) != 1 || d[0].File != id || d[0].Stored != 1 {
		t.Fatalf("drift: %+v err=%v", d, err)
	}
	if d, _, _ = k.Parser.CheckFileFacts(ctx, 0, 10, true); len(d) != 1 {
		t.Fatalf("repair pass: %+v", d)
	}
	if d, _, err = k.Parser.CheckFileFacts(ctx, 0, 10, false); err != nil || len(d) != 0 {
		t.Fatalf("after repair: %+v err=%v", d, err)
	}
}
