package outcome

import (
	"fmt"
	"strings"
)

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func fmtAge(s int64) string {
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%ds", s/60, s%60)
}

func fmtDuration(ms int64) string {
	return fmt.Sprintf("%.1f s", float64(ms)/1000)
}

// Human renders the outcome as the one line of design section 14.2, plus the
// work worktree and exact next commands after a stop or refusal. The result
// has no trailing newline.
func (o *Outcome) Human() string {
	var b strings.Builder
	op := strings.TrimSpace(o.Operation + " " + o.Branch)
	switch o.Outcome {
	case Landed:
		fmt.Fprintf(&b, "landed %s into %s at %s", o.Branch, o.Target, shortSHA(o.LandedCommit))
		var parts []string
		if o.Stats != nil {
			parts = append(parts,
				"rebased "+plural(o.Stats.RebasedCommits, "commit"),
				plural(o.Stats.AutofixCommits, "autofix commit"),
				fmtDuration(o.Stats.DurationMS))
		}
		if len(parts) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(parts, ", "))
		}
	case Busy:
		fmt.Fprintf(&b, "busy: %s is held by", o.Target)
		if h := o.Holder; h != nil {
			fmt.Fprintf(&b, " %s", h.describe())
		}
		b.WriteString("; nothing changed")
	case Stopped:
		msg := o.Message
		if msg == "" {
			msg = o.Reason
		}
		fmt.Fprintf(&b, "stopped: %s: %s", op, msg)
		writeWorkWorktree(&b, o)
		writeNext(&b, o)
	case Refused:
		msg := o.Message
		if msg == "" {
			msg = o.Reason
		}
		fmt.Fprintf(&b, "refused: %s: %s", op, msg)
		if h := o.Holder; h != nil && h.OpID != "" {
			fmt.Fprintf(&b, "\n  held by %s", h.describe())
		}
		if o.Hint != "" {
			fmt.Fprintf(&b, "\n  %s", o.Hint)
		}
		writeNext(&b, o)
	case Rebased:
		fmt.Fprintf(&b, "rebased %s onto %s", o.Branch, o.Onto)
	case UpToDate:
		fmt.Fprintf(&b, "up-to-date: %s already contains %s", o.Branch, o.Onto)
	case NothingToLand:
		fmt.Fprintf(&b, "nothing-to-land: %s already holds every change of %s", o.Target, o.Branch)
	case Aborted, Quit:
		fmt.Fprintf(&b, "%s: %s", o.Outcome, op)
	default:
		fmt.Fprintf(&b, "%s", o.Outcome)
		if o.Message != "" {
			fmt.Fprintf(&b, ": %s", o.Message)
		}
	}
	for _, w := range o.Warnings {
		fmt.Fprintf(&b, "\n  warning: %s", w.Warning)
		if w.Reason != "" {
			fmt.Fprintf(&b, " (%s)", w.Reason)
		}
		if w.Detail != "" {
			fmt.Fprintf(&b, ": %s", w.Detail)
		}
	}
	return b.String()
}

// describe renders the holder: the operation and branch, its stage, how long
// it has been in its state (running: in the stage; stopped: since the stop) and
// the owner process.
func (h *Holder) describe() string {
	stage := h.Stage
	if stage == "" {
		stage = "unknown stage"
	}
	state := "for"
	if h.State == "stopped" {
		state = "stopped"
		return fmt.Sprintf("%s %s (%s, %s %s ago, pid %d on %s)", h.Operation, h.Branch, stage, state, fmtAge(h.AgeS), h.PID, h.Host)
	}
	return fmt.Sprintf("%s %s (%s %s %s, pid %d, %s)", h.Operation, h.Branch, stage, state, fmtAge(h.AgeS), h.PID, h.Host)
}

func writeWorkWorktree(b *strings.Builder, o *Outcome) {
	if o.WorkWorktree == nil {
		return
	}
	fmt.Fprintf(b, "\n  work worktree: %s", o.WorkWorktree.Path)
	if o.WorkBranch != "" {
		fmt.Fprintf(b, "   (work branch %s)", o.WorkBranch)
	}
}

var nextLabels = map[string]string{
	FlagContinue: "resolve and `git add` the files there, then:",
	FlagSkip:     "drop the replayed commit instead:",
	FlagAbort:    "give up:",
	FlagQuit:     "keep the worktree and work branch, stop tracking:",
}

func writeNext(b *strings.Builder, o *Outcome) {
	width := len(nextLabels[FlagContinue]) + 2
	for _, cmd := range o.Next {
		label := "then:"
		for flag, l := range nextLabels {
			if strings.Contains(cmd, " "+flag) {
				label = l
			}
		}
		pad := 1
		if n := width - len(label); n > 1 {
			pad = n
		}
		fmt.Fprintf(b, "\n  %s%s%s", label, strings.Repeat(" ", pad), cmd)
	}
}
