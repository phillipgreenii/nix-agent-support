// Package prfacts fetches the files and commits field groups of a pull
// request: the PR's changed files (path, additions, deletions) and its commits
// (sha, the commit's own first-author GitHub login, headline). Both are read
// per PR, paged first: 100, with the Provider's own caps (3000 files, 1000
// commits), through the metered Provider so every page is admitted by the
// governor and booked.
//
// Both groups are stamped: the engine stores each row with the headRefOid and
// baseRefOid of the Target it was fetched for, and the row is fresh only while
// those equal the current summary's. The fetchers therefore return the Target's
// stamps as they were BEFORE the call: a head that moves during a paged read
// leaves a row that is stale by stamp and is re-fetched against the newer
// summary. They leave Outcome.FreshUntil zero; the engine applies fresh_until
// (none for an open PR) and terminal_ttl (a merged or closed PR) through
// groups.Rules.Schedule.
package prfacts

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// spend is the governor request both groups make for n targets: one GraphQL
// point per target as an estimate. The real cost is booked per page by the
// metered runner.
func spend(class governor.Class, n int) governor.Request {
	return governor.Request{Bucket: governor.BucketGraphQL, Class: class, Cost: 1}
}

// fetchEach reads the group for every target with read, one Outcome per target
// in order. read returns the stored content bytes of one target.
//
// A not-found answer is groups.ErrGone for its target (a PR GitHub no longer
// finds); any other error, a governor refusal included, is returned untouched
// and the Outcome carries no content (a read that fails mid-page never yields
// the pages it did read). The call's group is stated to the metered runner when
// the engine left it empty, so the spend is booked under it.
func fetchEach(ctx context.Context, group string, call ratefeed.Meta, targets []groups.Target,
	read func(ctx context.Context, t groups.Target) ([]byte, error),
) []groups.Outcome {
	if call.Group == "" {
		call.Group = group
	}
	if call.BatchSize == 0 {
		call.BatchSize = 1
	}
	ctx = metered.WithCall(ctx, call)
	out := make([]groups.Outcome, len(targets))
	for i, t := range targets {
		out[i].Stamps = t.Stamps
		content, err := read(ctx, t)
		switch {
		case err == nil:
			out[i].Content = content
			out[i].Fingerprint = groups.Fingerprint(content)
		case metered.IsNotFound(err):
			out[i].Err = groups.ErrGone
		default:
			out[i].Err = err
		}
	}
	return out
}
