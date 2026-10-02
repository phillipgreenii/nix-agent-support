// Package cirun is the single authority for reading the `pg-connector ci
// list` fan-out payload stored in a PR's gathered facts: which runs describe
// the PR's current head, which of them a check_interpreters pattern excludes,
// and how each run counts (passed, failed, pending). internal/interpret rolls
// the result up to the dashboard's CI state and internal/links turns the
// failing runs into build links, so both are derived from this one reading and
// the menu cannot disagree with the dashboard about what is failing.
package cirun

import (
	"encoding/json"
	"regexp"
	"strconv"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
)

// Run is one workflow run of the fan-out payload ({"runs":[...]}).
type Run struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
	HeadSHA    string `json:"head_sha"`
	Attempt    int    `json:"attempt"`
}

// Outcome is how a counted run weighs in the rollup.
type Outcome int

const (
	// Pending: not completed, or completed without a settled conclusion.
	Pending Outcome = iota
	// Passed: success, neutral or skipped.
	Passed
	// Failed: any other conclusion, a cancelled newest run included.
	Failed
)

// Counted is a run that counts toward the rollup, with its outcome.
type Counted struct {
	Run
	Outcome Outcome
}

type fanOut struct {
	Runs []Run `json:"runs"`
}

// CompileExcluder builds the predicate for check names to leave out of the
// rollup: the union of every check_interpreters entry's Patterns regardless of
// Type (mirroring internal/snapshot/builder.go's excluderFromInterpreters). A
// mis-configured pattern is skipped, never an error.
func CompileExcluder(interps []config.CheckInterpreterConfig) func(name string) bool {
	var pats []*regexp.Regexp
	for _, ip := range interps {
		for _, p := range ip.Patterns {
			re, err := regexp.Compile(p)
			if err != nil {
				continue // mis-configured pattern must not break interpretation
			}
			pats = append(pats, re)
		}
	}
	if len(pats) == 0 {
		return func(string) bool { return false }
	}
	return func(name string) bool {
		for _, re := range pats {
			if re.MatchString(name) {
				return true
			}
		}
		return false
	}
}

// newer reports whether a supersedes b (same workflow name, same SHA): higher
// attempt wins, then higher numeric id (string compare if unparsable).
func newer(a, b Run) bool {
	if a.Attempt != b.Attempt {
		return a.Attempt > b.Attempt
	}
	ai, aerr := strconv.ParseUint(a.ID, 10, 64)
	bi, berr := strconv.ParseUint(b.ID, 10, 64)
	if aerr == nil && berr == nil {
		return ai > bi
	}
	return a.ID > b.ID
}

// CurrentRuns narrows the fan-out's run history to the runs that describe the
// PR's current state: only runs on the head SHA, collapsed to the newest run per
// workflow name. Runs that cannot be attributed to a SHA (headSHA unknown, or the
// run carries no head_sha) fall back to the old behavior: kept as-is, uncollapsed.
func CurrentRuns(runs []Run, headSHA string) []Run {
	var out []Run
	newest := map[string]int{} // workflow name -> index in out
	for _, r := range runs {
		if headSHA == "" || r.HeadSHA == "" {
			out = append(out, r)
			continue
		}
		if r.HeadSHA != headSHA {
			continue
		}
		if i, ok := newest[r.Name]; ok {
			if newer(r, out[i]) {
				out[i] = r
			}
			continue
		}
		newest[r.Name] = len(out)
		out = append(out, r)
	}
	return out
}

// Classify is the outcome of one run.
func Classify(r Run) Outcome {
	if r.Status != "completed" || r.Conclusion == "" || r.Conclusion == "pending" || r.Conclusion == "expected" {
		return Pending
	}
	switch r.Conclusion {
	case "success", "neutral", "skipped":
		return Passed
	default:
		return Failed
	}
}

// Evaluate returns the runs that count toward the CI rollup for headSHA, each
// with its outcome: only runs on the head (newest per workflow name), minus
// those whose name a check_interpreters pattern excludes. A malformed or empty
// payload degrades to no runs, not an error. That payload is the PR branch's
// workflow-run HISTORY across every pushed commit (capped at 100), and pushing
// a new commit makes GitHub cancel the previous commit's in-flight runs, so
// counting older commits' cancelled runs would report a failure for a green
// head. A cancelled run that is itself the newest for its name still counts as
// failed.
func Evaluate(raw json.RawMessage, interpreters []config.CheckInterpreterConfig, headSHA string) []Counted {
	var fo fanOut
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &fo)
	}
	excluded := CompileExcluder(interpreters)
	var out []Counted
	for _, r := range CurrentRuns(fo.Runs, headSHA) {
		if excluded(r.Name) {
			continue
		}
		out = append(out, Counted{Run: r, Outcome: Classify(r)})
	}
	return out
}
