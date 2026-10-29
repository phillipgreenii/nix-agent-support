package trackeradapter

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
)

// The fixture databases (testdata/*.sql, loaded into a temp database) were built by the real
// claude-tool-tracker ingest from testdata/events (see testdata/gen.sh).
// tracker.db holds five calls: sess-a/tu-a1 (CETA approve, call_id 1),
// sess-a/tu-a2 (CETA ask answered by a person, errored, 4), sess-b/tu-b1
// (CETA ask delivered as reject in an unattended session, 16), sess-b/tu-b2
// (allowed by a rule, 21) and sess-c/tu-c1 (a CETA-only session, 25).
// tracker_reingest.db is the same events ingested in another order, so every
// call_id differs.

// fixtureCopy builds a fresh writable copy of a fixture database from its SQL
// dump (testdata/<name without .db>.sql, produced by gen.sh from the real
// tracker ingest).
func fixtureCopy(t *testing.T, name string) string {
	t.Helper()
	dump, err := os.ReadFile(filepath.Join("testdata", strings.TrimSuffix(name, ".db")+".sql"))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), name)
	db, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(dump)); err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func openFixture(t *testing.T, name string) *Adapter {
	t.Helper()
	a, err := Open(fixtureCopy(t, name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func byTool(t *testing.T, rows []Row, toolUseID string) Row {
	t.Helper()
	for _, r := range rows {
		if r.ToolUseID == toolUseID {
			return r
		}
	}
	t.Fatalf("no row for %s", toolUseID)
	return Row{}
}

func TestRows_MapsEveryColumnToTheEvaluationRow(t *testing.T) {
	a := openFixture(t, "tracker.db")
	rows, err := a.Rows("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("got %d rows, want 5", len(rows))
	}

	a1 := byTool(t, rows, "tu-a1")
	if a1.ID != 1 || a1.CallID != 1 || a1.SessionID != "sess-a" || a1.ToolName != "Bash" ||
		a1.CWD != "/nonexistent/example-repo" || a1.ToolInputJSON != `{"command":"git status"}` {
		t.Errorf("a1 identity fields: %+v", a1)
	}
	if a1.ToolSummary == "" {
		t.Error("tool summary not derived from tool_input")
	}
	if a1.EngineVerdict != "approve" || a1.DeliveredVerdict != "approve" {
		t.Errorf("a1 verdicts: engine %q delivered %q", a1.EngineVerdict, a1.DeliveredVerdict)
	}
	if a1.HookDecision == nil || *a1.HookDecision != "allow" {
		t.Errorf("a1 hook decision: %v", a1.HookDecision)
	}
	if !a1.SandboxEnabled.Valid || a1.SandboxEnabled.Int64 != 1 {
		t.Errorf("a1 sandbox: %+v", a1.SandboxEnabled)
	}
	if a1.PermissionMode == nil || *a1.PermissionMode != "default" {
		t.Errorf("a1 permission mode: %v", a1.PermissionMode)
	}
	if a1.SettledBy != "ceta" || a1.Settlement != "approved" || a1.ConvertedAsk || a1.Outcome != asklog.OutcomeApproved {
		t.Errorf("a1 settlement: %+v", a1)
	}
	if a1.IsError == nil || *a1.IsError {
		t.Errorf("a1 is_error: %v", a1.IsError)
	}
	if a1.Reason != "read-only" {
		t.Errorf("a1 reason: %q", a1.Reason)
	}
	if a1.ApprovalSourceInput() != "hook" {
		t.Errorf("a1 approval source: %s", a1.ApprovalSourceInput())
	}

	// a person answered an ask and the call then errored: failed, prompted.
	a2 := byTool(t, rows, "tu-a2")
	if a2.EngineVerdict != "ask" || a2.SettledBy != "person" || a2.Settlement != "approved" || a2.Outcome != asklog.OutcomeFailed {
		t.Errorf("a2: %+v", a2)
	}
	if a2.IsError == nil || !*a2.IsError {
		t.Errorf("a2 is_error: %v", a2.IsError)
	}
	if a2.OutcomeNotes == nil || *a2.OutcomeNotes != "boom" {
		t.Errorf("a2 error text: %v", a2.OutcomeNotes)
	}
	if a2.PromptID == nil || a2.ApprovalSourceInput() != "user" {
		t.Errorf("a2 approval source: %s", a2.ApprovalSourceInput())
	}

	// engine said ask, an unattended session delivered reject.
	b1 := byTool(t, rows, "tu-b1")
	if b1.EngineVerdict != "ask" || b1.DeliveredVerdict != "reject" || !b1.ConvertedAsk ||
		b1.SettledBy != "ceta" || b1.Settlement != "refused" || b1.Outcome != asklog.OutcomeRejected {
		t.Errorf("b1: %+v", b1)
	}
	if b1.HookDecision == nil || *b1.HookDecision != "ask" {
		t.Errorf("b1 hook decision (the engine's, not the delivered): %v", b1.HookDecision)
	}

	// no CETA event at all: no hook decision.
	b2 := byTool(t, rows, "tu-b2")
	if b2.HookDecision != nil || b2.EngineVerdict != "" || b2.SettledBy != "rule" {
		t.Errorf("b2: %+v", b2)
	}

	c1 := byTool(t, rows, "tu-c1")
	if c1.Settlement != "unresolved" || c1.Outcome != asklog.OutcomeUnresolved || c1.EngineVerdict != "abstain" {
		t.Errorf("c1: %+v", c1)
	}
}

// ApprovalSourceInput is a test helper: what asklog.ApprovalSource says about
// the row, so the mapping is checked through the real consumer.
func (r Row) ApprovalSourceInput() string {
	return asklog.ApprovalSource(r.PermissionMode, r.PromptID, r.HookDecision)
}

func TestRows_SinceFiltersOnCallStart(t *testing.T) {
	a := openFixture(t, "tracker.db")
	// every fixture event is at about 1790000000000 ms, long before 2030
	rows, err := a.Rows("2030-01-01", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("a future --since kept %d rows", len(rows))
	}
	rows, err = a.Rows("2020-01-01T00:00:00Z", nil)
	if err != nil || len(rows) != 5 {
		t.Errorf("past --since: %d rows, err %v", len(rows), err)
	}
	if _, err := a.Rows("last tuesday", nil); err == nil {
		t.Error("an unparseable --since must be an error")
	}
}

func TestOpen_RefusesMissingAndMisshapenDatabases(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "absent.db")); err == nil {
		t.Error("a missing database must be an error, not an empty history")
	}
	if _, err := Open(t.TempDir()); err == nil {
		t.Error("a directory must be an error")
	}

	// a SQLite file that is not a tracker database
	p := filepath.Join(t.TempDir(), "other.db")
	db, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE unrelated (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(p); err == nil || !strings.Contains(err.Error(), "not shaped as expected") {
		t.Errorf("a misshapen database must be a clear error, got %v", err)
	}

	// a tracker database whose view lost a column the adapter reads
	q := fixtureCopy(t, "tracker.db")
	db, err = sql.Open("sqlite", "file:"+q)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP VIEW session_sources; CREATE VIEW session_sources AS SELECT 'x' AS session_id`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(q); err == nil || !strings.Contains(err.Error(), "session_sources") {
		t.Errorf("a view missing columns must name the view, got %v", err)
	}
}

func TestAdapter_IsReadOnly(t *testing.T) {
	p := fixtureCopy(t, "tracker.db")
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	for _, stmt := range []string{
		`CREATE TABLE intruder (x INTEGER)`,
		`DROP VIEW tool_calls`,
	} {
		if _, err := a.db.Exec(stmt); err == nil {
			t.Errorf("write through the adapter succeeded: %s", stmt)
		} else if !strings.Contains(strings.ToLower(err.Error()), "readonly") && !strings.Contains(strings.ToLower(err.Error()), "read-only") {
			t.Errorf("%s: unexpected error %v", stmt, err)
		}
	}
}

func TestSkippedCounts_PerWindow(t *testing.T) {
	a := openFixture(t, "tracker.db")
	all, err := a.SkippedCounts(0, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	if all.Files != 5 || all.SkippedInvalidJSON != 1 || all.SkippedMissingField != 0 || all.SkippedTotal != 1 {
		t.Errorf("whole history: %+v", all)
	}
	if all.LinesRead != 27 || all.EventsNew != 26 {
		t.Errorf("whole history lines/new: %+v", all)
	}
	none, err := a.SkippedCounts(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if none != (SkipCounts{}) {
		t.Errorf("a window before any ingest run must be empty, got %+v", none)
	}
}

func TestSessionSources_PerSession(t *testing.T) {
	a := openFixture(t, "tracker.db")
	list, err := a.SessionSources()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d sessions: %+v", len(list), list)
	}
	c, ok, err := a.SourcesFor("sess-c")
	if err != nil || !ok {
		t.Fatalf("sess-c: %v %v", ok, err)
	}
	if c.HasTracker || !c.HasCeta || len(c.Sources) != 1 || c.Sources[0] != "ceta" || c.CetaEvents != 1 || c.TrackerEvents != 0 {
		t.Errorf("a CETA-only session: %+v", c)
	}
	ab, ok, err := a.SourcesFor("sess-a")
	if err != nil || !ok {
		t.Fatalf("sess-a: %v %v", ok, err)
	}
	if !ab.HasTracker || !ab.HasCeta || strings.Join(ab.Sources, ",") != "ceta,tracker" {
		t.Errorf("sess-a: %+v", ab)
	}
	if _, ok, err := a.SourcesFor("no-such-session"); ok || err != nil {
		t.Errorf("unknown session: %v %v", ok, err)
	}
}

func TestLabels_ExcludeAndJoinByLastingKey(t *testing.T) {
	deny := "deny"
	labels := map[asklog.CallKey]asklog.TrackerLabel{
		{SessionID: "sess-b", ToolUseID: "tu-b1"}: {CorrectDec: &deny, CorrectDecExplanation: "net is never ok"},
		{SessionID: "sess-a", ToolUseID: "tu-a2"}: {Excluded: true, ExcludedReason: "test data"},
	}
	for _, db := range []string{"tracker.db", "tracker_reingest.db"} {
		a := openFixture(t, db)
		rows, err := a.Rows("", labels)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 4 {
			t.Errorf("%s: an excluded call must drop out of Rows, got %d rows", db, len(rows))
		}
		b1 := byTool(t, rows, "tu-b1")
		if b1.CorrectDec == nil || *b1.CorrectDec != "deny" {
			t.Errorf("%s: label not joined to tu-b1 (call_id %d)", db, b1.CallID)
		}
		// a lookup by call_id still returns the excluded call (show needs it)
		a2key := asklog.CallKey{SessionID: "sess-a", ToolUseID: "tu-a2"}
		all, err := a.Rows("", nil)
		if err != nil {
			t.Fatal(err)
		}
		id := byTool(t, all, "tu-a2").CallID
		got, err := a.RowsByIDs([]int{id}, labels)
		if err != nil || len(got) != 1 || got[0].Excluded != 1 || got[0].Key() != a2key {
			t.Errorf("%s: RowsByIDs of an excluded call: %+v %v", db, got, err)
		}
	}
	a := openFixture(t, "tracker.db")
	a1 := byTool(t, mustRows(t, a), "tu-b1").CallID
	b := openFixture(t, "tracker_reingest.db")
	b1 := byTool(t, mustRows(t, b), "tu-b1").CallID
	if a1 == b1 {
		t.Errorf("the re-ingested fixture must renumber call_ids (both %d)", a1)
	}
}

func mustRows(t *testing.T, a *Adapter) []Row {
	t.Helper()
	rows, err := a.Rows("", nil)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestKey_ResolvesCallIDs(t *testing.T) {
	a := openFixture(t, "tracker.db")
	k, ok, err := a.Key(16)
	if err != nil || !ok || k != (asklog.CallKey{SessionID: "sess-b", ToolUseID: "tu-b1"}) {
		t.Errorf("Key(16) = %+v %v %v", k, ok, err)
	}
	if _, ok, err := a.Key(9999); ok || err != nil {
		t.Errorf("unknown call_id: %v %v", ok, err)
	}
}

func TestRows_TrackerSeenIsFalseForACallOnlyCetaReported(t *testing.T) {
	a := openFixture(t, "tracker.db")
	rows, err := a.Rows("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !byTool(t, rows, "tu-a1").TrackerSeen {
		t.Error("a1 has tracker events")
	}
	if byTool(t, rows, "tu-c1").TrackerSeen {
		t.Error("c1 carries only ceta events")
	}
}
