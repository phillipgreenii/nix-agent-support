package groups

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
)

// Settings are the plain durations the freshness table is built from.
type Settings struct {
	SummaryTTL, DetailTTL, DetailMaxAge, TerminalTTL time.Duration
}

// Rules builds the freshness table. Summary is refreshed in the background
// with SummaryTTL; detail is refreshed in the background with DetailTTL, a
// maximum age of DetailMaxAge, and may be extended on revalidation.
// TerminalTTL applies only to an issue for which Terminal is true.
func Rules(s Settings) groupkit.Table {
	return groupkit.Table{
		TerminalTTL: s.TerminalTTL,
		Groups: []groupkit.GroupSpec{
			{Name: Summary, TTL: s.SummaryTTL, Background: true},
			{Name: Detail, TTL: s.DetailTTL, MaxAge: s.DetailMaxAge, Extendable: true, Background: true},
		},
	}
}

// Terminal reports whether an issue closed at closedAt is terminal at now:
// true exactly when closedAt is non-zero and now is not before
// closedAt + terminalTTL. An issue closed for less than terminalTTL is tracked
// and refreshed with the normal TTLs. Every caller that schedules a group
// passes this result, never a bare closed flag, to Table.Schedule.
func Terminal(closedAt, now time.Time, terminalTTL time.Duration) bool {
	return !closedAt.IsZero() && !now.Before(closedAt.Add(terminalTTL))
}
