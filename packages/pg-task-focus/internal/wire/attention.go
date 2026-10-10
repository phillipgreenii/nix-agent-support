package wire

import (
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/view"
)

// Attention severities.
const (
	SeverityLow    = "low"
	SeverityMedium = "medium"
	SeverityHigh   = "high"
)

var taskGroups = map[projection.Kind]AttentionGroup{
	projection.Day:    {Key: "daily", Label: "Today"},
	projection.Week:   {Key: "weekly", Label: "This week"},
	projection.Sprint: {Key: "sprint", Label: "This sprint"},
}

// BuildAttention derives the attention feed from the state read at now. The
// feed is stateless: an item exists while its condition holds and is gone
// when it stops, so nothing is acknowledged or hidden. URLs are built from
// publicURL with the deep-link scheme `<public_url>/#/tasks/<task_id>` and
// `<public_url>/#/cycles/<cycle_id>`, and are omitted when it is empty or
// the item has no page (the store item).
func BuildAttention(s view.State, cfg *config.Config, now time.Time, publicURL string) Attention {
	att := cfg.Defaults().Attention
	base := strings.TrimRight(publicURL, "/")
	link := func(kind, id string) string {
		if base == "" {
			return ""
		}
		return base + "/#/" + kind + "/" + id
	}
	out := Attention{AsOf: at(now), Items: []AttentionItem{}}

	if s.Store.ReadOnly {
		out.Items = append(out.Items, AttentionItem{
			Type: "store", ID: "store", Summary: "pg-task-focus is read-only: " + s.Store.Reason,
			Severity: SeverityHigh, Group: AttentionGroup{Key: "system", Label: "System"},
		})
	}

	soon := time.Duration(att.DueSoonMinutes) * time.Minute
	for _, t := range s.Tasks {
		if t.Task.Status != projection.Open {
			continue
		}
		var sev string
		switch {
		case t.Overdue:
			sev = SeverityHigh
		case t.Task.Due.Sub(now) <= soon:
			sev = SeverityMedium
		default:
			continue
		}
		out.Items = append(out.Items, AttentionItem{
			Type: "task", ID: string(t.Task.ID), Summary: t.Task.Title, Severity: sev,
			Group: taskGroups[t.Kind], URL: link("tasks", string(t.Task.ID)),
		})
	}

	cycles := AttentionGroup{Key: "cycles", Label: "Cycles"}
	if f := s.Focus; f != nil && f.Overtime {
		over := -f.Remaining
		sev := SeverityMedium
		if over >= time.Duration(att.OvertimeHighMinutes)*time.Minute {
			sev = SeverityHigh
		}
		out.Items = append(out.Items, AttentionItem{
			Type: "cycle", ID: string(f.Cycle.ID), Severity: sev, Group: cycles,
			Summary: fmt.Sprintf("%s: %d min over", f.Cycle.Title, int(over/time.Minute)),
			URL:     link("cycles", string(f.Cycle.ID)),
		})
	}
	stale := time.Duration(att.StalePauseMinutes) * time.Minute
	for _, d := range s.Dimmed {
		segs := d.Cycle.Segments
		if len(segs) == 0 || segs[len(segs)-1].End == nil {
			continue
		}
		idle := now.Sub(*segs[len(segs)-1].End)
		if idle <= stale {
			continue
		}
		out.Items = append(out.Items, AttentionItem{
			Type: "cycle", ID: string(d.Cycle.ID), Severity: SeverityLow, Group: cycles,
			Summary: fmt.Sprintf("%s paused %d min", d.Cycle.Title, int(idle/time.Minute)),
			URL:     link("cycles", string(d.Cycle.ID)),
		})
	}
	return out
}
