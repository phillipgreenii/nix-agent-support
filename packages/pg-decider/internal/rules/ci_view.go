// Package rules holds the concrete PR decider rules. Each rule registers
// itself into internal/decide's registry from init() and is a pure function
// of the composite view: it reads nothing but the decide.Input it is given.
package rules

import (
	"strconv"

	"github.com/phillipgreenii/pg-decider/internal/view"
)

// ciRun is one entry of the composite view's additive `ci` section
// (docs/behavior/pg-desk/show.md), flattened from deskapi.CIRun. A build id is
// the run's ID plus its Attempt: GitHub keeps a run's id when it is re-run.
type ciRun struct {
	ID         string
	Attempt    int
	Name       string
	Status     string
	Conclusion string
	URL        string
	// Required is the optional `required` member: true when the merge gate
	// requires the check, false when it does not, nil when the view does not
	// say. A nil Required is treated as required, so a producer that omits
	// the member changes nothing.
	Required *bool
}

// buildID is the run id plus attempt, "<run id>:<attempt>": the encoding of one
// entry of a fix-ci item's failing_builds list.
func (r ciRun) buildID() string { return r.ID + ":" + strconv.Itoa(r.Attempt) }

// ciRuns returns the runs of the view's `ci` section (decoded by view.Parse
// through the deskapi contract). It returns nil when the view is nil or carries
// no `ci` section (a binary that predates it): "no CI data" is never a failure
// or a green.
func ciRuns(v *view.DeskView) []ciRun {
	if v == nil || v.CI == nil {
		return nil
	}
	var out []ciRun
	for _, r := range v.CI.Runs {
		out = append(out, ciRun{
			ID: r.ID, Attempt: r.Attempt, Name: r.Name, Status: r.Status,
			Conclusion: r.Conclusion, URL: r.URL, Required: r.Required,
		})
	}
	return out
}

// ciRunPending is pg-desk's notion of a run still in flight: not completed, or
// completed with an empty, "pending" or "expected" conclusion (deskCIStatus in
// packages/pg-desk/cmd/pg-desk/facts_view.go, mirrored, not imported).
func ciRunPending(r ciRun) bool {
	return r.Status != "completed" || r.Conclusion == "" || r.Conclusion == "pending" || r.Conclusion == "expected"
}

// ciRunPassed reports a finished run that counts as passing: success, neutral
// or skipped.
func ciRunPassed(r ciRun) bool {
	if ciRunPending(r) {
		return false
	}
	switch r.Conclusion {
	case "success", "neutral", "skipped":
		return true
	}
	return false
}

// ciRunNotRequired reports a run the view explicitly marks as not required to
// merge (`"required": false`). A run with no `required` member is required.
func ciRunNotRequired(r ciRun) bool { return r.Required != nil && !*r.Required }

// ciRunFailing reports a completed run whose conclusion is anything other than
// success, neutral or skipped, and that is not explicitly marked as not
// required to merge: a failing non-required run (a cancelled one included)
// blocks nothing, so it is not a failure to fix.
func ciRunFailing(r ciRun) bool {
	return !ciRunPending(r) && !ciRunPassed(r) && !ciRunNotRequired(r)
}

// ciGreen reports "CI is green": at least one completed run and none failing
// or pending. Empty runs, or any pending run, is not green.
func ciGreen(runs []ciRun) bool {
	passed := 0
	for _, r := range runs {
		switch {
		case ciRunPending(r), ciRunFailing(r):
			return false
		case ciRunNotRequired(r) && !ciRunPassed(r):
			// A finished, failed run of a non-required check neither fails nor
			// counts toward "at least one completed run".
		default:
			passed++
		}
	}
	return passed > 0
}
