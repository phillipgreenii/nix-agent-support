package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	_ "modernc.org/sqlite" // same driver internal/store registers

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// focusFixedNow is the injected clock of the focus tests: 2026-10-10 12:00 UTC.
var focusFixedNow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// newStubFocusVerb builds a stand-in verb that exercises every shared helper
// the real verbs use, in the spec's check order. It lives in the test only:
// production registers no verb in this packet.
//
//	--exit 0|1|2|3   the exit the stub reports (0 and 2 print the table)
//	--readonly       the stub is a reading verb (no NOTE, no run row)
//	--dry-run        the stub writes no run row
func newStubFocusVerb() *cobra.Command {
	c := &cobra.Command{
		Use:  "stubverb",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			exit, _ := cmd.Flags().GetInt("exit")
			readonly, _ := cmd.Flags().GetBool("readonly")
			dry, _ := cmd.Flags().GetBool("dry-run")
			env, err := openFocusEnv(cmd, !readonly)
			if err != nil {
				return err
			}
			defer env.Close()
			var run *focusRun
			if !readonly {
				run = newFocusRun(cmd.ErrOrStderr(), focusVerbSelect, env.Period, env.Actor, dry, env.Now)
				_ = run.Start(env.Store)
				run.SetCounts(focusCounts{Ranked: 1, Selected: 1}) // not an "empty" select
			}
			finish := func(err error) error {
				if run != nil {
					_ = run.Finish(focusExitCode(err))
				}
				return err
			}
			if err := env.CheckPeriodClosed(); err != nil {
				return finish(err)
			}
			var runID string
			if run != nil {
				runID = run.RunID()
			}
			if env.JSON {
				if err := printFocusJSON(cmd.OutOrStdout(), "stubverb", map[string]any{"period": env.Period.Key}, runID); err != nil {
					return finish(err)
				}
			} else if exit == 0 || exit == 2 {
				fmt.Fprintf(cmd.OutOrStdout(), "TABLE %s\n", env.Period.Key)
			}
			switch exit {
			case 1:
				return finish(focusUsageError("stub usage problem", "fix the stub and re-run"))
			case 2:
				return finish(focusPartialError("stub partial", "re-run the stub"))
			case 3:
				return finish(focusTotalError("stub total failure", "check the stub store"))
			}
			return finish(nil)
		},
	}
	addFocusFlags(c)
	c.Flags().Int("exit", 0, "")
	c.Flags().Bool("readonly", false, "")
	c.Flags().Bool("dry-run", false, "")
	return c
}

// focusFixture is a migrated store under a temp dir plus the seams pointed
// at it.
type focusFixture struct {
	path string
	cfg  *config.Config
}

// newFocusFixture points the config and store seams at a store of the given
// kind ("new" = migrated, "old" = not cut over) and fixes the clock.
func newFocusFixture(t *testing.T, kind string) *focusFixture {
	t.Helper()
	path := storeAtVersion(t, kind)
	cfg := openTestConfig("o/r")
	cfg.Actor = "tester"
	cfg.Focus.TimeZone = "UTC"
	withOpenSeams(t, cfg, func() (*store.Store, error) { return store.Open(path) })
	orig := focusNow
	t.Cleanup(func() { focusNow = orig })
	focusNow = func() time.Time { return focusFixedNow }
	t.Setenv(outputEnvVar, "")
	return &focusFixture{path: path, cfg: cfg}
}

// open returns a fresh handle on the fixture store (caller closes).
func (f *focusFixture) open(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(f.path)
	if err != nil {
		t.Fatalf("open fixture store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// runFocusVerb runs `pg-desk focus stubverb <args>` through the real command
// tree and returns stdout, stderr and the error main would print and exit on.
func runFocusVerb(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	verb := newStubFocusVerb()
	group := focusGroup()
	group.AddCommand(verb)
	var out, errb bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errb)
	rootCmd.SetArgs(append([]string{"focus", "stubverb"}, args...))
	defer func() {
		group.RemoveCommand(verb)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	}()
	err = rootCmd.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

func TestFocusGroupIsRegisteredAndListed(t *testing.T) {
	c, _, err := rootCmd.Find([]string{"focus"})
	if err != nil || c == nil || c.Name() != "focus" {
		t.Fatalf("rootCmd.Find(focus) = %v, %v", c, err)
	}
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"--help"})
	t.Cleanup(func() { rootCmd.SetOut(nil); rootCmd.SetErr(nil); rootCmd.SetArgs(nil) })
	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("pg-desk --help: %v", err)
	}
	if !strings.Contains(out.String(), "focus") {
		t.Errorf("pg-desk --help does not list the focus group:\n%s", out.String())
	}
	out.Reset()
	rootCmd.SetArgs([]string{"focus", "--help"})
	if err := rootCmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("pg-desk focus --help: %v", err)
	}
	for _, want := range []string{"pg-desk focus", "pg-desk.focus/v1", "6 period closed"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("focus --help missing %q:\n%s", want, out.String())
		}
	}
}

func TestFocusSharedFlagsAndHiddenPeriod(t *testing.T) {
	newFocusFixture(t, "new")
	out, _, err := runFocusVerb(t, "--help")
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	for _, want := range []string{"--date", "--json"} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "--period") {
		t.Errorf("--period must be hidden from help until a second value exists:\n%s", out)
	}
	// Accepted: "day".
	if _, _, err := runFocusVerb(t, "--period", "day"); err != nil {
		t.Errorf("--period day: %v", err)
	}
	// Anything else is a usage error with a remedy.
	for _, bad := range []string{"week", "sprint", "month", ""} {
		_, _, err := runFocusVerb(t, "--period", bad)
		assertFocusExit(t, err, 1, "--period")
	}
	// An unknown flag is a usage error with a remedy too.
	_, _, err = runFocusVerb(t, "--nope")
	assertFocusExit(t, err, 1, "stubverb --help")
}

// assertFocusExit checks the exit code and that the error is the one-line
// "<message>; <remedy>" main prints on stderr, containing want.
func assertFocusExit(t *testing.T, err error, code int, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want exit %d containing %q, got nil error", code, want)
	}
	if got := exitCodeFor(err); got != code {
		t.Errorf("exit code = %d, want %d (err: %v)", got, code, err)
	}
	msg := err.Error()
	if strings.Contains(msg, "\n") {
		t.Errorf("stderr remedy must be ONE line, got %q", msg)
	}
	if !strings.Contains(msg, "; ") {
		t.Errorf("error %q carries no one-line remedy after %q", msg, "; ")
	}
	if !strings.Contains(msg, want) {
		t.Errorf("error %q does not contain %q", msg, want)
	}
}

func TestFocusUnmigratedStoreRefusal(t *testing.T) {
	f := newFocusFixture(t, "old")
	_, stderr, err := runFocusVerb(t)
	assertFocusExit(t, err, 1, "not migrated for the focus tables")
	if !strings.Contains(err.Error(), "pg-desk migrate --cutover") {
		t.Errorf("refusal must name the way out, got %q", err)
	}
	if stderr != "" {
		t.Errorf("nothing may be printed before the refusal, got %q", stderr)
	}
	// Nothing applied: the store is still on the old schema.
	st := f.open(t)
	if v, verr := st.SchemaVersion(); verr != nil || v >= store.NewSchemaVersion {
		t.Errorf("store schema version = %d, %v; the refusal must apply nothing", v, verr)
	}
}

func TestFocusNoteForNonTodayDateOnWritingVerbs(t *testing.T) {
	newFocusFixture(t, "new")
	const note = "NOTE: writing into period 2026-10-09, not today\n"
	_, stderr, err := runFocusVerb(t, "--date", "2026-10-09")
	if err != nil {
		t.Fatalf("stub: %v", err)
	}
	if !strings.Contains(stderr, note) {
		t.Errorf("stderr missing %q:\n%s", note, stderr)
	}
	// Today, spelled either way: no NOTE.
	for _, args := range [][]string{{}, {"--date", "2026-10-10"}, {"--date", "2026-10-10"}} {
		_, stderr, err := runFocusVerb(t, args...)
		if err != nil {
			t.Fatalf("stub %v: %v", args, err)
		}
		if strings.Contains(stderr, "NOTE") {
			t.Errorf("today must not print a NOTE (args %v):\n%s", args, stderr)
		}
	}
	// A reading verb never writes, so it never says it is writing.
	_, stderr, err = runFocusVerb(t, "--date", "2026-10-09", "--readonly")
	if err != nil {
		t.Fatalf("stub readonly: %v", err)
	}
	if strings.Contains(stderr, "NOTE") {
		t.Errorf("a reading verb must not print the NOTE:\n%s", stderr)
	}
	// An unparseable date is a usage error and prints nothing else.
	_, _, err = runFocusVerb(t, "--date", "tomorrow")
	assertFocusExit(t, err, 1, `--date "tomorrow"`)
}

func TestFocusAddressedPeriodNeverComesFromStoredDraft(t *testing.T) {
	f := newFocusFixture(t, "new")
	st := f.open(t)
	if _, err := st.FocusDraftReplace(store.FocusDraft{
		PeriodType: store.FocusPeriodDay, PeriodKey: "2026-10-08", Cap: 5,
		MadeAt: "2026-10-08T23:50:00Z", BodyJSON: "{}",
	}); err != nil {
		t.Fatalf("store draft: %v", err)
	}
	out, _, err := runFocusVerb(t, "--json", "--readonly")
	if err != nil {
		t.Fatalf("stub: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if got["period"] != "2026-10-10" {
		t.Errorf("addressed period = %v, want today 2026-10-10 despite the stored draft for 2026-10-08", got["period"])
	}
}

func TestResolveFocusPeriod(t *testing.T) {
	newCmd := func(args ...string) *cobra.Command {
		c := &cobra.Command{Use: "x", RunE: func(*cobra.Command, []string) error { return nil }}
		addFocusFlags(c)
		if err := c.ParseFlags(args); err != nil {
			t.Fatalf("parse %v: %v", args, err)
		}
		return c
	}
	ny := &config.Config{Focus: config.FocusConfig{TimeZone: "America/New_York"}}
	// 03:00 UTC on the 10th is still the 9th in New York.
	now := time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name      string
		cfg       *config.Config
		args      []string
		wantKey   string
		wantToday bool
		wantErr   bool
	}{
		{"default is today in focus.time_zone", ny, nil, "2026-10-09", true, false},
		{"explicit today", ny, []string{"--date", "2026-10-09"}, "2026-10-09", true, false},
		{"lenient spelling is canonicalised", ny, []string{"--date", "2026-9-3"}, "2026-09-03", false, false},
		{"other day is not today", ny, []string{"--date", "2026-10-10"}, "2026-10-10", false, false},
		{"utc zone", &config.Config{Focus: config.FocusConfig{TimeZone: "UTC"}}, nil, "2026-10-10", true, false},
		{"bad date", ny, []string{"--date", "10/10/2026"}, "", false, true},
		{"period week", ny, []string{"--period", "week"}, "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, today, err := resolveFocusPeriod(newCmd(tc.args...), tc.cfg, now)
			if tc.wantErr {
				assertFocusExit(t, err, 1, "")
				return
			}
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if p.Type != "day" || p.Key != tc.wantKey || today != tc.wantToday {
				t.Errorf("got (%+v, today=%v), want (day %s, today=%v)", p, today, tc.wantKey, tc.wantToday)
			}
		})
	}
	// A nil config addresses the local zone and does not panic.
	if _, _, err := resolveFocusPeriod(newCmd(), nil, now); err != nil {
		t.Errorf("nil cfg: %v", err)
	}
}

func TestDraftPredicates(t *testing.T) {
	day := func(key string) store.FocusDraft {
		return store.FocusDraft{PeriodType: "day", PeriodKey: key}
	}
	today := focusPeriod{Type: "day", Key: "2026-10-10"}
	closed := func(keys ...string) func(string) bool {
		return func(k string) bool {
			for _, c := range keys {
				if c == k {
					return true
				}
			}
			return false
		}
	}

	for _, tc := range []struct {
		name       string
		draft      store.FocusDraft
		addressed  focusPeriod
		applicable bool
	}{
		{"same period", day("2026-10-10"), today, true},
		{"earlier draft", day("2026-10-09"), today, false},
		{"later draft", day("2026-10-11"), today, false},
		{"other type", store.FocusDraft{PeriodType: "week", PeriodKey: "2026-10-10"}, today, false},
		{"explicit other date", day("2026-10-10"), focusPeriod{Type: "day", Key: "2026-10-09"}, false},
	} {
		t.Run("applicable/"+tc.name, func(t *testing.T) {
			if got := isApplicableDraft(tc.draft, tc.addressed); got != tc.applicable {
				t.Errorf("isApplicableDraft = %v, want %v", got, tc.applicable)
			}
		})
	}

	for _, tc := range []struct {
		name   string
		draft  store.FocusDraft
		closed func(string) bool
		stale  bool
	}{
		{"earlier period", day("2026-10-09"), closed(), true},
		{"today and open", day("2026-10-10"), closed(), false},
		{"today but closed", day("2026-10-10"), closed("2026-10-10"), true},
		{"later period, open", day("2026-10-11"), closed(), false},
		{"later period, closed", day("2026-10-11"), closed("2026-10-11"), true},
		{"nil closed func", day("2026-10-10"), nil, false},
		{"midnight rollover", day("2026-10-09"), closed(), true},
	} {
		t.Run("stale/"+tc.name, func(t *testing.T) {
			if got := isStaleDraft(tc.draft, "2026-10-10", tc.closed); got != tc.stale {
				t.Errorf("isStaleDraft = %v, want %v", got, tc.stale)
			}
		})
	}
}

// TestFocusExitCodes is the verb x documented-code table. It grows with each
// verb packet; this packet covers what exists: usage, an unmigrated store, a
// closed period, partial and total failure, for the stub verb.
func TestFocusExitCodes(t *testing.T) {
	closePeriod := func(t *testing.T, f *focusFixture, key string) {
		t.Helper()
		st := f.open(t)
		if err := st.FocusLockTx(func(tx *store.FocusTx) error {
			p, err := tx.GetOrCreatePeriod("day", key)
			if err != nil {
				return err
			}
			return tx.ClosePeriod(p.ID, "2026-10-09T20:00:00Z", "done")
		}); err != nil {
			t.Fatalf("close period: %v", err)
		}
	}

	for _, tc := range []struct {
		name       string
		kind       string
		args       []string
		prep       func(t *testing.T, f *focusFixture)
		wantCode   int
		wantTable  bool
		wantRemedy string // substring of the stderr remedy; "" for exit 0
	}{
		{name: "ok", kind: "new", wantCode: 0, wantTable: true},
		{name: "usage: stub reports usage", kind: "new", args: []string{"--exit", "1"}, wantCode: 1, wantRemedy: "fix the stub"},
		{name: "usage: period week", kind: "new", args: []string{"--period", "week"}, wantCode: 1, wantRemedy: "pass --period day"},
		{name: "usage: bad date", kind: "new", args: []string{"--date", "x"}, wantCode: 1, wantRemedy: "pass --date YYYY-MM-DD"},
		{name: "usage: unmigrated store", kind: "old", wantCode: 1, wantRemedy: "pg-desk migrate --cutover"},
		{name: "partial", kind: "new", args: []string{"--exit", "2"}, wantCode: 2, wantTable: true, wantRemedy: "re-run"},
		{name: "total failure", kind: "new", args: []string{"--exit", "3"}, wantCode: 3, wantRemedy: "check the stub store"},
		{
			name: "period closed", kind: "new", args: []string{"--date", "2026-10-09"}, wantCode: 6,
			prep:       func(t *testing.T, f *focusFixture) { closePeriod(t, f, "2026-10-09") },
			wantRemedy: "use --date <tomorrow>",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFocusFixture(t, tc.kind)
			if tc.prep != nil {
				tc.prep(t, f)
			}
			stdout, _, err := runFocusVerb(t, tc.args...)
			if got := focusExitCode(err); got != tc.wantCode {
				t.Fatalf("exit code = %d, want %d (err: %v)", got, tc.wantCode, err)
			}
			if hasTable := strings.Contains(stdout, "TABLE"); hasTable != tc.wantTable {
				t.Errorf("stdout table present = %v, want %v:\n%s", hasTable, tc.wantTable, stdout)
			}
			if tc.wantCode == 0 {
				return
			}
			assertFocusExit(t, err, tc.wantCode, "")
			if !strings.Contains(err.Error(), tc.wantRemedy) {
				t.Errorf("remedy %q does not contain %q", err.Error(), tc.wantRemedy)
			}
			if tc.wantCode == 4 || tc.wantCode == 5 || tc.wantCode == 7 {
				t.Errorf("exit %d is never used by focus", tc.wantCode)
			}
		})
	}
}

func TestFocusExitCodeVocabularyIsPinned(t *testing.T) {
	if exitPeriodClosed != 6 {
		t.Errorf("exitPeriodClosed = %d, want 6", exitPeriodClosed)
	}
	for name, err := range map[string]error{
		"usage":   focusUsageError("m", "r"),
		"partial": focusPartialError("m", "r"),
		"total":   focusTotalError("m", "r"),
		"closed":  focusPeriodClosedError("2026-10-09"),
	} {
		switch c := exitCodeFor(err); c {
		case 1, 2, 3, 6:
		default:
			t.Errorf("%s error exits %d, outside the focus vocabulary 0/1/2/3/6", name, c)
		}
	}
	if got := exitCodeFor(focusUsageError("m", "r")); got != 1 {
		t.Errorf("usage = %d", got)
	}
	// ensureFocusRemedy keeps the code and adds a remedy to a bare error.
	wrapped := ensureFocusRemedy(newExitError(exitTotal, fmt.Errorf("boom")), "look at the store")
	if exitCodeFor(wrapped) != exitTotal || !strings.HasSuffix(wrapped.Error(), "; look at the store") {
		t.Errorf("ensureFocusRemedy = %q (exit %d)", wrapped, exitCodeFor(wrapped))
	}
	if got := ensureFocusRemedy(focusUsageError("m", "r"), "other"); got.Error() != "m; r" {
		t.Errorf("ensureFocusRemedy replaced an existing remedy: %q", got)
	}
}

func TestFocusJSONEnvelopeFromEnvironment(t *testing.T) {
	newFocusFixture(t, "new")
	t.Setenv(outputEnvVar, "json")
	out, _, err := runFocusVerb(t)
	if err != nil {
		t.Fatalf("stub: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("PG_DESK_OUTPUT=json must print JSON: %v\n%s", err, out)
	}
	if got["contract"] != "pg-desk.focus/v1" {
		t.Errorf("contract = %v", got["contract"])
	}
	if id, _ := got["run_id"].(string); len(id) != 26 {
		t.Errorf("a writing verb's JSON carries its run_id, got %v", got["run_id"])
	}
}

// TestFocusJSONGolden pins the envelope convention: a contract member, the
// verb, the payload members, and run_id only for a verb that writes.
func TestFocusJSONGolden(t *testing.T) {
	var b bytes.Buffer
	if err := printFocusJSON(&b, "select", map[string]any{
		"period": "2026-10-10",
		"count":  2,
		// A payload may not impersonate the envelope.
		"contract": "evil",
		"run_id":   "evil",
	}, "01ARYZ6S41TSV4RRFFQ69G5FAV"); err != nil {
		t.Fatalf("printFocusJSON: %v", err)
	}
	const wantWriting = `{
  "contract": "pg-desk.focus/v1",
  "count": 2,
  "period": "2026-10-10",
  "run_id": "01ARYZ6S41TSV4RRFFQ69G5FAV",
  "verb": "select"
}
`
	if b.String() != wantWriting {
		t.Errorf("writing-verb envelope:\n%s\nwant:\n%s", b.String(), wantWriting)
	}

	b.Reset()
	if err := printFocusJSON(&b, "show", map[string]any{"period": "2026-10-10"}, ""); err != nil {
		t.Fatalf("printFocusJSON: %v", err)
	}
	const wantReading = `{
  "contract": "pg-desk.focus/v1",
  "period": "2026-10-10",
  "verb": "show"
}
`
	if b.String() != wantReading {
		t.Errorf("reading-verb envelope:\n%s\nwant:\n%s", b.String(), wantReading)
	}
}

// TestFocusRunRowWrittenByStubVerbs proves the integration of the helpers: a
// writing stub verb leaves one focus_run row per run, whatever the exit,
// including a total failure on a writable store, and the usage and
// period_closed outcomes are counted.
func TestFocusRunRowWrittenByStubVerbs(t *testing.T) {
	f := newFocusFixture(t, "new")
	st := f.open(t)
	if err := st.FocusLockTx(func(tx *store.FocusTx) error {
		p, err := tx.GetOrCreatePeriod("day", "2026-10-09")
		if err != nil {
			return err
		}
		return tx.ClosePeriod(p.ID, "2026-10-09T20:00:00Z", "")
	}); err != nil {
		t.Fatalf("close period: %v", err)
	}
	for _, args := range [][]string{
		{},                                    // ok
		{"--exit", "1"},                       // usage
		{"--exit", "2"},                       // partial
		{"--exit", "3"},                       // total failure, store writable
		{"--date", "2026-10-09"},              // period closed
		{"--readonly", "--date", "2026-10-9"}, // reading verb: no row
		{"--dry-run", "--exit", "3"},          // dry run: no row
	} {
		runFocusVerb(t, args...) //nolint:errcheck // exit codes are asserted elsewhere
	}
	stats, err := st.FocusRunStats()
	if err != nil {
		t.Fatalf("FocusRunStats: %v", err)
	}
	for outcome, want := range map[string]int{"ok": 1, "usage": 1, "partial": 1, "total": 1, "period_closed": 1} {
		if got := stats.ByVerbOutcome[[2]string{"select", outcome}]; got != want {
			t.Errorf("runs{select,%s} = %d, want %d (all: %v)", outcome, got, want, stats.ByVerbOutcome)
		}
	}
	total := 0
	for _, n := range stats.ByVerbOutcome {
		total += n
	}
	if total != 5 {
		t.Errorf("focus_run rows = %d, want 5 (a reading verb and a dry run write none): %v", total, stats.ByVerbOutcome)
	}
}

// snapshotAllTables dumps every table of the SQLite file at path, sorted, so
// a dry-run test can assert that NOTHING changed.
func snapshotAllTables(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer func() { _ = db.Close() }()
	names, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var tables []string
	for names.Next() {
		var n string
		if err := names.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		tables = append(tables, n)
	}
	_ = names.Close()
	var sb strings.Builder
	for _, tbl := range tables {
		rows, err := db.Query("SELECT * FROM " + tbl + " ORDER BY 1, 2") //nolint:gosec // table names come from sqlite_master
		if err != nil {
			t.Fatalf("select %s: %v", tbl, err)
		}
		cols, _ := rows.Columns()
		fmt.Fprintf(&sb, "## %s %v\n", tbl, cols)
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatalf("scan %s: %v", tbl, err)
			}
			fmt.Fprintf(&sb, "%v\n", vals)
		}
		_ = rows.Close()
	}
	return sb.String()
}

func TestFocusDryRunWritesNothingInAnyTable(t *testing.T) {
	f := newFocusFixture(t, "new")
	before := snapshotAllTables(t, f.path)
	for _, args := range [][]string{
		{"--dry-run"},
		{"--dry-run", "--exit", "3"},
		{"--dry-run", "--date", "2026-10-09"},
		{"--dry-run", "--json"},
	} {
		_, stderr, _ := runFocusVerb(t, args...)
		line := lastLine(stderr)
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("dry run %v printed no JSON stderr line: %v\n%s", args, err, stderr)
		}
		if rec["dry_run"] != true {
			t.Errorf("dry run %v: dry_run = %v, want true", args, rec["dry_run"])
		}
	}
	if after := snapshotAllTables(t, f.path); after != before {
		t.Errorf("a dry run changed the store.\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}
