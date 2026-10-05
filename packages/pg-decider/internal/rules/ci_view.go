// Package rules holds the concrete PR decider rules. Each rule registers
// itself into internal/decide's registry from init() and is a pure function
// of the composite view: it reads nothing but the decide.Input it is given.
package rules

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/phillipgreenii/pg-decider/internal/view"
)

// ciRun is one entry of the composite view's additive `ci` section
// (`"ci": {"runs": [{"id", "attempt", "name", "status", "conclusion",
// "url"}]}`, docs/behavior/pg-desk/show.md). View does not decode that section,
// so the rules that need it read it from View.Raw through this struct. A build
// id is the run's ID plus its Attempt: GitHub keeps a run's id when it is
// re-run.
type ciRun struct {
	ID         string
	Attempt    int
	Name       string
	Status     string
	Conclusion string
	URL        string
}

// UnmarshalJSON accepts the run id as a string (the documented shape) or a
// bare number, and a missing or null attempt as 0.
func (r *ciRun) UnmarshalJSON(data []byte) error {
	var w struct {
		ID         json.RawMessage `json:"id"`
		Attempt    *int            `json:"attempt"`
		Name       string          `json:"name"`
		Status     string          `json:"status"`
		Conclusion string          `json:"conclusion"`
		URL        string          `json:"url"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*r = ciRun{Name: w.Name, Status: w.Status, Conclusion: w.Conclusion, URL: w.URL}
	if len(w.ID) > 0 && string(w.ID) != "null" {
		var s string
		if json.Unmarshal(w.ID, &s) == nil {
			r.ID = s
		} else {
			r.ID = strings.TrimSpace(string(w.ID))
		}
	}
	if w.Attempt != nil {
		r.Attempt = *w.Attempt
	}
	return nil
}

// buildID is the run id plus attempt, "<run id>:<attempt>": the encoding of one
// entry of a fix-ci item's failing_builds list.
func (r ciRun) buildID() string { return r.ID + ":" + strconv.Itoa(r.Attempt) }

// ciRuns decodes the runs of the view's `ci` section. It returns nil when the
// view is nil, carries no `ci` section (a binary that predates it) or the
// section does not decode: "no CI data" is never a failure or a green.
func ciRuns(v *view.View) []ciRun {
	if v == nil || len(v.Raw) == 0 {
		return nil
	}
	var w struct {
		CI *struct {
			Runs []ciRun `json:"runs"`
		} `json:"ci"`
	}
	if err := json.Unmarshal(v.Raw, &w); err != nil || w.CI == nil {
		return nil
	}
	return w.CI.Runs
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

// ciRunFailing reports a completed run whose conclusion is anything other than
// success, neutral or skipped.
func ciRunFailing(r ciRun) bool { return !ciRunPending(r) && !ciRunPassed(r) }

// ciGreen reports "CI is green": at least one completed run and none failing
// or pending. Empty runs, or any pending run, is not green.
func ciGreen(runs []ciRun) bool {
	passed := 0
	for _, r := range runs {
		switch {
		case ciRunPending(r), ciRunFailing(r):
			return false
		default:
			passed++
		}
	}
	return passed > 0
}
