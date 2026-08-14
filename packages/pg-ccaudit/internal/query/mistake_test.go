package query

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/pg-ccaudit/internal/ingest"
	"github.com/phillipgreenii/pg-ccaudit/internal/store"
)

// The Tier 1 fixture corpus is SEPARATE from the error-census one
// (internal/ingest/testdata/mistakes vs .../corpus) and that separation is
// deliberate: adding the mistake scenarios to the existing corpus would change
// every hand-computed answer in TestCannedQueriesAgainstFixtureCorpus, so a
// mistake-query change could only be made by re-deriving assertions it has nothing
// to do with.
//
// Layout of internal/ingest/testdata/mistakes, by LINE ORDINAL (seq is 0-based, so
// seq N is line N+1). Every expected value below is derived from this table by
// hand; nothing is copied from a run.
//
// projM/sess-m.jsonl — session S-M, main loop, 2026-08-01:
//
//	 0 Write  /w/a.txt v1                    16 result ok
//	 1 result ok                             17 Bash   rm /w/tmp.txt      <- write-then-delete of 15
//	 2 Edit   /w/a.txt v1->v2                18 result ok
//	 3 result ok                             19 INTERRUPTION sentinel
//	 4 Edit   /w/a.txt v2->v1  <- reverses 2 20 TYPED  "scratch files go in…"
//	 5 result ok                             21 text   "You're right — …"  <- ack-phrase
//	 6 TYPED  "no, that is not what I asked" 22 Edit   /w/a.txt v1->v3
//	 7 text   "Correction: I reverted…"      23 result ok
//	 8 Bash   git checkout -- /w/a.txt       24 Edit   /w/a.txt v3->v4
//	 9 result ok                             25 result ok
//	10 Bash   rg 'foo bar' /w                26 Bash   jq . x.json
//	11 result ERROR rg: unrecognized         27 result ERROR user rejected
//	12 Bash   rg "foo bar" /w  <- retry of 10 28 QUEUED "also update the README…"
//	13 result ok                             29 user   promptSource=system
//	14 text   "Correction: the quoting…"     30 user   promptSource=sdk
//	15 Write  /w/tmp.txt                     31 system hookErrors ["ceta: denied"]
//
// projM/sess-s.jsonl — session S-S, SIDECHAIN, 2026-08-02:
//
//	0 user   subagent brief (prose, no promptSource)
//	1 Bash   git reset --hard HEAD
//	2 result ok
//	3 text   "Correction: that reset discarded…"
//	4 Read   /w/b.txt
//	5 result ERROR permission denied
func buildMistakeIndex(t *testing.T) (string, *sql.DB) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "projects")
	src := filepath.Join("..", "ingest", "testdata", "mistakes")
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(root, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatalf("copy mistake fixture: %v", err)
	}

	dbPath := filepath.Join(base, "transcripts.db")
	w, err := store.Open(dbPath, false)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if _, err := ingest.Run(context.Background(), w, ingest.Options{
		Root:       root,
		FinalAfter: ingest.FinalAfterImmediate,
		Progress:   io.Discard,
	}); err != nil {
		t.Fatalf("ingest.Run: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	db, err := store.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("store.OpenReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return root, db
}

// TestTier1QueriesAgainstFixtureCorpus is criterion 2: every Tier 1 structural
// query, asserted against HAND-COMPUTED answers over a committed fixture.
//
// "Returns rows" is not the bar. A candidate query that groups the wrong thing, or
// pairs a turn with the wrong preceding action, returns cleanly and hands Tier 2 a
// set that cannot be right — and Tier 2 costs money per candidate, so a silent
// recall or precision defect here is paid for twice.
func TestTier1QueriesAgainstFixtureCorpus(t *testing.T) {
	root, db := buildMistakeIndex(t)

	t.Run("human-turns", func(t *testing.T) {
		// S-M user records: 12 tool results (seq 1,3,5,9,11,13,16,18,23,25,27,33),
		// 2 typed (6,20), 1 queued (28), 1 system (29), 1 sdk (30), 1 interruption
		// sentinel (19) = 18. S-S: 1 brief (0) + 2 tool results (2,5) = 3. Total 21.
		// unlabelled_prose = the sentinel + the brief = 2.
		// no_prose_records = 12 + 2 = 14.  2+1+1+1+2+14 = 21.
		// inflation_factor = 21 / (2 typed + 1 queued) = 7.0.
		_, rows := runNamed(t, db, root, "human-turns", nil, "", "")
		assertRows(t, "human-turns", rows, [][]string{
			{"2", "1", "1", "1", "2", "14", "21", "1", "0", "7"},
		})
	})

	t.Run("human-turns excludes injected and tool-result records", func(t *testing.T) {
		// The whole point of criterion 1: reading every type=='user' record as a human
		// turn inflates the count by the factor this query reports. 20 / 3 = 6.7 on the
		// fixture; 74.9 on the real corpus.
		_, rows := runNamed(t, db, root, "human-turns", nil, "", "")
		if rows[0][0] != "2" || rows[0][1] != "1" {
			t.Fatalf("typed/queued = %s/%s, want 2/1", rows[0][0], rows[0][1])
		}
		if rows[0][6] == rows[0][0] {
			t.Fatal("all_user_records equals typed — the query is not separating turns from records")
		}
	})

	t.Run("typed-turn-candidates", func(t *testing.T) {
		// One row per human turn, never per parallel sibling call.
		// seq 6  typed : nearest preceding tool line is 4 (Edit), gap 2, 0 errors.
		// seq 20 typed : nearest is 17 (Bash rm),  gap 3, 0 errors.
		// seq 28 queued: nearest is 26 (Bash jq),  gap 2, 1 error -> signature.
		// S-S has no typed or queued turn, so it contributes nothing.
		cols, rows := runNamed(t, db, root, "typed-turn-candidates", nil, "", "")
		if len(rows) != 3 {
			t.Fatalf("typed-turn-candidates returned %d rows, want 3 (one per human turn)", len(rows))
		}
		idx := colIndex(t, cols)
		want := []struct {
			seq, prevSeq, gap, errs, tools string
		}{
			{"6", "4", "2", "0", "Edit"},
			{"20", "17", "3", "0", "Bash"},
			{"28", "26", "2", "1", "Bash"},
		}
		for i, w := range want {
			got := rows[i]
			if got[idx["turn_seq"]] != w.seq || got[idx["prev_tool_seq"]] != w.prevSeq ||
				got[idx["seq_gap"]] != w.gap || got[idx["prev_errors"]] != w.errs ||
				got[idx["prev_tool_names"]] != w.tools {
				t.Errorf("row %d: turn_seq=%s prev=%s gap=%s errors=%s tools=%s, want %v",
					i, got[idx["turn_seq"]], got[idx["prev_tool_seq"]], got[idx["seq_gap"]],
					got[idx["prev_errors"]], got[idx["prev_tool_names"]], w)
			}
		}
		if rows[2][idx["prev_signature"]] == "" {
			t.Error("the queued turn follows a failed call; prev_signature must carry that failure's signature")
		}
	})

	t.Run("interruptions", func(t *testing.T) {
		// Exactly the sentinel at S-M seq 19, paired with the Bash rm at 17.
		cols, rows := runNamed(t, db, root, "interruptions", nil, "", "")
		if len(rows) != 1 {
			t.Fatalf("interruptions returned %d rows, want 1", len(rows))
		}
		idx := colIndex(t, cols)
		if rows[0][idx["seq"]] != "19" || rows[0][idx["prev_tool_seq"]] != "17" ||
			rows[0][idx["prev_tool_names"]] != "Bash" {
			t.Errorf("interruption row = %v", rows[0])
		}
	})

	t.Run("denied-tool-calls", func(t *testing.T) {
		// S-M seq 27: the user rejected the Bash jq call.
		// S-S seq  5: the permission layer denied a Read.
		// The KIND distinction is what routes them differently at Tier 3.
		cols, rows := runNamed(t, db, root, "denied-tool-calls", nil, "", "")
		if len(rows) != 2 {
			t.Fatalf("denied-tool-calls returned %d rows, want 2", len(rows))
		}
		idx := colIndex(t, cols)
		if rows[0][idx["kind"]] != "user-rejected" || rows[0][idx["tool_name"]] != "Bash" ||
			rows[0][idx["lead_cmd"]] != "jq" {
			t.Errorf("row 0 = %v, want user-rejected Bash(jq)", rows[0])
		}
		if rows[1][idx["kind"]] != "permission-denied" || rows[1][idx["tool_name"]] != "Read" ||
			rows[1][idx["is_sidechain"]] != "1" {
			t.Errorf("row 1 = %v, want permission-denied Read in a sidechain", rows[1])
		}
	})

	t.Run("undo-signatures", func(t *testing.T) {
		// Four rows, ordered by (path, seq, kind):
		//   sess-m 4  edit-reversal     (seq 4 restores seq 2's old_string "v1")
		//   sess-m 8  git-undo          (git checkout -- /w/a.txt)
		//   sess-m 17 write-then-delete (rm of the file Written at 15)
		//   sess-s 1  git-undo          (git reset --hard HEAD, SIDECHAIN)
		// Only ONE edit-reversal exists: seq 22 writes v3 and seq 24 writes v4, and no
		// later Edit restores either, so the naive "any two edits to one file" reading
		// would over-report here and does not.
		cols, rows := runNamed(t, db, root, "undo-signatures", nil, "", "")
		if len(rows) != 4 {
			t.Fatalf("undo-signatures returned %d rows, want 4:\n%v", len(rows), rows)
		}
		idx := colIndex(t, cols)
		want := []struct{ kind, seq, undone, sidechain string }{
			{"edit-reversal", "4", "2", "0"},
			{"git-undo", "8", "", "0"},
			{"write-then-delete", "17", "15", "0"},
			{"git-undo", "1", "", "1"},
		}
		for i, w := range want {
			got := rows[i]
			if got[idx["kind"]] != w.kind || got[idx["seq"]] != w.seq ||
				got[idx["undone_seq"]] != w.undone || got[idx["is_sidechain"]] != w.sidechain {
				t.Errorf("row %d = kind=%s seq=%s undone=%s sidechain=%s, want %v",
					i, got[idx["kind"]], got[idx["seq"]], got[idx["undone_seq"]], got[idx["is_sidechain"]], w)
			}
		}
	})

	t.Run("undo-signatures ignores a git verb that is only mentioned", func(t *testing.T) {
		// sess-m seq 32 runs
		//   sqlite3 audit.db "SELECT … WHERE cmd LIKE '%git reset%' OR cmd LIKE '%git revert%'"
		// which CONTAINS two undo verbs and undoes nothing. Without the boundary guard
		// it is a git-undo row; with it, it is not. Measured on the real corpus the
		// guard removed 58 of 116 git-undo rows, all of this shape — sqlite and python
		// heredocs whose text quoted an undo command — so the negative case is the half
		// of this query's precision that matters.
		cols, rows := runNamed(t, db, root, "undo-signatures", nil, "", "")
		idx := colIndex(t, cols)
		for _, r := range rows {
			if r[idx["kind"]] == "git-undo" && r[idx["seq"]] == "32" {
				t.Fatalf("seq 32 only MENTIONS git reset inside a SQL string; it must not be a git-undo: %v", r)
			}
		}
		if len(rows) != 4 {
			t.Fatalf("undo rows = %d, want the same 4 as before seq 32 existed", len(rows))
		}
	})

	t.Run("file-churn at the default N", func(t *testing.T) {
		// /w/a.txt is touched 5 times in S-M: Write(0), Edit(2), Edit(4), Edit(22),
		// Edit(24). /w/tmp.txt is touched once. At N=5 exactly one group qualifies.
		cols, rows := runNamed(t, db, root, "file-churn", nil, "", "")
		if len(rows) != 1 {
			t.Fatalf("file-churn returned %d rows, want 1:\n%v", len(rows), rows)
		}
		idx := colIndex(t, cols)
		if rows[0][idx["file_path"]] != "/w/a.txt" || rows[0][idx["edits"]] != "5" ||
			rows[0][idx["writes"]] != "1" || rows[0][idx["first_seq"]] != "0" ||
			rows[0][idx["last_seq"]] != "24" {
			t.Errorf("churn row = %v", rows[0])
		}
	})

	t.Run("file-churn N is a real threshold", func(t *testing.T) {
		// N=6 must exclude the 5-edit group, and N=1 must include the single Write to
		// /w/tmp.txt. If N were ignored both runs would return the same rows, which is
		// exactly the kind of silently-inert parameter this asserts against.
		_, six := runNamed(t, db, root, "file-churn", []string{"6"}, "", "")
		if len(six) != 0 {
			t.Errorf("file-churn n=6 returned %d rows, want 0", len(six))
		}
		_, one := runNamed(t, db, root, "file-churn", []string{"1"}, "", "")
		if len(one) != 2 {
			t.Errorf("file-churn n=1 returned %d rows, want 2 (/w/a.txt and /w/tmp.txt)", len(one))
		}
	})

	t.Run("escaping-retries", func(t *testing.T) {
		// seq 10 `rg 'foo bar' /w` and seq 12 `rg "foo bar" /w` are byte-different and
		// identical once spaces and quotes are removed. The first errored, the retry did
		// not — which is the whole shape: nothing the shell cares about changed.
		cols, rows := runNamed(t, db, root, "escaping-retries", nil, "", "")
		if len(rows) != 1 {
			t.Fatalf("escaping-retries returned %d rows, want 1:\n%v", len(rows), rows)
		}
		idx := colIndex(t, cols)
		if rows[0][idx["first_seq"]] != "10" || rows[0][idx["retry_seq"]] != "12" ||
			rows[0][idx["seq_gap"]] != "2" || rows[0][idx["first_is_error"]] != "1" ||
			rows[0][idx["retry_is_error"]] != "0" {
			t.Errorf("escaping-retry row = %v", rows[0])
		}
	})

	t.Run("ack-markers with structural provenance", func(t *testing.T) {
		// Four acknowledgments, and provenance is derived from the promptSource of the
		// nearest preceding USER record — never from a second marker phrase:
		//   sess-m 7  Correction:   preceded by the typed turn at 6  -> user-caught
		//   sess-m 14 Correction:   preceded by a tool result at 13  -> self-caught
		//   sess-m 21 ack-phrase    preceded by the typed turn at 20 -> user-caught
		//   sess-s 3  Correction:   preceded by a tool result at 2   -> self-caught
		cols, rows := runNamed(t, db, root, "ack-markers", nil, "", "")
		if len(rows) != 4 {
			t.Fatalf("ack-markers returned %d rows, want 4:\n%v", len(rows), rows)
		}
		idx := colIndex(t, cols)
		want := []struct{ kind, provenance, seq string }{
			{"correction-marker", "user-caught", "7"},
			{"correction-marker", "self-caught", "14"},
			{"ack-phrase", "user-caught", "21"},
			{"correction-marker", "self-caught", "3"},
		}
		for i, w := range want {
			got := rows[i]
			if got[idx["kind"]] != w.kind || got[idx["provenance"]] != w.provenance ||
				got[idx["seq"]] != w.seq {
				t.Errorf("row %d = kind=%s provenance=%s seq=%s, want %v",
					i, got[idx["kind"]], got[idx["provenance"]], got[idx["seq"]], w)
			}
		}
	})

	t.Run("hook-rejections still reads the structured payload", func(t *testing.T) {
		// The fixture DOES carry a non-empty hookErrors payload (sess-m seq 31), so this
		// asserts the detector works — which matters precisely because it returns ZERO
		// rows on the real corpus, where Claude Code writes only `[]`. Without a fixture
		// that exercises it, "0 rows" would be indistinguishable from a broken query.
		cols, rows := runNamed(t, db, root, "hook-rejections", nil, "", "")
		if len(rows) != 1 {
			t.Fatalf("hook-rejections returned %d rows, want 1", len(rows))
		}
		idx := colIndex(t, cols)
		if rows[0][idx["events"]] != "1" || rows[0][idx["hook_invocations"]] != "2" {
			t.Errorf("hook-rejection row = %v", rows[0])
		}
	})

	t.Run("the window is honoured", func(t *testing.T) {
		// S-M is 2026-08-01 and S-S is 2026-08-02, so a window that excludes the
		// sidechain day must drop its git-undo row.
		_, all := runNamed(t, db, root, "undo-signatures", nil, "", "")
		_, day1 := runNamed(t, db, root, "undo-signatures", nil, "2026-08-01", "2026-08-02")
		if len(all) != 4 || len(day1) != 3 {
			t.Errorf("undo rows all=%d day1=%d, want 4 and 3", len(all), len(day1))
		}
	})

	t.Run("concentration-by-signature", func(t *testing.T) {
		// Three distinct error signatures in the fixture, each once, so every
		// worst_session is 1 — the shape that says "not a runaway".
		_, rows := runNamed(t, db, root, "concentration-by-signature", nil, "", "")
		if len(rows) != 3 {
			t.Fatalf("concentration-by-signature returned %d rows, want 3:\n%v", len(rows), rows)
		}
		for _, r := range rows {
			if r[1] != "1" || r[2] != "1" || r[3] != "1" {
				t.Errorf("row %v: want total/sessions/worst all 1", r)
			}
		}
	})
}

func colIndex(t *testing.T, cols []string) map[string]int {
	t.Helper()
	m := make(map[string]int, len(cols))
	for i, c := range cols {
		m[c] = i
	}
	return m
}

// TestUserTextAppliesTheT3aRule asserts the capture rule, because it is the one
// place a well-meaning change would quietly turn a 0.4 MB table into a 35 MB one
// (or, worse, drop the human turns the whole census rests on).
func TestUserTextAppliesTheT3aRule(t *testing.T) {
	_, db := buildMistakeIndex(t)

	type rowT struct {
		seq         int64
		textLen     int64
		text        sql.NullString
		interrupted int64
		promptSrc   sql.NullString
	}
	rows, err := db.Query(`
		SELECT u.seq, u.text_len, u.text, u.interrupted, e.prompt_source
		FROM user_text u JOIN events e ON e.path = u.path AND e.seq = u.seq
		WHERE u.path LIKE '%sess-m.jsonl' ORDER BY u.seq`)
	if err != nil {
		t.Fatalf("query user_text: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var got []rowT
	for rows.Next() {
		var r rowT
		if err := rows.Scan(&r.seq, &r.textLen, &r.text, &r.interrupted, &r.promptSrc); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	// Six prose records in sess-m: the sentinel (19), two typed (6, 20), one queued
	// (28), one system (29) and one sdk (30). The eleven tool-result records
	// contribute NO row at all — they are not turns.
	if len(got) != 6 {
		t.Fatalf("sess-m produced %d user_text rows, want 6: %+v", len(got), got)
	}
	for _, r := range got {
		if r.textLen <= 0 {
			t.Errorf("seq %d: text_len must ALWAYS be populated so an unstored record stays countable", r.seq)
		}
		src := r.promptSrc.String
		human := src == "typed" || src == "queued"
		switch {
		case human || r.interrupted == 1:
			if !r.text.Valid || r.text.String == "" {
				t.Errorf("seq %d (prompt_source=%q interrupted=%d): text MUST be stored — the census reads it",
					r.seq, src, r.interrupted)
			}
		default:
			if r.text.Valid {
				t.Errorf("seq %d (prompt_source=%q): text MUST NOT be stored — harness injections scale to ~35 MB corpus-wide",
					r.seq, src)
			}
		}
	}
}
