// Package runs fetches the CI `runs` group of a pull request: the workflow runs
// on the PR's head branch, with the job results of the current head's non-passing
// runs attached. The group is read over REST through ratefeed.Feed.REST (so the
// governor sees the rate headers, which `gh run list` hides), the branch comes
// from the PR's stored summary (Target.HeadRefName), and job results are cached
// by (run id, attempt) in the store's job table: a completed attempt's jobs never
// change, so they are fetched once.
//
// The group's freshness is content-dependent (freshness.go): ci_active_ttl while
// a run on the CURRENT head is queued or in progress, ci_failed_ttl while the
// current head has a non-passing completed run, otherwise until the head or the
// rollup changes, and never past ci_max_age.
package runs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// ProviderName tags every run, as the old ciprovider.ProviderName does.
const ProviderName = "github-actions"

// Content is the content of the runs group: what list_runs returns today. Runs
// are in GitHub's newest-first order. as_of and stale are left empty because
// they are derived at read time.
type Content struct {
	Runs []schema.CIRun `json:"runs"`
}

// Decode reads the content of a stored runs row.
func Decode(content []byte) (Content, error) {
	if len(content) == 0 {
		return Content{}, errors.New("runs: no content")
	}
	var c Content
	if err := json.Unmarshal(content, &c); err != nil {
		return Content{}, fmt.Errorf("runs: decode content: %w", err)
	}
	return c, nil
}

// canonical returns the content bytes of c: its JSON with a non-nil run list.
func (c Content) canonical() ([]byte, error) {
	if c.Runs == nil {
		c.Runs = []schema.CIRun{}
	}
	return json.Marshal(c)
}

// fpJob and fpRun are the fields the spec's change rule reads. Timestamps and
// log URLs are not signals, so url and id (of a run and of a job) are left out.
type fpJob struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

type fpRun struct {
	ID         string  `json:"id"`
	Attempt    int     `json:"attempt"`
	Name       string  `json:"name"`
	HeadSHA    string  `json:"head_sha"`
	Status     string  `json:"status"`
	Conclusion string  `json:"conclusion"`
	Jobs       []fpJob `json:"jobs"`
}

// Fingerprint is the group's fingerprint: a hash over, per run, its id, attempt,
// workflow name, head SHA, status and conclusion and, per gathered job, its name,
// status and conclusion. The runs keep their order (a new run is a change); the
// jobs of a run are sorted, so a reordered job list is not one.
func Fingerprint(c Content) string {
	rows := make([]fpRun, 0, len(c.Runs))
	for _, r := range c.Runs {
		row := fpRun{
			ID: r.ID, Attempt: r.Attempt, Name: r.Name, HeadSHA: r.HeadSHA,
			Status: r.Status, Conclusion: r.Conclusion, Jobs: []fpJob{},
		}
		for _, j := range r.Jobs {
			row.Jobs = append(row.Jobs, fpJob{Name: j.Name, Status: j.Status, Conclusion: j.Conclusion})
		}
		sort.Slice(row.Jobs, func(a, b int) bool {
			x, y := row.Jobs[a], row.Jobs[b]
			if x.Name != y.Name {
				return x.Name < y.Name
			}
			if x.Status != y.Status {
				return x.Status < y.Status
			}
			return x.Conclusion < y.Conclusion
		})
		rows = append(rows, row)
	}
	b, _ := json.Marshal(rows) // strings and ints always marshal
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
