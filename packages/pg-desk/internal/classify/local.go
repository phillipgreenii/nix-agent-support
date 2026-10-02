package classify

// Local change sources: the change records pg-desk itself originates (origin
// pg-desk in the change log) rather than deriving from a snapshot diff. They
// are link changes (derived AND external, for both entities of a link), the
// one-hop propagation of a change to the entities linked to it, and the
// time-based "thread resolved".
//
// Everything here is a pure function of its arguments. The store is read only
// by the func NewStoreWorkLookup returns, and no clock or config file is
// consulted (now and the thread window are passed in). The write entry point
// calls these functions and appends the resulting records; this file never
// writes.

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Entity and relation names this file depends on. A work item is recognized
// from the work-item extractor's own shapes only: an issue -> pr link with
// relation "work" (from the issue's repo + pr_number metadata) and an
// issue -> issue link with relation "parent" (a child to its anchor). Never
// from titles or dedup keys.
const (
	localTypePR     = "pr"
	localTypeIssue  = "issue"
	localTypeThread = "thread"
	localRelWork    = "work"
	localRelParent  = "parent"

	// localDefaultThreadWindow is the design default of
	// watch.thread.active_window.
	localDefaultThreadWindow = 7 * 24 * time.Hour
)

// LinkChange is one link added or removed between two observations of a
// link set.
type LinkChange struct {
	Link  store.XrefLink
	Added bool
}

// localLinkKey is the identity of one claim on a link.
type localLinkKey struct {
	repo, fromType, fromID, toType, toID, relation, origin string
}

func localKeyOf(l store.XrefLink) localLinkKey {
	return localLinkKey{l.Repo, l.FromType, l.FromID, l.ToType, l.ToID, l.Relation, l.Origin}
}

func (a localLinkKey) less(b localLinkKey) bool {
	for _, p := range [][2]string{
		{a.repo, b.repo},
		{a.fromType, b.fromType},
		{a.fromID, b.fromID},
		{a.toType, b.toType},
		{a.toID, b.toID},
		{a.relation, b.relation},
		{a.origin, b.origin},
	} {
		if p[0] != p[1] {
			return p[0] < p[1]
		}
	}
	return false
}

// DiffLinks reports the links that differ between two link sets. A link is
// identified by (Repo, FromType, FromID, ToType, ToID, Relation, Origin):
// links only in after are Added, links only in before are removed.
// FirstSeen, LastConfirmed, Evidence, ActedAt and Reason movements alone are
// not a change. An external claim on an already-derived link has a different
// Origin and is therefore an added link. The result is sorted by identity, so
// it does not depend on the order of the inputs.
func DiffLinks(before, after []store.XrefLink) []LinkChange {
	prev := make(map[localLinkKey]store.XrefLink, len(before))
	for _, l := range before {
		if _, dup := prev[localKeyOf(l)]; !dup {
			prev[localKeyOf(l)] = l
		}
	}
	next := make(map[localLinkKey]store.XrefLink, len(after))
	for _, l := range after {
		if _, dup := next[localKeyOf(l)]; !dup {
			next[localKeyOf(l)] = l
		}
	}
	var out []LinkChange
	for k, l := range next {
		if _, ok := prev[k]; !ok {
			out = append(out, LinkChange{Link: l, Added: true})
		}
	}
	for k, l := range prev {
		if _, ok := next[k]; !ok {
			out = append(out, LinkChange{Link: l, Added: false})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return localKeyOf(out[i].Link).less(localKeyOf(out[j].Link))
	})
	return out
}

// Targeted is a change record addressed to one entity.
type Targeted struct {
	EntityType, EntityID string
	Record
}

// WorkLookup reports whether issueID is one of prID's own work items: an
// anchor linked to the PR with relation work, or a child whose parent link
// names such an anchor.
type WorkLookup func(prID, issueID string) bool

// NewStoreWorkLookup builds a WorkLookup that reads the xref links of repo
// from st, using only the relations work (issue -> pr) and parent
// (issue -> issue). A store error answers false: a lookup that cannot be
// answered never fabricates ownership.
func NewStoreWorkLookup(st *store.Store, repo string) WorkLookup {
	hasWork := func(prID, issueID string) bool {
		links, err := st.ListXrefLinksFrom(repo, localTypeIssue, issueID)
		if err != nil {
			return false
		}
		for _, l := range links {
			if l.Relation == localRelWork && l.ToType == localTypePR && l.ToID == prID {
				return true
			}
		}
		return false
	}
	return func(prID, issueID string) bool {
		if hasWork(prID, issueID) {
			return true
		}
		links, err := st.ListXrefLinksFrom(repo, localTypeIssue, issueID)
		if err != nil {
			return false
		}
		for _, l := range links {
			if l.Relation == localRelParent && l.ToType == localTypeIssue && hasWork(prID, l.ToID) {
				return true
			}
		}
		return false
	}
}

// localIsWorkLink reports whether l itself proves issueID is prID's work
// item: an issue -> pr link with relation work between exactly those two.
func localIsWorkLink(l store.XrefLink, prID, issueID string) bool {
	return l.Relation == localRelWork &&
		l.FromType == localTypeIssue && l.FromID == issueID &&
		l.ToType == localTypePR && l.ToID == prID
}

// localKindFor picks the change kind for the entity (entityType, entityID)
// at one end of link l: work_changed on a pr when the other end is one of
// its own work items, link_changed for every other end and every non-pr
// type. The changed link counts as ownership evidence itself, so a removed or
// just-added work link is routed correctly whatever the store reflects.
func localKindFor(l store.XrefLink, entityType, entityID, otherType, otherID string, own WorkLookup) Kind {
	if entityType != localTypePR || otherType != localTypeIssue {
		return KindLinkChanged
	}
	if localIsWorkLink(l, entityID, otherID) || (own != nil && own(entityID, otherID)) {
		return KindWorkChanged
	}
	return KindLinkChanged
}

// localTargets accumulates Targeted records, de-duplicated per
// (entity, kind), in first-appearance order.
type localTargets struct {
	seen map[localTargetKey]bool
	out  []Targeted
}

type localTargetKey struct {
	entityType, entityID string
	kind                 Kind
}

func (ts *localTargets) add(entityType, entityID string, kind Kind) {
	k := localTargetKey{entityType, entityID, kind}
	if ts.seen == nil {
		ts.seen = map[localTargetKey]bool{}
	}
	if ts.seen[k] {
		return
	}
	ts.seen[k] = true
	ts.out = append(ts.out, Targeted{EntityType: entityType, EntityID: entityID, Record: Record{Kind: kind}})
}

// LinkRecords returns the change records for a set of link changes: for each
// change one record for the From entity and one for the To entity (both
// entities of the link, derived or external, any entity type), de-duplicated
// per (entity, kind). The kind for a pr end is work_changed when the other
// end is one of its own work items (see localKindFor), else link_changed;
// every non-pr end is link_changed. own may be nil (no further ownership
// knowledge beyond the changed links themselves).
func LinkRecords(changes []LinkChange, own WorkLookup) []Targeted {
	var ts localTargets
	for _, c := range changes {
		l := c.Link
		ts.add(l.FromType, l.FromID, localKindFor(l, l.FromType, l.FromID, l.ToType, l.ToID, own))
		ts.add(l.ToType, l.ToID, localKindFor(l, l.ToType, l.ToID, l.FromType, l.FromID, own))
	}
	return ts.out
}

// PropagationRecords applies the rule "a change to a linked entity appends a
// change record to every entity linked to it". links is every link touching
// the changed entity (both directions); the result has one record per linked
// entity (de-duplicated per entity and kind) with the same kind choice as
// LinkRecords. The changed entity itself is never a target.
//
// This is ONE HOP by construction: the function reads only the links it is
// given and the records it returns are plain values that nothing here feeds
// back into it. A propagated record therefore cannot propagate again, so a
// chain a - b - c cannot cascade from a change to a into c, and a cycle
// cannot loop. Callers MUST NOT re-invoke it on the records it produced.
func PropagationRecords(changedType, changedID string, links []store.XrefLink, own WorkLookup) []Targeted {
	var ts localTargets
	for _, l := range links {
		switch {
		case l.FromType == changedType && l.FromID == changedID:
			if l.ToType == changedType && l.ToID == changedID {
				continue
			}
			ts.add(l.ToType, l.ToID, localKindFor(l, l.ToType, l.ToID, l.FromType, l.FromID, own))
		case l.ToType == changedType && l.ToID == changedID:
			ts.add(l.FromType, l.FromID, localKindFor(l, l.FromType, l.FromID, l.ToType, l.ToID, own))
		}
	}
	return ts.out
}

// ThreadResolved reports whether a thread has just become resolved: no reply
// inside window. It is true only when both snapshots are observed and
// active, new is a thread whose last_reply_at (from thread_show) is older
// than window at now, and old was NOT already past the window at old.AsOf, so
// the record fires once on the transition and not on every later hydration.
//
// It never fabricates: false when new's last_reply_at is missing or
// unparseable, and false when old.AsOf is unparseable (the transition cannot
// be established). An old snapshot without a usable last_reply_at counts as
// not past the window. window <= 0 means the design default of 7 days.
// last_reply_at may be a Slack ts-shaped string (epoch seconds with a
// fractional part) or an RFC3339 string.
func ThreadResolved(old, new Snapshot, now time.Time, window time.Duration) bool {
	if !old.Exists || !old.Active || !new.Exists || !new.Active || new.Type != localTypeThread {
		return false
	}
	if window <= 0 {
		window = localDefaultThreadWindow
	}
	newReply, ok := localLastReply(new.Payload)
	if !ok || now.Sub(newReply) <= window {
		return false
	}
	oldAsOf, ok := localParseReplyTime(old.AsOf)
	if !ok {
		return false
	}
	if oldReply, ok := localLastReply(old.Payload); ok && oldAsOf.Sub(oldReply) > window {
		return false
	}
	return true
}

// localLastReply reads thread_show.last_reply_at from a thread payload.
func localLastReply(payload json.RawMessage) (time.Time, bool) {
	if len(payload) == 0 {
		return time.Time{}, false
	}
	var f struct {
		ThreadShow struct {
			LastReplyAt json.RawMessage `json:"last_reply_at"`
		} `json:"thread_show"`
	}
	if err := json.Unmarshal(payload, &f); err != nil {
		return time.Time{}, false
	}
	raw := strings.TrimSpace(string(f.ThreadShow.LastReplyAt))
	if raw == "" || raw == "null" {
		return time.Time{}, false
	}
	if strings.HasPrefix(raw, `"`) {
		var s string
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return time.Time{}, false
		}
		raw = s
	}
	return localParseReplyTime(raw)
}

// localParseReplyTime parses an RFC3339 timestamp or a Slack ts-shaped string
// ("1759060800.000200": epoch seconds, optional fractional part). Anything
// else, and any non-positive epoch, is unparseable.
func localParseReplyTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	secStr, fracStr, hasFrac := strings.Cut(s, ".")
	sec, err := strconv.ParseInt(secStr, 10, 64)
	if err != nil || sec <= 0 {
		return time.Time{}, false
	}
	var nanos int64
	if hasFrac {
		if fracStr == "" {
			return time.Time{}, false
		}
		if len(fracStr) > 9 {
			fracStr = fracStr[:9] // finer than nanoseconds is dropped
		}
		for _, r := range fracStr {
			if r < '0' || r > '9' {
				return time.Time{}, false
			}
		}
		n, err := strconv.ParseInt(fracStr+strings.Repeat("0", 9-len(fracStr)), 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		nanos = n
	}
	return time.Unix(sec, nanos).UTC(), true
}
