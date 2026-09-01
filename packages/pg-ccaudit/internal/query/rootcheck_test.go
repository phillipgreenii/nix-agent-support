package query

import "testing"

// TestFailedReadsByRootAgainstFixtureCorpus is bead pg2-hyn34's detector: it must
// separate a FABRICATED absolute root from a genuinely missing file at a
// legitimate root from a legitimate path referenced by a different tool, over a
// fixture that is DELIBERATELY SEPARATE from the other corpora (mirroring
// `mistakes` vs `corpus` vs `refusals` in mistake_test.go) so this query's own
// fixture can change without moving any other test's hand-computed answer.
//
// Layout of internal/ingest/testdata/rootcheck, by LINE ORDINAL (seq is 0-based):
//
//	projX/sess-x.jsonl — sessions F-MAIN (main loop) and F-SUB (SIDECHAIN):
//	 0 Read /home/user/secret.txt             <- FABRICATED root: /home is not a
//	 1 result ERROR File does not exist: …        real root on this machine
//	 2 Read /Users/phillipg/repo/config.yaml  <- GENUINE missing file, LEGITIMATE
//	 3 result ERROR File does not exist: …        root (/Users)
//	 4 Bash cat /nix/store/abc123-pkg/bin/tool <- LEGITIMATE root via Bash command
//	 5 result ERROR No such file or directory     extraction, SIDECHAIN (F-SUB)
//	 6 Bash echo hi                           <- no absolute path anywhere
//	 7 result ERROR boom
func TestFailedReadsByRootAgainstFixtureCorpus(t *testing.T) {
	root, db := buildIndexFrom(t, "rootcheck")

	t.Run("separates fabricated from genuine-missing from legitimate", func(t *testing.T) {
		cols, rows := runNamed(t, db, root, "failed-reads-by-root", nil, "", "")
		idx := colIndex(t, cols)
		if len(rows) != 4 {
			t.Fatalf("failed-reads-by-root returned %d rows, want 4:\n%v", len(rows), rows)
		}
		// Ordered: known_root=0 (fabricated) first, then known_root=1 roots by
		// (calls DESC, root), then the NULL/no-absolute-path bucket last.
		want := []struct {
			root, known, calls, mainLoop, subagent, sessions, tools, sample string
		}{
			{"/home", "0", "1", "1", "0", "1", "Read", "/home/user/secret.txt"},
			{"/Users", "1", "1", "1", "0", "1", "Read", "/Users/phillipg/repo/config.yaml"},
			{"/nix", "1", "1", "0", "1", "1", "Bash", "/nix/store/abc123-pkg/bin/tool"},
			{"(no absolute path)", "", "1", "1", "0", "1", "Bash", ""},
		}
		for i, w := range want {
			got := rows[i]
			if got[idx["root"]] != w.root || got[idx["known_root"]] != w.known ||
				got[idx["calls"]] != w.calls || got[idx["main_loop"]] != w.mainLoop ||
				got[idx["subagent"]] != w.subagent || got[idx["sessions"]] != w.sessions ||
				got[idx["tool_names"]] != w.tools || got[idx["sample_path"]] != w.sample {
				t.Errorf("row %d = %v, want %+v", i, got, w)
			}
		}
	})

	t.Run("the fabricated root and the genuine miss share the SAME normalized signature", func(t *testing.T) {
		// This is the whole point of the bead: top-signatures collapses both
		// /home/user/secret.txt and /Users/phillipg/repo/config.yaml into the
		// IDENTICAL "File does not exist: PATH" key (the other two errors in this
		// fixture normalize to their own distinct signatures and do not collide),
		// so top-signatures alone cannot separate a fabricated root from a genuine
		// miss. failed-reads-by-root, reading input_json instead of the normalized
		// signature, can — that is the whole point of shipping it alongside T-6
		// rather than weakening the normalizer.
		cols, sigRows := runNamed(t, db, root, "top-signatures", nil, "", "")
		idx := colIndex(t, cols)
		found := false
		for _, r := range sigRows {
			if r[idx["signature"]] == "File does not exist: PATH" {
				found = true
				if r[idx["errors"]] != "2" {
					t.Errorf("\"File does not exist: PATH\" errors = %s, want 2 (the normalizer must not distinguish these)", r[idx["errors"]])
				}
			}
		}
		if !found {
			t.Fatalf("expected a collapsed \"File does not exist: PATH\" signature among: %v", sigRows)
		}
	})

	t.Run("valid_roots is a real parameter, not decoration", func(t *testing.T) {
		// Narrowing the valid set to exclude /nix must flip that group's known_root
		// from 1 to 0 — proving the machine's root set is genuinely read from the
		// bound parameter rather than a compiled-in constant.
		cols, rows := runNamed(t, db, root, "failed-reads-by-root", []string{"/Users,/Volumes,/private"}, "", "")
		idx := colIndex(t, cols)
		found := false
		for _, r := range rows {
			if r[idx["root"]] == "/nix" {
				found = true
				if r[idx["known_root"]] != "0" {
					t.Errorf("/nix known_root = %s, want 0 once excluded from valid_roots", r[idx["known_root"]])
				}
			}
		}
		if !found {
			t.Fatal("expected a /nix row even with a narrowed valid_roots")
		}
	})

	t.Run("the window is honoured", func(t *testing.T) {
		_, all := runNamed(t, db, root, "failed-reads-by-root", nil, "", "")
		_, before := runNamed(t, db, root, "failed-reads-by-root", nil, "2020-01-01", "2020-02-01")
		if len(all) != 4 {
			t.Errorf("unwindowed rows = %d, want 4", len(all))
		}
		if len(before) != 0 {
			t.Errorf("a window before the corpus returned %d rows, want 0", len(before))
		}
	})
}

// TestRootFirstLastSeenAgainstFixtureCorpus is pg2-z38lk item 1: first/last
// occurrence keyed on the extracted ROOT instead of the signature, over the
// SAME rootcheck fixture (and the SAME extraction) as failed-reads-by-root, so
// the two queries are proven to agree about what a call's root is.
//
// Each root in the fixture occurs exactly once, at the tool_use line's own
// timestamp (root-first-last-seen, like failed-reads-by-root, dates from the
// CALL's event, not the result's): /home at seq 0 (00:00:00), /Users at seq 2
// (00:00:02), /nix at seq 4 (00:00:04). The "(no absolute path)" bucket (seq 6)
// is excluded outright — dating the first/last occurrence of "nothing was
// found" is not a meaningful trend line.
func TestRootFirstLastSeenAgainstFixtureCorpus(t *testing.T) {
	root, db := buildIndexFrom(t, "rootcheck")

	t.Run("every real root is dated, the no-path bucket is not", func(t *testing.T) {
		cols, rows := runNamed(t, db, root, "root-first-last-seen", nil, "", "")
		idx := colIndex(t, cols)
		if len(rows) != 3 {
			t.Fatalf("root-first-last-seen returned %d rows, want 3:\n%v", len(rows), rows)
		}
		want := []struct{ root, ts, occurrences, sessions string }{
			{"/home", "2026-08-01T00:00:00.000Z", "1", "1"},
			{"/Users", "2026-08-01T00:00:02.000Z", "1", "1"},
			{"/nix", "2026-08-01T00:00:04.000Z", "1", "1"},
		}
		for i, w := range want {
			got := rows[i]
			if got[idx["root"]] != w.root ||
				got[idx["first_seen"]] != w.ts || got[idx["last_seen"]] != w.ts ||
				got[idx["occurrences"]] != w.occurrences || got[idx["sessions"]] != w.sessions {
				t.Errorf("row %d = %v, want %+v", i, got, w)
			}
		}
		for _, r := range rows {
			if r[idx["root"]] == "(no absolute path)" {
				t.Errorf("the no-absolute-path bucket must not be dated: %v", r)
			}
		}
	})

	t.Run("root is a real filter, isolating one class", func(t *testing.T) {
		_, rows := runNamed(t, db, root, "root-first-last-seen", []string{"/home"}, "", "")
		assertRows(t, "root-first-last-seen /home", rows, [][]string{
			{"/home", "2026-08-01T00:00:00.000Z", "2026-08-01T00:00:00.000Z", "1", "1"},
		})
	})

	t.Run("the window is honoured", func(t *testing.T) {
		_, all := runNamed(t, db, root, "root-first-last-seen", nil, "", "")
		_, before := runNamed(t, db, root, "root-first-last-seen", nil, "2020-01-01", "2020-02-01")
		if len(all) != 3 {
			t.Errorf("unwindowed rows = %d, want 3", len(all))
		}
		if len(before) != 0 {
			t.Errorf("a window before the corpus returned %d rows, want 0", len(before))
		}
	})
}
