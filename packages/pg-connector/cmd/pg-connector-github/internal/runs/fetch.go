package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Fetcher fetches the runs group; it implements groups.Fetcher.
type Fetcher struct {
	// Feed admits each REST request through the governor and books its spend and
	// rate reading.
	Feed *ratefeed.Feed
	// Store is the job table.
	Store JobStore
	// Rules are the daemon's freshness rules. The fetcher takes the CI TTLs from
	// Settings; Rules is carried so the assembly builds every group fetcher the
	// same way.
	Rules groups.Rules
	// Settings supplies ci_active_ttl, ci_failed_ttl and ci_max_age.
	Settings Settings
	// Clock is the fetch's time source; nil means the system clock.
	Clock governor.Clock
	// Retry bounds the retry of a transient failure of one request; nil means
	// github.DefaultRetryPolicy (three attempts in all, 500 ms then 1 s).
	Retry *github.RetryPolicy
}

var _ groups.Fetcher = (*Fetcher)(nil)

// Group implements groups.Fetcher.
func (f *Fetcher) Group() string { return groups.Runs }

// MaxBatch implements groups.Fetcher: one PR per call, because a runs refresh is
// one request for one branch.
func (f *Fetcher) MaxBatch() int { return 1 }

// Spend implements groups.Fetcher: one REST core request. The real count is
// booked per call by ratefeed.Feed.REST.
func (f *Fetcher) Spend(class governor.Class, _ int) governor.Request {
	return governor.Request{Bucket: governor.BucketRESTCore, Class: class, Cost: 1}
}

func (f *Fetcher) now() time.Time {
	if f.Clock == nil {
		return time.Now()
	}
	return f.Clock.Now()
}

// Fetch implements groups.Fetcher. Every Outcome carries the stamps the target
// had before the call. The cost is the number of REST requests made (runs and
// jobs, retries included).
func (f *Fetcher) Fetch(ctx context.Context, call ratefeed.Meta, targets []groups.Target) []groups.Outcome {
	out := make([]groups.Outcome, len(targets))
	for i, t := range targets {
		out[i] = f.fetchOne(ctx, call, t)
		out[i].Stamps = t.Stamps
	}
	return out
}

// errBadAnswer marks an answer that is not what was asked for; never retried.
var errBadAnswer = errors.New("runs: unusable answer")

type wireRuns struct {
	WorkflowRuns []struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		HTMLURL    string `json:"html_url"`
		HeadSHA    string `json:"head_sha"`
		RunAttempt int    `json:"run_attempt"`
	} `json:"workflow_runs"`
}

func (f *Fetcher) fetchOne(ctx context.Context, call ratefeed.Meta, t groups.Target) groups.Outcome {
	var o groups.Outcome
	if t.HeadRefName == "" {
		o.Err = fmt.Errorf("runs: %s has no head branch in its summary", t.NodeID)
		return o
	}
	ref, err := entity.ParseRef(t.Ref.String())
	if err != nil {
		o.Err = fmt.Errorf("runs: %s: unusable repository reference: %w", t.NodeID, err)
		return o
	}
	repo := ref.NameWithOwner()
	call.Group, call.BatchSize = groups.Runs, 1

	endpoint := "repos/" + repo + "/actions/runs?branch=" + url.QueryEscape(t.HeadRefName) + "&per_page=100"
	resp, cost, err := f.request(ctx, call, endpoint)
	o.Cost += cost
	if err != nil {
		o.Err = classifyRunsError(resp, err)
		return o
	}
	var wire wireRuns
	if err := json.Unmarshal(resp.Body, &wire); err != nil {
		o.Err = fmt.Errorf("%w: parse runs of %s: %w", errBadAnswer, repo, err)
		return o
	}
	runs := make([]schema.CIRun, 0, len(wire.WorkflowRuns))
	for _, r := range wire.WorkflowRuns {
		runs = append(runs, schema.CIRun{
			ID: fmt.Sprintf("%d", r.ID), Name: r.Name,
			Status: strings.ToLower(r.Status), Conclusion: strings.ToLower(r.Conclusion),
			URL: r.HTMLURL, Provider: ProviderName, HeadSHA: r.HeadSHA,
			Repo: repo, PRID: ref.String(), Attempt: r.RunAttempt,
		})
	}

	jobCost, lastErr := f.attachJobs(ctx, call, repo, t.Stamps.Head, runs)
	o.Cost += jobCost
	o.LastError = lastErr

	content := Content{Runs: runs}
	raw, err := content.canonical()
	if err != nil {
		o.Err = fmt.Errorf("runs: encode %s: %w", t.NodeID, err)
		return o
	}
	o.Content = raw
	o.Fingerprint = Fingerprint(content)
	if !t.Terminal {
		o.FreshUntil, _ = Freshness(content, t.Stamps.Head, f.now(), f.Settings)
	}
	return o
}

// classifyRunsError maps a failed runs request to the outcome's error: a 404 is
// groups.ErrGone, a rate-limit answer and a governor refusal pass unchanged.
func classifyRunsError(resp *ratefeed.Response, err error) error {
	var rl *ratefeed.RateLimitError
	if errors.As(err, &rl) || errors.Is(err, governor.ErrRefused) {
		return err
	}
	if (resp != nil && resp.StatusCode == 404) || metered.IsNotFound(err) {
		return groups.ErrGone
	}
	return err
}

// attachJobs fills runs[i].Jobs for every eligible run from the job table, and
// fetches (and stores) the rows it does not hold, at most MaxJobFetches per
// call, newest first. A failed fetch leaves the run's jobs as the table holds
// them (none when it has none) and adds to the returned last error; the failure
// is not a content change because the content is built from the same jobs. Once
// a request is refused or rate limited no further job request is made.
func (f *Fetcher) attachJobs(ctx context.Context, call ratefeed.Meta, repo, head string, runs []schema.CIRun) (cost int64, lastErr string) {
	var problems []string
	fetches := 0
	stop := false
	for i := range runs {
		r := runs[i]
		if !jobsEligible(r, head) {
			continue
		}
		attempt := attemptOf(r)
		if f.Store != nil {
			row, ok, err := f.Store.GetJobs(ctx, r.ID, attempt)
			if err != nil {
				problems = append(problems, fmt.Sprintf("jobs of run %s attempt %d: read job cache: %v", r.ID, attempt, err))
			} else if ok {
				runs[i].Jobs = fromJobResults(row.Jobs)
				continue
			}
		}
		if stop || fetches >= MaxJobFetches {
			continue
		}
		fetches++
		endpoint := fmt.Sprintf("repos/%s/actions/runs/%s/attempts/%d/jobs?per_page=100", repo, r.ID, attempt)
		resp, c, err := f.request(ctx, call, endpoint)
		cost += c
		var jobs []schema.CIJob
		if err == nil {
			jobs, err = parseJobs(resp.Body)
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("jobs of run %s attempt %d: %v", r.ID, attempt, err))
			var rl *ratefeed.RateLimitError
			if errors.As(err, &rl) || errors.Is(err, governor.ErrRefused) || ctx.Err() != nil {
				stop = true
			}
			continue
		}
		runs[i].Jobs = jobs
		if f.Store != nil {
			if perr := f.Store.PutJobs(ctx, store.JobRow{RunID: r.ID, Attempt: attempt, Jobs: toJobResults(jobs), FetchedAt: f.now().UTC()}); perr != nil {
				problems = append(problems, fmt.Sprintf("jobs of run %s attempt %d: cache jobs: %v", r.ID, attempt, perr))
			}
		}
	}
	return cost, strings.Join(problems, "; ")
}

// request makes one REST read through the Feed, retrying a transient failure
// (HTTP 5xx, a timeout or a connection error): three attempts in all with the
// retry policy's backoff, each admitted again and never past the context's
// deadline. A rate-limit answer or a refusal is final. The cost is the number of
// requests that reached the Feed's gh runner.
func (f *Fetcher) request(ctx context.Context, call ratefeed.Meta, endpoint string) (*ratefeed.Response, int64, error) {
	policy := github.DefaultRetryPolicy()
	if f.Retry != nil {
		policy = *f.Retry
	}
	var cost int64
	for attempt := 1; ; attempt++ {
		resp, err := f.Feed.REST(ctx, ratefeed.RESTCall{Meta: call, Args: []string{endpoint}})
		refused := errors.Is(err, governor.ErrRefused)
		if !refused {
			cost++
		}
		if err == nil && resp != nil && resp.StatusCode < 400 {
			return resp, cost, nil
		}
		if err == nil {
			err = fmt.Errorf("runs: GET %s: HTTP %d", endpoint, resp.StatusCode)
		}
		var rl *ratefeed.RateLimitError
		if refused || errors.As(err, &rl) || errors.Is(err, errBadAnswer) || ctx.Err() != nil {
			return resp, cost, err
		}
		transient := false
		if resp != nil {
			transient = resp.StatusCode >= 500
		} else {
			transient = github.IsTransient(ctx, err)
		}
		if !transient || attempt >= policy.MaxAttempts {
			return resp, cost, err
		}
		delay := time.Duration(0)
		if policy.Backoff != nil {
			delay = policy.Backoff(attempt)
		}
		if !fits(ctx, delay, policy.MinRetryWindow) {
			return resp, cost, err
		}
		if serr := sleep(ctx, policy, delay); serr != nil {
			return resp, cost, err
		}
	}
}

// fits reports whether a retry started after delay still leaves minWindow before
// the context's deadline; a context without one always fits.
func fits(ctx context.Context, delay, minWindow time.Duration) bool {
	dl, ok := ctx.Deadline()
	if !ok || minWindow <= 0 {
		return true
	}
	return time.Until(dl) >= delay+minWindow
}

// sleep waits d, or returns ctx.Err() as soon as ctx is done.
func sleep(ctx context.Context, p github.RetryPolicy, d time.Duration) error {
	if p.Sleep != nil {
		return p.Sleep(ctx, d)
	}
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
