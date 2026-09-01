package query

import "testing"

// TestErrorThenNarrationSkipsSystemLines is pg2-z38lk item 4's fix, verified
// against a fixture DELIBERATELY SEPARATE from the other corpora (mirroring
// `mistakes`/`refusals`/`rootcheck`/`effectivecmd` in the sibling test files),
// so this query's own fixture can change without moving any other test's
// hand-computed answer.
//
// The bead's investigation: a 2026-08-31 retro window returned 64/64 sidechain
// rows and 0 main-loop for this query. Ingest itself treats main-loop and
// sidechain narration identically (ingest.go's appendLine records
// assistant_text for every `type=="assistant"` line regardless of
// is_sidechain), so the gap was in the QUERY's exact `a.seq = r.seq + 1` bet:
// Claude Code interleaves harness-injected `system`-type lines (hook-summary
// events) between a tool result and the assistant's next line far more often
// in a main-loop transcript than a sidechain one, breaking the +1 adjacency
// silently on the main-loop side while the sidechain side (whose hook pipeline
// is thinner) kept matching.
//
// Layout of internal/ingest/testdata/narrationgap, by LINE ORDINAL (seq is
// 0-based):
//
//	projN/sess-n.jsonl — session N-MAIN, main loop:
//	 0 Bash false     4 Bash false2    9  Bash false3
//	 1 result ERROR   5 result ERROR   10 result ERROR
//	 2 system (hook)   6 system (hook)  11 system (hook)
//	 3 text "…-v."     7 system (hook)  12 Bash retry3      <- next non-system
//	                   8 text "…time."  13 result ok            line is ANOTHER
//	                                                             tool_use, not
//	                                                             prose: must NOT
//	                                                             match.
//
//	projN/sess-nsub.jsonl — session N-SUB, SIDECHAIN, no system lines at all:
//	0 Bash false4
//	1 result ERROR
//	2 text "Immediate reaction, no system line here."
func TestErrorThenNarrationSkipsSystemLines(t *testing.T) {
	root, db := buildIndexFrom(t, "narrationgap")

	t.Run("one intervening system line is skipped", func(t *testing.T) {
		cols, rows := runNamed(t, db, root, "error-then-narration", nil, "", "")
		idx := colIndex(t, cols)
		if len(rows) != 3 {
			t.Fatalf("error-then-narration returned %d rows, want 3:\n%v", len(rows), rows)
		}
		if rows[0][idx["path"]] != "projN/sess-n.jsonl" || rows[0][idx["error_seq"]] != "1" ||
			rows[0][idx["is_sidechain"]] != "0" || rows[0][idx["narration"]] != "That surprised me; retrying with -v." {
			t.Errorf("row 0 (one system line skipped) = %v", rows[0])
		}
	})

	t.Run("two consecutive intervening system lines are both skipped", func(t *testing.T) {
		_, rows := runNamed(t, db, root, "error-then-narration", nil, "", "")
		cols, _ := runNamed(t, db, root, "error-then-narration", nil, "", "")
		idx := colIndex(t, cols)
		if rows[1][idx["path"]] != "projN/sess-n.jsonl" || rows[1][idx["error_seq"]] != "5" ||
			rows[1][idx["narration"]] != "Two hook lines in a row this time." {
			t.Errorf("row 1 (two system lines skipped) = %v", rows[1])
		}
	})

	t.Run("main-loop matches now, exactly the class the retro found at 0", func(t *testing.T) {
		_, rows := runNamed(t, db, root, "error-then-narration", nil, "", "")
		mainLoop := 0
		for _, r := range rows {
			if r[1] == "0" {
				mainLoop++
			}
		}
		if mainLoop != 2 {
			t.Errorf("main-loop rows = %d, want 2 (the retro's own gap was 0 main-loop rows)", mainLoop)
		}
	})

	t.Run("sidechain with no intervening line still matches, unchanged", func(t *testing.T) {
		cols, rows := runNamed(t, db, root, "error-then-narration", nil, "", "")
		idx := colIndex(t, cols)
		if rows[2][idx["path"]] != "projN/sess-nsub.jsonl" || rows[2][idx["is_sidechain"]] != "1" ||
			rows[2][idx["narration"]] != "Immediate reaction, no system line here." {
			t.Errorf("row 2 (sidechain, unchanged behaviour) = %v", rows[2])
		}
	})

	t.Run("the assistant's real next action being another tool call, not prose, still does not match", func(t *testing.T) {
		// seq 10's error is followed by a system line, then ANOTHER tool_use (seq
		// 12), never assistant prose. The fix must not loosen this into "the next
		// prose the assistant eventually writes" — that would abandon the query's
		// whole point (immediate reaction, not eventual reaction).
		_, rows := runNamed(t, db, root, "error-then-narration", nil, "", "")
		for _, r := range rows {
			if r[3] == "10" {
				t.Fatalf("seq 10's next non-system line is a tool_use, not narration; it must not match: %v", r)
			}
		}
	})
}
