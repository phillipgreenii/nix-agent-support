package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Tests of `pg-desk pr reinterpret` (docs/behavior/pg-desk/reinterpret.md, "The
// command"; INV-REINTERPRET-3, -8, -9 and -12). They run the real command in
// process over a real on-disk store, with no connector on PATH.

const riRepo = "o/r"

func riID(n int) string { return fmt.Sprintf("o/r#%d", n) }

// riFacts is a stored-facts document of a PR in state, authored by author.
func riFacts(state string, merged bool, author string) string {
	return fmt.Sprintf(`{"pr_show":{"state":%q,"merged":%t,"author":%q,"title":"fix: x","repo":"o/r"},"head_sha":"deadbeef","as_of":"2026-09-16T00:00:00Z"}`, state, merged, author)
}

// riStore is a schema-2 store at path with the open seam pointed at it.
type riStore struct {
	path string
	seed *store.Store
	// onOpen, when set, runs on every store the command opens.
	onOpen func(*store.Store)
}

func newRIStore(t *testing.T) *riStore {
	t.Helper()
	path := storeAtVersion(t, "new")
	rs := &riStore{path: path}
	seed, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.Close() })
	rs.seed = seed
	// The shared fixture holds o/r#1 with empty facts and a degraded row; drop
	// it so every test controls its own rows.
	rs.exec(t, `DELETE FROM interpretation`)
	rs.exec(t, `DELETE FROM entity`)
	withOpenSeams(t, openTestConfig(riRepo), func() (*store.Store, error) {
		st, err := store.Open(path)
		if err == nil && rs.onOpen != nil {
			rs.onOpen(st)
		}
		return st, err
	})
	return rs
}

func (rs *riStore) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", rs.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// pr seeds PR n with facts and a STALE interpretation (waiting on someone-else,
// old panel, no stamp), so the running stamp differs from its row.
func (rs *riStore) pr(t *testing.T, n int, facts string) {
	t.Helper()
	rs.seed.SeedEntityForTest(t, store.Entity{Repo: riRepo, EntityType: "pr", EntityID: riID(n), Facts: facts, AsOf: "2026-09-16T00:00:00Z", ContentHash: fmt.Sprintf("h%d", n)})
	rs.seed.SeedInterpretationForTest(t, store.Interpretation{
		Repo: riRepo, EntityType: "pr", EntityID: riID(n),
		Enrichment: "{}", Urgency: "{}", Dispositions: "[]", MatchReasons: "[]",
		Approvals: `{"waiting_on":"someone-else","reason":"old-reason"}`, Panel: "old-panel", AsOf: "2020-01-01T00:00:00Z",
	})
}

func (rs *riStore) row(t *testing.T, n int) store.Interpretation {
	t.Helper()
	row, found, err := rs.seed.GetInterpretation(riRepo, "pr", riID(n))
	if err != nil || !found {
		t.Fatalf("row %d: found=%v err=%v", n, found, err)
	}
	return row
}

func (rs *riStore) kinds(t *testing.T, n int) []string {
	t.Helper()
	return kindsOf(t, rs.seed, "pr", riID(n))
}

func riRun(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	c := newReinterpretCmd()
	var out, errb bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SetArgs(args)
	c.SilenceUsage = true
	c.SilenceErrors = true
	err = c.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

func riStamp() string { return runningStamp(openTestConfig(riRepo)) }

func riSummary(open, changed, unchanged, stamped, skipped, failed int) string {
	return fmt.Sprintf("reinterpreted %d open PRs: %d changed, %d unchanged; %d not open (stamped only), %d skipped, %d failed\n",
		open, changed, unchanged, stamped, skipped, failed)
}

func riGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if os.Getenv("PG_DESK_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Errorf("%s differs:\n--- want\n%s--- got\n%s", name, want, got)
	}
}

func TestReinterpretRegisteredUnderPR(t *testing.T) {
	c, _, err := rootCmd.Find([]string{"pr", "reinterpret"})
	if err != nil || c.Name() != "reinterpret" || c.Parent() != typeGroup(entityTypePR) {
		t.Fatalf("pr reinterpret not registered: %v, %v", c, err)
	}
}

// INV-REINTERPRET-9: exactly one of <pr> and --open, refused before any store
// is opened.
func TestReinterpretRequiresExactlyOneOfPRAndOpen(t *testing.T) {
	rs := newRIStore(t)
	opened := false
	rs.onOpen = func(*store.Store) { opened = true }
	for _, args := range [][]string{{}, {"1", "--open"}, {"1", "2"}} {
		if _, _, err := riRun(t, args...); err == nil {
			t.Errorf("args %v: error = nil, want a refusal", args)
		}
	}
	if opened {
		t.Error("the store was opened for a refused invocation")
	}
}

// INV-REINTERPRET-9 and -12: a schema-1 store is refused with the cutover
// message and nothing is written.
func TestReinterpretRefusesSchema1Store(t *testing.T) {
	open, _ := newStoreAt(t, "old")
	withOpenSeams(t, openTestConfig(riRepo), open)
	for _, args := range [][]string{{"--open"}, {"1"}} {
		_, _, err := riRun(t, args...)
		wantOldSchemaRefusal(t, err)
	}
}

func TestReinterpretConfigAndRefErrors(t *testing.T) {
	newRIStore(t)
	deskConfigLoad = func(context.Context) (*config.Config, error) { return nil, errors.New("boom") }
	if _, _, err := riRun(t, "--open"); err == nil || !strings.Contains(err.Error(), "load config") {
		t.Errorf("config error = %v", err)
	}
	deskConfigLoad = func(context.Context) (*config.Config, error) { return &config.Config{}, nil }
	if _, _, err := riRun(t, "--open"); err == nil || !strings.Contains(err.Error(), "no repository") {
		t.Errorf("no repo error = %v", err)
	}
	deskConfigLoad = func(context.Context) (*config.Config, error) { return openTestConfig(riRepo), nil }
	if _, _, err := riRun(t, "other/repo#5"); err == nil || !strings.Contains(err.Error(), "other/repo") {
		t.Errorf("unresolvable ref error = %v", err)
	}
}

// The main scenario: --open re-interprets the open PRs (a changed one and an
// unchanged one), stamps the closed and merged ones without touching their
// content or history, and a second run finds nothing behind.
func TestReinterpretOpenReinterpretsOpenAndStampsTheRest(t *testing.T) {
	rs := newRIStore(t)
	rs.pr(t, 10, riFacts("open", false, "me"))    // changed (stale decision)
	rs.pr(t, 11, riFacts("open", false, "me"))    // becomes unchanged below
	rs.pr(t, 12, riFacts("closed", false, "me"))  // not open
	rs.pr(t, 13, riFacts("open", true, "me"))     // merged flag folded in: not open
	rs.pr(t, 14, `{"pr_show":{"state":"weird"}}`) // no recognisable state: not open
	rs.pr(t, 15, `{}`)                            // no pr_show at all: not open

	// Make 11 identical to what the interpreter derives, then behind again.
	if _, _, err := riRun(t, "11"); err != nil {
		t.Fatal(err)
	}
	rs.exec(t, `UPDATE interpretation SET interp_version = NULL WHERE entity_id = ?`, riID(11))
	closedBefore, mergedBefore := rs.row(t, 12), rs.row(t, 13)
	histBefore12, histBefore13 := rs.kinds(t, 12), rs.kinds(t, 13)

	out, errOut, err := riRun(t, "--open", "--verbose")
	if err != nil {
		t.Fatalf("err = %v, stderr %q", err, errOut)
	}
	riGolden(t, "pr_reinterpret_open.verbose.txt", out)
	if !strings.HasSuffix(out, riSummary(2, 1, 1, 4, 0, 0)) {
		t.Errorf("output does not end in the summary: %q", out)
	}

	if got := rs.row(t, 10); got.InterpVersion != riStamp() || got.Panel == "old-panel" {
		t.Errorf("open row = %+v, want re-interpreted under %q", got, riStamp())
	}
	if got := rs.kinds(t, 10); len(got) != 1 || !strings.HasPrefix(got[0], "interpretation_changed") {
		t.Errorf("changed PR history = %v, want one interpretation_changed", got)
	}
	if got := rs.kinds(t, 11); len(got) != 1 {
		t.Errorf("unchanged PR history = %v, want only the record of the earlier run", got)
	}
	// Not open: content identical, stamp current, no new record.
	for _, n := range []int{12, 13} {
		before := closedBefore
		if n == 13 {
			before = mergedBefore
		}
		got := rs.row(t, n)
		if got.InterpVersion != riStamp() {
			t.Errorf("pr %d stamp = %q, want %q", n, got.InterpVersion, riStamp())
		}
		got.InterpVersion, before.InterpVersion = "", ""
		if got != before {
			t.Errorf("pr %d content changed:\nbefore %+v\nafter  %+v", n, before, got)
		}
	}
	if fmt.Sprint(rs.kinds(t, 12)) != fmt.Sprint(histBefore12) || fmt.Sprint(rs.kinds(t, 13)) != fmt.Sprint(histBefore13) {
		t.Error("a not-open PR gained a change record")
	}
	for _, n := range []int{14, 15} {
		if got := rs.row(t, n); got.InterpVersion != riStamp() || got.Panel != "old-panel" {
			t.Errorf("pr %d = %+v, want stamped with content kept", n, got)
		}
	}

	// A second run: nothing behind, nothing written.
	out, _, err = riRun(t, "--open", "--verbose")
	if err != nil || out != riSummary(0, 0, 0, 0, 0, 0) {
		t.Errorf("second run = %q, %v, want an empty pass", out, err)
	}
}

// INV-REINTERPRET-3: <pr> re-interprets the named PR in full whatever its
// state and stamp.
func TestReinterpretNamedClosedPRIsReinterpretedInFull(t *testing.T) {
	rs := newRIStore(t)
	rs.pr(t, 20, riFacts("closed", false, "me"))
	out, _, err := riRun(t, riID(20))
	if err != nil || out != riSummary(1, 1, 0, 0, 0, 0) {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if got := rs.row(t, 20); got.Panel == "old-panel" || got.InterpVersion != riStamp() {
		t.Errorf("closed PR row = %+v, want re-interpreted", got)
	}
	if got := rs.kinds(t, 20); len(got) != 1 {
		t.Errorf("history = %v, want one record", got)
	}
}

// --dry-run prints what would change and writes nothing, not even a stamp.
func TestReinterpretDryRunWritesNothing(t *testing.T) {
	rs := newRIStore(t)
	rs.pr(t, 30, riFacts("open", false, "me"))
	rs.pr(t, 31, riFacts("closed", false, "me"))
	rs.pr(t, 32, `not json`)
	b30, b31, b32 := rs.row(t, 30), rs.row(t, 31), rs.row(t, 32)
	out, _, err := riRun(t, "--open", "--dry-run", "--verbose")
	if err == nil {
		t.Fatal("err = nil, want the failed row to fail the run")
	}
	riGolden(t, "pr_reinterpret_dry_run.verbose.txt", out)
	for n, b := range map[int]store.Interpretation{30: b30, 31: b31, 32: b32} {
		if got := rs.row(t, n); got != b {
			t.Errorf("pr %d row changed under --dry-run: %+v -> %+v", n, b, got)
		}
		if h := rs.kinds(t, n); len(h) != 0 {
			t.Errorf("pr %d history = %v under --dry-run", n, h)
		}
	}
}

// INV-REINTERPRET-8: a failing row keeps its content, is marked failed under
// the running stamp, does not stop the pass, fails the exit code, is skipped
// by the next catch-up and retried by <pr>.
func TestReinterpretFailureIsolationAndRetry(t *testing.T) {
	rs := newRIStore(t)
	rs.pr(t, 40, `not json`)
	rs.pr(t, 41, riFacts("open", false, "me"))
	before := rs.row(t, 40)

	out, errOut, err := riRun(t, "--open")
	if err == nil || exitCodeFor(err) != 1 {
		t.Fatalf("err = %v, want exit 1", err)
	}
	if out != riSummary(1, 1, 0, 0, 0, 1) || !strings.Contains(errOut, riID(40)) {
		t.Errorf("out = %q, stderr = %q", out, errOut)
	}
	got := rs.row(t, 40)
	if got.InterpVersion != riStamp()+store.FailedStampSuffix {
		t.Errorf("failed row stamp = %q", got.InterpVersion)
	}
	got.InterpVersion, before.InterpVersion = "", ""
	if got != before {
		t.Errorf("failed row content changed: %+v -> %+v", before, got)
	}
	if rs.row(t, 41).Panel == "old-panel" {
		t.Error("the healthy row after the failing one was not processed")
	}

	// A second catch-up does not read the failed row again.
	out, _, err = riRun(t, "--open")
	if err != nil || out != riSummary(0, 0, 0, 0, 0, 0) {
		t.Errorf("second pass = %q, %v, want it to skip the failed row", out, err)
	}
	// <pr> retries it: still failing.
	if _, _, err := riRun(t, riID(40)); err == nil {
		t.Error("retry of a still-broken row succeeded")
	}
	// Repair the facts: <pr> now succeeds and stamps it current.
	rs.seed.SeedEntityForTest(t, store.Entity{Repo: riRepo, EntityType: "pr", EntityID: riID(40), Facts: riFacts("open", false, "me"), AsOf: "a", ContentHash: "h40b"})
	if out, _, err := riRun(t, riID(40)); err != nil || out != riSummary(1, 1, 0, 0, 0, 0) {
		t.Errorf("retry after repair = %q, %v", out, err)
	}
	if rs.row(t, 40).InterpVersion != riStamp() {
		t.Error("repaired row is not stamped current")
	}
}

// An interpretation whose entity is gone is marked failed; <pr> naming an
// entity that was never stored fails with no row to mark.
func TestReinterpretEntityGone(t *testing.T) {
	rs := newRIStore(t)
	rs.pr(t, 50, riFacts("open", false, "me"))
	rs.exec(t, `DELETE FROM entity WHERE entity_id = ?`, riID(50))
	if _, _, err := riRun(t, "--open"); err == nil {
		t.Fatal("err = nil, want a failure")
	}
	if got := rs.row(t, 50).InterpVersion; got != riStamp()+store.FailedStampSuffix {
		t.Errorf("stamp = %q, want failed marker", got)
	}
	if _, errOut, err := riRun(t, riID(99)); err == nil || !strings.Contains(errOut, "not stored") {
		t.Errorf("never-stored PR: err = %v, stderr %q", err, errOut)
	}
}

// A hydration that commits different facts between the read and the write
// makes the write skip, counted as skipped, never an error.
func TestReinterpretSkipsWhenFactsChangeUnderneath(t *testing.T) {
	for _, tc := range []struct {
		name  string
		facts string
		step  store.InterpretationWriteStep
	}{
		{"reinterpreted", riFacts("open", false, "me"), store.StepBegin},
		{"stamp only", riFacts("closed", false, "me"), store.StepBegin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs := newRIStore(t)
			rs.pr(t, 60, tc.facts)
			fired := false
			rs.onOpen = func(st *store.Store) {
				st.SetInterpretationWriteHook(func(step store.InterpretationWriteStep) error {
					if step == tc.step && !fired {
						fired = true
						rs.exec(t, `UPDATE entity SET content_hash = 'newer' WHERE entity_id = ?`, riID(60))
					}
					return nil
				})
			}
			before := rs.row(t, 60)
			out, _, err := riRun(t, "--open", "--verbose")
			if err != nil || !strings.HasSuffix(out, riSummary(0, 0, 0, 0, 1, 0)) {
				t.Fatalf("out = %q, err = %v", out, err)
			}
			if rs.row(t, 60) != before {
				t.Error("a skipped write changed the row")
			}
		})
	}
}

// Store faults: each failing step is reported and counted, never panics, and a
// store-stage failure does not mark the row failed.
func TestReinterpretStoreFaults(t *testing.T) {
	boom := errors.New("injected")
	t.Run("write fails and the row is not marked", func(t *testing.T) {
		rs := newRIStore(t)
		rs.pr(t, 70, riFacts("open", false, "me"))
		rs.onOpen = func(st *store.Store) {
			st.SetInterpretationWriteHook(func(step store.InterpretationWriteStep) error {
				if step == store.StepUpsert {
					return boom
				}
				return nil
			})
		}
		_, errOut, err := riRun(t, "--open")
		if err == nil || !strings.Contains(errOut, "injected") {
			t.Fatalf("err = %v, stderr %q", err, errOut)
		}
		if got := rs.row(t, 70).InterpVersion; got != "" {
			t.Errorf("stamp = %q, want the row untouched (still behind)", got)
		}
	})
	t.Run("stamp fails", func(t *testing.T) {
		rs := newRIStore(t)
		rs.pr(t, 71, riFacts("closed", false, "me"))
		rs.onOpen = func(st *store.Store) {
			st.SetInterpretationWriteHook(func(step store.InterpretationWriteStep) error {
				if step == store.StepUpsert {
					return boom
				}
				return nil
			})
		}
		_, errOut, err := riRun(t, "--open")
		if err == nil || !strings.Contains(errOut, "stamp") {
			t.Fatalf("err = %v, stderr %q", err, errOut)
		}
	})
	t.Run("marking failed fails", func(t *testing.T) {
		rs := newRIStore(t)
		rs.pr(t, 72, `not json`)
		rs.onOpen = func(st *store.Store) {
			st.SetInterpretationWriteHook(func(step store.InterpretationWriteStep) error { return boom })
		}
		_, errOut, err := riRun(t, "--open")
		if err == nil || !strings.Contains(errOut, "mark failed") {
			t.Fatalf("err = %v, stderr %q", err, errOut)
		}
	})
	t.Run("reading the entity fails", func(t *testing.T) {
		rs := newRIStore(t)
		rs.pr(t, 73, riFacts("open", false, "me"))
		rs.onOpen = func(st *store.Store) {
			// Break the entity table under the open handle's feet.
			rs.exec(t, `ALTER TABLE entity RENAME TO entity_gone`)
		}
		_, errOut, err := riRun(t, riID(73))
		if err == nil || !strings.Contains(errOut, "get entity") {
			t.Fatalf("err = %v, stderr %q", err, errOut)
		}
	})
	t.Run("reading the interpretation fails", func(t *testing.T) {
		rs := newRIStore(t)
		rs.pr(t, 74, riFacts("open", false, "me"))
		rs.onOpen = func(st *store.Store) {
			rs.exec(t, `ALTER TABLE interpretation RENAME TO interpretation_gone`)
		}
		_, errOut, err := riRun(t, riID(74))
		if err == nil || !strings.Contains(errOut, "read interpretation") {
			t.Fatalf("err = %v, stderr %q", err, errOut)
		}
	})
	t.Run("selecting the behind rows fails", func(t *testing.T) {
		rs := newRIStore(t)
		rs.onOpen = func(st *store.Store) {
			rs.exec(t, `ALTER TABLE interpretation RENAME TO interpretation_gone`)
		}
		if _, _, err := riRun(t, "--open"); err == nil {
			t.Fatal("err = nil, want the behind query's error")
		}
	})
}

// A cancelled context stops the pass between rows.
func TestReinterpretStopsOnCancelledContext(t *testing.T) {
	rs := newRIStore(t)
	rs.pr(t, 80, riFacts("open", false, "me"))
	c := newReinterpretCmd()
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	c.SetArgs([]string{"--open"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.ExecuteContext(ctx); err == nil {
		t.Fatal("err = nil, want the cancelled context")
	}
	if rs.row(t, 80).Panel != "old-panel" {
		t.Error("a cancelled pass wrote a row")
	}
}

// INV-REINTERPRET-2 and the plan's "No network" test: the command runs with no
// pg-connector (or anything else) on PATH, and leaves the entity row exactly as
// it was.
func TestReinterpretMakesNoNetworkCallAndLeavesEntitiesAlone(t *testing.T) {
	rs := newRIStore(t)
	rs.pr(t, 90, riFacts("open", false, "me"))
	before, _, _ := rs.seed.GetEntity(riRepo, "pr", riID(90))
	t.Setenv("PATH", t.TempDir())
	if _, _, err := riRun(t, "--open"); err != nil {
		t.Fatal(err)
	}
	after, _, _ := rs.seed.GetEntity(riRepo, "pr", riID(90))
	if after != before {
		t.Errorf("entity changed:\nbefore %+v\nafter  %+v", before, after)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed pipe") }

// A PR with facts but no interpretation row yet is re-interpreted and counted
// unchanged (there is no old decision to differ from).
func TestReinterpretNamedPRWithNoRowYet(t *testing.T) {
	rs := newRIStore(t)
	rs.seed.SeedEntityForTest(t, store.Entity{Repo: riRepo, EntityType: "pr", EntityID: riID(95), Facts: riFacts("open", false, "me"), AsOf: "a", ContentHash: "h95"})
	out, _, err := riRun(t, riID(95), "--verbose")
	if err != nil || !strings.HasSuffix(out, riSummary(1, 0, 1, 0, 0, 0)) {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if rs.row(t, 95).InterpVersion != riStamp() {
		t.Error("new row is not stamped")
	}
}

// A closed output stream is an error, whichever line fails.
func TestReinterpretReportsOutputErrors(t *testing.T) {
	for _, args := range [][]string{{"--open"}, {"--open", "--dry-run"}} {
		newRIStore(t)
		c := newReinterpretCmd()
		c.SetOut(failingWriter{})
		c.SetErr(&bytes.Buffer{})
		c.SetArgs(args)
		if err := c.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "closed pipe") {
			t.Errorf("args %v: err = %v, want the write error", args, err)
		}
	}
}
