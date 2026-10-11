package focus

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// The CHANGES block of `focus select` (spec section 7.2 step 7): every row
// the run will newly select (will mint, will release) and every row it will
// remove (will hold, left running), each with its CAUSE, so a removal is
// visible BEFORE it takes a bead out of play. A preview prints nothing else
// after the draft. The words of a row's consequence come from BeadStates (the
// one table `focus show` reads too); the display words of a cause are here.
//
// Telemetry (D24): rendering emits no OpenTelemetry or Prometheus signal and
// logs nothing.

// ChangesHeading is the first line of the block.
const ChangesHeading = "CHANGES"

// NeverClosedLine is the line printed for an older period that holds rows
// and was never closed, when the first lock would delete those rows.
func NeverClosedLine(period string) string {
	return "period " + period + " was never closed; its rows are deleted by this lock"
}

// ChangeDirectionWord is the word of a row's direction in the CHANGES block.
func ChangeDirectionWord(direction string) string {
	if direction == DirectionRemove {
		return "remove"
	}
	return "select"
}

// CauseDisplay is the display words of a stored cause for a row: operator
// shows "you", cap shows "cap", dropped shows "no longer a candidate:
// hidden" or "no longer a candidate: inactive" (by the row's loss reason),
// new_period shows "new plan"; a selection has no cause and shows "-".
func (c ChangeRow) CauseDisplay() string {
	if c.CauseText == "" {
		return "-"
	}
	return c.CauseText
}

// RenderChanges prints the CHANGES block of res: the heading, one aligned
// row per change (selections first, then removals) and, when the first lock
// deletes the rows of a period that was never closed, one line per such
// period. An empty block says so.
func RenderChanges(res PreviewResult) string {
	var sb strings.Builder
	writeChanges(&sb, res)
	return sb.String()
}

func writeChanges(w io.Writer, res PreviewResult) {
	_, _ = fmt.Fprintln(w, ChangesHeading)
	if len(res.Changes) == 0 {
		_, _ = fmt.Fprintln(w, "  (none)")
	} else {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "  CHANGE\tKEY\tTITLE\tEFFECT\tCAUSE\tBEAD")
		for _, c := range res.Changes {
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n",
				ChangeDirectionWord(c.Direction), c.Key, blankDash(c.Title), c.Echo, c.CauseDisplay(), blankDash(c.BeadID))
		}
		_ = tw.Flush()
	}
	for _, p := range res.NeverClosed {
		_, _ = fmt.Fprintln(w, NeverClosedLine(p))
	}
}

func blankDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
