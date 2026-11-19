package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// requiredContexts reads the required status check contexts of the base branch
// from the repository's branch rules (`repos/<repo>/rules/branches/<base>`). The
// answer is the set of contexts and the number of requests spent. A nil set means
// "not known": no base branch, an unreadable answer, or an answer with no
// required_status_checks rule (classic branch protection is not part of this
// answer, so no rule says nothing about what is required). A context is a check
// name, never a configured value: nothing here names a check or a workflow.
func (f *Fetcher) requiredContexts(ctx context.Context, call ratefeed.Meta, repo, base string) (map[string]bool, int64) {
	if base == "" {
		return nil, 0
	}
	endpoint := "repos/" + repo + "/rules/branches/" + url.PathEscape(base) + "?per_page=100"
	resp, cost, err := f.request(ctx, call, endpoint)
	if err != nil {
		return nil, cost
	}
	set, err := parseRequiredContexts(resp.Body)
	if err != nil {
		return nil, cost
	}
	return set, cost
}

// parseRequiredContexts reads the contexts of every required_status_checks rule
// of a branch-rules answer. It returns nil when the answer holds no context.
func parseRequiredContexts(body []byte) (map[string]bool, error) {
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			Checks []struct {
				Context string `json:"context"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(body, &rules); err != nil {
		return nil, fmt.Errorf("%w: parse branch rules: %w", errBadAnswer, err)
	}
	var set map[string]bool
	for _, r := range rules {
		if r.Type != "required_status_checks" {
			continue
		}
		for _, c := range r.Parameters.Checks {
			if c.Context == "" {
				continue
			}
			if set == nil {
				set = map[string]bool{}
			}
			set[c.Context] = true
		}
	}
	return set, nil
}

// markRequired sets Required on every run from the required contexts. A run is
// required when its name, one of its job names, or "<run name> / <job name>" (the
// check name of a reusable workflow's job) is a context. It is not required only
// when its jobs were fetched and none matches; with no jobs the run's own name
// is all there is to match, so a miss says nothing and Required stays nil, which
// a consumer reads as required. With no contexts nothing is marked.
func markRequired(runs []schema.CIRun, contexts map[string]bool) {
	if len(contexts) == 0 {
		return
	}
	for i := range runs {
		r := &runs[i]
		matched := contexts[r.Name]
		for _, j := range r.Jobs {
			if contexts[j.Name] || contexts[r.Name+" / "+j.Name] {
				matched = true
			}
		}
		switch {
		case matched:
			t := true
			r.Required = &t
		case len(r.Jobs) > 0:
			f := false
			r.Required = &f
		}
	}
}
