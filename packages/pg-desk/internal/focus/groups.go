package focus

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// derivedOriginPrefix begins the origin of a link a pipeline extractor
// derived (as opposed to an externally managed one).
const derivedOriginPrefix = "derived:"

// Group is one correlation group: candidates joined by DERIVED work links
// (a PR and the anchor or work-item beads that name it). The group takes ONE
// slot; the rank chooses its representative (the highest-ranked member) and
// the others are not separately listed.
//
// MemberDue and MemberPriority are the raw due_date and priority strings the
// members' stored issue_show carry (an empty string, or an absent member, is
// "none"; a PR has neither of its own). They are what the rank's own key
// parsers read: the rank applies its time zone, its 7-day horizon and its
// priority mapping table, so it MUST recompute the inherited values from
// these. EarliestDue and HighestPriority are the best-effort raw values
// Groups itself can pick without those parsers: the earliest member due that
// parses as an RFC 3339 timestamp or a YYYY-MM-DD date (compared as an
// instant, UTC for a date-only value) and the member priority of the form
// "P<n>" or "<n>" with the smallest n; each is "" when no member qualifies.
type Group struct {
	Members         []Key
	EarliestDue     string
	HighestPriority string
	MemberDue       map[Key]string
	MemberPriority  map[Key]string
}

// Groups computes the correlation groups of a candidate set. Only a derived
// link of relation work (origin prefix derived:) joins two candidates; the
// source link of a minted focus bead, and a work link someone recorded
// externally, never do, so a focus bead's priority never leaks into its PR's.
// The reach is the connected component over those links, restricted to the
// members of set. A group has at least two members (a candidate joined to
// nothing needs no group); members are in candidate order (kind, then key)
// and groups are ordered by their first member.
//
// Groups is pure: no clock, no tracker call, no store access, no write, no
// telemetry and no log line.
func Groups(in Inputs, set CandidateSet) []Group {
	cands := map[Key]Candidate{}
	for _, c := range set.Candidates {
		cands[c.Key] = c
	}
	parent := map[Key]Key{}
	var find func(k Key) Key
	find = func(k Key) Key {
		p, ok := parent[k]
		if !ok || p == k {
			return k
		}
		root := find(p)
		parent[k] = root
		return root
	}
	for _, l := range in.Links {
		if l.Relation != relationWork || !strings.HasPrefix(l.Origin, derivedOriginPrefix) {
			continue
		}
		from, to := Key{l.FromType, l.FromID}, Key{l.ToType, l.ToID}
		if from == to {
			continue
		}
		if _, ok := cands[from]; !ok {
			continue
		}
		if _, ok := cands[to]; !ok {
			continue
		}
		rf, rt := find(from), find(to)
		if rf != rt {
			parent[rf] = rt
		}
		parent[from], parent[to] = find(from), find(to)
	}
	byRoot := map[Key][]Candidate{}
	for k, c := range cands {
		if _, joined := parent[k]; !joined {
			continue
		}
		r := find(k)
		byRoot[r] = append(byRoot[r], c)
	}
	var out []Group
	a := analyze(in)
	for _, members := range byRoot {
		if len(members) < 2 {
			continue
		}
		sort.Slice(members, func(i, j int) bool { return candidateLess(members[i], members[j]) })
		g := Group{MemberDue: map[Key]string{}, MemberPriority: map[Key]string{}}
		for _, m := range members {
			g.Members = append(g.Members, m.Key)
			if v := a.views[m.Key]; v != nil {
				g.MemberDue[m.Key] = v.dueDate
				g.MemberPriority[m.Key] = v.priority
			}
		}
		g.EarliestDue = earliestDue(g.Members, g.MemberDue)
		g.HighestPriority = highestPriority(g.Members, g.MemberPriority)
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		return memberLess(out[i].Members[0], out[j].Members[0], cands)
	})
	return out
}

// candidateLess is the candidate order (kind, then key) over Candidate values.
func candidateLess(a, b Candidate) bool {
	if a.Kind.order() != b.Kind.order() {
		return a.Kind.order() < b.Kind.order()
	}
	return keyLess(a.Key, b.Key)
}

func memberLess(a, b Key, cands map[Key]Candidate) bool {
	return candidateLess(cands[a], cands[b])
}

func earliestDue(members []Key, due map[Key]string) string {
	best, bestT := "", time.Time{}
	for _, m := range members {
		raw := due[m]
		t, ok := parseDue(raw)
		if !ok {
			continue
		}
		if best == "" || t.Before(bestT) {
			best, bestT = raw, t
		}
	}
	return best
}

func parseDue(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.DateOnly} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func highestPriority(members []Key, prio map[Key]string) string {
	best, bestN := "", -1
	for _, m := range members {
		raw := prio[m]
		n, ok := parsePriorityNumber(raw)
		if !ok {
			continue
		}
		if bestN < 0 || n < bestN {
			best, bestN = raw, n
		}
	}
	return best
}

// parsePriorityNumber reads "P2", "p2" or "2"; a smaller number is a higher
// priority.
func parsePriorityNumber(raw string) (int, bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "P"), "p")
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
