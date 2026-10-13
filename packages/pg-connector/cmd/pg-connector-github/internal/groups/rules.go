package groups

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Stamps are the head and base commits a stamped group's content describes
// (headRefOid and baseRefOid).
type Stamps struct {
	Head, Base string
}

// StampsOf reads the head and base stamps from a summary.
func StampsOf(s summary.Summary) Stamps {
	return Stamps{Head: s.HeadRefOid, Base: s.BaseRefOid}
}

// Fingerprint is the sha256 of the bytes in hex, for groups whose fingerprint
// is the whole content. A group packet may supply its own.
func Fingerprint(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// Level is how fresh a stored group is.
type Level int

const (
	// LevelFresh: serve the stored content.
	LevelFresh Level = iota
	// LevelStale: past its time window, or fetched for another head or base, or
	// empty; refresh it (serving it meanwhile is the caller's rule).
	LevelStale
	// LevelHardStale: past the maximum age; the content MUST be re-pulled before
	// it is served as current.
	LevelHardStale
)

// String returns the level's name.
func (l Level) String() string {
	switch l {
	case LevelFresh:
		return "fresh"
	case LevelStale:
		return "stale"
	case LevelHardStale:
		return "hard-stale"
	}
	return "unknown"
}

// Freshness is the verdict of Rules.Fresh.
type Freshness struct {
	Level Level
	// Reason says why the group is not fresh; empty when it is.
	Reason string
}

// IsFresh reports whether the content can be served as current.
func (f Freshness) IsFresh() bool { return f.Level == LevelFresh }

// Rules encodes the spec's "Field groups" freshness rules and "Defaults"
// table. It holds no clock: every method takes the time it is to judge by.
type Rules struct {
	s config.Settings
}

// NewRules builds the rules from the daemon's settings.
func NewRules(s config.Settings) *Rules { return &Rules{s: s} }

// TTL is the group's time to live after a fetch; zero means no time limit (the
// stamped files and commits groups). For runs it is ci_active_ttl, the shortest
// of the content-dependent TTLs: the runs group applies ci_active_ttl and
// ci_failed_ttl itself. An unknown group has none.
func (r *Rules) TTL(group string) time.Duration {
	switch group {
	case Summary:
		return r.s.SummaryTTL.Std()
	case Detail:
		return r.s.DetailTTL.Std()
	case Conversation:
		return r.s.ConversationTTL.Std()
	case Pending:
		return r.s.PendingTTL.Std()
	case Runs:
		return r.s.CIActiveTTL.Std()
	}
	return 0
}

// MaxAge is the group's hard maximum age after a fetch; zero means none.
func (r *Rules) MaxAge(group string) time.Duration {
	switch group {
	case Detail:
		return r.s.DetailMaxAge.Std()
	case Conversation:
		return r.s.ConversationMaxAge.Std()
	case Runs:
		return r.s.CIMaxAge.Std()
	}
	return 0
}

// Schedule returns the fresh_until and max_age_at of a group fetched at
// fetchedAt. For a PR in a terminal state every group is fresh until
// fetchedAt + terminal_ttl and has no maximum age. Otherwise fresh_until is
// fetchedAt + TTL(group) (zero when the group has no TTL: files and commits,
// which are fresh while their stamps match) and max_age_at is
// fetchedAt + MaxAge(group) (zero when none).
func (r *Rules) Schedule(group string, terminal bool, fetchedAt time.Time) (freshUntil, maxAgeAt time.Time) {
	if terminal {
		return fetchedAt.Add(r.s.TerminalTTL.Std()), time.Time{}
	}
	if ttl := r.TTL(group); ttl > 0 {
		freshUntil = fetchedAt.Add(ttl)
	}
	if age := r.MaxAge(group); age > 0 {
		maxAgeAt = fetchedAt.Add(age)
	}
	return freshUntil, maxAgeAt
}

// Extend is the rule revalidation uses for a detail or conversation row whose
// content GitHub confirmed unchanged: fresh_until becomes
// min(max_age_at, now + ttl). ok is false when the row is already at its
// maximum age, when its MaxAgeAt is zero, and for any group that is not
// extendable (only detail and conversation are); the caller then re-pulls the
// group. Extend is never called for a terminal PR, whose fresh_until is already
// terminal_ttl.
func (r *Rules) Extend(row store.GroupRow, group string, now time.Time) (freshUntil time.Time, ok bool) {
	if group != Detail && group != Conversation {
		return time.Time{}, false
	}
	if row.MaxAgeAt.IsZero() || !now.Before(row.MaxAgeAt) {
		return time.Time{}, false
	}
	freshUntil = now.Add(r.TTL(group))
	if freshUntil.After(row.MaxAgeAt) {
		freshUntil = row.MaxAgeAt
	}
	return freshUntil, true
}

// Background reports whether the group is refreshed by the background cycle.
// The pending group is never refreshed in the background; it is read through
// only when asked for.
func (r *Rules) Background(group string) bool { return group != Pending }

// Stamped reports whether the group's rows record the head and base they were
// fetched for and are fresh only while those match the current summary.
func (r *Rules) Stamped(group string) bool {
	return group == Files || group == Commits || group == Runs
}

// Fresh judges a stored row against the current summary's stamps at now. An
// empty row (no content, as an invalidation of a never-fetched group leaves) is
// never served and is stale. A row at or past max_age_at is hard-stale. A
// stamped group fetched for another head or base is stale even inside its time
// window. Otherwise the row is fresh while now is before fresh_until; a zero
// fresh_until means "no time limit" for a stamped group and "unknown, stale"
// for any other.
func (r *Rules) Fresh(row store.GroupRow, cur Stamps, now time.Time) Freshness {
	if len(row.Content) == 0 {
		return Freshness{LevelStale, "no content stored"}
	}
	if !row.MaxAgeAt.IsZero() && !now.Before(row.MaxAgeAt) {
		return Freshness{LevelHardStale, "past the maximum age"}
	}
	stamped := r.Stamped(row.Group)
	if stamped {
		if row.FetchedForHead != cur.Head {
			return Freshness{LevelStale, "fetched for a different head"}
		}
		if row.FetchedForBase != cur.Base {
			return Freshness{LevelStale, "fetched for a different base"}
		}
	}
	if row.FreshUntil.IsZero() {
		if stamped {
			return Freshness{}
		}
		return Freshness{LevelStale, "no fresh_until recorded"}
	}
	if !now.Before(row.FreshUntil) {
		return Freshness{LevelStale, "past fresh_until"}
	}
	return Freshness{}
}
