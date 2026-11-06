package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/calstate"
	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/query"
)

func laTrigger(t *testing.T, onMissed string) query.CalendarTrigger {
	t.Helper()
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	return query.CalendarTrigger{At: query.ClockTime{Hour: 3, Minute: 30}, Days: []time.Weekday{time.Sunday}, Loc: loc, OnMissed: onMissed, TZSet: true}
}

func calSource(t *testing.T, name, onMissed string) query.Source {
	t.Helper()
	return query.Source{Name: name, Query: query.TimerQuery{
		Meta:     query.Meta{EmitTypes: []string{"scan.due"}, Trig: laTrigger(t, onMissed)},
		Metadata: map[string]string{"lens": "security"},
	}}
}

// The status line for a fired slot and a SKIPPED slot, built from a state file
// written through calstate, as the status command renders it.
func TestAttachCalendar_firedAndSkippedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, calstate.FileName)
	loc, _ := time.LoadLocation("America/Los_Angeles")
	slot := time.Date(2026, 11, 1, 3, 30, 0, 0, loc)
	if err := calstate.Write(path, map[string]calstate.Record{
		"fired-q":   {LastSlot: slot, Outcome: calstate.OutcomeFired, DecidedAt: slot},
		"skipped-q": {LastSlot: slot, Outcome: calstate.OutcomeSkipped, Reason: calstate.ReasonRouterNotRunning, DecidedAt: slot},
		"catchup-q": {LastSlot: slot, Outcome: calstate.OutcomeCatchUp, Reason: calstate.ReasonHostAsleep, DecidedAt: slot},
		"late-q":    {LastSlot: slot, Outcome: calstate.OutcomeFired, Reason: "late by 40s", DecidedAt: slot},
	}); err != nil {
		t.Fatal(err)
	}
	srcs := query.SourceSet{
		calSource(t, "fired-q", "run-now"), calSource(t, "skipped-q", "next-slot"),
		calSource(t, "catchup-q", "run-now"), calSource(t, "late-q", "run-now"), calSource(t, "new-q", "run-now"),
		{Name: "plain", Query: query.TimerQuery{Meta: query.Meta{EmitTypes: []string{"x"}}}},
	}
	reports := make([]core.SourceReport, len(srcs))
	for i, s := range srcs {
		reports[i] = core.SourceReport{Name: s.Name}
	}
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	attachCalendar(reports, srcs, path, now)

	if reports[5].Calendar != nil {
		t.Fatal("a non-calendar source must get no calendar info")
	}
	view := func(i int) calendarView {
		c := reports[i].Calendar
		v := calendarView{Schedule: c.Schedule, OnMissed: c.OnMissed, Next: c.Next}
		if c.Last != nil {
			v.Last = &struct {
				Slot    string `json:"slot"`
				Outcome string `json:"outcome"`
				Reason  string `json:"reason"`
			}{c.Last.Slot, c.Last.Outcome, c.Last.Reason}
		}
		return v
	}
	const sched = "calendar 03:30 sun America/Los_Angeles"
	want := []string{
		"fired-q  " + sched + "  last: 2026-11-01T03:30-08:00 fired | next: 2026-11-08T03:30-08:00 | on_missed=run-now",
		"skipped-q  " + sched + "  last: 2026-11-01T03:30-08:00 SKIPPED (on_missed=next-slot, router not running) | next: 2026-11-08T03:30-08:00",
		"catchup-q  " + sched + "  last: 2026-11-01T03:30-08:00 catch-up (host asleep) | next: 2026-11-08T03:30-08:00 | on_missed=run-now",
		"late-q  " + sched + "  last: 2026-11-01T03:30-08:00 fired (late by 40s) | next: 2026-11-08T03:30-08:00 | on_missed=run-now",
		"new-q  " + sched + "  last: no slot handled yet | next: 2026-11-08T03:30-08:00 | on_missed=run-now",
	}
	for i, w := range want {
		if got := view(i).line(srcs[i].Name); got != w {
			t.Errorf("line %d:\n got %q\nwant %q", i, got, w)
		}
	}
}

// A corrupt state file reads as no state in the status view (doctor reports it).
func TestAttachCalendar_corruptStateReadsAsNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), calstate.FileName)
	if err := os.WriteFile(path, []byte("{trunc"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcs := query.SourceSet{calSource(t, "q", "run-now")}
	reports := []core.SourceReport{{Name: "q"}}
	attachCalendar(reports, srcs, path, time.Now())
	if reports[0].Calendar == nil || reports[0].Calendar.Last != nil {
		t.Fatalf("calendar = %+v, want schedule with no last", reports[0].Calendar)
	}
}

func TestRenderStatusText_printsCalendarLine(t *testing.T) {
	var st statusReply
	st.Sources = []sourceView{{Name: "weekly-scan", Type: "pull", Mode: "pull", Calendar: &calendarView{
		Schedule: "calendar 03:30 sun America/Los_Angeles", OnMissed: "run-now", Next: "2026-11-08T03:30-08:00",
	}}}
	var b strings.Builder
	renderStatusText(&b, "sock", st)
	if !strings.Contains(b.String(), "    weekly-scan  calendar 03:30 sun America/Los_Angeles  last: no slot handled yet | next: 2026-11-08T03:30-08:00 | on_missed=run-now\n") {
		t.Fatalf("status text lacks the calendar line:\n%s", b.String())
	}
}

func TestRunQuerySmoke_calendarShowsMetadataSlotNowAndNextThree(t *testing.T) {
	src := calSource(t, "weekly-scan", "run-now")
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	cs := calendarSmokeFor(src, now)
	if cs == nil || cs.SmokeSlot != "now" || len(cs.Next) != 3 || cs.Next[0] != "2026-11-08T03:30-08:00" || cs.Next[1] != "2026-11-15T03:30-08:00" {
		t.Fatalf("smoke = %+v", cs)
	}
	if calendarSmokeFor(query.Source{Name: "p", Query: query.TimerQuery{}}, now) != nil {
		t.Fatal("a period source has no calendar smoke")
	}
	evts, err := src.Query.Run(t.Context(), query.Env{})
	if err != nil {
		t.Fatal(err)
	}
	m := []runQueryMatch{{ID: evts[0].Item.ID, Type: evts[0].Item.Type, Title: evts[0].Item.Title, Metadata: evts[0].Item.Metadata}}
	var text, js strings.Builder
	renderRunQueryText(&text, "weekly-scan", m, cs)
	for _, w := range []string{"metadata: lens=security scheduled_for=", "the smoke tick's slot is now", "next fire times: 2026-11-08T03:30-08:00, 2026-11-15T03:30-08:00, 2026-11-22T03:30-08:00"} {
		if !strings.Contains(text.String(), w) {
			t.Errorf("text lacks %q:\n%s", w, text.String())
		}
	}
	renderRunQueryJSON(&js, "weekly-scan", m, cs)
	for _, w := range []string{`"metadata"`, `"lens":"security"`, `"smokeSlot":"now"`, `"next"`} {
		if !strings.Contains(js.String(), w) {
			t.Errorf("json lacks %q:\n%s", w, js.String())
		}
	}
}

// calendarWorkspace is a hermetic config with one calendar query and one role.
func calendarWorkspace(t *testing.T, trigger string) doctorWorkspace {
	t.Helper()
	w := newDoctorWorkspace(t, "/bin/echo")
	w.writeStub(t, "handler", "exit 0")
	cfg := `[[query]]
name = "weekly-scan"
type = "timer"
emits = ["scan.due"]
metadata = { lens = "security" }
[query.trigger]
kind = "calendar"
at = "03:30"
days = ["sun"]
on_missed = "run-now"
` + trigger + `
[[role]]
name = "k11-live"
enabled = true
binds = ["scan.due"]
handler = "` + w.handler + `"
`
	if err := os.WriteFile(filepath.Join(w.dir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestConfigCheck_printsCalendarScheduleAndNextFire(t *testing.T) {
	calendarWorkspace(t, `tz = "America/Los_Angeles"`)
	var stdout, stderr strings.Builder
	if code := configCheck(&stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr=%q", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 || lines[0] != "config ok" ||
		!strings.HasPrefix(lines[1], "weekly-scan: calendar 03:30 sun America/Los_Angeles on_missed=run-now next fire 20") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestDoctor_calendarStateWarnsOnceWhenCorrupt(t *testing.T) {
	w := calendarWorkspace(t, `tz = "America/Los_Angeles"`)
	if err := os.WriteFile(filepath.Join(w.logDir, calstate.FileName), []byte("{trunc"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	doctor(&out, "", "")
	if n := strings.Count(out.String(), "warn calendar state"); n != 1 {
		t.Fatalf("want exactly one calendar state warn line:\n%s", out.String())
	}
}

func TestDoctor_calendarStateWarnsWhenUnwritable(t *testing.T) {
	w := calendarWorkspace(t, `tz = "America/Los_Angeles"`)
	if err := os.Chmod(w.logDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(w.logDir, 0o755) })
	if f, err := os.CreateTemp(w.logDir, "probe"); err == nil { // running as a user the mode does not bind
		_ = f.Close()
		t.Skip("directory mode is not enforced for this user")
	}
	var out strings.Builder
	doctor(&out, "", "")
	if n := strings.Count(out.String(), "warn calendar state"); n != 1 {
		t.Fatalf("want exactly one calendar state warn line, got %d:\n%s", n, out.String())
	}
}

func TestDoctor_calendarCleanStateHasNoWarning(t *testing.T) {
	calendarWorkspace(t, `tz = "America/Los_Angeles"`)
	var out strings.Builder
	doctor(&out, "", "")
	if strings.Contains(out.String(), "warn ") {
		t.Fatalf("unexpected warning:\n%s", out.String())
	}
}

func TestDoctorCalendar_warnsWhenTZUnsetAndHostIsUTC(t *testing.T) {
	mk := func(tzSet bool) config.Config {
		ct := laTrigger(t, "run-now")
		ct.TZSet = tzSet
		if !tzSet {
			ct.Loc = time.UTC // the host zone is UTC
		}
		return config.Config{Queries: query.SourceSet{{Name: "q", Query: query.TimerQuery{Meta: query.Meta{EmitTypes: []string{"x"}, Trig: ct}}}}}
	}
	var b strings.Builder
	doctorCalendar(&doctorReport{w: &b}, mk(false), t.TempDir(), time.Now())
	if !strings.Contains(b.String(), "warn calendar tz: q sets no tz and the host zone is UTC") {
		t.Fatalf("no UTC-host warning:\n%s", b.String())
	}
	b.Reset()
	doctorCalendar(&doctorReport{w: &b}, mk(true), t.TempDir(), time.Now())
	if strings.Contains(b.String(), "calendar tz") {
		t.Fatalf("tz set must not warn:\n%s", b.String())
	}
}
