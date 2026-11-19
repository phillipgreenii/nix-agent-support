package parser_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

const rebootDupSession = "example-session-dup"

func rebootDupContent() string {
	return `{"type":"user","uuid":"u1","sessionId":"` + rebootDupSession + `","timestamp":"2026-10-29T20:00:00Z","message":{"role":"user","content":"hello one"}}` + "\n" +
		`{"type":"user","uuid":"u2","sessionId":"` + rebootDupSession + `","timestamp":"2026-10-29T20:00:01Z","message":{"role":"user","content":"hello two"}}` + "\n" +
		blobLine(rebootDupSession, "h-dup-1", "blob text")
}

// rebootDupKit builds the state a reboot left behind: two append rows with the same
// inode, birth time and path on different devices, each parsed. The older row
// (a) keeps the one-per-hash blob record, the newer row (b) holds the path
// open and lacks it.
func rebootDupKit(t *testing.T) (k *testkit.Kit, a, b int64) {
	t.Helper()
	k = testkit.New(t)
	a = k.Add("example-proj", rebootDupSession+".jsonl", rebootDupContent())
	k.Parse()
	b = k.Add("example-proj", rebootDupSession+".jsonl", rebootDupContent())
	k.Parse()
	if err := k.Store.Commit(
		&store.Rec{T: store.TPathClose, At: 1, File: a, Path: "/claude/projects/example-proj/" + rebootDupSession + ".jsonl"},
		&store.Rec{T: store.TIdent, At: 2, File: b, Dev: 2, Ino: uint64(a), Birth: 0},
	); err != nil {
		t.Fatal(err)
	}
	return k, a, b
}

func countRecords(t *testing.T, k *testkit.Kit) int {
	t.Helper()
	a, err := k.Engine.Query(context.Background(), mustReq(t, `{"op":"query","from":"records","aggregate":[{"fn":"count","as":"n"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(a.Rows)
	var rows [][]int
	_ = json.Unmarshal(b, &rows)
	return rows[0][0]
}

func mustReq(t *testing.T, s string) query.Request {
	t.Helper()
	var r query.Request
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// Pins the evidence the repair rests on: the query layer joins records to files
// on the current generation only, so two rows holding the same file return
// every record twice.
func TestDuplicateAppendRowsDoubleCountQueries(t *testing.T) {
	k, a, b := rebootDupKit(t)
	// b holds every record of a except the one-per-hash blob record.
	nA := k.Count(`SELECT COUNT(*) FROM records WHERE file_id=?`, a)
	nB := k.Count(`SELECT COUNT(*) FROM records WHERE file_id=?`, b)
	if nB != nA-1 || nA < 3 {
		t.Fatalf("fixture shape: a has %d records, b has %d", nA, nB)
	}
	if got := countRecords(t, k); got != nA+nB {
		t.Fatalf("records returned with duplicate rows: %d, want %d (each record once per row)", got, nA+nB)
	}
}

func TestRetireDuplicateAppendRows(t *testing.T) {
	k, a, b := rebootDupKit(t)
	before := k.Count(`SELECT COUNT(DISTINCT off||'/'||item) FROM records WHERE file_id IN (?,?)`, a, b)
	nA := k.Count(`SELECT COUNT(*) FROM records WHERE file_id=?`, a)
	total := countRecords(t, k)

	dry, err := k.Parser.RetireDuplicateAppendRows(true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Groups != 1 || dry.Retired != 1 || dry.Repointed != 1 || dry.Cleared != int64(nA-1) {
		t.Fatalf("dry run report: %+v", dry)
	}
	if got := countRecords(t, k); got != total {
		t.Fatalf("dry run changed records: %d", got)
	}

	rep, err := k.Parser.RetireDuplicateAppendRows(false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Retired != 1 || rep.Repointed != 1 || rep.Cleared != int64(nA-1) || len(rep.Skipped) != 0 {
		t.Fatalf("report: %+v", rep)
	}
	// Every distinct record survives, once, on the keeper.
	if got := countRecords(t, k); got != before {
		t.Fatalf("records after repair: %d, want %d", got, before)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE file_id=?`, b); n != before {
		t.Fatalf("keeper holds %d records, want %d", n, before)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE file_id=?`, a); n != 0 {
		t.Fatalf("loser still holds %d records", n)
	}
	if st := k.Count(`SELECT COUNT(*) FROM files WHERE id=? AND state='deleted'`, a); st != 1 {
		t.Fatal("loser was not tombstoned")
	}
	// The post-repair query of the acceptance criteria.
	if n := k.Count(`SELECT COUNT(*) FROM (SELECT f.ino,f.birth_ns,p.path FROM files f JOIN paths p ON p.file_id=f.id
WHERE f.mode='append' AND f.state!='deleted' GROUP BY 1,2,3 HAVING COUNT(*)>1)`); n != 0 {
		t.Fatalf("%d duplicate live groups remain", n)
	}
	if n := k.Count(`SELECT record_count FROM session_stats`); n != before {
		t.Fatalf("session_stats.record_count %d, want %d", n, before)
	}

	// A second pass is a no-op.
	again, err := k.Parser.RetireDuplicateAppendRows(false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Retired != 0 || again.Repointed != 0 || again.Cleared != 0 || again.AlreadyRetired != 1 {
		t.Fatalf("second pass: %+v", again)
	}
	if got := countRecords(t, k); got != before {
		t.Fatalf("second pass changed records: %d", got)
	}
}

// The keeper (the row holding the path open) has not parsed all of the loser's
// copy: nothing is retired and the loser is reported.
func TestRetireDuplicateAppendRowsSkipsShorterKeeper(t *testing.T) {
	k, a, _ := rebootDupKit(t)
	k.Append(a, `{"type":"user","uuid":"u3","sessionId":"`+rebootDupSession+`","timestamp":"2026-10-29T20:00:02Z","message":{"role":"user","content":"newer"}}`+"\n")
	k.Parse()
	total := countRecords(t, k)
	rep, err := k.Parser.RetireDuplicateAppendRows(false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Retired != 0 || len(rep.Skipped) != 1 {
		t.Fatalf("report: %+v", rep)
	}
	if got := countRecords(t, k); got != total {
		t.Fatalf("records changed: %d, want %d", got, total)
	}
}
