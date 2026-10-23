package groupkit

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

const (
	testKind     = "example-kind"
	Summary      = "summary"
	Detail       = "detail"
	Conversation = "conversation"
	Files        = "files"
	Commits      = "commits"
	Pending      = "pending"
	Runs         = "runs"
)

func allGroups() []string {
	return []string{Summary, Detail, Conversation, Files, Commits, Pending, Runs}
}

// testTable is a table with the daemon defaults the old rules tests ran
// against: detail and conversation 10 min / 30 min, pending 2 min, summary and
// runs 60 s (runs max age 30 min), files and commits stamped without a time
// limit, terminal 24 h.
func testTable() Table {
	return Table{
		TerminalTTL: 24 * time.Hour,
		Groups: []GroupSpec{
			{Name: Summary, TTL: 60 * time.Second, Background: true},
			{Name: Detail, TTL: 10 * time.Minute, MaxAge: 30 * time.Minute, Extendable: true, Background: true},
			{Name: Conversation, TTL: 10 * time.Minute, MaxAge: 30 * time.Minute, Extendable: true, Background: true},
			{Name: Files, Stamped: true, Background: true},
			{Name: Commits, Stamped: true, Background: true},
			{Name: Pending, TTL: 2 * time.Minute},
			{Name: Runs, TTL: 60 * time.Second, MaxAge: 30 * time.Minute, Stamped: true, Background: true},
		},
	}
}

func TestScheduleDetailAndConversation(t *testing.T) {
	r := testTable()
	for _, g := range []string{Detail, Conversation} {
		fresh, max := r.Schedule(g, false, t0)
		if want := t0.Add(10 * time.Minute); !fresh.Equal(want) {
			t.Errorf("%s fresh_until = %v, want %v", g, fresh, want)
		}
		if want := t0.Add(30 * time.Minute); !max.Equal(want) {
			t.Errorf("%s max_age_at = %v, want %v", g, max, want)
		}
	}
}

func TestSchedulePendingSummaryRunsFilesCommits(t *testing.T) {
	r := testTable()
	fresh, max := r.Schedule(Pending, false, t0)
	if !fresh.Equal(t0.Add(2*time.Minute)) || !max.IsZero() {
		t.Errorf("pending = %v, %v; want +2m and no maximum", fresh, max)
	}
	fresh, max = r.Schedule(Summary, false, t0)
	if !fresh.Equal(t0.Add(60*time.Second)) || !max.IsZero() {
		t.Errorf("summary = %v, %v; want +60s and no maximum", fresh, max)
	}
	fresh, max = r.Schedule(Runs, false, t0)
	if !fresh.Equal(t0.Add(60*time.Second)) || !max.Equal(t0.Add(30*time.Minute)) {
		t.Errorf("runs = %v, %v; want +60s and +30m", fresh, max)
	}
	for _, g := range []string{Files, Commits} {
		fresh, max = r.Schedule(g, false, t0)
		if !fresh.IsZero() || !max.IsZero() {
			t.Errorf("%s = %v, %v; want zero, zero", g, fresh, max)
		}
	}
}

func TestScheduleTerminalUsesTerminalTTLForEveryGroup(t *testing.T) {
	r := testTable()
	for _, g := range allGroups() {
		fresh, max := r.Schedule(g, true, t0)
		if !fresh.Equal(t0.Add(24*time.Hour)) || !max.IsZero() {
			t.Errorf("%s terminal = %v, %v; want +24h and no maximum", g, fresh, max)
		}
	}
}

func TestExtendClampsAtMaxAge(t *testing.T) {
	r := testTable()
	row := store.GroupRow{Group: Detail, FetchedAt: t0, FreshUntil: t0.Add(10 * time.Minute), MaxAgeAt: t0.Add(30 * time.Minute)}

	now := t0.Add(8 * time.Minute)
	got, ok := r.Extend(row, Detail, now)
	if !ok || !got.Equal(now.Add(10*time.Minute)) {
		t.Errorf("Extend at +8m = %v, %v; want +18m", got, ok)
	}
	now = t0.Add(25 * time.Minute)
	got, ok = r.Extend(row, Detail, now)
	if !ok || !got.Equal(t0.Add(30*time.Minute)) {
		t.Errorf("Extend at +25m = %v, %v; want clamp to +30m", got, ok)
	}
	for _, at := range []time.Duration{30 * time.Minute, 45 * time.Minute} {
		if got, ok = r.Extend(row, Detail, t0.Add(at)); ok || !got.IsZero() {
			t.Errorf("Extend at +%v = %v, %v; want not ok", at, got, ok)
		}
	}
	conv := row
	conv.Group = Conversation
	if got, ok = r.Extend(conv, Conversation, t0.Add(time.Minute)); !ok || !got.Equal(t0.Add(11*time.Minute)) {
		t.Errorf("conversation Extend = %v, %v", got, ok)
	}
	row.MaxAgeAt = time.Time{}
	if _, ok = r.Extend(row, Detail, t0); ok {
		t.Error("Extend of a row with no MaxAgeAt must report not ok")
	}
	if _, ok = r.Extend(store.GroupRow{MaxAgeAt: t0.Add(time.Hour)}, Pending, t0); ok {
		t.Error("Extend of a non-extendable group must report not ok")
	}
}

func TestBackgroundAndStamped(t *testing.T) {
	r := testTable()
	for _, g := range allGroups() {
		if got, want := r.Background(g), g != Pending; got != want {
			t.Errorf("Background(%s) = %v, want %v", g, got, want)
		}
		if got, want := r.Stamped(g), g == Files || g == Commits || g == Runs; got != want {
			t.Errorf("Stamped(%s) = %v, want %v", g, got, want)
		}
	}
}

func TestFresh(t *testing.T) {
	r := testTable()
	cur := Stamps{Primary: "h1", Secondary: "b1"}
	timed := func(g string) store.GroupRow {
		fresh, max := r.Schedule(g, false, t0)
		return store.GroupRow{Group: g, Content: []byte("x"), FetchedAt: t0, FreshUntil: fresh, MaxAgeAt: max}
	}
	stamped := func(g string) store.GroupRow {
		row := timed(g)
		row.SetStamps("h1", "b1")
		return row
	}

	for _, tc := range []struct {
		name string
		row  store.GroupRow
		cur  Stamps
		at   time.Duration
		want Level
	}{
		{"detail inside window", timed(Detail), cur, 5 * time.Minute, LevelFresh},
		{"detail at fresh_until", timed(Detail), cur, 10 * time.Minute, LevelStale},
		{"detail past fresh_until", timed(Detail), cur, 11 * time.Minute, LevelStale},
		{"detail at max age", timed(Detail), cur, 30 * time.Minute, LevelHardStale},
		{"conversation hard-stale", timed(Conversation), cur, time.Hour, LevelHardStale},
		{"pending inside window", timed(Pending), cur, time.Minute, LevelFresh},
		{"pending past window", timed(Pending), cur, 3 * time.Minute, LevelStale},
		{"files matching, no time limit", stamped(Files), cur, 48 * time.Hour, LevelFresh},
		{"commits matching", stamped(Commits), cur, time.Hour, LevelFresh},
		{"files other head", stamped(Files), Stamps{"h2", "b1"}, time.Second, LevelStale},
		{"commits other base", stamped(Commits), Stamps{"h1", "b2"}, time.Second, LevelStale},
		{"runs other head inside window", stamped(Runs), Stamps{"h2", "b1"}, time.Second, LevelStale},
		{"runs matching inside window", stamped(Runs), cur, 30 * time.Second, LevelFresh},
		{"runs hard-stale", stamped(Runs), cur, 30 * time.Minute, LevelHardStale},
		// Stamps do not apply to unstamped groups.
		{"detail ignores stamps", timed(Detail), Stamps{"other", "other"}, time.Minute, LevelFresh},
		{"zero fresh_until on unstamped group", store.GroupRow{Group: Detail, Content: []byte("x")}, cur, 0, LevelStale},
		{"empty content", store.GroupRow{Group: Detail, FreshUntil: t0.Add(time.Hour)}, cur, 0, LevelStale},
		{"empty stamped content", store.GroupRow{Group: Files}, cur, 0, LevelStale},
	} {
		got := r.Fresh(tc.row, tc.cur, t0.Add(tc.at))
		if got.Level != tc.want {
			t.Errorf("%s: level = %v (%s), want %v", tc.name, got.Level, got.Reason, tc.want)
		}
		if (got.Level == LevelFresh) != (got.Reason == "") || got.IsFresh() != (got.Level == LevelFresh) {
			t.Errorf("%s: reason %q inconsistent with level %v", tc.name, got.Reason, got.Level)
		}
	}
}

// A terminal entity's rows have no maximum age, so a stamped row stays fresh until
// terminal_ttl and a stamp mismatch still wins inside that window.
func TestFreshTerminalRows(t *testing.T) {
	r := testTable()
	fresh, max := r.Schedule(Files, true, t0)
	row := store.GroupRow{Group: Files, Content: []byte("x"), FreshUntil: fresh, MaxAgeAt: max}
	row.SetStamps("h1", "b1")
	if got := r.Fresh(row, Stamps{"h1", "b1"}, t0.Add(23*time.Hour)); !got.IsFresh() {
		t.Errorf("terminal files at +23h = %v", got)
	}
	if got := r.Fresh(row, Stamps{"h1", "b1"}, t0.Add(24*time.Hour)); got.Level != LevelStale {
		t.Errorf("terminal files at +24h = %v", got)
	}
	if got := r.Fresh(row, Stamps{"h9", "b1"}, t0.Add(time.Hour)); got.Level != LevelStale {
		t.Errorf("terminal files, other head = %v", got)
	}
}

func TestFingerprint(t *testing.T) {
	// sha256("abc"), the FIPS 180 test vector.
	if fp := Fingerprint([]byte("abc")); fp != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("Fingerprint = %s", fp)
	}
	if Fingerprint([]byte("a")) == Fingerprint([]byte("b")) {
		t.Error("distinct content, same fingerprint")
	}
}

func TestAccessorsAndUnknownGroup(t *testing.T) {
	tbl := testTable()
	if tbl.TTL(Runs) != 60*time.Second || tbl.MaxAge(Runs) != 30*time.Minute || !tbl.Stamped(Runs) || !tbl.Background(Runs) {
		t.Errorf("runs accessors = %v %v %v %v", tbl.TTL(Runs), tbl.MaxAge(Runs), tbl.Stamped(Runs), tbl.Background(Runs))
	}
	if tbl.Background(Pending) || tbl.Stamped(Pending) || tbl.TTL(Pending) != 2*time.Minute || tbl.MaxAge(Pending) != 0 {
		t.Error("pending accessors wrong")
	}
	const unknown = "example-unknown"
	if _, ok := tbl.Spec(unknown); ok {
		t.Error("Spec reported an unlisted group")
	}
	if tbl.TTL(unknown) != 0 || tbl.MaxAge(unknown) != 0 || tbl.Stamped(unknown) || tbl.Background(unknown) {
		t.Error("an unlisted group must have no TTL, no max age and no flags")
	}
	if _, ok := tbl.Extend(store.GroupRow{MaxAgeAt: t0.Add(time.Hour)}, unknown, t0); ok {
		t.Error("an unlisted group must not be extendable")
	}
	fresh, max := tbl.Schedule(unknown, false, t0)
	if !fresh.IsZero() || !max.IsZero() {
		t.Errorf("unlisted group schedule = %v, %v", fresh, max)
	}
}

func TestFreshReasonTexts(t *testing.T) {
	tbl := testTable()
	row := store.GroupRow{Group: Files, Content: []byte("x")}
	row.SetStamps("h1", "b1")
	for _, tc := range []struct {
		name   string
		row    store.GroupRow
		cur    Stamps
		reason string
	}{
		{"empty", store.GroupRow{Group: Detail}, Stamps{}, "no content stored"},
		{"head", row, Stamps{"h2", "b1"}, "fetched for a different head"},
		{"base", row, Stamps{"h1", "b2"}, "fetched for a different base"},
		{"no fresh_until", store.GroupRow{Group: Detail, Content: []byte("x")}, Stamps{}, "no fresh_until recorded"},
		{"past fresh_until", store.GroupRow{Group: Detail, Content: []byte("x"), FreshUntil: t0}, Stamps{}, "past fresh_until"},
		{"max age", store.GroupRow{Group: Detail, Content: []byte("x"), MaxAgeAt: t0}, Stamps{}, "past the maximum age"},
	} {
		if got := tbl.Fresh(tc.row, tc.cur, t0).Reason; got != tc.reason {
			t.Errorf("%s: reason = %q, want %q", tc.name, got, tc.reason)
		}
	}
}

func TestLevelString(t *testing.T) {
	for l, want := range map[Level]string{LevelFresh: "fresh", LevelStale: "stale", LevelHardStale: "hard-stale", Level(9): "unknown"} {
		if l.String() != want {
			t.Errorf("Level(%d).String() = %q, want %q", int(l), l.String(), want)
		}
	}
}
