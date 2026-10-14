package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// Tests of the job table (schema version 2) and its accessors. The migration is
// also covered with the other schema tests (TestOpenEmptyCreatesCurrentSchema,
// TestOpenFromEachFixture); the tests here pin the job table's arrival on a
// populated store and the accessors' behavior. They open with the whole shipped
// ladder, not a truncated one: the typed accessors always read the current
// schema (query.baseline_at, for one), so a store held at version 2 cannot be
// read through them.

func jobRow(run string, attempt int, fetched time.Time, jobs ...JobResult) JobRow {
	return JobRow{RunID: run, Attempt: attempt, Jobs: jobs, FetchedAt: fetched}
}

func sameJobRow(a, b JobRow) bool {
	return a.RunID == b.RunID && a.Attempt == b.Attempt && slices.Equal(a.Jobs, b.Jobs) && a.FetchedAt.Equal(b.FetchedAt)
}

func jobRowCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM job`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOpenVersion1FixtureGainsAnEmptyJobTable(t *testing.T) {
	fx, ok := fixtures(t)[1]
	if !ok {
		t.Fatal("no version 1 fixture")
	}
	path := dbPath(t)
	copyFile(t, fx, path)
	s, res := mustOpen(t, path, fastOpts())

	if res.Outcome != OutcomeMigrated || res.FromVersion != 1 || res.ToVersion != SchemaVersion {
		t.Fatalf("result = %+v, want migrated 1 -> %d", res, SchemaVersion)
	}
	ctx := context.Background()
	assertFixtureData(t, ctx, s)

	// The new table exists, is empty, and is usable.
	if n := jobRowCount(t, s); n != 0 {
		t.Fatalf("job rows after migrating = %d, want an empty table", n)
	}
	if _, ok, err := s.GetJobs(ctx, fixtureJobs().RunID, fixtureJobs().Attempt); err != nil || ok {
		t.Errorf("GetJobs on the empty table: ok=%v err=%v", ok, err)
	}
	want := jobRow("77", 1, time.UnixMilli(1760000000000),
		JobResult{ID: "1", Name: "build", Status: "completed", Conclusion: "success"})
	if err := s.PutJobs(ctx, want); err != nil {
		t.Fatalf("PutJobs into the migrated table: %v", err)
	}
	if got, ok, err := s.GetJobs(ctx, "77", 1); err != nil || !ok || !sameJobRow(got, want) {
		t.Errorf("row written after migrating = %+v ok=%v err=%v, want %+v", got, ok, err, want)
	}
}

func TestOpenVersion2FixtureMigratesAndKeepsItsSampleJob(t *testing.T) {
	fx, ok := fixtures(t)[jobTableVersion]
	if !ok {
		t.Fatalf("no version %d fixture", jobTableVersion)
	}
	path := dbPath(t)
	copyFile(t, fx, path)
	s, res := mustOpen(t, path, fastOpts())

	if res.Outcome != OutcomeMigrated || res.FromVersion != jobTableVersion || res.ToVersion != SchemaVersion {
		t.Fatalf("result = %+v, want migrated %d -> %d", res, jobTableVersion, SchemaVersion)
	}
	ctx := context.Background()
	assertFixtureData(t, ctx, s)
	assertFixtureJobs(t, ctx, s, jobTableVersion)
	if n := jobRowCount(t, s); n != 1 {
		t.Errorf("the fixture holds %d job rows, want its one sample row", n)
	}
}

func TestJobsRoundTripInOrderAndSurviveAReopen(t *testing.T) {
	ctx := context.Background()
	path := dbPath(t)
	s, _ := mustOpen(t, path, fastOpts())

	// The names are deliberately not alphabetical, and one job carries no id,
	// url or conclusion yet.
	want := jobRow(
		"5001", 1, time.Date(2026, 10, 12, 9, 30, 0, 123_000_000, time.UTC),
		JobResult{
			ID: "11", Name: "zeta", Status: "completed", Conclusion: "failure",
			URL: "https://example.invalid/org/repo/actions/runs/5001/job/11",
		},
		JobResult{Name: "alpha", Status: "in_progress"},
		JobResult{ID: "13", Name: "mid", Status: "completed", Conclusion: "success"},
	)
	if err := s.PutJobs(ctx, want); err != nil {
		t.Fatal(err)
	}
	check := func(s *Store, when string) {
		t.Helper()
		got, ok, err := s.GetJobs(ctx, "5001", 1)
		if err != nil || !ok || !sameJobRow(got, want) {
			t.Errorf("%s: row = %+v ok=%v err=%v, want %+v (jobs in the stored order)", when, got, ok, err, want)
		}
	}
	check(s, "same handle")

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, res := mustOpen(t, path, fastOpts())
	if res.Outcome != OutcomeOpened {
		t.Fatalf("reopen result = %+v, want opened", res)
	}
	check(s2, "after reopen")
}

func TestPutJobsReplacesTheSameRunAndAttemptAndNothingElse(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	t0 := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

	// Two attempts of one run and the same attempt number of another run are
	// three rows.
	first := jobRow("100", 1, t0, JobResult{Name: "a"})
	otherAttempt := jobRow("100", 2, t0, JobResult{Name: "b"})
	otherRun := jobRow("200", 1, t0, JobResult{Name: "c"})
	for _, r := range []JobRow{first, otherAttempt, otherRun} {
		if err := s.PutJobs(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if n := jobRowCount(t, s); n != 3 {
		t.Fatalf("rows = %d, want 3 (the key is run id and attempt together)", n)
	}

	// A second put for the first pair replaces it: neither merged with the old
	// jobs nor added as a fourth row.
	replacement := jobRow("100", 1, t0.Add(time.Hour), JobResult{Name: "a2"}, JobResult{Name: "a3"})
	if err := s.PutJobs(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if n := jobRowCount(t, s); n != 3 {
		t.Errorf("rows after the replace = %d, want 3", n)
	}
	for _, want := range []JobRow{replacement, otherAttempt, otherRun} {
		got, ok, err := s.GetJobs(ctx, want.RunID, want.Attempt)
		if err != nil || !ok || !sameJobRow(got, want) {
			t.Errorf("(%s, %d) = %+v ok=%v err=%v, want %+v", want.RunID, want.Attempt, got, ok, err, want)
		}
	}
}

func TestGetJobsForAnUnknownRunAndAttemptIsNotFound(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())

	if got, ok, err := s.GetJobs(ctx, "100", 1); err != nil || ok || !sameJobRow(got, JobRow{}) {
		t.Fatalf("empty store: row = %+v ok=%v err=%v, want the zero row, not found, no error", got, ok, err)
	}
	if err := s.PutJobs(ctx, jobRow("100", 1, time.UnixMilli(1000), JobResult{Name: "a"})); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		run     string
		attempt int
	}{
		{"same run, other attempt", "100", 2},
		{"other run, same attempt", "200", 1},
		{"a prefix of the run id", "10", 1},
		{"the run id extended", "1000", 1},
	} {
		if got, ok, err := s.GetJobs(ctx, c.run, c.attempt); err != nil || ok {
			t.Errorf("%s: row = %+v ok=%v err=%v, want not found", c.name, got, ok, err)
		}
	}
}

// A completed attempt with no jobs is a FETCHED row (found, empty list), not a
// missing one, so the caller can tell "fetched, nothing there" from "never
// fetched".
func TestAnEmptyJobListIsAFetchedRowNotAMissingOne(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())

	if err := s.PutJobs(ctx, jobRow("300", 1, time.UnixMilli(5000))); err != nil { // a nil list
		t.Fatal(err)
	}
	got, ok, err := s.GetJobs(ctx, "300", 1)
	if err != nil || !ok {
		t.Fatalf("row = %+v ok=%v err=%v, want found", got, ok, err)
	}
	if got.Jobs == nil || len(got.Jobs) != 0 {
		t.Errorf("Jobs = %#v, want an empty, non-nil list", got.Jobs)
	}
	var raw string
	if err := s.DB().QueryRow(`SELECT jobs FROM job WHERE run_id = '300'`).Scan(&raw); err != nil || raw != "[]" {
		t.Errorf("stored jobs text = %q err=%v, want []", raw, err)
	}
}

func TestPruneJobsBeforeDeletesOnlyOlderRowsAndCountsThem(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	horizon := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

	if n, err := s.PruneJobsBefore(ctx, horizon); err != nil || n != 0 {
		t.Fatalf("prune of an empty table = %d err=%v, want 0", n, err)
	}
	for _, r := range []JobRow{
		jobRow("1", 1, horizon.Add(-2*time.Hour), JobResult{Name: "old"}),
		jobRow("2", 1, horizon.Add(-time.Millisecond), JobResult{Name: "just older"}),
		jobRow("3", 1, horizon, JobResult{Name: "at the horizon"}),
		jobRow("4", 1, horizon.Add(time.Hour), JobResult{Name: "newer"}),
	} {
		if err := s.PutJobs(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.PruneJobsBefore(ctx, horizon)
	if err != nil || n != 2 {
		t.Fatalf("pruned %d err=%v, want 2 (the rows fetched before the horizon)", n, err)
	}
	for run, wantKept := range map[string]bool{"1": false, "2": false, "3": true, "4": true} {
		if _, ok, err := s.GetJobs(ctx, run, 1); err != nil || ok != wantKept {
			t.Errorf("run %s: found=%v err=%v, want found=%v (a row fetched exactly at the horizon is kept)", run, ok, err, wantKept)
		}
	}
	if n, err := s.PruneJobsBefore(ctx, horizon); err != nil || n != 0 {
		t.Errorf("second prune = %d err=%v, want 0", n, err)
	}
	if n, err := s.PruneJobsBefore(ctx, horizon.Add(24*time.Hour)); err != nil || n != 2 {
		t.Errorf("prune past every row = %d err=%v, want the remaining 2", n, err)
	}
	if n := jobRowCount(t, s); n != 0 {
		t.Errorf("rows left = %d, want 0", n)
	}
}

// A row whose jobs text cannot be decoded is an error naming the run, never
// "not found": a caller that took it for "never fetched" would refetch forever.
func TestGetJobsReportsAMalformedJobsColumnAsAnError(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())
	if _, err := s.DB().Exec(`INSERT INTO job (run_id, attempt, jobs, fetched_at) VALUES ('400', 1, 'not json', 1)`); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetJobs(ctx, "400", 1)
	if err == nil || ok || !strings.Contains(err.Error(), "400") {
		t.Errorf("row = %+v ok=%v err=%v, want a decode error naming run 400", got, ok, err)
	}
}
