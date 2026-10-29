package trackercompare

import (
	"testing"
	"time"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/asklog"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/trackeradapter"
)

func trk(session, tu string, id int, startMs int64) trackeradapter.Row {
	r := trackeradapter.Row{CallID: id, ToolUseID: tu, StartMs: startMs, TrackerSeen: true}
	r.SessionID = session
	r.Outcome = asklog.OutcomeApproved
	return r
}

func cet(id int, session, tu, created string) asklog.CompareRow {
	return asklog.CompareRow{ID: id, SessionID: session, ToolUseID: tu, Outcome: asklog.OutcomeApproved, CreatedAt: created}
}

func TestCompare_TrackerDuplicateKeysAreReportedAndLatestIsCompared(t *testing.T) {
	first, second := trk("s", "t1", 1, 1000), trk("s", "t1", 2, 1000)
	first.Outcome = asklog.OutcomeFailed // would differ, but the latest row is compared
	rep, err := Compare(Input{
		Ceta:    []asklog.CompareRow{cet(1, "s", "t1", "1970-01-01T00:00:01Z")},
		Tracker: []trackeradapter.Row{first, second},
		Sources: []trackeradapter.SessionSource{{SessionID: "s", HasTracker: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Duplicates) != 1 || rep.Duplicates[0] != (Duplicate{"tracker", "s", "t1", 2}) {
		t.Errorf("duplicates: %+v", rep.Duplicates)
	}
	if len(rep.Differences) != 0 || rep.Clean {
		t.Errorf("report: %+v", rep)
	}
}

func TestCompare_TrackerCallWithOnlyCetaEventsIsNotATrackerRecording(t *testing.T) {
	ceta := trk("s", "t1", 1, 1000)
	ceta.TrackerSeen = false
	rep, _ := Compare(Input{
		Ceta:    []asklog.CompareRow{cet(1, "s", "t1", "1970-01-01T00:00:01Z")},
		Tracker: []trackeradapter.Row{ceta},
		Sources: []trackeradapter.SessionSource{{SessionID: "s", HasTracker: false, HasCeta: true}},
	})
	if len(rep.OnlyCeta) != 1 || rep.TrackerCalls != 0 || len(rep.SessionsWithoutTracker) != 1 {
		t.Errorf("report: %+v", rep)
	}
}

func TestCompare_BadCreatedAtIsAnError(t *testing.T) {
	if _, err := Compare(Input{Ceta: []asklog.CompareRow{cet(1, "s", "t1", "yesterday")}}); err == nil {
		t.Error("want an error for an unparseable created_at")
	}
}

func TestParseTime_Forms(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"2026-10-01T00:00:00Z":   time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		"2026-10-01":             time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		"2026-10-01T00:00:00.5Z": time.Date(2026, 10, 1, 0, 0, 0, 500_000_000, time.UTC),
		"90m":                    now.Add(-90 * time.Minute),
	} {
		got, err := ParseTime(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "-5h", "soon"} {
		if _, err := ParseTime(bad, now); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}
