package query_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
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
	rs, err := k.Store.Read().QueryContext(context.Background(), `SELECT file_id, COALESCE(project,''), COALESCE(file_session,''), COALESCE(agent_id,''), last_time_ns FROM file_facts ORDER BY file_id`)
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
			if _, err := tx.ExecContext(context.Background(), q); err != nil {
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
	if !k.Store.FileFactsComplete(t.Context()) {
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
		_, err := tx.ExecContext(context.Background(), `UPDATE file_facts SET last_time_ns=1 WHERE file_id=?`, id)
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
