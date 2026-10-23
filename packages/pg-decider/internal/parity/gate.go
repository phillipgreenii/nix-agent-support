package parity

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// Outcome is one scenario's result: its Report (the decider's plan against the
// frozen golden), or the error that kept it from being compared (wrapping
// ErrUnsupported when the real binaries cannot load it).
type Outcome struct {
	Scenario Scenario
	Report   Report
	Err      error
	// Refused is how the decider declined each legacy-seeded entity before its
	// refresh (RunNew); empty for a scenario with none.
	Refused map[string]string
}

// Clean is true when the scenario ran and its report is clean.
func (o Outcome) Clean() bool { return o.Err == nil && o.Report.Clean() }

// Render prints a table of every scenario with its matches, expected
// differences and failures, then each failure in full. It reports whether the
// whole run was clean.
func Render(w io.Writer, outs []Outcome) bool {
	clean := true
	fmt.Fprintf(w, "%-40s %7s %9s %11s %7s  %s\n", "SCENARIO", "MATCHED", "EXPECTED", "UNEXPLAINED", "MISSING", "STATUS")
	for _, o := range outs {
		status := "ok"
		switch {
		case o.Err != nil && errors.Is(o.Err, ErrUnsupported):
			status = "UNSUPPORTED"
		case o.Err != nil:
			status = "ERROR"
		case !o.Report.Clean():
			status = "FAIL"
		}
		fmt.Fprintf(w, "%-40s %7d %9d %11d %7d  %s\n", o.Scenario.Name,
			len(o.Report.Matched), len(o.Report.Expected), len(o.Report.Unexplained), len(o.Report.Missing), status)
		if !o.Clean() {
			clean = false
		}
	}
	for _, o := range outs {
		if o.Err == nil && len(o.Report.Expected) == 0 && o.Report.Clean() {
			continue
		}
		fmt.Fprintf(w, "\n%s\n", o.Scenario.Name)
		if o.Err != nil {
			kind := "ERROR"
			if errors.Is(o.Err, ErrUnsupported) {
				kind = "UNSUPPORTED"
			}
			fmt.Fprintf(w, "  %s: %v\n", kind, o.Err)
			continue
		}
		for _, x := range o.Report.Expected {
			fmt.Fprintf(w, "  expected    %-5s %s %s %s (%s): %s\n", x.ID, x.Entity, x.Kind, x.Op, x.Side, firstSentence(x.Reason))
		}
		for _, d := range o.Report.Unexplained {
			fmt.Fprintf(w, "  UNEXPLAINED %s %s %s (%s)\n", d.Entity, d.Kind, d.Op, d.Side)
		}
		for _, x := range o.Report.Missing {
			fmt.Fprintf(w, "  MISSING     %-5s %s %s %s (%s): listed but did not occur\n", x.ID, x.Entity, x.Kind, x.Op, x.Side)
		}
	}
	if !clean {
		fmt.Fprintln(w, "\nUNEXPLAINED (old-only) = a stale expected entry: the golden lists it, the decider does not plan it.")
		fmt.Fprintln(w, "UNEXPLAINED (new-only) = the decider plans it, the golden does not list it.")
	}
	return clean
}

// firstSentence is the reason up to its first full stop followed by a space, so the
// table stays readable; the full reason is in testdata/expected-diff.json.
func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i >= 0 {
		return s[:i+1]
	}
	return s
}
