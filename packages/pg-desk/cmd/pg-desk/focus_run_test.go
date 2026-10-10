package main

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// newRunFixture opens a migrated store for the run-record tests.
func newRunFixture(t *testing.T) *store.Store {
	t.Helper()
	return newFocusFixture(t, "new").open(t)
}

func runStat(t *testing.T, st *store.Store, verb, outcome string) int {
	t.Helper()
	stats, err := st.FocusRunStats()
	if err != nil {
		t.Fatalf("FocusRunStats: %v", err)
	}
	return stats.ByVerbOutcome[[2]string{verb, outcome}]
}

func TestFocusRunRowWritten(t *testing.T) {
	st := newRunFixture(t)
	var stderr bytes.Buffer
	run := newFocusRun(&stderr, focusVerbPull, focusPeriod{Type: "day", Key: "2026-10-10"}, "tester", false, focusFixedNow)
	if err := run.Start(st); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Even a total failure that commits nothing else leaves its row.
	if err := run.Finish(exitTotal); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if got := runStat(t, st, "pull", "total"); got != 1 {
		t.Errorf("runs{pull,total} = %d, want 1", got)
	}
}

func TestFocusRunStartedRowCarriesActorPeriodAndTime(t *testing.T) {
	st := newRunFixture(t)
	// A period row that exists is referenced by the run row.
	if _, err := st.GetOrCreateFocusPeriod("day", "2026-10-10"); err != nil {
		t.Fatalf("period: %v", err)
	}
	run := newFocusRun(&bytes.Buffer{}, focusVerbClose, focusPeriod{Type: "day", Key: "2026-10-10"}, "tester", false, focusFixedNow)
	if err := run.Start(st); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := run.Finish(0); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	// FocusRunStats.LastSelectAt only reads select rows; read the row back
	// through a second run of verb select to prove started_at is the
	// injected clock.
	sel := newFocusRun(&bytes.Buffer{}, focusVerbSelect, focusPeriod{Type: "day", Key: "2026-10-10"}, "tester", false, focusFixedNow)
	if err := sel.Start(st); err != nil {
		t.Fatalf("Start select: %v", err)
	}
	if err := sel.Finish(0); err != nil {
		t.Fatalf("Finish select: %v", err)
	}
	stats, err := st.FocusRunStats()
	if err != nil {
		t.Fatalf("FocusRunStats: %v", err)
	}
	if want := focusFixedNow.Format(time.RFC3339); stats.LastSelectAt != want {
		t.Errorf("LastSelectAt = %q, want the injected start time %q", stats.LastSelectAt, want)
	}
}

// TestRunRowExitCodeFinalisedLast: the row exists from Start with a NULL
// exit_code (counted as total), Finish writes exit_code and counts together
// and only then prints the stderr line.
func TestRunRowExitCodeFinalisedLast(t *testing.T) {
	st := newRunFixture(t)
	var stderr bytes.Buffer
	run := newFocusRun(&stderr, focusVerbSelect, focusPeriod{Type: "day", Key: "2026-10-10"}, "tester", false, focusFixedNow)
	if err := run.Start(st); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// A crash here leaves exit_code NULL, which the statistics count as total.
	if got := runStat(t, st, "select", "total"); got != 1 {
		t.Fatalf("before Finish: runs{select,total} = %d, want 1 (NULL exit_code is total)", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("the stderr line must not be printed before the row is finalized, got %q", stderr.String())
	}
	run.SetCounts(focusCounts{Ranked: 4, Selected: 2})
	if err := run.Finish(exitPartial); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if got := runStat(t, st, "select", "partial"); got != 1 {
		t.Errorf("after Finish(2): runs{select,partial} = %d, want 1", got)
	}
	if got := runStat(t, st, "select", "total"); got != 0 {
		t.Errorf("after Finish(2): runs{select,total} = %d, want 0", got)
	}
	var rec map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &rec); err != nil {
		t.Fatalf("stderr line: %v", err)
	}
	if rec["exit_code"] != float64(2) {
		t.Errorf("stderr exit_code = %v, want 2", rec["exit_code"])
	}
	// A second Finish neither rewrites the row nor reprints.
	n := stderr.Len()
	if err := run.Finish(0); err != nil || stderr.Len() != n {
		t.Errorf("a second Finish must be a no-op (err %v, printed %d more bytes)", err, stderr.Len()-n)
	}
	if got := runStat(t, st, "select", "partial"); got != 1 {
		t.Errorf("a second Finish changed the row: partial = %d", got)
	}
}

func TestFocusRunOutcomeWordsFollowTheExitCode(t *testing.T) {
	st := newRunFixture(t)
	for _, tc := range []struct {
		exit    int
		verb    string
		outcome string
	}{
		{0, focusVerbPull, "ok"},
		{1, focusVerbClose, "usage"},
		{2, focusVerbRepair, "partial"},
		{3, focusVerbPull, "total"},
		{6, focusVerbClose, "period_closed"},
	} {
		run := newFocusRun(&bytes.Buffer{}, tc.verb, focusPeriod{Type: "day", Key: "2026-10-10"}, "a", false, focusFixedNow)
		if err := run.Start(st); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := run.Finish(tc.exit); err != nil {
			t.Fatalf("Finish(%d): %v", tc.exit, err)
		}
		if got := runStat(t, st, tc.verb, tc.outcome); got < 1 {
			t.Errorf("exit %d on %s: runs{%s,%s} = %d, want >= 1", tc.exit, tc.verb, tc.verb, tc.outcome, got)
		}
	}
	// An empty select is outcome "empty".
	run := newFocusRun(&bytes.Buffer{}, focusVerbSelect, focusPeriod{Type: "day", Key: "2026-10-10"}, "a", false, focusFixedNow)
	if err := run.Start(st); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := run.Finish(0); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if got := runStat(t, st, "select", "empty"); got != 1 {
		t.Errorf("runs{select,empty} = %d, want 1 (selected 0, nothing removed)", got)
	}
}

// TestSelectStderrLineIsParseableJSON checks the pg-desk.focus-select/v1
// line at the helper level: ONE line, the specified member set, the run id,
// and the per-entity seq.
func TestSelectStderrLineIsParseableJSON(t *testing.T) {
	st := newRunFixture(t)
	var stderr bytes.Buffer
	run := newFocusRun(&stderr, focusVerbSelect, focusPeriod{Type: "day", Key: "2026-10-10"}, "tester", false, focusFixedNow)
	if err := run.Start(st); err != nil {
		t.Fatalf("Start: %v", err)
	}
	run.SetCounts(focusCounts{
		Cap: 5, Kept: focusKept{Claimed: 1, Deferred: 2},
		Ranked: 9, Selected: 4,
		Removed:    focusRemoved{Operator: 1, Cap: 2, Dropped: 3, NewPeriod: 4},
		Forced:     []string{"o/r#1"},
		Handadded:  []string{"o/r#2"},
		Absorbed:   []string{"o/r#3"},
		Hydrated:   []string{"PROJ-1"},
		Tiers:      focusTierCounts{Overdue: 1, Started: 2, NotStarted: 6},
		RankInputs: focusRankInputs{UnparseableDue: 1, UnmappedPriority: 2, AgeFallback: 3, UnblocksUnavailable: 4, StatusCategoryAbsent: 5},

		DraftAgeSeconds: 3600, DriftRows: 2, FreshOrder: true,
		Coverage: focusCoverageSummary{Incomplete: true, Reasons: []string{"jira-assigned failed (timeout)"}},
	})
	run.Annotation("o/r#1", "ok", 41)
	run.Annotation("o/r#2", "failed", 99) // a failed write has no seq
	if err := run.Finish(exitPartial); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	out := stderr.String()
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("the record must be ONE newline-terminated line, got %q", out)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	wantMembers := []string{
		"absorbed", "actor", "annotations", "cap", "contract", "coverage", "draft_age_seconds", "drift_rows",
		"dry_run", "exit_code", "forced", "fresh_order", "handadded", "hydrated", "kept", "period", "rank_inputs",
		"ranked", "removed", "run_id", "selected", "tiers", "verb",
	}
	var gotMembers []string
	for k := range rec {
		gotMembers = append(gotMembers, k)
	}
	sort.Strings(gotMembers)
	if strings.Join(gotMembers, ",") != strings.Join(wantMembers, ",") {
		t.Errorf("members\n got: %v\nwant: %v", gotMembers, wantMembers)
	}
	if rec["contract"] != "pg-desk.focus-select/v1" || rec["verb"] != "select" ||
		rec["period"] != "2026-10-10" || rec["actor"] != "tester" ||
		rec["dry_run"] != false || rec["exit_code"] != float64(2) {
		t.Errorf("header members wrong: %v", rec)
	}
	if id, _ := rec["run_id"].(string); id != run.RunID() || len(id) != 26 {
		t.Errorf("run_id = %v, want the run's own 26-char ULID %s", rec["run_id"], run.RunID())
	}
	anns, _ := rec["annotations"].([]any)
	if len(anns) != 2 {
		t.Fatalf("annotations = %v", rec["annotations"])
	}
	a0, _ := anns[0].(map[string]any)
	a1, _ := anns[1].(map[string]any)
	if a0["key"] != "o/r#1" || a0["outcome"] != "ok" || a0["seq"] != float64(41) {
		t.Errorf("annotation 0 = %v", a0)
	}
	if a1["key"] != "o/r#2" || a1["outcome"] != "failed" || a1["seq"] != float64(0) {
		t.Errorf("a failed annotation must record seq 0, got %v", a1)
	}
	removed, _ := rec["removed"].(map[string]any)
	if removed["operator"] != float64(1) || removed["cap"] != float64(2) || removed["dropped"] != float64(3) || removed["new_period"] != float64(4) {
		t.Errorf("removed = %v", removed)
	}
	if ri, _ := rec["rank_inputs"].(map[string]any); len(ri) != 5 {
		t.Errorf("rank_inputs must carry the five counters, got %v", ri)
	}
	if tiers, _ := rec["tiers"].(map[string]any); len(tiers) != 3 {
		t.Errorf("tiers = %v", tiers)
	}
	if kept, _ := rec["kept"].(map[string]any); kept["claimed"] != float64(1) || kept["deferred"] != float64(2) {
		t.Errorf("kept = %v", kept)
	}
}

// TestFocusCountsJSONNeverOmitsAbsentCounts: a zero-valued run still writes
// every member, as zero or empty.
func TestFocusCountsJSONNeverOmitsAbsentCounts(t *testing.T) {
	s, err := focusCounts{}.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("counts_json: %v", err)
	}
	for _, k := range []string{
		"cap", "kept", "ranked", "selected", "removed", "forced", "handadded", "absorbed", "hydrated", "tiers",
		"rank_inputs", "annotations", "draft_age_seconds", "drift_rows", "fresh_order", "coverage",
	} {
		if _, ok := m[k]; !ok {
			t.Errorf("counts_json omits %q: %s", k, s)
		}
	}
	for _, k := range []string{"forced", "handadded", "absorbed", "hydrated", "annotations"} {
		if string(m[k]) != "[]" {
			t.Errorf("%s = %s, want []", k, m[k])
		}
	}
	if !strings.Contains(string(m["coverage"]), `"reasons":[]`) {
		t.Errorf("coverage = %s, want reasons []", m["coverage"])
	}
}

// TestFocusRunCountsReadByStatistics: the run statistics (metrics, doctor)
// read draft_age_seconds and drift_rows from the finalized row.
func TestFocusRunCountsReadByStatistics(t *testing.T) {
	st := newRunFixture(t)
	run := newFocusRun(&bytes.Buffer{}, focusVerbSelect, focusPeriod{Type: "day", Key: "2026-10-10"}, "a", false, focusFixedNow)
	if err := run.Start(st); err != nil {
		t.Fatalf("Start: %v", err)
	}
	run.SetCounts(focusCounts{Selected: 3, DraftAgeSeconds: 7200, DriftRows: 5})
	if err := run.Finish(0); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	stats, err := st.FocusRunStats()
	if err != nil {
		t.Fatalf("FocusRunStats: %v", err)
	}
	if stats.LastLockDraftAgeSeconds == nil || *stats.LastLockDraftAgeSeconds != 7200 ||
		stats.LastLockDriftRows == nil || *stats.LastLockDriftRows != 5 {
		t.Errorf("last lock stats = %v / %v, want 7200 / 5", stats.LastLockDraftAgeSeconds, stats.LastLockDriftRows)
	}
}

func TestFocusRunDryRunWritesNoRowButPrintsTheLine(t *testing.T) {
	st := newRunFixture(t)
	var stderr bytes.Buffer
	run := newFocusRun(&stderr, focusVerbSelect, focusPeriod{Type: "day", Key: "2026-10-10"}, "a", true, focusFixedNow)
	if err := run.Start(st); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := run.Finish(0); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	var rec map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &rec); err != nil || rec["dry_run"] != true {
		t.Fatalf("dry-run line: %v / %v", err, rec)
	}
	stats, err := st.FocusRunStats()
	if err != nil {
		t.Fatalf("FocusRunStats: %v", err)
	}
	if len(stats.ByVerbOutcome) != 0 {
		t.Errorf("a dry run wrote a focus_run row: %v", stats.ByVerbOutcome)
	}
}

// TestFocusRunWithoutWritableStoreStillPrintsTheLine: when Start cannot
// insert the row, the run goes on and Finish prints the line without
// pretending to finalize one.
func TestFocusRunWithoutWritableStoreStillPrintsTheLine(t *testing.T) {
	st := newRunFixture(t)
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	var stderr bytes.Buffer
	run := newFocusRun(&stderr, focusVerbPull, focusPeriod{Type: "day", Key: "2026-10-10"}, "a", false, focusFixedNow)
	if err := run.Start(st); err == nil {
		t.Fatal("Start on a closed store: want an error")
	}
	if err := run.Finish(exitTotal); err != nil {
		t.Errorf("Finish without a row must not error: %v", err)
	}
	var rec map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &rec); err != nil || rec["exit_code"] != float64(3) {
		t.Errorf("line = %q (%v)", stderr.String(), err)
	}
}

// ---------------------------------------------------------------- ULID

func TestRunIDIsAULID(t *testing.T) {
	g := &ulidGen{}
	// The ULID specification's own example timestamp encodes to 01ARYZ6S41.
	id := g.New(time.UnixMilli(1469918176385))
	if len(id) != 26 {
		t.Fatalf("len(%q) = %d, want 26", id, len(id))
	}
	if !strings.HasPrefix(id, "01ARYZ6S41") {
		t.Errorf("%q: timestamp part is not 01ARYZ6S41", id)
	}
	for _, c := range id {
		if !strings.ContainsRune(crockfordBase32, c) {
			t.Errorf("%q has non-Crockford character %q", id, c)
		}
	}
	// 128 bits in 26 characters: the first character carries 3 bits, so it is at most 7.
	if id[0] > '7' {
		t.Errorf("%q: first character exceeds 7", id)
	}
	if zero := crockfordEncode([16]byte{}); zero != strings.Repeat("0", 26) {
		t.Errorf("zero value encodes to %q", zero)
	}
	var ones [16]byte
	for i := range ones {
		ones[i] = 0xff
	}
	if max := crockfordEncode(ones); max != "7"+strings.Repeat("Z", 25) {
		t.Errorf("all-ones encodes to %q", max)
	}
}

func TestRunIDsAreSortableAndUnique(t *testing.T) {
	g := &ulidGen{}
	base := time.UnixMilli(1_800_000_000_000)
	const n = 20000
	ids := make([]string, 0, n)
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		// Many calls share one millisecond; every 100th advances it.
		id := g.New(base.Add(time.Duration(i/100) * time.Millisecond))
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %q at call %d", id, i)
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if !sort.StringsAreSorted(ids) {
		t.Error("ids are not in creation order when sorted lexicographically")
	}
	// A clock that goes backwards never makes ids go backwards.
	prev := g.New(base.Add(time.Hour))
	next := g.New(base)
	if next <= prev {
		t.Errorf("clock regression produced a smaller id: %q after %q", next, prev)
	}
}

func TestRunIDSameMillisecondIncrementsAndWrapsReseed(t *testing.T) {
	// A fixed all-0xff entropy source makes the 80-bit counter wrap on the
	// second call within a millisecond, which must move to the next
	// millisecond instead of repeating or going backwards.
	g := &ulidGen{rand: bytes.NewReader(bytes.Repeat([]byte{0xff}, 20))}
	at := time.UnixMilli(1_800_000_000_000)
	first := g.New(at)
	second := g.New(at)
	if second <= first {
		t.Fatalf("a counter wrap must still sort after its predecessor: %q then %q", first, second)
	}
	// Exhausted entropy falls back to the runtime generator, never an error.
	g3 := &ulidGen{rand: bytes.NewReader(nil)}
	if id := g3.New(at); len(id) != 26 {
		t.Errorf("fallback id %q", id)
	}
}

func TestRunIDsFromFocusRunAreDistinct(t *testing.T) {
	a := newFocusRun(nil, focusVerbSelect, focusPeriod{}, "a", true, focusFixedNow)
	b := newFocusRun(nil, focusVerbSelect, focusPeriod{}, "a", true, focusFixedNow)
	if a.RunID() == b.RunID() {
		t.Errorf("two runs share the run id %s", a.RunID())
	}
}
