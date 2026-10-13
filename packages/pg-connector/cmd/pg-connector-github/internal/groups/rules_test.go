package groups

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// defaultRules uses the spec's "Defaults" table: detail and conversation
// 10 min / 30 min, pending 2 min, terminal 24 h.
func defaultRules() *Rules { return NewRules(config.Defaults().Settings) }

func TestScheduleDetailAndConversation(t *testing.T) {
	r := defaultRules()
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
	r := defaultRules()
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
	r := defaultRules()
	for _, g := range All() {
		fresh, max := r.Schedule(g, true, t0)
		if !fresh.Equal(t0.Add(24*time.Hour)) || !max.IsZero() {
			t.Errorf("%s terminal = %v, %v; want +24h and no maximum", g, fresh, max)
		}
	}
}

func TestExtendClampsAtMaxAge(t *testing.T) {
	r := defaultRules()
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
	r := defaultRules()
	for _, g := range All() {
		if got, want := r.Background(g), g != Pending; got != want {
			t.Errorf("Background(%s) = %v, want %v", g, got, want)
		}
		if got, want := r.Stamped(g), g == Files || g == Commits || g == Runs; got != want {
			t.Errorf("Stamped(%s) = %v, want %v", g, got, want)
		}
	}
}

func TestFresh(t *testing.T) {
	r := defaultRules()
	cur := Stamps{Head: "h1", Base: "b1"}
	timed := func(g string) store.GroupRow {
		fresh, max := r.Schedule(g, false, t0)
		return store.GroupRow{Group: g, Content: []byte("x"), FetchedAt: t0, FreshUntil: fresh, MaxAgeAt: max}
	}
	stamped := func(g string) store.GroupRow {
		row := timed(g)
		row.FetchedForHead, row.FetchedForBase = "h1", "b1"
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

// A terminal PR's rows have no maximum age, so a stamped row stays fresh until
// terminal_ttl and a stamp mismatch still wins inside that window.
func TestFreshTerminalRows(t *testing.T) {
	r := defaultRules()
	fresh, max := r.Schedule(Files, true, t0)
	row := store.GroupRow{Group: Files, Content: []byte("x"), FreshUntil: fresh, MaxAgeAt: max, FetchedForHead: "h1", FetchedForBase: "b1"}
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

func TestStampsOfAndFingerprint(t *testing.T) {
	got := StampsOf(summary.Summary{HeadRefOid: "h", BaseRefOid: "b", Title: "ignored"})
	if got != (Stamps{"h", "b"}) {
		t.Errorf("StampsOf = %+v", got)
	}
	// sha256("abc"), the FIPS 180 test vector.
	if fp := Fingerprint([]byte("abc")); fp != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("Fingerprint = %s", fp)
	}
	if Fingerprint([]byte("a")) == Fingerprint([]byte("b")) {
		t.Error("distinct content, same fingerprint")
	}
}
