package classify

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
)

// Summary classifies a refreshed summary against the one stored before it.
//
// Both arguments are in the stored form (groups.Stored); they are normalised
// here for the comparison and Updated is read only for the revalidation rule.
// Gone is not compared (the engine's gone path handles it). Both arguments are
// the content of a real previous and a real refreshed read: the baseline rule
// is the engine's, which never calls Summary for a first sighting.
//
// Every difference in a compared field is recorded as a field of res, under
// its schema.Issue JSON key, so a real difference is never lost: a difference
// that no specific kind covers carries fields_changed naming its fields. A
// change to the updated token alone yields the zero Result.
//
// dependents is []string{groups.Detail} whenever next differs from prev in a
// normalised field or in the Updated token, and empty when they are identical.
// bodyCap is reserved: a summary carries no body text to cut.
func Summary(prev, next groups.SummaryContent, bodyCap int) (res engine.Result, dependents []string) {
	_ = bodyCap
	p, n := groups.Normalise(prev), groups.Normalise(next)
	p.Gone, n.Gone = false, false
	for _, c := range []*groups.SummaryContent{&p, &n} {
		c.Issue.ServedFrom, c.Issue.AgeSeconds, c.Issue.Groups = "", nil, nil
	}

	pv, nv := issueValues(p.Issue), issueValues(n.Issue)
	changed := map[string]bool{}
	var fields []Field
	for _, k := range slices.Sorted(maps.Keys(unionKeys(pv, nv))) {
		if bytes.Equal(pv[k], nv[k]) {
			continue
		}
		changed[k] = true
		fields = append(fields, Field{Name: k, Before: pv[k], After: nv[k]})
	}

	if len(changed) > 0 || prev.Updated != next.Updated || !bytes.Equal(jsonOf(p), jsonOf(n)) {
		dependents = []string{groups.Detail}
	}
	if len(changed) == 0 {
		return engine.Result{}, dependents
	}

	var kinds []string
	claimed := map[string]bool{}
	claim := func(keys ...string) {
		for _, k := range keys {
			claimed[k] = true
		}
	}

	if changed["state"] {
		kinds = append(kinds, KindStatusChanged)
		claim("state", "status_category")
	}
	if wasClosed, isClosed := groups.IsClosed(p.Issue), groups.IsClosed(n.Issue); wasClosed != isClosed {
		if isClosed {
			kinds = append(kinds, KindClosed)
		} else {
			kinds = append(kinds, KindReopened)
		}
		claim("status_category")
	}
	for _, k := range []struct{ key, kind string }{
		{"assignee", KindAssigneeChanged},
		{"priority", KindPriorityChanged},
		{"labels", KindLabelsChanged},
		{"parent", KindParentChanged},
		{"title", KindTitleChanged},
	} {
		if changed[k.key] {
			kinds = append(kinds, k.kind)
			claim(k.key)
		}
	}
	if changed["comment_count"] || changed["latest_comment_at"] {
		kinds = append(kinds, KindCommentsChanged)
		claim("comment_count", "latest_comment_at")
	}
	// A rename is a different key on both sides; a missing key is missing data,
	// never a rename.
	if changed["id"] && p.Issue.ID != "" && n.Issue.ID != "" {
		kinds = append(kinds, KindRenamed)
		claim("id")
	}
	for k := range changed {
		if !claimed[k] {
			kinds = append(kinds, KindFieldsChanged)
			break
		}
	}

	return engine.Result{Kinds: engine.OrderKinds(Kinds(), kinds), Fields: engine.Normalize(fields)}, dependents
}

// unionKeys is the set of keys of either map.
func unionKeys(a, b map[string]json.RawMessage) map[string]struct{} {
	out := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		out[k] = struct{}{}
	}
	for k := range b {
		out[k] = struct{}{}
	}
	return out
}
