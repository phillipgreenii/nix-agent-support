package changesop

import "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feedop"

// mapError puts an error of the feed (or of a query run it made) on the closed
// wire taxonomy: the shared feedop mapping with vcs.ErrAuthInvalid as the
// unauthenticated sentinel.
func mapError(op string, err error) error { return feedop.MapError(op, err, authErrors) }
