package store

import (
	"strings"
	"testing"
)

func failureInterp() Interpretation {
	return Interpretation{
		Repo: "o/r", EntityType: "pr", EntityID: "o/r#1",
		Ownership: "mine", Enrichment: "{}", Urgency: "low", Category: "c",
		Dispositions: "[]", Approvals: "{}", GateState: "g", MatchReasons: "[]", Panel: "p", AsOf: "A",
	}
}

// A store that can no longer be read fails the schema probe: both calls
// return an error naming the key, and neither claims a row.
func TestInterpretationAccessOnAClosedStoreFailsWithTheKey(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	err := s.UpsertInterpretation(failureInterp())
	if err == nil || !strings.Contains(err.Error(), "store: upsert interpretation (o/r,pr,o/r#1)") {
		t.Fatalf("upsert err = %v", err)
	}
	got, found, err := s.GetInterpretation("o/r", "pr", "o/r#1")
	if err == nil || found || got != (Interpretation{}) || !strings.Contains(err.Error(), "store: get interpretation (o/r,pr,o/r#1)") {
		t.Fatalf("get = %+v found=%v err=%v, want a keyed error and a zero row", got, found, err)
	}
}

// An insert the database refuses surfaces as the upsert's own keyed error and
// leaves the previously stored row as it was.
func TestUpsertInterpretationWriteFailureKeepsThePreviousRow(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	if err := s.UpsertInterpretation(failureInterp()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sql.Exec(`CREATE TRIGGER inject_fail BEFORE UPDATE ON interpretation BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	changed := failureInterp()
	changed.AsOf = "B"
	err := s.UpsertInterpretation(changed)
	if err == nil || !strings.Contains(err.Error(), "store: upsert interpretation (o/r,pr,o/r#1)") || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("err = %v", err)
	}
	got, found, err := s.GetInterpretation("o/r", "pr", "o/r#1")
	if err != nil || !found || got.AsOf != "A" {
		t.Fatalf("got = %+v found=%v err=%v, want the previous row kept", got, found, err)
	}
}

// A missing row is found=false with no error; a row that cannot be read is an
// error (not "not found").
func TestGetInterpretationDistinguishesNotFoundFromAReadFailure(t *testing.T) {
	s := OpenNewSchemaForTest(t)
	got, found, err := s.GetInterpretation("o/r", "pr", "o/r#404")
	if err != nil || found || got != (Interpretation{}) {
		t.Fatalf("got = %+v found=%v err=%v, want a clean not-found", got, found, err)
	}

	if _, err := s.sql.Exec(`DROP TABLE interpretation`); err != nil {
		t.Fatal(err)
	}
	got, found, err = s.GetInterpretation("o/r", "pr", "o/r#1")
	if err == nil || found || got != (Interpretation{}) || !strings.Contains(err.Error(), "store: get interpretation (o/r,pr,o/r#1)") {
		t.Fatalf("got = %+v found=%v err=%v, want a keyed read error", got, found, err)
	}
}
