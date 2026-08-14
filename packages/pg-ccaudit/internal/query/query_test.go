package query

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-ccaudit/internal/ingest"
	"github.com/phillipgreenii/pg-ccaudit/internal/store"
)

// The three normalized signatures the committed fixture corpus produces. Derived
// by hand from the fixture bodies through the ingest normalizer's documented
// steps; see internal/ingest/testhelp_test.go for the line-by-line derivation of
// every count asserted below.
const (
	sigRTK     = "<tool_use_error>rtk: command not found</tool_use_error>"
	sigSleep   = "Blocked: sleep N"
	sigMissing = "File does not exist: PATH"
)

// buildIndex ingests the committed fixture corpus into a temp database. The
// corpus is copied into a temp dir first, so no test can mutate the checked-in
// fixture and none of them can reach the real transcript corpus.
func buildIndex(t *testing.T) (string, string, *sql.DB) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "projects")
	src := filepath.Join("..", "ingest", "testdata", "corpus")
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
		t.Fatalf("copy fixture corpus: %v", err)
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
	return dbPath, root, db
}

// runNamed executes a canned query and renders every cell as a string so a whole
// result set can be compared against a hand-written table. Absolute temp paths
// are reduced to their corpus-relative form.
func runNamed(t *testing.T, db *sql.DB, root, name string, args []string, since, until string) ([]string, [][]string) {
	t.Helper()
	q, err := Lookup(name)
	if err != nil {
		t.Fatalf("Lookup(%q): %v", name, err)
	}
	req := Request{Query: q, Args: args, Since: since, Until: until, Format: FormatTable}
	res, err := Run(context.Background(), db, req)
	if err != nil {
		t.Fatalf("Run(%s): %v", name, err)
	}
	out := make([][]string, 0, len(res.Rows))
	for _, row := range res.Rows {
		cells := make([]string, len(row))
		for i, c := range row {
			s := cellString(c)
			s = strings.ReplaceAll(s, root+string(filepath.Separator), "")
			cells[i] = s
		}
		out = append(out, cells)
	}
	return res.Columns, out
}

func assertRows(t *testing.T, name string, got, want [][]string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s rows mismatch\n got: %v\nwant: %v", name, got, want)
	}
}

// Every canned query in the shipped set, checked against hand-computed answers.
// "Returns without error" is explicitly NOT the bar here: a query that silently
// groups the wrong thing returns cleanly and reports a wrong number, which is the
// failure mode this whole index exists to eliminate.
func TestCannedQueriesAgainstFixtureCorpus(t *testing.T) {
	_, root, db := buildIndex(t)

	t.Run("error-rate-by-tool", func(t *testing.T) {
		// Bash: A1 A2 A4 B1 B2 = 5 calls, all errored.
		// Read: A3 (ok) C1 (error) = 2 calls, 1 error.
		// Grep: C2 = 1 call, no error.
		_, rows := runNamed(t, db, root, "error-rate-by-tool", nil, "", "")
		assertRows(t, "error-rate-by-tool", rows, [][]string{
			{"Bash", "5", "5", "100"},
			{"Read", "2", "1", "50"},
			{"Grep", "1", "0", "0"},
		})
	})

	t.Run("top-signatures", func(t *testing.T) {
		// sleep: A4 (main) B1 B2 (subagent) = 3 across 2 sessions, 2 subagent.
		// rtk:   A1 A2 = 2 in 1 session, none subagent.
		// missing: C1 = 1.
		_, rows := runNamed(t, db, root, "top-signatures", nil, "", "")
		assertRows(t, "top-signatures", rows, [][]string{
			{sigSleep, "3", "2", "2"},
			{sigRTK, "2", "1", "0"},
			{sigMissing, "1", "1", "0"},
		})
	})

	t.Run("top-signatures honours its limit", func(t *testing.T) {
		_, rows := runNamed(t, db, root, "top-signatures", []string{"1"}, "", "")
		if len(rows) != 1 {
			t.Fatalf("%d rows with limit 1", len(rows))
		}
		if rows[0][0] != sigSleep {
			t.Errorf("top row = %q, want %q", rows[0][0], sigSleep)
		}
	})

	t.Run("bash-by-lead-cmd", func(t *testing.T) {
		// sleep: A4 ("VAR=1 nice sleep 5") B1 B2 ("sleep 30") = 3, all errored.
		// rtk:   A1 ("sudo rtk find foo") A2 ("rtk find foo") = 2, all errored.
		_, rows := runNamed(t, db, root, "bash-by-lead-cmd", nil, "", "")
		assertRows(t, "bash-by-lead-cmd", rows, [][]string{
			{"sleep", "3", "3", "100"},
			{"rtk", "2", "2", "100"},
		})
	})

	t.Run("session-concentration", func(t *testing.T) {
		// The sleep signature fires 3 times across 2 sessions; S-SUB accounts for
		// 2 of them, so the worst single session holds two thirds of the class.
		_, rows := runNamed(t, db, root, "session-concentration", []string{sigSleep}, "", "")
		assertRows(t, "session-concentration", rows, [][]string{
			{"3", "2", "2", "1.5"},
		})
	})

	t.Run("session-concentration for an absent signature", func(t *testing.T) {
		_, rows := runNamed(t, db, root, "session-concentration", []string{"no such signature"}, "", "")
		assertRows(t, "session-concentration", rows, [][]string{
			{"0", "0", "0", "0"},
		})
	})

	t.Run("retry-chains", func(t *testing.T) {
		// Within the default window of 6 line ordinals, in the same file AND the
		// same session:
		//   sess-main A1@0 failed -> A2@3 (gap 3, different input)
		//   sess-main A2@3 failed -> A4@8 (gap 5, different input)
		//   sess-sub  B1@0 failed -> B2@2 (gap 2, IDENTICAL input)
		// A4@8 has no later Bash call within 6, and Read/Grep never repeat.
		_, rows := runNamed(t, db, root, "retry-chains", nil, "", "")
		assertRows(t, "retry-chains", rows, [][]string{
			{"S-MAIN", "0", "Bash", "projA/sess-main.jsonl", "0", "3", "3", "0", "1", sigRTK},
			{"S-MAIN", "0", "Bash", "projA/sess-main.jsonl", "3", "8", "5", "0", "1", sigRTK},
			{"S-SUB", "1", "Bash", "projA/sess-sub.jsonl", "0", "2", "2", "1", "1", sigSleep},
		})
	})

	t.Run("retry-chains window is honoured", func(t *testing.T) {
		// At n=2 only the sess-sub pair (gap 2) survives; the gap-3 and gap-5
		// pairs drop out. This proves the chosen window is doing work rather than
		// matching everything.
		_, rows := runNamed(t, db, root, "retry-chains", []string{"2"}, "", "")
		assertRows(t, "retry-chains n=2", rows, [][]string{
			{"S-SUB", "1", "Bash", "projA/sess-sub.jsonl", "0", "2", "2", "1", "1", sigSleep},
		})
	})

	t.Run("error-then-narration", func(t *testing.T) {
		// Only two errors are immediately followed by assistant prose:
		// sess-main seq 1 -> seq 2, and sess-bad seq 1 -> seq 2. sess-sub writes no
		// prose at all, and sess-main's other errors are followed by a tool_use
		// line or by end of file.
		_, rows := runNamed(t, db, root, "error-then-narration", nil, "", "")
		assertRows(t, "error-then-narration", rows, [][]string{
			{"S-MAIN", "0", "projA/sess-main.jsonl", "1", sigRTK, "rtk is not on PATH here; using rg instead."},
			{"S-BAD", "0", "projB/sess-bad.jsonl", "1", sigMissing, "That root does not exist on this machine."},
		})
	})

	t.Run("sidechain-split", func(t *testing.T) {
		// The load-bearing split. Note it is NOT uniform: the sleep class is
		// 2-of-3 subagent while the other two are wholly main-loop, so the three
		// classes route to three different places. A single aggregate ratio would
		// have said nothing useful about any of them.
		_, rows := runNamed(t, db, root, "sidechain-split", nil, "", "")
		assertRows(t, "sidechain-split", rows, [][]string{
			{sigSleep, "1", "2", "3", "2"},
			{sigRTK, "2", "0", "2", "1"},
			{sigMissing, "1", "0", "1", "1"},
		})
	})

	t.Run("sidechain-split filtered to one signature", func(t *testing.T) {
		_, rows := runNamed(t, db, root, "sidechain-split", []string{sigSleep}, "", "")
		assertRows(t, "sidechain-split filtered", rows, [][]string{
			{sigSleep, "1", "2", "3", "2"},
		})
	})

	t.Run("cost-by-signature", func(t *testing.T) {
		// duration_ms_sum is 0 for every row and that is CORRECT, not a bug: the
		// transcript records a top-level durationMs only on `system` events, never
		// on the user event carrying a tool_result. elapsed_ms is the real
		// measurement, taken between the tool_use line's ts and its result's ts:
		//   sleep:   A4 1000 + B1 31000 + B2 31000 = 63000 (mean 21000)
		//   rtk:     A1 2000 + A2  5000            =  7000 (mean  3500)
		//   missing: C1 1000                       =  1000 (mean  1000)
		_, rows := runNamed(t, db, root, "cost-by-signature", nil, "", "")
		assertRows(t, "cost-by-signature", rows, [][]string{
			{sigSleep, "3", "0", "63000", "21000"},
			{sigRTK, "2", "0", "7000", "3500"},
			{sigMissing, "1", "0", "1000", "1000"},
		})
	})

	t.Run("hook-rejections", func(t *testing.T) {
		// Two events carry a non-empty hookErrors payload (sess-main seq 7 with
		// hookCount 3, sess-bad seq 8 with hookCount 2). sess-bad seq 7 carries an
		// EMPTY array — stored at ingest, filtered here — so "rejected nothing"
		// never masquerades as "no hook data".
		_, rows := runNamed(t, db, root, "hook-rejections", nil, "", "")
		assertRows(t, "hook-rejections", rows, [][]string{
			{`["ceta: denied"]`, "2", "2", "0", "5"},
		})
	})

	t.Run("first-seen", func(t *testing.T) {
		// Ranked by FIRST occurrence: rtk (07-22 10:00:02), sleep (07-22 10:00:13),
		// missing (07-24 08:00:01).
		_, rows := runNamed(t, db, root, "first-seen", nil, "", "")
		assertRows(t, "first-seen", rows, [][]string{
			{sigRTK, "2026-07-22T10:00:02.000Z", "2026-07-22T10:00:09.000Z", "2", "1"},
			{sigSleep, "2026-07-22T10:00:13.000Z", "2026-07-23T09:01:03.000Z", "3", "2"},
			{sigMissing, "2026-07-24T08:00:01.000Z", "2026-07-24T08:00:01.000Z", "1", "1"},
		})
	})

	t.Run("last-seen", func(t *testing.T) {
		// Same columns, ranked by MOST RECENT occurrence — the ordering that
		// answers "did the documented fix actually stop this class?".
		_, rows := runNamed(t, db, root, "last-seen", nil, "", "")
		assertRows(t, "last-seen", rows, [][]string{
			{sigMissing, "2026-07-24T08:00:01.000Z", "2026-07-24T08:00:01.000Z", "1", "1"},
			{sigSleep, "2026-07-22T10:00:13.000Z", "2026-07-23T09:01:03.000Z", "3", "2"},
			{sigRTK, "2026-07-22T10:00:02.000Z", "2026-07-22T10:00:09.000Z", "2", "1"},
		})
	})

	t.Run("last-seen for one signature proves recurrence", func(t *testing.T) {
		// The concrete mistake this query prevents: eyeballing the sleep class
		// would date it 07-22 from its first sighting, when it in fact recurred
		// through 07-23.
		_, rows := runNamed(t, db, root, "last-seen", []string{sigSleep}, "", "")
		assertRows(t, "last-seen filtered", rows, [][]string{
			{sigSleep, "2026-07-22T10:00:13.000Z", "2026-07-23T09:01:03.000Z", "3", "2"},
		})
	})

	t.Run("coverage", func(t *testing.T) {
		// 3 files, all fully consumed and quiescent, 21 good lines and the 2 bad
		// ones that make coverage provable rather than assumed.
		_, rows := runNamed(t, db, root, "coverage", nil, "", "")
		if len(rows) != 1 {
			t.Fatalf("%d rows, want 1", len(rows))
		}
		got := rows[0]
		if got[0] != "3" || got[1] != "3" || got[2] != "0" {
			t.Errorf("files/complete/open = %s/%s/%s, want 3/3/0", got[0], got[1], got[2])
		}
		if got[3] != "21" || got[4] != "2" {
			t.Errorf("lines_ok/lines_bad = %s/%s, want 21/2", got[3], got[4])
		}
		if got[5] != got[6] {
			t.Errorf("corpus_bytes %s != indexed_bytes %s; every byte should be indexed", got[5], got[6])
		}
	})
}

// T-10's comparability requirement in practice: a window makes two audits over
// different periods answerable from one index.
func TestWindowFiltersByTimestamp(t *testing.T) {
	_, root, db := buildIndex(t)

	// 2026-07-23 holds only the subagent session.
	_, rows := runNamed(t, db, root, "sidechain-split", nil, "2026-07-23", "2026-07-24")
	assertRows(t, "sidechain-split windowed", rows, [][]string{
		{sigSleep, "0", "2", "2", "1"},
	})

	// A window before the corpus returns nothing rather than everything — the
	// failure mode a NULL-swallowing predicate would produce.
	_, rows = runNamed(t, db, root, "sidechain-split", nil, "2020-01-01", "2020-02-01")
	if len(rows) != 0 {
		t.Errorf("%d rows for a window with no data, want 0", len(rows))
	}

	// The default (empty) window covers everything.
	_, all := runNamed(t, db, root, "sidechain-split", nil, "", "")
	if len(all) != 3 {
		t.Errorf("%d rows for the default window, want 3", len(all))
	}
}

// A row whose ts is NULL must not be silently dropped by the default window. The
// obvious spelling (`ts >= :since`) evaluates to NULL for such a row and excludes
// it from every unwindowed run.
func TestDefaultWindowIncludesRowsWithNoTimestamp(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "projects", "projX")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// No `timestamp` field anywhere.
	content := `{"type":"assistant","uuid":"n-0","sessionId":"S-NOTS","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_N1","name":"Bash","input":{"command":"ls"}}]}}` + "\n" +
		`{"type":"user","uuid":"n-1","sessionId":"S-NOTS","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_N1","is_error":true,"content":"boom"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "s.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	dbPath := filepath.Join(base, "transcripts.db")
	w, err := store.Open(dbPath, false)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := ingest.Run(context.Background(), w, ingest.Options{
		Root: filepath.Join(base, "projects"), FinalAfter: ingest.FinalAfterImmediate, Progress: io.Discard,
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	db, err := store.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, rows := runNamed(t, db, filepath.Join(base, "projects"), "top-signatures", nil, "", "")
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1 — a NULL ts must not be dropped by the default window", len(rows))
	}
	if rows[0][0] != "boom" || rows[0][1] != "1" {
		t.Errorf("row = %v, want [boom 1 ...]", rows[0])
	}
}

// A canned query is only comparable across audits if its identity is pinned.
// Changing a query's SQL without bumping its version silently invalidates every
// earlier result it will be compared against, so the version is asserted here.
func TestRegistryVersionsArePinned(t *testing.T) {
	want := map[string]int{
		"error-rate-by-tool":    1,
		"top-signatures":        1,
		"bash-by-lead-cmd":      1,
		"session-concentration": 1,
		"retry-chains":          1,
		"error-then-narration":  1,
		"sidechain-split":       1,
		"cost-by-signature":     1,
		"hook-rejections":       1,
		"first-seen":            1,
		"last-seen":             1,
		"coverage":              1,
		// pg2-oisvb: the mistake census. Tier 1's eight structural detectors plus the
		// per-signature runaway discount the ranked report applies to every finding.
		"concentration-by-signature": 1,
		"human-turns":                1,
		"typed-turn-candidates":      1,
		"interruptions":              1,
		"denied-tool-calls":          1,
		"undo-signatures":            1,
		"file-churn":                 1,
		"escaping-retries":           1,
		"ack-markers":                1,
	}
	got := map[string]int{}
	for _, q := range All() {
		got[q.Name] = q.Version
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("query set/versions changed\n got: %v\nwant: %v\n"+
			"If the SQL's MEANING changed, bump the version AND this table; if a query was added, add it here.", got, want)
	}
}

// Every row of the specified query table must be present under the name an agent
// will actually type.
func TestEveryRequiredQueryIsRegistered(t *testing.T) {
	required := []string{
		"error-rate-by-tool",
		"top-signatures",
		"bash-by-lead-cmd",
		"session-concentration",
		"retry-chains",
		"error-then-narration",
		"sidechain-split",
		"cost-by-signature",
		"hook-rejections",
		"first-seen",
		"last-seen",
	}
	for _, name := range required {
		if _, err := Lookup(name); err != nil {
			t.Errorf("required query %q is not registered: %v", name, err)
		}
	}
}

func TestEveryQueryHasDocAndNotes(t *testing.T) {
	for _, q := range All() {
		if strings.TrimSpace(q.Doc) == "" {
			t.Errorf("query %s has no Doc", q.Name)
		}
		if strings.TrimSpace(q.Notes) == "" {
			t.Errorf("query %s has no Notes; a column whose meaning is not written down gets misquoted", q.Name)
		}
	}
}

func TestLookupUnknownQuery(t *testing.T) {
	if _, err := Lookup("no-such-query"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestTooManyArgumentsIsRejected(t *testing.T) {
	q, err := Lookup("coverage")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if _, err := (Request{Query: q, Args: []string{"x"}}).Bind(); err == nil {
		t.Fatal("expected an error for an argument to a query that takes none")
	}
}

func TestNumericArgumentIsValidated(t *testing.T) {
	q, err := Lookup("retry-chains")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if _, err := (Request{Query: q, Args: []string{"not-a-number"}}).Bind(); err == nil {
		t.Fatal("expected an error for a non-integer window")
	}
}

// Every rendering must carry the query name, its version and the window, so a
// result pasted into a review is self-describing.
func TestRenderStampsProvenance(t *testing.T) {
	_, root, db := buildIndex(t)
	q, err := Lookup("sidechain-split")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	_ = root
	for _, f := range []Format{FormatTable, FormatTSV, FormatJSON} {
		req := Request{Query: q, Since: "2026-07-23", Until: "2026-07-24", Format: f}
		res, err := Run(context.Background(), db, req)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		var sb strings.Builder
		if err := Render(&sb, req, res); err != nil {
			t.Fatalf("Render(%s): %v", f, err)
		}
		out := sb.String()
		for _, want := range []string{"sidechain-split", "2026-07-23", "2026-07-24"} {
			if !strings.Contains(out, want) {
				t.Errorf("format %s output omits %q:\n%s", f, want, out)
			}
		}
		if f != FormatJSON && !strings.Contains(out, "v1") {
			t.Errorf("format %s output omits the query version:\n%s", f, out)
		}
		if f == FormatJSON && !strings.Contains(out, `"version": 1`) {
			t.Errorf("json output omits the query version:\n%s", out)
		}
	}
}

func TestParseFormat(t *testing.T) {
	for _, s := range []string{"table", "tsv", "json"} {
		if _, err := ParseFormat(s); err != nil {
			t.Errorf("ParseFormat(%q): %v", s, err)
		}
	}
	if _, err := ParseFormat("yaml"); err == nil {
		t.Error("expected an error for an unknown format")
	}
}
