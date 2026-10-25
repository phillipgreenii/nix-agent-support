package groups

import (
	"testing"
	"time"
)

var (
	t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	st = Settings{SummaryTTL: time.Minute, DetailTTL: 10 * time.Minute, DetailMaxAge: time.Hour, TerminalTTL: 24 * time.Hour}
)

func TestRulesSpecs(t *testing.T) {
	tbl := Rules(st)
	s, ok := tbl.Spec(Summary)
	if !ok || s.TTL != time.Minute || s.MaxAge != 0 || !s.Background || s.Extendable || s.Stamped {
		t.Fatalf("summary spec = %+v", s)
	}
	d, ok := tbl.Spec(Detail)
	if !ok || d.TTL != 10*time.Minute || d.MaxAge != time.Hour || !d.Extendable || !d.Background {
		t.Fatalf("detail spec = %+v", d)
	}
	if tbl.TerminalTTL != 24*time.Hour {
		t.Fatalf("terminal ttl = %v", tbl.TerminalTTL)
	}
	if _, ok := tbl.Spec("children"); ok {
		t.Fatal("children must not be a stored group")
	}
}

func TestScheduleOpenAndClosed(t *testing.T) {
	tbl := Rules(st)
	fu, ma := tbl.Schedule(Summary, false, t0)
	if !fu.Equal(t0.Add(time.Minute)) || !ma.IsZero() {
		t.Fatalf("open summary: %v %v", fu, ma)
	}
	fu, ma = tbl.Schedule(Detail, false, t0)
	if !fu.Equal(t0.Add(10*time.Minute)) || !ma.Equal(t0.Add(time.Hour)) {
		t.Fatalf("open detail: %v %v", fu, ma)
	}
	closedAt := t0.Add(-25 * time.Hour)
	term := Terminal(closedAt, t0, st.TerminalTTL)
	for _, g := range All() {
		fu, ma = tbl.Schedule(g, term, t0)
		if !fu.Equal(t0.Add(24*time.Hour)) || !ma.IsZero() {
			t.Fatalf("closed %s: %v %v", g, fu, ma)
		}
	}
}

func TestClosedLessThanTerminalTTLUsesNormalTTL(t *testing.T) {
	tbl := Rules(st)
	term := Terminal(t0.Add(-time.Hour), t0, st.TerminalTTL)
	if term {
		t.Fatal("closed for an hour must not be terminal")
	}
	fu, _ := tbl.Schedule(Summary, term, t0)
	if !fu.Equal(t0.Add(time.Minute)) {
		t.Fatalf("fresh_until = %v", fu)
	}
}

func TestTerminalBoundary(t *testing.T) {
	ttl := 24 * time.Hour
	closed := t0
	cases := []struct {
		name     string
		closedAt time.Time
		now      time.Time
		want     bool
	}{
		{"zero closedAt", time.Time{}, t0.Add(100 * time.Hour), false},
		{"before", closed, closed.Add(ttl - time.Nanosecond), false},
		{"exactly", closed, closed.Add(ttl), true},
		{"after", closed, closed.Add(ttl + time.Second), true},
	}
	for _, c := range cases {
		if got := Terminal(c.closedAt, c.now, ttl); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestExtendDetailOnly(t *testing.T) {
	tbl := Rules(st)
	// Extendable detail is recognised by the table; summary is not extendable.
	d, _ := tbl.Spec(Detail)
	s, _ := tbl.Spec(Summary)
	if !d.Extendable || s.Extendable {
		t.Fatal("only detail is extendable")
	}
}
