//go:build integration

package query_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store/countdriver"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

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
	rs, err := k.Store.Read().QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+q, args...)
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

// The newest record time of a file is kept in the parse commit transaction: a
// later append moves it, and a fresh database that parses the same copies again
// (a rebuild) arrives at the same table.
func TestFileFactsMaintainedAndSurviveRebuild(t *testing.T) {
	k := testkit.New(t)
	id := k.Add("example-proj", "example-session-1.jsonl", lines("example-session-1", 1, 5))
	k.AddAgent("example-proj", "example-session-1", "example-agent-1", lines("example-session-1", 2, 5))
	k.Parse()
	var last int64
	if err := k.Store.Read().QueryRowContext(context.Background(), `SELECT last_time_ns FROM file_facts WHERE file_id=?`, id).Scan(&last); err != nil {
		t.Fatal(err)
	}
	k.Append(id, lines("example-session-1", 9, 2))
	k.Parse()
	var after int64
	if err := k.Store.Read().QueryRowContext(context.Background(), `SELECT last_time_ns FROM file_facts WHERE file_id=?`, id).Scan(&after); err != nil {
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
	if !k2.Store.FileFactsComplete(t.Context()) {
		t.Fatal("a database created empty is not marked complete")
	}
}
