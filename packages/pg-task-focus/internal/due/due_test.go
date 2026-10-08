package due_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/due"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/zone"
)

func mustZone(t testing.TB, name string) zone.Zone {
	t.Helper()
	z, err := zone.Load(name)
	if err != nil {
		t.Fatalf("zone.Load(%q): %v", name, err)
	}
	return z
}

func date(y int, m time.Month, d int) civil.Date { return civil.Date{Year: y, Month: m, Day: d} }

func mustTime(t testing.TB, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("time.Parse(%q): %v", s, err)
	}
	return v
}

func weekday(w time.Weekday) *time.Weekday { return &w }
func intp(n int) *int                      { return &n }

// failer is the part of testing.TB that *testing.T and *rapid.T share.
type failer interface {
	Helper()
	Fatalf(format string, args ...any)
}

func noMatch(t failer, err error) *due.NoMatch {
	t.Helper()
	var nm *due.NoMatch
	if !errors.As(err, &nm) {
		t.Fatalf("error = %v (%T), want *due.NoMatch", err, err)
	}
	return nm
}

func TestResolveDaily(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	tests := []struct {
		name     string
		rule     due.Rule
		day      civil.Date
		wantAt   string
		wantDate civil.Date
	}{
		{"nine in the morning in New York on 2026-10-07", due.Rule{At: civil.TimeOfDay{Hour: 9}, TZ: ny}, date(2026, 10, 7), "2026-10-07T13:00:00Z", date(2026, 10, 7)},
		{"after the autumn change the offset is standard", due.Rule{At: civil.TimeOfDay{Hour: 9}, TZ: ny}, date(2026, 11, 2), "2026-11-02T14:00:00Z", date(2026, 11, 2)},
		{"half-hour offset zone", due.Rule{At: civil.TimeOfDay{Hour: 9}, TZ: mustZone(t, "Asia/Kolkata")}, date(2026, 10, 7), "2026-10-07T03:30:00Z", date(2026, 10, 7)},
		{"end of day in UTC", due.Rule{At: civil.TimeOfDay{Hour: 23, Minute: 59}, TZ: mustZone(t, "UTC")}, date(2026, 10, 7), "2026-10-07T23:59:00Z", date(2026, 10, 7)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := due.Resolve(due.Daily, tt.rule, tt.day, tt.day)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if !got.Instant.Equal(mustTime(t, tt.wantAt)) {
				t.Errorf("Instant = %s, want %s", got.Instant.Format(time.RFC3339), tt.wantAt)
			}
			if got.Instant.Location() != time.UTC {
				t.Errorf("Instant location = %v, want UTC", got.Instant.Location())
			}
			if got.Date != tt.wantDate {
				t.Errorf("Date = %v, want %v", got.Date, tt.wantDate)
			}
		})
	}
}

func TestResolveWeekly(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	thu := due.Rule{At: civil.TimeOfDay{Hour: 9}, TZ: ny, Weekday: weekday(time.Thursday)}
	tests := []struct {
		name       string
		rule       due.Rule
		start, end civil.Date
		wantAt     string
		wantDate   civil.Date
		wantNoHit  bool
	}{
		{"thursday in the week 2026-10-05 to 2026-10-11", thu, date(2026, 10, 5), date(2026, 10, 11), "2026-10-08T13:00:00Z", date(2026, 10, 8), false},
		{"a ten day week with two thursdays resolves to the first", thu, date(2026, 10, 8), date(2026, 10, 17), "2026-10-08T13:00:00Z", date(2026, 10, 8), false},
		{"a ten day week starting after a thursday resolves to the next one", thu, date(2026, 10, 9), date(2026, 10, 18), "2026-10-15T13:00:00Z", date(2026, 10, 15), false},
		{"a one day week that is the weekday", thu, date(2026, 10, 8), date(2026, 10, 8), "2026-10-08T13:00:00Z", date(2026, 10, 8), false},
		{"the weekday on the last civil date of the period", thu, date(2026, 10, 2), date(2026, 10, 8), "2026-10-08T13:00:00Z", date(2026, 10, 8), false},
		{"a period holding no thursday", thu, date(2026, 10, 9), date(2026, 10, 14), "", civil.Date{}, true},
		{"a one day period that is another weekday", thu, date(2026, 10, 7), date(2026, 10, 7), "", civil.Date{}, true},
		{"sunday in the week 2026-10-05 to 2026-10-11", due.Rule{At: civil.TimeOfDay{Hour: 18}, TZ: ny, Weekday: weekday(time.Sunday)}, date(2026, 10, 5), date(2026, 10, 11), "2026-10-11T22:00:00Z", date(2026, 10, 11), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := due.Resolve(due.Weekly, tt.rule, tt.start, tt.end)
			if tt.wantNoHit {
				nm := noMatch(t, err)
				if !strings.Contains(nm.Reason, "thu") || !strings.Contains(nm.Reason, tt.start.String()) || !strings.Contains(nm.Reason, tt.end.String()) {
					t.Errorf("Reason = %q, want it to name the weekday and the period's dates", nm.Reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if !got.Instant.Equal(mustTime(t, tt.wantAt)) || got.Date != tt.wantDate {
				t.Errorf("got %s on %v, want %s on %v", got.Instant.Format(time.RFC3339), got.Date, tt.wantAt, tt.wantDate)
			}
		})
	}
}

func TestResolveSprintDay(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	rule := func(day int) due.Rule {
		return due.Rule{At: civil.TimeOfDay{Hour: 9}, TZ: ny, Day: intp(day)}
	}
	start, end := date(2026, 10, 5), date(2026, 10, 18) // a 14 day sprint, end inclusive
	t.Run("day 1 is the sprint's first civil date", func(t *testing.T) {
		got, err := due.Resolve(due.Sprint, rule(1), start, end)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got.Date != start || !got.Instant.Equal(mustTime(t, "2026-10-05T13:00:00Z")) {
			t.Errorf("got %s on %v", got.Instant.Format(time.RFC3339), got.Date)
		}
	})
	t.Run("day 14 of a 14 day sprint is the last civil date", func(t *testing.T) {
		got, err := due.Resolve(due.Sprint, rule(14), start, end)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got.Date != end || !got.Instant.Equal(mustTime(t, "2026-10-18T13:00:00Z")) {
			t.Errorf("got %s on %v", got.Instant.Format(time.RFC3339), got.Date)
		}
	})
	t.Run("day 15 of a 14 day sprint matches no date", func(t *testing.T) {
		_, err := due.Resolve(due.Sprint, rule(15), start, end)
		nm := noMatch(t, err)
		for _, want := range []string{"15", "2026-10-18"} {
			if !strings.Contains(nm.Reason, want) {
				t.Errorf("Reason = %q, want it to name %q", nm.Reason, want)
			}
		}
		if nm.Error() != nm.Reason {
			t.Errorf("Error() = %q, want the reason %q", nm.Error(), nm.Reason)
		}
	})
	t.Run("a sprint across a month end counts calendar days", func(t *testing.T) {
		got, err := due.Resolve(due.Sprint, rule(5), date(2026, 10, 28), date(2026, 11, 10))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got.Date != date(2026, 11, 1) {
			t.Errorf("Date = %v, want 2026-11-01", got.Date)
		}
		// New York returns to standard time at 02:00 on 2026-11-01, so 09:00
		// that day is UTC-5.
		if !got.Instant.Equal(mustTime(t, "2026-11-01T14:00:00Z")) {
			t.Errorf("Instant = %s, want 2026-11-01T14:00:00Z", got.Instant.Format(time.RFC3339))
		}
	})
}

func TestRuleZoneDiffersFromPeriodZone(t *testing.T) {
	// The period's zone plays no part: Resolve takes civil dates only, and the
	// rule resolves in its own zone. The same civil date and time of day gives
	// a different instant in a different rule zone.
	day := date(2026, 10, 7)
	at := civil.TimeOfDay{Hour: 9}
	ny, err := due.Resolve(due.Daily, due.Rule{At: at, TZ: mustZone(t, "America/New_York")}, day, day)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	tokyo, err := due.Resolve(due.Daily, due.Rule{At: at, TZ: mustZone(t, "Asia/Tokyo")}, day, day)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !ny.Instant.Equal(mustTime(t, "2026-10-07T13:00:00Z")) {
		t.Errorf("New York instant = %s", ny.Instant.Format(time.RFC3339))
	}
	if !tokyo.Instant.Equal(mustTime(t, "2026-10-07T00:00:00Z")) {
		t.Errorf("Tokyo instant = %s", tokyo.Instant.Format(time.RFC3339))
	}
	if ny.Date != tokyo.Date {
		t.Errorf("dates differ: %v and %v; the date is the civil date resolved, not an instant's date", ny.Date, tokyo.Date)
	}
}

func TestResolveOnTransitionDay(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	tests := []struct {
		name   string
		at     civil.TimeOfDay
		day    civil.Date
		wantAt string
	}{
		{"a 02:30 rule in the spring forward gap resolves to 03:00 local", civil.TimeOfDay{Hour: 2, Minute: 30}, date(2026, 3, 8), "2026-03-08T07:00:00Z"},
		{"a 01:30 rule in the autumn overlap resolves to its earlier occurrence", civil.TimeOfDay{Hour: 1, Minute: 30}, date(2026, 11, 1), "2026-11-01T05:30:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := due.Resolve(due.Daily, due.Rule{At: tt.at, TZ: ny}, tt.day, tt.day)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if !got.Instant.Equal(mustTime(t, tt.wantAt)) {
				t.Errorf("Instant = %s, want %s", got.Instant.Format(time.RFC3339), tt.wantAt)
			}
		})
	}
	t.Run("a weekly rule resolving on the transition day uses the gap rule", func(t *testing.T) {
		r := due.Rule{At: civil.TimeOfDay{Hour: 2, Minute: 30}, TZ: ny, Weekday: weekday(time.Sunday)}
		got, err := due.Resolve(due.Weekly, r, date(2026, 3, 2), date(2026, 3, 8))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if !got.Instant.Equal(mustTime(t, "2026-03-08T07:00:00Z")) {
			t.Errorf("Instant = %s, want 2026-03-08T07:00:00Z", got.Instant.Format(time.RFC3339))
		}
	})
}

func TestResolveRejectsMalformedInput(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	good := due.Rule{At: civil.TimeOfDay{Hour: 9}, TZ: ny}
	d := date(2026, 10, 7)
	tests := []struct {
		name       string
		c          due.Cadence
		r          due.Rule
		start, end civil.Date
	}{
		{"unknown cadence", due.Cadence("monthly"), good, d, d},
		{"daily period that is more than one day", due.Daily, good, d, d.AddDays(1)},
		{"end before start", due.Weekly, due.Rule{At: good.At, TZ: ny, Weekday: weekday(time.Monday)}, d, d.AddDays(-1)},
		{"rule that does not fit its cadence", due.Daily, due.Rule{At: good.At, TZ: ny, Day: intp(1)}, d, d},
		{"rule without a zone", due.Daily, due.Rule{At: good.At}, d, d},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := due.Resolve(tt.c, tt.r, tt.start, tt.end)
			if err == nil {
				t.Fatal("Resolve returned no error")
			}
			var nm *due.NoMatch
			if errors.As(err, &nm) {
				t.Errorf("error is *NoMatch (%v); malformed input is not a rule that matches no date", err)
			}
		})
	}
}

func TestRuleJSONRoundTrip(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	tests := []struct {
		name string
		c    due.Cadence
		json string
		want due.Rule
	}{
		{"daily", due.Daily, `{"at":"09:00","tz":"America/New_York"}`, due.Rule{At: civil.TimeOfDay{Hour: 9}, TZ: ny}},
		{"weekly", due.Weekly, `{"at":"09:30","tz":"America/New_York","weekday":"thu"}`, due.Rule{At: civil.TimeOfDay{Hour: 9, Minute: 30}, TZ: ny, Weekday: weekday(time.Thursday)}},
		{"weekly sunday", due.Weekly, `{"at":"18:00","tz":"America/New_York","weekday":"sun"}`, due.Rule{At: civil.TimeOfDay{Hour: 18}, TZ: ny, Weekday: weekday(time.Sunday)}},
		{"sprint", due.Sprint, `{"at":"09:00","tz":"America/New_York","day":3}`, due.Rule{At: civil.TimeOfDay{Hour: 9}, TZ: ny, Day: intp(3)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got due.Rule
			if err := json.Unmarshal([]byte(tt.json), &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if err := got.Validate(tt.c); err != nil {
				t.Fatalf("Validate(%s): %v", tt.c, err)
			}
			if !equalRules(got, tt.want) {
				t.Fatalf("decoded %+v, want %+v", got, tt.want)
			}
			out, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(out) != tt.json {
				t.Errorf("Marshal = %s, want %s", out, tt.json)
			}
		})
	}
}

func equalRules(a, b due.Rule) bool {
	if a.At != b.At || a.TZ.Name() != b.TZ.Name() {
		return false
	}
	if (a.Weekday == nil) != (b.Weekday == nil) || (a.Weekday != nil && *a.Weekday != *b.Weekday) {
		return false
	}
	return (a.Day == nil) == (b.Day == nil) && (a.Day == nil || *a.Day == *b.Day)
}

func TestRuleValidateFieldsPerCadence(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	at := civil.TimeOfDay{Hour: 9}
	tests := []struct {
		name    string
		c       due.Cadence
		r       due.Rule
		wantErr bool
	}{
		{"daily with only at and tz", due.Daily, due.Rule{At: at, TZ: ny}, false},
		{"daily carrying weekday", due.Daily, due.Rule{At: at, TZ: ny, Weekday: weekday(time.Monday)}, true},
		{"daily carrying day", due.Daily, due.Rule{At: at, TZ: ny, Day: intp(1)}, true},
		{"weekly with weekday", due.Weekly, due.Rule{At: at, TZ: ny, Weekday: weekday(time.Monday)}, false},
		{"weekly missing weekday", due.Weekly, due.Rule{At: at, TZ: ny}, true},
		{"weekly carrying day", due.Weekly, due.Rule{At: at, TZ: ny, Weekday: weekday(time.Monday), Day: intp(1)}, true},
		{"sprint with day", due.Sprint, due.Rule{At: at, TZ: ny, Day: intp(1)}, false},
		{"sprint missing day", due.Sprint, due.Rule{At: at, TZ: ny}, true},
		{"sprint with day zero", due.Sprint, due.Rule{At: at, TZ: ny, Day: intp(0)}, true},
		{"sprint with negative day", due.Sprint, due.Rule{At: at, TZ: ny, Day: intp(-2)}, true},
		{"sprint carrying weekday", due.Sprint, due.Rule{At: at, TZ: ny, Day: intp(1), Weekday: weekday(time.Monday)}, true},
		{"rule without a zone", due.Daily, due.Rule{At: at}, true},
		{"unknown cadence", due.Cadence("monthly"), due.Rule{At: at, TZ: ny}, true},
		{"weekday outside sunday to saturday", due.Weekly, due.Rule{At: at, TZ: ny, Weekday: weekday(time.Weekday(7))}, true},
		{"time of day out of range", due.Daily, due.Rule{At: civil.TimeOfDay{Hour: 24}, TZ: ny}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.r.Validate(tt.c)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate(%s) = %v, wantErr %v", tt.c, err, tt.wantErr)
			}
		})
	}
}

func TestRuleJSONRejectsMalformedSnapshots(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{"missing at", `{"tz":"America/New_York"}`},
		{"missing tz", `{"at":"09:00"}`},
		{"invalid zone", `{"at":"09:00","tz":"Local"}`},
		{"empty zone", `{"at":"09:00","tz":""}`},
		{"invalid time of day", `{"at":"9:00","tz":"America/New_York"}`},
		{"invalid weekday", `{"at":"09:00","tz":"America/New_York","weekday":"thursday"}`},
		{"unknown field", `{"at":"09:00","tz":"America/New_York","extra":1}`},
		{"day is not an integer", `{"at":"09:00","tz":"America/New_York","day":1.5}`},
		{"trailing data", `{"at":"09:00","tz":"America/New_York"} {}`},
		{"not an object", `"09:00"`},
		{"null", `null`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r due.Rule
			if err := json.Unmarshal([]byte(tt.json), &r); err == nil {
				t.Fatalf("Unmarshal(%s) = %+v, want an error", tt.json, r)
			}
		})
	}
}

func TestRuleJSONMarshalRejectsAnInvalidRule(t *testing.T) {
	// A zero rule names no zone; it MUST NOT be written as a snapshot.
	if _, err := json.Marshal(due.Rule{}); err == nil {
		t.Fatal("Marshal of a rule with no zone returned no error")
	}
	if _, err := json.Marshal(due.Rule{TZ: mustZone(t, "UTC"), Weekday: weekday(time.Weekday(9))}); err == nil {
		t.Fatal("Marshal of a rule with an impossible weekday returned no error")
	}
}

var propertyZones = []string{"America/New_York", "Europe/London", "Asia/Kolkata", "Australia/Lord_Howe", "America/Santiago", "Pacific/Apia", "UTC"}

func genDate(t *rapid.T) civil.Date {
	base := civil.Date{Year: 2024, Month: time.January, Day: 1}
	return base.AddDays(rapid.IntRange(0, 365*4).Draw(t, "offset"))
}

func TestResolveSprintDayProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		z, err := zone.Load(rapid.SampledFrom(propertyZones).Draw(t, "zone"))
		if err != nil {
			t.Fatal(err)
		}
		start := genDate(t)
		span := rapid.IntRange(1, 60).Draw(t, "span")
		end := start.AddDays(span - 1)
		day := rapid.IntRange(1, 80).Draw(t, "day")
		rule := due.Rule{At: civil.TimeOfDay{Hour: rapid.IntRange(0, 23).Draw(t, "hour"), Minute: rapid.IntRange(0, 59).Draw(t, "minute")}, TZ: z, Day: &day}

		got, err := due.Resolve(due.Sprint, rule, start, end)
		if day > span {
			noMatch(t, err)
			return
		}
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got.Date != start.AddDays(day-1) {
			t.Fatalf("Date = %v, want %v", got.Date, start.AddDays(day-1))
		}
		if want := zone.ResolveCivil(z, got.Date, rule.At); !got.Instant.Equal(want) {
			t.Fatalf("Instant = %v, want the zone's resolution %v", got.Instant, want)
		}
	})
}

func TestResolveWeeklyFirstOccurrenceProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		z, err := zone.Load(rapid.SampledFrom(propertyZones).Draw(t, "zone"))
		if err != nil {
			t.Fatal(err)
		}
		start := genDate(t)
		span := rapid.IntRange(1, 21).Draw(t, "span")
		end := start.AddDays(span - 1)
		wd := time.Weekday(rapid.IntRange(0, 6).Draw(t, "weekday"))
		rule := due.Rule{At: civil.TimeOfDay{Hour: 9}, TZ: z, Weekday: &wd}

		got, err := due.Resolve(due.Weekly, rule, start, end)
		var first *civil.Date
		for i := 0; i < span; i++ {
			if d := start.AddDays(i); d.Weekday() == wd {
				first = &d
				break
			}
		}
		if first == nil {
			noMatch(t, err)
			return
		}
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got.Date != *first {
			t.Fatalf("Date = %v, want the first %v in the period: %v", got.Date, wd, *first)
		}
	})
}
