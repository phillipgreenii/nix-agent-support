package config

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/query"
)

// calTrig builds a timer query whose [query.trigger] is kind = "calendar" with
// the given extra lines.
func calTrig(lines ...string) string {
	return timerCfg("[query.trigger]\nkind = \"calendar\"\n" + strings.Join(lines, "\n"))
}

func TestLoad_calendarTriggerBuilds(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, calTrig(`at = "03:30"`, `days = ["sun", "wed"]`, `tz = "America/Los_Angeles"`, `on_missed = "run-now"`, `expires_in = "6h"`))
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ct, ok := query.Calendar(c.Queries[0].Query.Trigger())
	if !ok {
		t.Fatalf("trigger = %#v, want a calendar trigger", c.Queries[0].Query.Trigger())
	}
	if ct.At != (query.ClockTime{Hour: 3, Minute: 30}) || ct.OnMissed != "run-now" || ct.ExpiresIn != 6*time.Hour ||
		ct.Loc.String() != "America/Los_Angeles" || len(ct.Days) != 2 || ct.Days[0] != time.Sunday || ct.Days[1] != time.Wednesday {
		t.Errorf("calendar trigger = %+v", ct)
	}
}

func TestLoad_calendarTriggerDefaultsToHostZoneAndEveryDay(t *testing.T) {
	absentGlobalConfig(t)
	writeCfg(t, calTrig(`at = "00:00"`, `on_missed = "next-slot"`))
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ct, _ := query.Calendar(c.Queries[0].Query.Trigger())
	if ct.Loc != hostZone || len(ct.Days) != 0 || ct.ExpiresIn != 0 {
		t.Errorf("calendar trigger = %+v, want host zone, no days, no expiry", ct)
	}
}

// A zone name validates with time/tzdata embedded (the nix sandbox has no host
// zone files, so this is what keeps the build's check phase green).
func TestLoad_calendarZoneNameValidates(t *testing.T) {
	for _, tz := range []string{"America/Los_Angeles", "Europe/London", "UTC", "Etc/GMT+2"} {
		absentGlobalConfig(t)
		writeCfg(t, calTrig(`at = "03:30"`, `tz = "`+tz+`"`, `on_missed = "next-slot"`))
		if _, err := Load(); err != nil {
			t.Errorf("tz %q: %v", tz, err)
		}
	}
}

func TestLoad_calendarConfigErrors(t *testing.T) {
	ok := []string{`at = "03:30"`, `on_missed = "next-slot"`}
	with := func(repl string, lines ...string) string {
		var out []string
		for _, l := range ok {
			if !strings.HasPrefix(l, repl) {
				out = append(out, l)
			}
		}
		return calTrig(append(out, lines...)...)
	}
	cases := []struct{ name, body, want string }{
		{
			"every with at", timerCfg("[query.trigger]\nkind = \"period\"\nevery = \"5m\"\nat = \"03:30\""),
			`trigger: "every" and "at" are mutually exclusive ("every" belongs to kind = "period", "at" to kind = "calendar")`,
		},
		{
			"every with at on calendar", calTrig(`every = "5m"`, `at = "03:30"`, `on_missed = "next-slot"`),
			`"every" and "at" are mutually exclusive`,
		},
		{
			"every with days", timerCfg("[query.trigger]\nkind = \"period\"\nevery = \"5m\"\ndays = [\"sun\"]"),
			`trigger: "every" and "days" are mutually exclusive ("every" belongs to kind = "period", "days" to kind = "calendar")`,
		},
		{"unknown kind", timerCfg("[query.trigger]\nkind = \"cron\""), `unknown trigger kind "cron" (known: period, threshold, manual, calendar)`},
		{"requires timer", `
[[query]]
name = "c"
type = "command"
emits = ["e"]
[query.command]
argv = ["x"]
format = "jsonl"
[query.trigger]
kind = "calendar"
at = "03:30"
on_missed = "next-slot"

[[role]]
name = "r"
handler = "/bin/h"
binds = ["e"]
`, `calendar trigger requires type = "timer"`},
		{"at missing", with(`at`), `calendar trigger: at is required (24-hour HH:MM, e.g. at = "02:00")`},
		{"at 9:5", with(`at`, `at = "9:5"`), `calendar trigger: at "9:5" is not a 24-hour HH:MM time`},
		{"at with seconds", with(`at`, `at = "03:30:00"`), `at "03:30:00" is not a 24-hour HH:MM time`},
		{"at 24:00", with(`at`, `at = "24:00"`), `at "24:00" is not a 24-hour HH:MM time`},
		{"at 25:00", with(`at`, `at = "25:00"`), `at "25:00" is not a 24-hour HH:MM time`},
		{"at 12:60", with(`at`, `at = "12:60"`), `at "12:60" is not a 24-hour HH:MM time`},
		{"unknown weekday", with(``, `days = ["funday"]`), `calendar trigger: unknown weekday "funday" (known: mon, tue, wed, thu, fri, sat, sun)`},
		{"uppercase weekday", with(``, `days = ["Sun"]`), `unknown weekday "Sun"`},
		{"empty days", with(``, `days = []`), `calendar trigger: days must not be empty (omit days for every day)`},
		{"duplicate days", with(``, `days = ["sun", "mon", "sun"]`), `calendar trigger: days lists "sun" twice`},
		{"fixed offset tz", with(``, `tz = "+02:00"`), `calendar trigger: tz "+02:00" is not an IANA time zone name such as "America/Los_Angeles" (a fixed UTC offset is not accepted because it cannot follow daylight saving)`},
		{"empty tz", with(``, `tz = ""`), `tz "" is not an IANA time zone name`},
		{"Local tz", with(``, `tz = "Local"`), `tz "Local" is not an IANA time zone name`},
		{"relative path tz", with(``, `tz = "../x"`), `tz "../x" is not an IANA time zone name`},
		{"absolute path tz", with(``, `tz = "/usr/share/zoneinfo/UTC"`), `tz "/usr/share/zoneinfo/UTC" is not an IANA time zone name`},
		{"unknown tz", with(``, `tz = "Mars/Olympus"`), `tz "Mars/Olympus" is not an IANA time zone name`},
		{"on_missed absent", with(`on_missed`), `calendar trigger: on_missed is required (run-now fires one catch-up tick when the router was not running or the host was asleep at the slot, next-slot waits for the following slot)`},
		{"on_missed wrong", with(`on_missed`, `on_missed = "now"`), `calendar trigger: on_missed "now" is not valid (known: run-now, next-slot)`},
		{"expires_in zero", with(``, `expires_in = "0s"`), `calendar trigger: expires_in must be greater than 0`},
		{"expires_in negative", with(``, `expires_in = "-1h"`), `calendar trigger: expires_in must be greater than 0`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := loadErr(t, tc.body); !strings.Contains(got, tc.want) || !strings.Contains(got, `query[0] "`) {
				t.Errorf("error = %q, want it to contain %q with the query prefix", got, tc.want)
			}
		})
	}
}

// Every problem is reported together, not one per run.
func TestLoad_calendarErrorsAreAggregated(t *testing.T) {
	got := loadErr(t, calTrig(`at = "9:5"`, `days = ["funday"]`))
	for _, want := range []string{`at "9:5"`, `unknown weekday "funday"`, `on_missed is required`} {
		if !strings.Contains(got, want) {
			t.Errorf("error = %q, missing %q", got, want)
		}
	}
}

// ExpectedIntervalMsFor returns the largest slot gap for a calendar source, and
// an explicit expected_interval still wins.
func TestExpectedIntervalMsFor_calendarIsLargestSlotGap(t *testing.T) {
	// UTC has no DST, so over any 14-day window the gap is exactly 1 or 7 days.
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	old := calendarNow
	calendarNow = func() time.Time { return now }
	defer func() { calendarNow = old }()

	for _, tc := range []struct {
		name  string
		lines []string
		want  int64
	}{
		{"daily", []string{`at = "03:30"`, `tz = "UTC"`, `on_missed = "next-slot"`}, 86400 * 1000},
		{"weekly", []string{`at = "03:30"`, `days = ["sun"]`, `tz = "UTC"`, `on_missed = "next-slot"`}, 604800 * 1000},
	} {
		absentGlobalConfig(t)
		writeCfg(t, calTrig(tc.lines...))
		c, err := Load()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := ExpectedIntervalMsFor(c.Queries[0], c.ExpectedIntervalOverrides); got != tc.want {
			t.Errorf("%s: ExpectedIntervalMsFor = %d, want %d", tc.name, got, tc.want)
		}
	}
	absentGlobalConfig(t)
	writeCfg(t, strings.Replace(calTrig(`at = "03:30"`, `tz = "UTC"`, `on_missed = "next-slot"`), "emits", "expected_interval = \"2h\"\nemits", 1))
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := ExpectedIntervalMsFor(c.Queries[0], c.ExpectedIntervalOverrides); got != 2*3600*1000 {
		t.Errorf("override: ExpectedIntervalMsFor = %d, want 7200000", got)
	}
}
