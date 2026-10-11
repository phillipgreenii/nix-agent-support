package focus

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// The echo table of `focus select` (spec section 7.2 step 7): the resolved
// entity AND title of every applied token, so a token that resolved to
// something other than intended is visible at once, and each row's
// downstream consequence as an EFFECT (mint, hold, release, none) plus a
// BECAUSE clause from the "Shown as" vocabulary of the bead-state table. A
// dry run prints the same table as the apply that follows it.
//
// Telemetry (D24): rendering emits no OpenTelemetry or Prometheus signal and
// logs nothing.

// EchoHeading is the first line of the table.
const EchoHeading = "ECHO"

// MintNote explains the will-mint words once, under the table.
const MintNote = "will mint (the router creates it on its next run; show reads pending until then)"

// echoShown reports whether a row belongs in the echo table. A routine skip
// is not an applied token: it is reported on stderr (RenderSkips).
func (e EchoRow) echoShown() bool { return e.Kind != EchoSkipped }

// ConsequenceText is the row's consequence as the table prints it: the
// bead-state table's phrase for a row with a bead column, the effect word
// alone otherwise.
func (e EchoRow) ConsequenceText() string {
	if e.Consequence != "" {
		return e.Consequence
	}
	return e.Effect
}

// RenderEcho prints the echo table of res: one row per applied token (token,
// key, title, what it resolved to, effect, because, consequence, bead id).
func RenderEcho(res PreviewResult) string {
	var sb strings.Builder
	writeEcho(&sb, res)
	return sb.String()
}

func writeEcho(w io.Writer, res PreviewResult) {
	_, _ = fmt.Fprintln(w, EchoHeading)
	var rows []EchoRow
	mint := false
	for _, e := range res.Echo {
		if !e.echoShown() {
			continue
		}
		rows = append(rows, e)
		if e.Effect == "mint" {
			mint = true
		}
	}
	if len(rows) == 0 {
		_, _ = fmt.Fprintln(w, "  (nothing applied)")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  TOKEN\tKEY\tTITLE\tRESULT\tEFFECT\tBECAUSE\tCONSEQUENCE\tBEAD")
	for _, e := range rows {
		key := "-"
		if e.Key != (Key{}) {
			key = e.Key.String()
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			e.Token, key, blankDash(e.Title), e.Detail, blankDash(e.Effect), blankDash(e.Because), blankDash(e.Consequence), blankDash(e.BeadID))
	}
	_ = tw.Flush()
	if mint {
		_, _ = fmt.Fprintln(w, MintNote)
	}
}

// RenderSkips prints the routine skips of res, one line each, for stderr.
func RenderSkips(res PreviewResult) string {
	var sb strings.Builder
	for _, e := range res.Echo {
		if e.Kind == EchoSkipped {
			_, _ = fmt.Fprintf(&sb, "skipped %s: %s\n", e.Key, e.Detail)
		}
	}
	return sb.String()
}
