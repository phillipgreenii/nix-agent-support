package query

import (
	"context"
	"errors"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	lq "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The state words of a Confirmation.
const (
	StateClosed = "closed"
	StateOpen   = "open"
)

// Summaries is the part of the summary service the confirmer uses;
// *summary.Service satisfies it.
type Summaries interface {
	Ensure(ctx context.Context, key string, opts summary.EnsureOptions) (summary.Result, error)
	BookAfterDeparture(ctx context.Context, id string) error
}

var _ Summaries = (*summary.Service)(nil)

type confirmer struct{ svc Summaries }

var _ lq.Confirmer = confirmer{}

// NewConfirmer returns the Confirmer over the summary service: ONE fresh
// read-through of the issue, answered with state "closed" or "open" from its
// status category, or Gone when the origin answers not_found (the service has
// then tombstoned the issue). Any other failure, a pause or a governor refusal
// included, is returned as is, so the departure is retried by the next run; so
// is an answer served from a stored summary because the fetch could not run (a
// stale answer must not decide a departure). After a successful read it books
// the issue's next summary refresh (summary.Service.BookAfterDeparture).
func NewConfirmer(svc Summaries) lq.Confirmer { return confirmer{svc: svc} }

// Confirm implements lq.Confirmer. ref is only for the error text: the read is by
// the issue key.
func (c confirmer) Confirm(ctx context.Context, id, ref string) (lq.Confirmation, error) {
	res, err := c.svc.Ensure(ctx, id, summary.EnsureOptions{Fresh: true})
	switch {
	case errors.Is(err, scriptout.ErrNotFound):
		return lq.Confirmation{Gone: true}, nil
	case err != nil:
		return lq.Confirmation{}, err
	case res.Stale && !res.FetchedNow:
		return lq.Confirmation{}, scriptout.WrapError(scriptout.ErrUnavailable,
			fmt.Sprintf("the confirming read of %s could not reach the origin and answered a stored summary", ref))
	}
	if err := c.svc.BookAfterDeparture(ctx, id); err != nil {
		return lq.Confirmation{}, err
	}
	state := StateOpen
	if groups.IsClosed(res.Content.Issue) {
		state = StateClosed
	}
	return lq.Confirmation{State: state}, nil
}
