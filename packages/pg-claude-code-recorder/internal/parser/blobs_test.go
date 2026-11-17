package parser_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/parser"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

func blobLine(session, hash, text string) string {
	return `{"type":"api-request-blob","sessionId":"` + session + `","hash":"` + hash +
		`","message":{"role":"user","content":[{"type":"text","text":"` + text + `"},{"type":"text","text":"second block"}]}}` + "\n"
}

// A blob line is one opaque row: its blocks are not expanded, it carries the
// hash, and a hash seen again in the session (same file, a later append, or
// another file) is not stored twice.
func TestBlobLineIsOneRowPerHash(t *testing.T) {
	k := testkit.New(t)
	const s = "example-session-1"
	id := k.Add("example-proj", s+".jsonl",
		blobLine(s, "h1", "alpha")+blobLine(s, "h2", "beta")+blobLine(s, "h1", "alpha")+
			`{"type":"user","uuid":"u1","sessionId":"`+s+`","timestamp":"2026-10-29T20:00:00Z","message":{"role":"user","content":"hi"}}`+"\n")
	k.Parse()
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE type='api-request-blob'`); n != 2 {
		t.Fatalf("%d blob rows, want one per hash (2)", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE item>=0 AND type='text'`); n != 1 {
		t.Fatalf("%d text child rows, want 1 (the user line only): blob blocks were expanded", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records r JOIN fields f ON f.rec_id=r.id JOIN field_names n ON n.id=f.name_id
WHERE r.type='api-request-blob' AND n.name='blob_hash' AND f.s IN ('h1','h2')`); n != 2 {
		t.Fatalf("%d blob rows carry their hash, want 2", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE type='api-request-blob' AND time_ns=0`); n != 2 {
		t.Fatalf("a blob row has a time")
	}
	// The payload stays reachable: the row points at the copied line.
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE type='api-request-blob' AND len>0`); n != 2 {
		t.Fatalf("a blob row lost its pointer")
	}

	// A later append and a second file of the session repeat h1 and add h3.
	k.Append(id, blobLine(s, "h1", "alpha")+blobLine(s, "h3", "gamma"))
	k.Add("example-proj", s+"b.jsonl", blobLine(s, "h2", "beta"))
	k.Parse()
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE type='api-request-blob'`); n != 3 {
		t.Fatalf("%d blob rows after repeats, want 3", n)
	}
	// The same hash in another session is its own row.
	k.Add("example-proj", "other-session.jsonl", blobLine("other-session", "h1", "alpha"))
	k.Parse()
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE type='api-request-blob'`); n != 4 {
		t.Fatalf("%d blob rows with a second session, want 4", n)
	}

	// A re-parse of the file replaces its rows and keeps one per hash.
	if err := k.Parser.ReparseGen(id, 1); err != nil {
		t.Fatal(err)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE type='api-request-blob'`); n != 4 {
		t.Fatalf("%d blob rows after re-parse, want 4", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM blob_hashes`); n != 4 {
		t.Fatalf("%d blob_hashes rows, want 4", n)
	}
}

// Children stored before a blob became one row are removed in bounded slices
// against a real store; the blob rows and other records stay.
func TestPruneBlobChildrenRunsInBoundedSlices(t *testing.T) {
	k := testkit.New(t)
	const s = "example-session-1"
	k.Add("example-proj", s+".jsonl", blobLine(s, "h1", "alpha")+blobLine(s, "h2", "beta")+
		`{"type":"user","uuid":"u1","sessionId":"`+s+`","timestamp":"2026-10-29T20:00:00Z","message":{"role":"user","content":"hi"}}`+"\n")
	k.Parse()
	const perBlob = 7
	err := k.Store.WithWrite("", func(tx *sql.Tx) error {
		ctx := context.Background()
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO field_names(name) VALUES('user_text')`); err != nil {
			return err
		}
		for i := 0; i < perBlob; i++ {
			if _, err := tx.ExecContext(ctx, `INSERT INTO records(file_id,gen,off,item,len,source,type,parser_version,session,time_ns,session_ref,own_time)
SELECT file_id,gen,off,?1,len,source,'text',2,session,0,session_ref,0 FROM records WHERE type='api-request-blob'`, i); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO fields(rec_id,name_id,s) SELECT id,(SELECT id FROM field_names WHERE name='user_text'),'legacy' FROM records WHERE item>=0 AND type='text' AND parser_version=2`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO texts(rec_id,field,kind) SELECT id,'user_text','text' FROM records WHERE item>=0 AND parser_version=2`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO fts_word(rowid,body) SELECT id,'legacy' FROM texts WHERE rec_id IN (SELECT id FROM records WHERE parser_version=2)`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	legacy := func() int { return k.Count(`SELECT COUNT(*) FROM records WHERE parser_version=2`) }
	if got := legacy(); got != 2*perBlob {
		t.Fatalf("setup: %d legacy rows, want %d", got, 2*perBlob)
	}
	userBefore := k.Count(`SELECT COUNT(*) FROM records WHERE type='user' OR (type='text' AND parser_version=3)`)

	const slice = 5
	var calls, total int
	for {
		n, err := k.Parser.PruneBlobChildren(context.Background(), slice)
		if err != nil {
			t.Fatal(err)
		}
		if n > slice {
			t.Fatalf("a slice removed %d rows, bound is %d", n, slice)
		}
		if n == 0 {
			break
		}
		calls++
		total += n
	}
	if total != 2*perBlob || calls != 3 {
		t.Fatalf("removed %d rows in %d slices, want %d in 3", total, calls, 2*perBlob)
	}
	if got := legacy(); got != 0 {
		t.Fatalf("%d legacy rows left", got)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM fields WHERE s='legacy'`,
		`SELECT COUNT(*) FROM texts WHERE field='user_text' AND rec_id NOT IN (SELECT id FROM records)`,
	} {
		if n := k.Count(q); n != 0 {
			t.Fatalf("%s: %d orphans", q, n)
		}
	}
	if n := k.Count(`SELECT COUNT(*) FROM fts_word WHERE fts_word MATCH 'legacy'`); n != 0 {
		t.Fatalf("%d legacy fts rows left", n)
	}
	if n := k.Count(`SELECT COUNT(*) FROM records WHERE type='api-request-blob'`); n != 2 {
		t.Fatalf("%d blob rows after prune, want 2", n)
	}
	if after := k.Count(`SELECT COUNT(*) FROM records WHERE type='user' OR (type='text' AND parser_version=3)`); after != userBefore {
		t.Fatalf("other records changed: %d -> %d", userBefore, after)
	}
	if n := k.Count(`SELECT record_count FROM session_stats`); n != k.Count(`SELECT COUNT(*) FROM records`) {
		t.Fatalf("session_stats record_count %d does not match records", n)
	}
}

func TestBlobTypesAreKnownRecordTypes(t *testing.T) {
	have := map[string]bool{}
	for _, k := range parser.KnownRecordTypes {
		have[k] = true
	}
	for _, want := range strings.Fields("api-request-blob api-request api-request-shape last-prompt mode permission-mode atis-latch ai-title tools-used cost-state agent-name") {
		if !have[want] {
			t.Errorf("%s is not a known record type", want)
		}
	}
}
