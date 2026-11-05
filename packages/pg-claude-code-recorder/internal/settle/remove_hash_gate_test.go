package settle

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// recordDump reads every stored record of a file as one comparable string.
func (e *env) recordDump(file int64) string {
	e.t.Helper()
	rows, err := e.st.Read().Query(`SELECT gen, off, item, len, type, source, COALESCE(session,''), time_ns
FROM records WHERE file_id=? ORDER BY gen, off, item`, file)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var b strings.Builder
	for rows.Next() {
		var gen, ln int
		var off, ns int64
		var item int
		var typ, src, sessID string
		if err := rows.Scan(&gen, &off, &item, &ln, &typ, &src, &sessID, &ns); err != nil {
			e.t.Fatal(err)
		}
		b.WriteString(strings.Join([]string{
			strconv.Itoa(gen), strconv.FormatInt(off, 10), strconv.Itoa(item), strconv.Itoa(ln), typ, src, sessID, strconv.FormatInt(ns, 10),
		}, "|") + "\n")
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return b.String()
}

// A Conversation whose transcript is mostly bad lines is removable once the
// original and the archived copy match by full hash: removal never judges the
// parse. The archive then reparses to the same records (a parser version bump
// only re-reads the archived bytes).
func TestRemovalIgnoresTheBadLineRateAndTheArchiveReparsesIdentically(t *testing.T) {
	e := newRemoveEnv(t, false)
	e.write(e.transcript(), line(1)+"{broken\n{broken\n{broken\n{broken\n{broken\n", e.clk.Now().Add(-removeAge-time.Hour), false)
	e.ingest(e.transcript())
	e.parseAll()
	e.tickSettleOnly()
	if e.stage() != StageSettled {
		t.Fatalf("stage %s", e.stage())
	}
	file := e.fileID()
	before := e.recordDump(file)
	if before == "" {
		t.Fatal("the fixture stored no records")
	}
	if rep := e.remove(sess); len(rep.Removed) != 1 {
		t.Fatalf("a matching hash with many bad lines was not removed: %+v", rep)
	}
	if e.exists(e.transcript()) {
		t.Fatal("the original is still there")
	}
	// A parser version bump re-reads the archived bytes; the original is gone.
	if err := e.prs.ReparseGen(file, 1); err != nil {
		t.Fatal(err)
	}
	if after := e.recordDump(file); after != before {
		t.Fatalf("the archive reparsed differently\nbefore:\n%s\nafter:\n%s", before, after)
	}
	var bad int
	if err := e.st.Read().QueryRow(`SELECT COUNT(*) FROM bad_lines WHERE file_id=?`, file).Scan(&bad); err != nil || bad != 5 {
		t.Fatalf("bad lines after the reparse = %d (%v), want 5", bad, err)
	}
}

// A hash mismatch between the original and the copy still stops removal.
func TestRemovalStillRefusesAHashMismatch(t *testing.T) {
	e := newRemoveEnv(t, false)
	content := e.oldSettled(100, removeAge+time.Hour)
	// Same length, different bytes, same mtime: only the full hash can tell.
	changed := strings.Replace(content, "example message 1", "example message X", 1)
	if changed == content || len(changed) != len(content) {
		t.Fatal("the fixture did not change")
	}
	e.write(e.transcript(), changed, e.clk.Now().Add(-removeAge-time.Hour), false)
	rep := e.remove(sess)
	if len(rep.Removed) != 0 || !hasReason(e.skipReasons(rep, sess), "full_hash_mismatch") {
		t.Fatalf("%+v", rep)
	}
	if !e.exists(e.transcript()) {
		t.Fatal("the original is gone")
	}
}
