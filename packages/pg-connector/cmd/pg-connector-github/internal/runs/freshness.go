package runs

import (
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
)

// Settings are the daemon's settings; the runs group reads CIActiveTTL,
// CIFailedTTL and CIMaxAge.
type Settings = config.Settings

// activeStatuses are the run statuses of a run that has not finished.
var activeStatuses = map[string]bool{
	"queued": true, "in_progress": true, "waiting": true, "requested": true, "pending": true,
}

// passingConclusions are the conclusions of a completed run that is not
// non-passing.
var passingConclusions = map[string]bool{"success": true, "neutral": true, "skipped": true}

// Active reports whether a run on the CURRENT head is queued, in progress,
// waiting, requested or pending. A run on an older head never counts.
func Active(content Content, currentHead string) bool {
	if currentHead == "" {
		return false
	}
	for _, r := range content.Runs {
		if r.HeadSHA == currentHead && activeStatuses[strings.ToLower(r.Status)] {
			return true
		}
	}
	return false
}

// Failing reports whether a run on the CURRENT head is completed with a
// conclusion other than success, neutral or skipped. A run on an older head
// never counts.
func Failing(content Content, currentHead string) bool {
	if currentHead == "" {
		return false
	}
	for _, r := range content.Runs {
		if r.HeadSHA == currentHead && strings.ToLower(r.Status) == "completed" &&
			!passingConclusions[strings.ToLower(r.Conclusion)] {
			return true
		}
	}
	return false
}

// Freshness is the three-case rule for an OPEN pull request. maxAgeAt is
// fetchedAt + ci_max_age and freshUntil never passes it:
//
//  1. Active: min(fetchedAt + ci_active_ttl, maxAgeAt).
//  2. Failing (and not active): min(fetchedAt + ci_failed_ttl, maxAgeAt).
//  3. Otherwise maxAgeAt (the row also goes stale at once when the head or base
//     stamp changes, and when the CI rollup changes, which revalidation queues).
func Freshness(content Content, currentHead string, fetchedAt time.Time, s Settings) (freshUntil, maxAgeAt time.Time) {
	maxAgeAt = fetchedAt.Add(s.CIMaxAge.Std())
	var ttl time.Duration
	switch {
	case Active(content, currentHead):
		ttl = s.CIActiveTTL.Std()
	case Failing(content, currentHead):
		ttl = s.CIFailedTTL.Std()
	default:
		return maxAgeAt, maxAgeAt
	}
	freshUntil = fetchedAt.Add(ttl)
	if freshUntil.After(maxAgeAt) {
		freshUntil = maxAgeAt
	}
	return freshUntil, maxAgeAt
}
