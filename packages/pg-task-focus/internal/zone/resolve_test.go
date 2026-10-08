package zone_test

import (
	"fmt"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/zone"
)

func TestResolveCivilGapAndOverlap(t *testing.T) {
	// Values checked against the toolchain's zone data on 2026-10-08.
	tests := []struct {
		name string
		zone string
		date civil.Date
		tod  civil.TimeOfDay
		want string
	}{
		{"New York spring gap resolves to the end of the gap", "America/New_York", civil.Date{Year: 2026, Month: 3, Day: 8}, civil.TimeOfDay{Hour: 2, Minute: 30}, "2026-03-08T07:00:00Z"},
		{"New York autumn overlap resolves to the earlier occurrence", "America/New_York", civil.Date{Year: 2026, Month: 11, Day: 1}, civil.TimeOfDay{Hour: 1, Minute: 30}, "2026-11-01T05:30:00Z"},
		{"New York day before spring change", "America/New_York", civil.Date{Year: 2026, Month: 3, Day: 7}, civil.TimeOfDay{Hour: 2, Minute: 30}, "2026-03-07T07:30:00Z"},
		{"New York spring day before the gap", "America/New_York", civil.Date{Year: 2026, Month: 3, Day: 8}, civil.TimeOfDay{Hour: 1, Minute: 30}, "2026-03-08T06:30:00Z"},
		{"New York spring day first instant after the gap", "America/New_York", civil.Date{Year: 2026, Month: 3, Day: 8}, civil.TimeOfDay{Hour: 3, Minute: 0}, "2026-03-08T07:00:00Z"},
		{"New York spring day after the gap", "America/New_York", civil.Date{Year: 2026, Month: 3, Day: 8}, civil.TimeOfDay{Hour: 3, Minute: 30}, "2026-03-08T07:30:00Z"},
		{"New York day after spring change", "America/New_York", civil.Date{Year: 2026, Month: 3, Day: 9}, civil.TimeOfDay{Hour: 2, Minute: 30}, "2026-03-09T06:30:00Z"},
		{"New York day before autumn change", "America/New_York", civil.Date{Year: 2026, Month: 10, Day: 31}, civil.TimeOfDay{Hour: 1, Minute: 30}, "2026-10-31T05:30:00Z"},
		{"New York autumn day before the overlap", "America/New_York", civil.Date{Year: 2026, Month: 11, Day: 1}, civil.TimeOfDay{Hour: 0, Minute: 30}, "2026-11-01T04:30:00Z"},
		{"New York autumn day after the overlap", "America/New_York", civil.Date{Year: 2026, Month: 11, Day: 1}, civil.TimeOfDay{Hour: 2, Minute: 30}, "2026-11-01T07:30:00Z"},
		{"New York day after autumn change", "America/New_York", civil.Date{Year: 2026, Month: 11, Day: 2}, civil.TimeOfDay{Hour: 1, Minute: 30}, "2026-11-02T06:30:00Z"},
		{"Lord Howe half-hour gap, inside", "Australia/Lord_Howe", civil.Date{Year: 2026, Month: 10, Day: 4}, civil.TimeOfDay{Hour: 2, Minute: 15}, "2026-10-03T15:30:00Z"},
		{"Lord Howe half-hour gap, at its end", "Australia/Lord_Howe", civil.Date{Year: 2026, Month: 10, Day: 4}, civil.TimeOfDay{Hour: 2, Minute: 30}, "2026-10-03T15:30:00Z"},
		{"Lord Howe half-hour overlap resolves to the earlier occurrence", "Australia/Lord_Howe", civil.Date{Year: 2026, Month: 4, Day: 5}, civil.TimeOfDay{Hour: 1, Minute: 45}, "2026-04-04T14:45:00Z"},
		{"Santiago gap at midnight", "America/Santiago", civil.Date{Year: 2026, Month: 9, Day: 6}, civil.TimeOfDay{Hour: 0, Minute: 30}, "2026-09-06T04:00:00Z"},
		{"London spring gap", "Europe/London", civil.Date{Year: 2026, Month: 3, Day: 29}, civil.TimeOfDay{Hour: 1, Minute: 30}, "2026-03-29T01:00:00Z"},
		{"Kolkata plain conversion", "Asia/Kolkata", civil.Date{Year: 2026, Month: 10, Day: 7}, civil.TimeOfDay{Hour: 9, Minute: 0}, "2026-10-07T03:30:00Z"},
		{"New York summer plain conversion", "America/New_York", civil.Date{Year: 2026, Month: 7, Day: 15}, civil.TimeOfDay{Hour: 12, Minute: 0}, "2026-07-15T16:00:00Z"},
		{"UTC plain conversion", "UTC", civil.Date{Year: 2026, Month: 10, Day: 7}, civil.TimeOfDay{Hour: 13, Minute: 30}, "2026-10-07T13:30:00Z"},
		{"fixed offset zone plain conversion", "Etc/GMT+5", civil.Date{Year: 2026, Month: 10, Day: 7}, civil.TimeOfDay{Hour: 9, Minute: 0}, "2026-10-07T14:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := zone.ResolveCivil(mustLoad(t, tt.zone), tt.date, tt.tod)
			want := mustTime(t, tt.want)
			if !got.Equal(want) {
				t.Fatalf("ResolveCivil(%s, %v, %v) = %s, want %s", tt.zone, tt.date, tt.tod, got.Format(time.RFC3339), tt.want)
			}
			if got.Location() != time.UTC {
				t.Errorf("result location = %v, want UTC", got.Location())
			}
		})
	}
}

func TestResolveCivilSkippedDate(t *testing.T) {
	// Samoa skipped 2011-12-30 entirely when it crossed the date line.
	z := mustLoad(t, "Pacific/Apia")
	got := zone.ResolveCivil(z, civil.Date{Year: 2011, Month: 12, Day: 30}, civil.TimeOfDay{Hour: 9, Minute: 0})
	want := mustTime(t, "2011-12-30T10:00:00Z")
	if !got.Equal(want) {
		t.Fatalf("ResolveCivil on the skipped date = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	local := got.In(z.Location())
	if local.Year() != 2011 || local.Month() != 12 || local.Day() != 31 || local.Hour() != 0 || local.Minute() != 0 {
		t.Fatalf("result reads %v locally, want 2011-12-31 00:00", local)
	}
}

// transitions returns every zone transition instant in [from, to).
func transitions(z zone.Zone, from, to time.Time) []time.Time {
	var out []time.Time
	cur := from
	for i := 0; i < 256; i++ {
		_, end := cur.In(z.Location()).ZoneBounds()
		if end.IsZero() || !end.Before(to) {
			break
		}
		out = append(out, end)
		cur = end
	}
	return out
}

// wall reads t in loc as a wall-clock time held in a UTC value, so wall
// clock arithmetic is plain subtraction.
func wall(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), l.Nanosecond(), time.UTC)
}

func TestResolveCivilMidnightTransition(t *testing.T) {
	from := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	found := 0
	for _, name := range []string{"America/Santiago", "America/Havana", "Asia/Beirut"} {
		z := mustLoad(t, name)
		for _, tr := range transitions(z, from, to) {
			// A gap that starts at local 00:00: the old offset's clock
			// would read 00:00 at the transition, the new one reads later.
			gapStart := wall(tr.Add(-time.Nanosecond), z.Location()).Add(time.Nanosecond)
			if !wall(tr, z.Location()).After(gapStart) || gapStart.Hour() != 0 || gapStart.Minute() != 0 {
				continue
			}
			found++
			date := civil.Date{Year: gapStart.Year(), Month: gapStart.Month(), Day: gapStart.Day()}
			got := zone.ResolveCivil(z, date, civil.TimeOfDay{Hour: 0, Minute: 0})
			if !got.Equal(tr) {
				t.Errorf("%s %v 00:00 = %s, want the transition %s", name, date, got.Format(time.RFC3339), tr.Format(time.RFC3339))
			}
			local := got.In(z.Location())
			if local.Hour() != 1 || local.Minute() != 0 || local.Day() != date.Day || local.Month() != date.Month {
				t.Errorf("%s %v 00:00 reads %v locally, want 01:00 on the same civil date", name, date, local)
			}
		}
	}
	if found == 0 {
		t.Fatal("no zone among Santiago, Havana and Beirut has a gap that starts at local midnight in 2020 to 2030")
	}
}

var propertyZones = []string{
	"America/New_York", "Europe/London", "Asia/Kolkata", "Australia/Lord_Howe",
	"America/Santiago", "Pacific/Apia", "Australia/Sydney", "America/Havana",
	"Asia/Beirut", "Pacific/Chatham", "America/St_Johns", "UTC",
}

type propertyCase struct {
	z       zone.Zone
	nearTr  []civil.Date
	display string
}

func (c propertyCase) String() string { return c.display }

func TestResolveCivilProperty(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)
	start := civil.Date{Year: 2026, Month: time.January, Day: 1}
	var cases []propertyCase
	for _, name := range propertyZones {
		z := mustLoad(t, name)
		c := propertyCase{z: z, display: name}
		for _, tr := range transitions(z, from, to) {
			l := tr.In(z.Location())
			d := civil.Date{Year: l.Year(), Month: l.Month(), Day: l.Day()}
			c.nearTr = append(c.nearTr, d.AddDays(-1), d, d.AddDays(1))
		}
		cases = append(cases, c)
	}

	rapid.Check(t, func(rt *rapid.T) {
		c := rapid.SampledFrom(cases).Draw(rt, "zone")
		var d civil.Date
		if len(c.nearTr) > 0 && rapid.Bool().Draw(rt, "nearTransition") {
			d = rapid.SampledFrom(c.nearTr).Draw(rt, "date")
		} else {
			d = start.AddDays(rapid.IntRange(0, 729).Draw(rt, "dayOfTwoYears"))
		}
		step := rapid.IntRange(0, 95).Draw(rt, "quarterHour")
		tod := civil.TimeOfDay{Hour: step / 4, Minute: (step % 4) * 15}
		checkResolution(rt, c.z, d, tod)
	})
}

func checkResolution(t *rapid.T, z zone.Zone, d civil.Date, tod civil.TimeOfDay) {
	loc := z.Location()
	got := zone.ResolveCivil(z, d, tod)
	req := time.Date(d.Year, d.Month, d.Day, tod.Hour, tod.Minute, 0, 0, time.UTC)
	desc := fmt.Sprintf("%s %v %v -> %s", z.Name(), d, tod, got.Format(time.RFC3339Nano))

	if got.Location() != time.UTC {
		t.Fatalf("%s: result is not in UTC", desc)
	}

	// The candidate instants: the civil time read in each offset in effect a
	// day either side, kept when the instant reads back as the request.
	var offsets []int
	for _, probe := range []time.Time{req.Add(-24 * time.Hour), req.Add(24 * time.Hour)} {
		_, off := probe.In(loc).Zone()
		offsets = append(offsets, off)
	}
	largest := max(offsets[0], offsets[1])
	earliest := req.Add(-time.Duration(largest) * time.Second)
	if got.Before(earliest) {
		t.Fatalf("%s: earlier than the earliest candidate %s", desc, earliest.Format(time.RFC3339))
	}
	var valid []time.Time
	for _, off := range offsets {
		cand := req.Add(-time.Duration(off) * time.Second)
		if wall(cand, loc).Equal(req) {
			dup := false
			for _, v := range valid {
				dup = dup || v.Equal(cand)
			}
			if !dup {
				valid = append(valid, cand)
			}
		}
	}

	switch len(valid) {
	case 1:
		if !got.Equal(valid[0]) {
			t.Fatalf("%s: civil time exists once at %s", desc, valid[0].Format(time.RFC3339))
		}
		if !wall(got, loc).Equal(req) {
			t.Fatalf("%s: result does not read back as the request", desc)
		}
	case 2:
		want := valid[0]
		if valid[1].Before(want) {
			want = valid[1]
		}
		if !got.Equal(want) {
			t.Fatalf("%s: overlap must resolve to the earlier occurrence %s", desc, want.Format(time.RFC3339))
		}
	case 0:
		start, _ := got.In(loc).ZoneBounds()
		if !start.Equal(got) {
			t.Fatalf("%s: a gap must resolve to the transition instant, but %s starts an interval at %s", desc, got.Format(time.RFC3339), start.Format(time.RFC3339))
		}
		if wall(got, loc).Before(req) || !wall(got.Add(-time.Nanosecond), loc).Before(req) {
			t.Fatalf("%s: result is not the first valid instant after the gap", desc)
		}
	default:
		t.Fatalf("%s: impossible candidate count %d", desc, len(valid))
	}
}
