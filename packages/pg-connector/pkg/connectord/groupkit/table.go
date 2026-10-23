package groupkit

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// Fingerprint is the sha256 of the bytes in hex, for groups whose fingerprint
// is the whole content. A group may supply its own.
func Fingerprint(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// Level is how fresh a stored group is.
type Level int

const (
	// LevelFresh: serve the stored content.
	LevelFresh Level = iota
	// LevelStale: past its time window, or fetched for other stamps, or empty;
	// refresh it (serving it meanwhile is the caller's rule).
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

// Freshness is the verdict of Table.Fresh.
type Freshness struct {
	Level Level
	// Reason says why the group is not fresh; empty when it is.
	Reason string
}

// IsFresh reports whether the content can be served as current.
func (f Freshness) IsFresh() bool { return f.Level == LevelFresh }

// verdict builds a Freshness; the reason text arrives as an argument.
func verdict(level Level, reason string) Freshness { return Freshness{Level: level, Reason: reason} }

// GroupSpec is the freshness rule of one group.
type GroupSpec struct {
	Name string
	// TTL is the time to live after a fetch; zero means no time limit.
	TTL time.Duration
	// MaxAge is the hard maximum age after a fetch; zero means none.
	MaxAge time.Duration
	// Stamped groups record the two stamps their content was fetched for and
	// are fresh only while those match the current ones.
	Stamped bool
	// Extendable groups may have their fresh_until extended on revalidation.
	Extendable bool
	// Background groups are refreshed by the background cycle.
	Background bool
}

// Table is the data of the freshness rules: one GroupSpec per group, plus the
// TTL every group takes for an entity in a terminal state. It holds no clock:
// every method takes the time it is to judge by. A group the table does not
// list has no TTL, no maximum age and no flags.
type Table struct {
	Groups      []GroupSpec
	TerminalTTL time.Duration
}

// Stamps are the two stamps a stamped group's content describes, compared with
// the row's own through GroupRow.Stamps.
type Stamps struct{ Primary, Secondary string }

// Spec returns the named group's spec and whether the table lists it.
func (t Table) Spec(group string) (GroupSpec, bool) {
	for _, g := range t.Groups {
		if g.Name == group {
			return g, true
		}
	}
	return GroupSpec{}, false
}

// TTL is the group's time to live after a fetch; zero means no time limit.
func (t Table) TTL(group string) time.Duration {
	g, _ := t.Spec(group)
	return g.TTL
}

// MaxAge is the group's hard maximum age after a fetch; zero means none.
func (t Table) MaxAge(group string) time.Duration {
	g, _ := t.Spec(group)
	return g.MaxAge
}

// Background reports whether the group is refreshed by the background cycle.
func (t Table) Background(group string) bool {
	g, _ := t.Spec(group)
	return g.Background
}

// Stamped reports whether the group's rows record the stamps they were fetched
// for and are fresh only while those match the current ones.
func (t Table) Stamped(group string) bool {
	g, _ := t.Spec(group)
	return g.Stamped
}

// Schedule returns the fresh_until and max_age_at of a group fetched at
// fetchedAt. For an entity in a terminal state every group is fresh until
// fetchedAt + TerminalTTL and has no maximum age. Otherwise fresh_until is
// fetchedAt + TTL(group) (zero when the group has no TTL, a stamped group
// fresh while its stamps match) and max_age_at is fetchedAt + MaxAge(group)
// (zero when none).
func (t Table) Schedule(group string, terminal bool, fetchedAt time.Time) (freshUntil, maxAgeAt time.Time) {
	if terminal {
		return fetchedAt.Add(t.TerminalTTL), time.Time{}
	}
	if ttl := t.TTL(group); ttl > 0 {
		freshUntil = fetchedAt.Add(ttl)
	}
	if age := t.MaxAge(group); age > 0 {
		maxAgeAt = fetchedAt.Add(age)
	}
	return freshUntil, maxAgeAt
}

// Extend is the rule revalidation uses for a row of an extendable group whose
// content the origin confirmed unchanged: fresh_until becomes
// min(max_age_at, now + ttl). ok is false when the row is already at its
// maximum age, when its MaxAgeAt is zero, and for any group that is not
// extendable; the caller then re-pulls the group. Extend is never called for an
// entity in a terminal state, whose fresh_until is already the terminal TTL.
func (t Table) Extend(row store.GroupRow, group string, now time.Time) (freshUntil time.Time, ok bool) {
	g, _ := t.Spec(group)
	if !g.Extendable {
		return time.Time{}, false
	}
	if row.MaxAgeAt.IsZero() || !now.Before(row.MaxAgeAt) {
		return time.Time{}, false
	}
	freshUntil = now.Add(g.TTL)
	if freshUntil.After(row.MaxAgeAt) {
		freshUntil = row.MaxAgeAt
	}
	return freshUntil, true
}

// Fresh judges a stored row against the current stamps at now. An empty row (no
// content, as an invalidation of a never-fetched group leaves) is never served
// and is stale. A row at or past max_age_at is hard-stale. A stamped group
// fetched for other stamps is stale even inside its time window. Otherwise the
// row is fresh while now is before fresh_until; a zero fresh_until means "no
// time limit" for a stamped group and "unknown, stale" for any other.
func (t Table) Fresh(row store.GroupRow, cur Stamps, now time.Time) Freshness {
	if len(row.Content) == 0 {
		return verdict(LevelStale, "no content stored")
	}
	if !row.MaxAgeAt.IsZero() && !now.Before(row.MaxAgeAt) {
		return verdict(LevelHardStale, "past the maximum age")
	}
	stamped := t.Stamped(row.Group)
	if stamped {
		primary, secondary := row.Stamps()
		if primary != cur.Primary {
			return verdict(LevelStale, "fetched for a different head")
		}
		if secondary != cur.Secondary {
			return verdict(LevelStale, "fetched for a different base")
		}
	}
	if row.FreshUntil.IsZero() {
		if stamped {
			return Freshness{}
		}
		return verdict(LevelStale, "no fresh_until recorded")
	}
	if !now.Before(row.FreshUntil) {
		return verdict(LevelStale, "past fresh_until")
	}
	return Freshness{}
}
