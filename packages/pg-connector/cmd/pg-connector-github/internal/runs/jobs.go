package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// MaxJobFetches is the most job requests one refresh makes (the old
// maxJobFetchesPerList). A cached (run id, attempt) never counts.
const MaxJobFetches = 10

// JobStore is the part of the store the runs group needs: the job table.
// *store.Store satisfies it.
type JobStore interface {
	PutJobs(ctx context.Context, j store.JobRow) error
	GetJobs(ctx context.Context, runID string, attempt int) (store.JobRow, bool, error)
	PruneJobsBefore(ctx context.Context, t time.Time) (int64, error)
}

var _ JobStore = (*store.Store)(nil)

// Rerunnable reports whether a run that ended with this conclusion can be
// rerun, and is the set of conclusions whose jobs are fetched: failure,
// timed_out, startup_failure and cancelled (the old rerunnableConclusions).
// Excluded on purpose: success, skipped, neutral, stale and action_required.
func Rerunnable(conclusion string) bool {
	switch strings.ToLower(conclusion) {
	case "failure", "timed_out", "startup_failure", "cancelled":
		return true
	}
	return false
}

// PruneJobs deletes the job rows fetched more than expiry before now and returns
// how many. The daemon calls it on its sweep timer.
func PruneJobs(ctx context.Context, st JobStore, now time.Time, expiry time.Duration) (int64, error) {
	return st.PruneJobsBefore(ctx, now.Add(-expiry))
}

// jobsEligible reports whether run r may have its jobs fetched: completed, a
// rerunnable conclusion, on the current head.
func jobsEligible(r schema.CIRun, currentHead string) bool {
	return currentHead != "" && r.HeadSHA == currentHead &&
		strings.EqualFold(r.Status, "completed") && Rerunnable(r.Conclusion)
}

// attemptOf is the attempt a run's job row is keyed by: the run's own, or 1 when
// GitHub reported none.
func attemptOf(r schema.CIRun) int {
	if r.Attempt > 0 {
		return r.Attempt
	}
	return 1
}

func toJobResults(jobs []schema.CIJob) []store.JobResult {
	out := make([]store.JobResult, len(jobs))
	for i, j := range jobs {
		out[i] = store.JobResult{ID: j.ID, Name: j.Name, Status: j.Status, Conclusion: j.Conclusion, URL: j.URL}
	}
	return out
}

func fromJobResults(jobs []store.JobResult) []schema.CIJob {
	if len(jobs) == 0 {
		return nil
	}
	out := make([]schema.CIJob, len(jobs))
	for i, j := range jobs {
		out[i] = schema.CIJob{ID: j.ID, Name: j.Name, Status: j.Status, Conclusion: j.Conclusion, URL: j.URL}
	}
	return out
}

// wireJobs is the answer of GET .../actions/runs/{id}/attempts/{n}/jobs.
type wireJobs struct {
	Jobs []struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		HTMLURL    string `json:"html_url"`
	} `json:"jobs"`
}

// parseJobs converts a jobs answer, lower-casing status and conclusion as the
// old fetchJobs does.
func parseJobs(body []byte) ([]schema.CIJob, error) {
	var w wireJobs
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("%w: parse jobs: %w", errBadAnswer, err)
	}
	var out []schema.CIJob
	for _, j := range w.Jobs {
		cj := schema.CIJob{
			Name: j.Name, Status: strings.ToLower(j.Status),
			Conclusion: strings.ToLower(j.Conclusion), URL: j.HTMLURL,
		}
		if j.ID != 0 {
			cj.ID = fmt.Sprintf("%d", j.ID)
		}
		out = append(out, cj)
	}
	return out, nil
}
