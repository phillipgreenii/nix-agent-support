package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/trackercompare"
)

// The tracker side is the real-ingest fixture internal/trackeradapter/testdata/tracker.sql
// (five calls, see its gen.sh). The CETA side is a real ask-log database (the
// store's own schema) seeded to agree with it, then mutated per case. Every
// call of the fixture starts at 2026-09-21T14:13:20Z.
const ctCreated = "2026-09-21T14:13:20Z"

// ctSeed writes the ask log into a fresh XDG_DATA_HOME. The four rows agree
// with the tracker fixture on every compared field.
func ctSeed(t *testing.T, mutate ...string) {
	t.Helper()
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	store, err := asklog.NewStore(filepath.Join(data, "claude-extended-tool-approver", "asks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	_, err = store.DB().Exec(`INSERT INTO tool_decisions
		(id, session_id, cwd, tool_name, tool_use_id, tool_input_hash, tool_input_json,
		 hook_decision, outcome, outcome_notes, permission_mode, sandbox_enabled, tool_response_exit_code, created_at)
		VALUES
		 (1,'sess-a','/x','Bash','tu-a1','h1','{}','allow','approved',NULL,'default',1,0,'` + ctCreated + `'),
		 (2,'sess-a','/x','Bash','tu-a2','h2','{}','ask','failed','tool_failure: boom','default',0,1,'` + ctCreated + `'),
		 (3,'sess-b','/x','Bash','tu-b1','h3','{}','ask','rejected',NULL,'dontAsk',1,NULL,'` + ctCreated + `'),
		 (4,'sess-b','/x','Write','tu-b2','h4','{}',NULL,'approved',NULL,'default',NULL,NULL,'` + ctCreated + `')`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, q := range mutate {
		if _, err := store.DB().Exec(q); err != nil {
			t.Fatalf("mutate %q: %v", q, err)
		}
	}
}

// ctRun runs compare-tracker in-process with --format json and returns the
// report and the command's error.
func ctRun(t *testing.T, args ...string) (trackercompare.Report, error) {
	t.Helper()
	out, err := ctRunRaw(t, append([]string{"--format", "json"}, args...)...)
	var rep trackercompare.Report
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("not json: %v\n%s", jerr, out)
	}
	return rep, err
}

func ctRunRaw(t *testing.T, args ...string) (string, error) {
	t.Helper()
	db := trackerFixture(t, "tracker.db")
	var buf bytes.Buffer
	cmd := newCompareTrackerCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(append([]string{"--tracker-db", db, "--since", "2026-09-21"}, args...))
	err := cmd.Execute()
	return buf.String(), err
}

func ctExit(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var ee exitCodeError
	if errors.As(err, &ee) {
		return ee.code
	}
	t.Fatalf("unexpected error: %v", err)
	return -1
}

func TestCompareTracker_NoDifferencesIsClean(t *testing.T) {
	ctSeed(t)
	rep, err := ctRun(t)
	if code := ctExit(t, err); code != 0 {
		t.Fatalf("exit %d, report %+v", code, rep)
	}
	if !rep.Clean || rep.Matched != 4 || rep.CetaCalls != 4 || rep.TrackerCalls != 4 ||
		len(rep.OnlyCeta)+len(rep.OnlyTracker)+len(rep.Differences)+len(rep.Duplicates)+len(rep.SessionsWithoutTracker) != 0 {
		t.Errorf("not clean: %+v", rep)
	}
	out, err := ctRunRaw(t)
	if ctExit(t, err) != 0 || !strings.Contains(out, "Result: clean") {
		t.Errorf("table output not clean:\n%s", out)
	}
}

func TestCompareTracker_OnlyOneSide(t *testing.T) {
	ctSeed(t,
		`INSERT INTO tool_decisions (id,session_id,cwd,tool_name,tool_use_id,tool_input_hash,tool_input_json,hook_decision,outcome,created_at)
		 VALUES (9,'sess-a','/x','Bash','tu-zz','h9','{}','allow','approved','`+ctCreated+`')`,
		`DELETE FROM tool_decisions WHERE tool_use_id = 'tu-b2'`)
	rep, err := ctRun(t)
	if ctExit(t, err) != 2 {
		t.Fatalf("want exit 2, got %v", err)
	}
	if len(rep.OnlyCeta) != 1 || rep.OnlyCeta[0].ToolUseID != "tu-zz" || rep.OnlyCeta[0].CetaID != 9 {
		t.Errorf("only ceta: %+v", rep.OnlyCeta)
	}
	if len(rep.OnlyTracker) != 1 || rep.OnlyTracker[0].ToolUseID != "tu-b2" || rep.OnlyTracker[0].CallID != 21 {
		t.Errorf("only tracker: %+v", rep.OnlyTracker)
	}
	if rep.Clean || len(rep.Differences) != 0 {
		t.Errorf("unexpected: %+v", rep)
	}
}

func TestCompareTracker_EachDifferenceCategory(t *testing.T) {
	cases := []struct {
		name, mutate, category, ceta, tracker string
		toolUse                               string
	}{
		{"verdict", `UPDATE tool_decisions SET hook_decision='deny' WHERE tool_use_id='tu-a1'`, "verdict", "deny", "allow", "tu-a1"},
		{"verdict missing in tracker", `UPDATE tool_decisions SET hook_decision='allow' WHERE tool_use_id='tu-b2'`, "verdict", "allow", "(none)", "tu-b2"},
		{"outcome approved vs denied", `UPDATE tool_decisions SET outcome='denied' WHERE tool_use_id='tu-a1'`, "outcome", "denied", "approved", "tu-a1"},
		{"outcome vs rejected", `UPDATE tool_decisions SET outcome='rejected' WHERE tool_use_id='tu-a1'`, "outcome", "rejected", "approved", "tu-a1"},
		{"outcome vs failed", `UPDATE tool_decisions SET outcome='failed' WHERE tool_use_id='tu-a1'`, "outcome", "failed", "approved", "tu-a1"},
		{"outcome vs unresolved", `UPDATE tool_decisions SET outcome='unresolved' WHERE tool_use_id='tu-a1'`, "outcome", "unresolved", "approved", "tu-a1"},
		{"outcome vs pending", `UPDATE tool_decisions SET outcome='pending' WHERE tool_use_id='tu-b1'`, "outcome", "pending", "rejected", "tu-b1"},
		{"permission_mode", `UPDATE tool_decisions SET permission_mode='plan' WHERE tool_use_id='tu-a1'`, "permission_mode", "plan", "default", "tu-a1"},
		{"permission_mode absent", `UPDATE tool_decisions SET permission_mode=NULL WHERE tool_use_id='tu-a1'`, "permission_mode", "(none)", "default", "tu-a1"},
		{"agent_type", `UPDATE tool_decisions SET agent_type='Explore' WHERE tool_use_id='tu-a1'`, "agent_type", "Explore", "(none)", "tu-a1"},
		{"sandbox_enabled", `UPDATE tool_decisions SET sandbox_enabled=0 WHERE tool_use_id='tu-a1'`, "sandbox_enabled", "0", "1", "tu-a1"},
		{"sandbox_enabled null", `UPDATE tool_decisions SET sandbox_enabled=1 WHERE tool_use_id='tu-b2'`, "sandbox_enabled", "1", "(none)", "tu-b2"},
		{"is_error", `UPDATE tool_decisions SET tool_response_exit_code=1 WHERE tool_use_id='tu-a1'`, "is_error", "true", "false", "tu-a1"},
		{"denial reason presence", `UPDATE tool_decisions SET outcome_notes='auto_mode_classifier: no' WHERE tool_use_id='tu-a1'`, "denial_reason", "true", "false", "tu-a1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctSeed(t, tc.mutate)
			rep, err := ctRun(t)
			if ctExit(t, err) != 2 {
				t.Fatalf("want exit 2, got %v", err)
			}
			if len(rep.Differences) != 1 {
				t.Fatalf("want exactly one difference, got %+v", rep.Differences)
			}
			d := rep.Differences[0]
			if d.Category != tc.category || d.Ceta != tc.ceta || d.Tracker != tc.tracker || d.ToolUseID != tc.toolUse {
				t.Errorf("difference = %+v, want %s ceta=%s tracker=%s on %s", d, tc.category, tc.ceta, tc.tracker, tc.toolUse)
			}
			if rep.DifferenceCounts[tc.category] != 1 || rep.Clean {
				t.Errorf("counts/clean: %+v", rep)
			}
			if len(rep.OnlyCeta)+len(rep.OnlyTracker) != 0 {
				t.Errorf("a matched call must not be listed as one-sided: %+v", rep)
			}
		})
	}
}

func TestCompareTracker_IsErrorOnlyWhenBothKnown(t *testing.T) {
	// CETA knows nothing of is_error (no exit code, not failed): no difference
	// even though the tracker knows it.
	ctSeed(t, `UPDATE tool_decisions SET tool_response_exit_code=NULL WHERE tool_use_id='tu-a1'`)
	rep, err := ctRun(t)
	if ctExit(t, err) != 0 {
		t.Fatalf("%v %+v", err, rep.Differences)
	}
}

func TestCompareTracker_SessionWithoutTrackerFile(t *testing.T) {
	ctSeed(t,
		// sess-c has a ceta event file in the tracker and no tracker file.
		`INSERT INTO tool_decisions (id,session_id,cwd,tool_name,tool_use_id,tool_input_hash,tool_input_json,hook_decision,outcome,created_at)
		 VALUES (9,'sess-c','/x','Bash','tu-c1','h9','{}','abstain','approved','`+ctCreated+`')`,
		// sess-z is unknown to the tracker altogether.
		`INSERT INTO tool_decisions (id,session_id,cwd,tool_name,tool_use_id,tool_input_hash,tool_input_json,hook_decision,outcome,created_at)
		 VALUES (10,'sess-z','/x','Bash','tu-z1','h10','{}','allow','approved','`+ctCreated+`'),
		        (11,'sess-z','/x','Bash','tu-z2','h11','{}','allow','approved','`+ctCreated+`')`)
	rep, err := ctRun(t)
	if ctExit(t, err) != 2 {
		t.Fatalf("want exit 2, got %v", err)
	}
	want := []trackercompare.SessionNoTracker{{SessionID: "sess-c", CetaRows: 1}, {SessionID: "sess-z", CetaRows: 2}}
	if len(rep.SessionsWithoutTracker) != 2 || rep.SessionsWithoutTracker[0] != want[0] || rep.SessionsWithoutTracker[1] != want[1] {
		t.Errorf("sessions without tracker = %+v, want %+v", rep.SessionsWithoutTracker, want)
	}
	// sess-c's call is carried by ceta.* events only, so the tracker did not record it.
	found := false
	for _, c := range rep.OnlyCeta {
		found = found || c.ToolUseID == "tu-c1"
	}
	if !found || len(rep.OnlyCeta) != 3 {
		t.Errorf("only ceta = %+v", rep.OnlyCeta)
	}
	out, _ := ctRunRaw(t)
	if !strings.Contains(out, "sess-c (1 CETA rows)") || !strings.Contains(out, "sess-z (2 CETA rows)") {
		t.Errorf("table output does not name the sessions:\n%s", out)
	}
}

func TestCompareTracker_DuplicateAndUnkeyedCetaRows(t *testing.T) {
	ctSeed(t,
		`INSERT INTO tool_decisions (id,session_id,cwd,tool_name,tool_use_id,tool_input_hash,tool_input_json,hook_decision,outcome,permission_mode,sandbox_enabled,tool_response_exit_code,created_at)
		 VALUES (9,'sess-a','/x','Bash','tu-a1','h1','{}','allow','approved','default',1,0,'`+ctCreated+`')`,
		`INSERT INTO tool_decisions (id,session_id,cwd,tool_name,tool_use_id,tool_input_hash,tool_input_json,hook_decision,outcome,created_at)
		 VALUES (10,'sess-a','/x','Bash',NULL,'h10','{}','allow','approved','`+ctCreated+`')`)
	rep, err := ctRun(t)
	if ctExit(t, err) != 2 {
		t.Fatalf("want exit 2, got %v", err)
	}
	if len(rep.Duplicates) != 1 || rep.Duplicates[0] != (trackercompare.Duplicate{Side: "ceta", SessionID: "sess-a", ToolUseID: "tu-a1", Count: 2}) {
		t.Errorf("duplicates = %+v", rep.Duplicates)
	}
	if rep.UnkeyedCeta != 1 {
		t.Errorf("unkeyed = %d", rep.UnkeyedCeta)
	}
	if len(rep.Differences) != 0 {
		t.Errorf("the latest duplicate agrees with the tracker: %+v", rep.Differences)
	}
}

func TestCompareTracker_WindowSelectsCalls(t *testing.T) {
	ctSeed(t)
	// Entirely after the calls: nothing selected, clean.
	rep, err := ctRun(t, "--since", "2026-09-22")
	if ctExit(t, err) != 0 || rep.CetaCalls != 0 || rep.TrackerCalls != 0 {
		t.Errorf("after-window: %v %+v", err, rep)
	}
	// until is exclusive: ending exactly at the calls' second excludes the CETA
	// rows (14:13:20.000) and the tracker calls (14:13:20.001+) alike.
	rep, err = ctRun(t, "--since", "2026-09-21T14:00:00Z", "--until", "2026-09-21T14:13:20Z")
	if ctExit(t, err) != 0 || rep.CetaCalls != 0 || rep.TrackerCalls != 0 {
		t.Errorf("until exclusive: %v %+v", err, rep)
	}
	rep, err = ctRun(t, "--since", "2026-09-21T14:13:00Z", "--until", "2026-09-21T14:14:00Z")
	if ctExit(t, err) != 0 || rep.Matched != 4 {
		t.Errorf("inside: %v %+v", err, rep)
	}
}

func TestCompareTracker_EitherSideTimeSelectsAndBothAreReadWhole(t *testing.T) {
	// a1's CETA row is stamped one second before the window opens, while the
	// tracker's call starts inside it: the key is compared, never reported as
	// only-the-tracker-recorded it.
	ctSeed(t, `UPDATE tool_decisions SET created_at='2026-09-21T14:13:19Z' WHERE tool_use_id='tu-a1'`)
	rep, err := ctRun(t, "--since", "2026-09-21T14:13:20Z", "--until", "2026-09-21T14:14:00Z")
	if ctExit(t, err) != 0 || rep.Matched != 4 || len(rep.OnlyTracker) != 0 {
		t.Errorf("%v %+v", err, rep)
	}
}

func TestCompareTracker_IngestSkippedCountsForTheWindow(t *testing.T) {
	ctSeed(t)
	// The fixture's one ingest run started at 2026-10-29T21:26:03.510Z and
	// skipped one invalid line; the five event files were read in it.
	rep, _ := ctRun(t, "--since", "2026-10-29T21:26:03.510Z")
	if rep.Skips.SkippedTotal != 1 || rep.Skips.SkippedInvalidJSON != 1 || rep.Skips.IngestFiles != 5 || rep.Skips.LinesRead != 27 {
		t.Errorf("window containing the run: %+v", rep.Skips)
	}
	rep, _ = ctRun(t, "--since", "2026-10-29T21:26:03.511Z")
	if rep.Skips.IngestFiles != 0 || rep.Skips.SkippedTotal != 0 {
		t.Errorf("window after the run: %+v", rep.Skips)
	}
	rep, _ = ctRun(t, "--since", "2026-10-29T00:00:00Z", "--until", "2026-10-29T21:26:03.510Z")
	if rep.Skips.IngestFiles != 0 {
		t.Errorf("until is exclusive: %+v", rep.Skips)
	}
	out, _ := ctRunRaw(t, "--since", "2026-10-29T21:26:03.510Z")
	if !strings.Contains(out, "Ingest skipped lines for the window: 1 (invalid json 1, missing field 0) of 27 read in 5 files") {
		t.Errorf("table output:\n%s", out)
	}
}

func TestCompareTracker_TableOutputNamesTheDifferences(t *testing.T) {
	ctSeed(t, `UPDATE tool_decisions SET hook_decision='deny' WHERE tool_use_id='tu-a1'`)
	out, err := ctRunRaw(t)
	if ctExit(t, err) != 2 {
		t.Fatalf("want exit 2: %v", err)
	}
	for _, want := range []string{
		"Only CETA recorded (0)", "Only the tracker recorded (0)", "Differences (1)", "verdict: 1",
		"sess-a tu-a1 verdict: ceta=deny tracker=allow", "Result: differences found",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table output lacks %q:\n%s", want, out)
		}
	}
}

func TestCompareTracker_RejectsBadFlagsAndOpensBothDatabasesReadOnly(t *testing.T) {
	ctSeed(t)
	if _, err := ctRunRaw(t, "--format", "xml"); err == nil || ctExit2(err) {
		t.Errorf("bad format: %v", err)
	}
	if _, err := ctRunRaw(t, "--since", "last tuesday"); err == nil || ctExit2(err) {
		t.Errorf("bad since: %v", err)
	}
	if _, err := ctRunRaw(t, "--since", "2026-09-22", "--until", "2026-09-21"); err == nil || ctExit2(err) {
		t.Errorf("until before since: %v", err)
	}
	// a missing tracker database is an error, never an empty comparison
	var buf bytes.Buffer
	cmd := newCompareTrackerCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--tracker-db", filepath.Join(t.TempDir(), "absent.db")})
	if err := cmd.Execute(); err == nil || ctExit2(err) {
		t.Errorf("missing tracker db: %v", err)
	}

	askDB := asklog.DefaultDBPath()
	before := sha256.Sum256(mustRead(t, askDB))
	trk := trackerFixture(t, "tracker.db")
	trkBefore := sha256.Sum256(mustRead(t, trk))
	cmd = newCompareTrackerCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--tracker-db", trk, "--since", "2026-09-21"})
	_ = cmd.Execute()
	if sha256.Sum256(mustRead(t, askDB)) != before {
		t.Error("the ask log was modified")
	}
	if sha256.Sum256(mustRead(t, trk)) != trkBefore {
		t.Error("the tracker database was modified")
	}
}

func ctExit2(err error) bool {
	var ee exitCodeError
	return errors.As(err, &ee)
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCompareTracker_DurationSince(t *testing.T) {
	got, err := trackercompare.ParseTime("48h", time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC))
	if err != nil || !got.Equal(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("48h: %v %v", got, err)
	}
}
