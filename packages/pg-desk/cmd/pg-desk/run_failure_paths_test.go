package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// failTableWrites installs a SQLite trigger on the store's own file that
// aborts every statement of kind op ("INSERT" or "UPDATE") on table: a fault
// injected into the REAL store through a second connection, so the store's
// own code takes its own error exit.
func failTableWrites(t *testing.T, st *store.Store, op, table string) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", st.Path()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ddl := fmt.Sprintf(`CREATE TRIGGER inject_fail_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, 'injected failure'); END`, op, table, op, table)
	if _, err := db.ExecContext(context.Background(), ddl); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
}

// failBackendColumnWrites makes every UPDATE that names entity.backend fail,
// leaving the version-lock UPDATE of an interpretation write alone.
func failBackendColumnWrites(t *testing.T, st *store.Store) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", st.Path()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), `CREATE TRIGGER inject_fail_backend BEFORE UPDATE OF backend ON entity BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
}

func dropTable(t *testing.T, st *store.Store, table string) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", st.Path()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), "DROP TABLE "+table); err != nil {
		t.Fatalf("drop %s: %v", table, err)
	}
}

// --- hydrateJiraIssue ---------------------------------------------------

// A store that cannot answer "is it known" fails before any connector call.
func TestHydrateJiraIssueReadFailureIsReturnedBeforeAnyConnectorCall(t *testing.T) {
	f := newBackendFixture(t)
	st, err := deskStoreOpen()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	err = hydrateJiraIssue(context.Background(), pipeline.New(f.cfg, st), f.cfg, st, "PROJ-20", gather.ChangeAdded)
	if err == nil || !strings.Contains(err.Error(), "check entity known") {
		t.Fatalf("err = %v, want the known-entity read failure", err)
	}
	if calls := f.calls(); len(calls) != 0 {
		t.Fatalf("calls = %q, want none", calls)
	}
}

// A removed event for an id the store never held is a no-op.
func TestHydrateJiraIssueRemovedForAnUnknownIDIsANoop(t *testing.T) {
	f := newBackendFixture(t)
	if err := f.hydrateJira(t, "PROJ-21", gather.ChangeRemoved); err != nil {
		t.Fatalf("err = %v, want a no-op", err)
	}
	if calls := f.calls(); len(calls) != 0 {
		t.Fatalf("calls = %q, want none", calls)
	}
	if _, found, _ := f.seed.GetEntity("o/r", "issue", "PROJ-21"); found {
		t.Fatal("a removed event for an unknown id created an entity")
	}
}

// No watched issue query configured: nothing to do, no connector call.
func TestHydrateJiraIssueWithNoWatchedQueriesIsANoop(t *testing.T) {
	f := newBackendFixture(t)
	f.cfg.Watch.Issue.Queries = nil
	if err := f.hydrateJira(t, "PROJ-22", gather.ChangeAdded); err != nil {
		t.Fatal(err)
	}
	if calls := f.calls(); len(calls) != 0 {
		t.Fatalf("calls = %q, want none", calls)
	}
}

// An owning backend's read that is degraded writes nothing and says so (the
// caller retries).
func TestHydrateJiraIssueDegradedOwningReadWritesNothing(t *testing.T) {
	f := newBackendFixture(t)
	f.write("show-PROJ-23.json", `{"protocolVersion":1,"schemaVersion":4,"result":{"id":"PROJ-23","title":"t","owner":"me","assignee":"x","issue_type":"task","state":"open","stale":true,"updated_at":"2026-09-30T00:00:00Z","as_of":"2026-09-30T00:00:00Z"}}`)
	f.write("held-PROJ-23", "")
	err := f.hydrateJira(t, "PROJ-23", gather.ChangeAdded)
	if err == nil || !strings.Contains(err.Error(), "PROJ-23 read from jira-a was degraded (issue answer is stale): nothing written, retry") {
		t.Fatalf("err = %v, want the degraded-read error", err)
	}
	if _, found, _ := f.seed.GetEntity("o/r", "issue", "PROJ-23"); found {
		t.Fatal("a degraded read wrote an entity")
	}
}

// A failing owning-backend read (the daemon is down) is returned as is, with
// the stored backend pinned and nothing written over the stored row.
func TestHydrateJiraIssueOwningReadFailureIsReturned(t *testing.T) {
	f := newBackendFixture(t, "PROJ-24")
	f.setBackend("PROJ-24", "jira-a")
	before := f.entity("issue", "PROJ-24")
	f.write("down", "")
	err := f.hydrateJira(t, "PROJ-24", gather.ChangeChanged)
	if err == nil {
		t.Fatal("err = nil, want the read failure")
	}
	if after := f.entity("issue", "PROJ-24"); after.Version != before.Version || after.Facts != before.Facts {
		t.Fatalf("before=%+v after=%+v, want the stored row untouched", before, after)
	}
}

// A non-owning backend whose unpinned hydration fails returns that failure.
func TestHydrateJiraIssueNonOwningHydrationFailureIsReturned(t *testing.T) {
	f := newBackendFixture(t, "PROJ-25")
	f.setBackend("PROJ-25", "beads-a")
	f.write("down", "")
	err := f.hydrateJira(t, "PROJ-25", gather.ChangeChanged)
	if err == nil {
		t.Fatal("err = nil, want the hydration failure")
	}
	if e := f.entity("issue", "PROJ-25"); e.Backend != "beads-a" {
		t.Fatalf("backend = %q, want the stored column kept", e.Backend)
	}
}

// A non-owning entity whose column already names the resolved backend gets
// the hydration and no column write.
func TestHydrateJiraIssueNonOwningEntityWithTheColumnAlreadySetWritesNoColumn(t *testing.T) {
	f := newBackendFixture(t, "PROJ-26")
	f.setBackend("PROJ-26", "beads-a")
	f.write("show-PROJ-26.json", `{"protocolVersion":1,"schemaVersion":4,"result":{"id":"PROJ-26","title":"t","updated_at":"2026-09-30T00:00:00Z","as_of":"2026-09-30T00:00:00Z"}}`)
	if err := f.hydrateJira(t, "PROJ-26", gather.ChangeChanged); err != nil {
		t.Fatalf("err = %v", err)
	}
	if calls := f.calls(); len(calls) != 1 || strings.Contains(calls[0], "--backend") {
		t.Fatalf("calls = %q, want the one unpinned read", calls)
	}
	if e := f.entity("issue", "PROJ-26"); e.Backend != "beads-a" {
		t.Fatalf("backend = %q", e.Backend)
	}
}

// With no agent tracker backend configured an unheld id has no backend to
// write: it is hydrated unpinned and its column stays NULL.
func TestHydrateJiraIssueUnresolvedBackendIsHydratedAndLeftNull(t *testing.T) {
	f := newBackendFixture(t)
	f.cfg.AgentTrackerBackend = ""
	f.write("show-PROJ-27.json", `{"protocolVersion":1,"schemaVersion":4,"result":{"id":"PROJ-27","title":"t","updated_at":"2026-09-30T00:00:00Z","as_of":"2026-09-30T00:00:00Z"}}`)
	if err := f.hydrateJira(t, "PROJ-27", gather.ChangeAdded); err != nil {
		t.Fatalf("err = %v", err)
	}
	e := f.entity("issue", "PROJ-27")
	if e.Facts == "" || e.Backend != "" {
		t.Fatalf("entity = %+v, want a hydrated row with a NULL backend", e)
	}
}

// A failure writing the backend column after a successful non-owning
// hydration is returned naming the backend.
func TestHydrateJiraIssueBackendColumnWriteFailureIsReturned(t *testing.T) {
	f := newBackendFixture(t)
	f.write("show-PROJ-28.json", `{"protocolVersion":1,"schemaVersion":4,"result":{"id":"PROJ-28","title":"t","updated_at":"2026-09-30T00:00:00Z","as_of":"2026-09-30T00:00:00Z"}}`)
	failBackendColumnWrites(t, f.seed)
	err := f.hydrateJira(t, "PROJ-28", gather.ChangeAdded)
	if err == nil || !strings.Contains(err.Error(), "write backend beads-a") {
		t.Fatalf("err = %v, want the column write failure", err)
	}
}

// --- runThread ----------------------------------------------------------

func runThreadDirect(t *testing.T, st *store.Store, text, threadID string, change gather.ChangeKind) error {
	t.Helper()
	cfg := jiraTestCfg()
	runThreadShow = func(context.Context, string) (threadShowResult, bool, error) {
		return threadShowResult{Text: text, AsOf: "2026-09-18T00:00:00Z"}, false, nil
	}
	return runThread(context.Background(), pipeline.New(cfg, st, pipeline.WithStamp("test/stamp")), cfg, st, threadID, change)
}

func newThreadStore(t *testing.T) *store.Store {
	t.Helper()
	st, _ := setupRunThreadTest(t, jiraTestCfg(), threadShowResult{}, false, nil)
	return st
}

func seedThreadLink(t *testing.T, st *store.Store, fromType, fromID, threadID string) {
	t.Helper()
	if err := st.UpsertXref(store.Xref{
		Repo: "acme/widgets", FromType: fromType, FromID: fromID, ToType: "thread", ToID: threadID,
		Evidence: "permalink", FirstSeen: "2026-09-01T00:00:00Z", LastConfirmed: "2026-09-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunThreadPermalinkXrefWriteFailureIsReturnedNamingThePR(t *testing.T) {
	st := newThreadStore(t)
	failTableWrites(t, st, "INSERT", "xref")
	err := runThreadDirect(t, st, "see https://github.com/acme/widgets/pull/7 here", "T10", gather.ChangeChanged)
	if err == nil || !strings.Contains(err.Error(), "run thread T10: upsert permalink xref for acme/widgets#7") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunThreadTicketKeyLookupFailureIsReturnedNamingTheKey(t *testing.T) {
	st := newThreadStore(t)
	dropTable(t, st, "xref")
	err := runThreadDirect(t, st, "tracked in PROJ-9", "T11", gather.ChangeChanged)
	if err == nil || !strings.Contains(err.Error(), "run thread T11: list xrefs for ticket PROJ-9") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunThreadTicketKeyXrefWriteFailureIsReturnedNamingThePR(t *testing.T) {
	st := newThreadStore(t)
	seedPRFacts(t, st, "acme/widgets#7")
	seedXref(t, st, "acme/widgets#7", "PROJ-9")
	failTableWrites(t, st, "INSERT", "xref")
	err := runThreadDirect(t, st, "tracked in PROJ-9", "T12", gather.ChangeChanged)
	if err == nil || !strings.Contains(err.Error(), "run thread T12: upsert ticket-key xref for acme/widgets#7") {
		t.Fatalf("err = %v", err)
	}
}

// Only PR-anchored xrefs to the ticket key link the thread; another entity
// type that references the same key is ignored.
func TestRunThreadTicketKeyIgnoresNonPRAnchors(t *testing.T) {
	st := newThreadStore(t)
	if err := st.UpsertXref(store.Xref{
		Repo: "acme/widgets", FromType: "thread", FromID: "T-other", ToType: "issue", ToID: "PROJ-9",
		Evidence: "ticket-key", FirstSeen: "2026-09-01T00:00:00Z", LastConfirmed: "2026-09-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := runThreadDirect(t, st, "tracked in PROJ-9", "T13", gather.ChangeChanged); err != nil {
		t.Fatal(err)
	}
	if linked, _ := st.ListXrefsByTo("acme/widgets", "thread", "T13"); len(linked) != 0 {
		t.Fatalf("linked = %+v, want none: a non-PR anchor must not link the thread", linked)
	}
}

func TestRunThreadLinkedPRListFailureIsReturned(t *testing.T) {
	st := newThreadStore(t)
	dropTable(t, st, "xref")
	err := runThreadDirect(t, st, "no keys or permalinks here", "T14", gather.ChangeChanged)
	if err == nil || !strings.Contains(err.Error(), "run thread T14: list linked PRs") {
		t.Fatalf("err = %v", err)
	}
}

// A linked non-PR entity is skipped (nothing is re-interpreted for it).
func TestRunThreadLinkedNonPREntityIsNotReinterpreted(t *testing.T) {
	st := newThreadStore(t)
	seedThreadLink(t, st, "issue", "PROJ-30", "T15")
	if err := runThreadDirect(t, st, "nothing", "T15", gather.ChangeChanged); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := st.GetInterpretation("acme/widgets", "issue", "PROJ-30"); found {
		t.Fatal("a linked issue was re-interpreted by run thread")
	}
}

// Every linked PR is re-interpreted even when an earlier one fails, and the
// failures are reported together, each naming its PR.
func TestRunThreadReinterpretFailuresAreJoinedAndTheRestStillRun(t *testing.T) {
	st := newThreadStore(t)
	for _, id := range []string{"acme/widgets#1", "acme/widgets#3"} {
		if err := st.UpsertEntityForTest(store.Entity{
			Repo: "acme/widgets", EntityType: entityTypePR, EntityID: id,
			Facts: "{not json", AsOf: "2026-09-17T00:00:00Z", ContentHash: "h",
		}); err != nil {
			t.Fatal(err)
		}
		seedThreadLink(t, st, entityTypePR, id, "T16")
	}
	seedPRFacts(t, st, "acme/widgets#2")
	seedThreadLink(t, st, entityTypePR, "acme/widgets#2", "T16")

	err := runThreadDirect(t, st, "nothing", "T16", gather.ChangeChanged)
	if err == nil {
		t.Fatal("err = nil, want the joined re-interpret failures")
	}
	for _, want := range []string{"run thread T16:", "re-interpret acme/widgets#1:", "re-interpret acme/widgets#3:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, missing %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "acme/widgets#2") {
		t.Errorf("err = %q names the PR that interpreted fine", err.Error())
	}
	if _, found, _ := st.GetInterpretation("acme/widgets", entityTypePR, "acme/widgets#2"); !found {
		t.Fatal("the good PR was not re-interpreted after an earlier failure")
	}
}

// A thread the connector fails to fetch (not a not_found answer) fails the
// call with the cause wrapped, and writes nothing.
func TestRunThreadFetchErrorIsWrappedAndWritesNothing(t *testing.T) {
	st := newThreadStore(t)
	boom := fmt.Errorf("connector exploded")
	runThreadShow = func(context.Context, string) (threadShowResult, bool, error) {
		return threadShowResult{}, false, boom
	}
	cfg := jiraTestCfg()
	err := runThread(context.Background(), pipeline.New(cfg, st, pipeline.WithStamp("test/stamp")), cfg, st, "T17", gather.ChangeChanged)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "run thread T17: fetch thread") {
		t.Fatalf("err = %v", err)
	}
	if linked, _ := st.ListXrefsByTo("acme/widgets", "thread", "T17"); len(linked) != 0 {
		t.Fatalf("linked = %+v, want none", linked)
	}
}
