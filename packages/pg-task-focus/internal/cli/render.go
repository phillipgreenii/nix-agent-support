package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/wire"
)

// displayZone is the zone times are shown in: the zone of the day period, or
// UTC while there is none. Every time is shown with its zone identifier, so no
// zone is ever implicit.
func displayZone(s wire.State) *time.Location {
	for _, p := range s.Periods {
		if p.Kind == "day" {
			if loc, err := time.LoadLocation(p.Zone); err == nil {
				return loc
			}
		}
	}
	return time.UTC
}

// clock writes an instant as "09:30 America/New_York".
func clock(t wire.Instant, loc *time.Location) string {
	return t.Time().In(loc).Format("15:04") + " " + loc.String()
}

// dur writes a length as "1h15m", "12m" or "45s".
func dur(seconds int64) string {
	if seconds < 0 {
		seconds = -seconds
	}
	h, m, sec := seconds/3600, seconds%3600/60, seconds%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%ds", sec)
}

// renderStatus is the human form of `status`: the READ-ONLY sentence first
// when the store is read-only, then the periods, the checklist, the next task,
// the focus cycle, the dimmed cycles and the resume offer.
func renderStatus(w io.Writer, s wire.State) {
	if sentence := s.Store.ReadOnlySentence(); sentence != "" {
		fmt.Fprintln(w, sentence)
	}
	if !s.Initialized {
		fmt.Fprintln(w, "Not set up yet: set your day, week and sprint with `pg-task-focus period change`.")
		return
	}
	loc := displayZone(s)
	fmt.Fprintf(w, "Profile: %s\n", s.Profile)
	for _, p := range s.Periods {
		line := fmt.Sprintf("%s: %s", capital(p.Kind), p.Start)
		if p.End != p.Start {
			line += ".." + p.End
		}
		line += " (" + p.Zone + ")"
		if p.Banner != "" {
			line += "  " + p.Banner
		}
		fmt.Fprintln(w, line)
	}
	now := s.ReadAt.Time()
	if len(s.Tasks) > 0 {
		fmt.Fprintln(w, "Tasks:")
	}
	for _, t := range s.Tasks {
		fmt.Fprintf(w, "  %-5s %-22s %-10s %s\n", t.Kind, t.Definition, t.Status, taskDue(t, now, loc))
	}
	if s.Next != nil {
		fmt.Fprintf(w, "Next: %s, %s\n", s.Next.Title, taskDue(*s.Next, now, loc))
	}
	if f := s.Focus; f != nil {
		fmt.Fprintf(w, "Focus: %s\n", cycleLine(*f, now, loc))
	}
	for _, d := range s.Dimmed {
		action := ""
		if d.CanSwitch {
			action = "  [switch to resume]"
		}
		fmt.Fprintf(w, "Paused: %s%s\n", cycleLine(d.Cycle, now, loc), action)
	}
	if o := s.ResumeOffer; o != nil {
		fmt.Fprintf(w, "Resume %s? (%s)\n", o.Cycle.Title, o.Action)
	}
}

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// taskDue says when a task is due, with its zone, and how far away.
func taskDue(t wire.Task, now time.Time, loc *time.Location) string {
	switch t.Status {
	case "completed", "skipped":
		return ""
	}
	due := t.Due.Time()
	zone := t.DueZone
	if zone == "" {
		zone = loc.String()
	}
	at := "due " + due.In(loadZone(zone, loc)).Format("15:04") + " " + zone
	if due.Before(now) {
		return at + ", overdue " + dur(int64(now.Sub(due)/time.Second))
	}
	return at + ", in " + dur(int64(due.Sub(now)/time.Second))
}

func loadZone(name string, fallback *time.Location) *time.Location {
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return fallback
}

// cycleLine is one cycle with its timer, the way the focus and a dimmed cycle
// are shown.
func cycleLine(c wire.Cycle, now time.Time, loc *time.Location) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s)", c.Title, c.ID)
	switch c.Status {
	case "running":
		since := ""
		if c.RunningSince != nil {
			since = " since " + c.RunningSince.Time().In(loc).Format("15:04")
		}
		if c.Overtime {
			fmt.Fprintf(&b, " running%s, overtime +%s", since, dur(c.RemainingSeconds))
		} else {
			fmt.Fprintf(&b, " running%s, %s elapsed, %s left", since, dur(c.ElapsedSeconds), dur(c.RemainingSeconds))
		}
	case "paused":
		fmt.Fprintf(&b, " paused, %s elapsed", dur(c.ElapsedSeconds))
		if c.RemainingSeconds < 0 {
			fmt.Fprintf(&b, ", over by %s", dur(c.RemainingSeconds))
		} else {
			fmt.Fprintf(&b, ", %s left", dur(c.RemainingSeconds))
		}
	default:
		fmt.Fprintf(&b, " %s", c.Status)
	}
	if c.NotInProfile {
		b.WriteString(" [not in the active profile]")
	}
	_ = now
	return b.String()
}

// renderPreview prints the dry run of a period or profile change.
func renderPreview(w io.Writer, d wire.DryRunResult) {
	p := d.Preview
	fmt.Fprintln(w, "Preview (nothing was changed):")
	list := func(title string, refs []wire.TaskRef) {
		if len(refs) == 0 {
			return
		}
		fmt.Fprintf(w, "%s:\n", title)
		for _, r := range refs {
			over := ""
			if r.Overdue {
				over = "  (overdue at once)"
			}
			fmt.Fprintf(w, "  %s  %s%s\n", r.ID, r.Title, over)
		}
	}
	list("Open tasks of the periods being left", p.Leaving)
	list("Tasks the new periods would materialize", p.Materialize)
	list("Tasks the profile would add", p.ProfileAdd)
	list("Tasks the profile would withdraw", p.ProfileWithdraw)
	list("Tasks the profile would reinstate", p.ProfileReinstate)
	for _, n := range p.NotMaterialized {
		fmt.Fprintf(w, "Not materialized: %s (%s)\n", n.Definition, n.Reason)
	}
	if len(p.BlockingCycles) > 0 {
		fmt.Fprintln(w, "Blocking cycles (stop them first, or end them at a time):")
		for _, c := range p.BlockingCycles {
			fmt.Fprintf(w, "  %s  %s  (%s)\n", c.ID, c.Title, c.Status)
		}
	}
	if d.Note != "" {
		fmt.Fprintln(w, d.Note)
	}
}
