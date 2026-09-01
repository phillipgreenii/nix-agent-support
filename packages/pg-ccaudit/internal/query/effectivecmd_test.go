package query

import "testing"

// TestBashByEffectiveCmdAgainstFixtureCorpus is pg2-z38lk item 2's detector,
// asserted against hand-computed answers over a fixture DELIBERATELY SEPARATE
// from the other corpora (mirroring `mistakes`/`refusals`/`rootcheck` in the
// sibling test files), so this query's own fixture can change without moving
// any other test's hand-computed answer.
//
// Layout of internal/ingest/testdata/effectivecmd, by LINE ORDINAL (seq is
// 0-based). Single session E-MAIN, main loop.
//
//	 0 Bash `cd /repo && jq . file.json`         <- peeled once:       jq
//	 1 result ERROR jq: error …
//	 2 Bash `jq . plain.json`                    <- no cd at all:      jq
//	 3 result ok
//	 4 Bash `cd /a && cd /b && rtk find .`        <- peeled TWICE:      rtk
//	 5 result ERROR rtk: command not found
//	 6 Bash `cd /tmp`                            <- bare cd, no chain: cd  (fallback)
//	 7 result ERROR boom
//	 8 Bash `sudo cd /tmp && jq . x.json`         <- wrapped BEFORE cd: cd  (fallback,
//	 9 result ERROR boom2                            known miss documented in Notes)
//	10 Bash `cd /var && docker ps`  (2026-08-11)  <- peeled once:       docker
//	11 result ERROR docker: command not found
//
// lead_cmd (computed at ingest, UNCHANGED by this query) is 'cd' for every row
// above except seq 2 ('jq' directly, no peeling needed) — confirming
// bash-by-lead-cmd's own 45%-lead-with-cd evidence shape even in this small
// fixture.
func TestBashByEffectiveCmdAgainstFixtureCorpus(t *testing.T) {
	root, db := buildIndexFrom(t, "effectivecmd")

	t.Run("cd is peeled through to the real command, chains and all", func(t *testing.T) {
		cols, rows := runNamed(t, db, root, "bash-by-effective-cmd", nil, "", "")
		idx := colIndex(t, cols)
		if len(rows) != 4 {
			t.Fatalf("bash-by-effective-cmd returned %d rows, want 4:\n%v", len(rows), rows)
		}
		// Ordered by (errors DESC, calls DESC, effective_cmd ASC): cd (2 errors, 2
		// calls) leads; among the errors=1 tier, jq's 2 calls outrank the 1-call
		// docker/rtk tie, which breaks alphabetically.
		want := []struct{ cmd, calls, errors, pct string }{
			{"cd", "2", "2", "100"},
			{"jq", "2", "1", "50"},
			{"docker", "1", "1", "100"},
			{"rtk", "1", "1", "100"},
		}
		for i, w := range want {
			got := rows[i]
			if got[idx["effective_cmd"]] != w.cmd || got[idx["calls"]] != w.calls ||
				got[idx["errors"]] != w.errors || got[idx["error_pct"]] != w.pct {
				t.Errorf("row %d = %v, want %+v", i, got, w)
			}
		}
	})

	t.Run("bash-by-lead-cmd is untouched: everything but the pass-through still reads cd", func(t *testing.T) {
		// The whole point of "alongside, not replacing": bash-by-lead-cmd's own
		// grouping must be unaffected by this query's addition.
		cols, rows := runNamed(t, db, root, "bash-by-lead-cmd", nil, "", "")
		idx := colIndex(t, cols)
		found := false
		for _, r := range rows {
			if r[idx["lead_cmd"]] == "cd" {
				found = true
				if r[idx["calls"]] != "5" {
					t.Errorf("lead_cmd=cd calls = %s, want 5 (every row except the plain `jq . plain.json`)", r[idx["calls"]])
				}
			}
		}
		if !found {
			t.Fatal("expected a lead_cmd='cd' row — bash-by-lead-cmd's own peeling must be unchanged")
		}
	})

	t.Run("the sudo-wrapped-before-cd case falls back safely rather than guessing wrong", func(t *testing.T) {
		// seq 8's raw command is `sudo cd /tmp && jq . x.json`: lead_cmd is 'cd'
		// (sudo already peeled at ingest), but this query cannot safely locate
		// where 'cd' begins without re-deriving ingest's own wrapper peel loop, so
		// it must fall back to 'cd' rather than misreading the leading 'sudo' as
		// the effective command.
		_, rows := runNamed(t, db, root, "bash-by-effective-cmd", nil, "", "")
		for _, r := range rows {
			if r[0] == "sudo" {
				t.Fatalf("the wrapped-before-cd case must fall back to 'cd', never surface 'sudo': %v", r)
			}
		}
	})

	t.Run("the window is honoured", func(t *testing.T) {
		_, all := runNamed(t, db, root, "bash-by-effective-cmd", nil, "", "")
		_, day1 := runNamed(t, db, root, "bash-by-effective-cmd", nil, "", "2026-08-11")
		if len(all) != 4 {
			t.Errorf("unwindowed rows = %d, want 4", len(all))
		}
		if len(day1) != 3 {
			t.Errorf("a window ending before the docker row returned %d rows, want 3", len(day1))
		}
	})
}
