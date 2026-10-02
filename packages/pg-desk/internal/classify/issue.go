package classify

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"time"
)

func init() { Register("issue", issueClassifier{}) }

// issueClassifier is the Strategy for the issue entity type. Snapshot.Payload
// is a marshaled gather.IssueFacts: issue_show (a raw schema.Issue) and,
// only when the read-issue-deps switch is on, issue_deps.
//
// Signal-to-kind mapping (the design names each kind but not its trigger):
//
//   - closed:           state moves from a non-terminal to a terminal state.
//   - reopened:         state moves from a terminal to a non-terminal state.
//   - opened:           state moves from another non-terminal state to "open".
//   - status_changed:   the state string differs between two known states
//     (so closed, reopened and opened are always accompanied by it).
//   - assignee_changed: the assignee differs.
//   - deps_changed:     the set of (id, type) edges in issue_show.deps
//     differs, or the id set of issue_deps differs when BOTH snapshots carry
//     it. An absent issue_deps (switch off, or the read failed) is never read
//     as every dependency having been removed.
//   - comments_changed: schema.Issue carries NO comment list or count, so the
//     proxy is updated_at advancing while every other issue_show field
//     (except the read-time as_of and stale) is unchanged. A labels-only or
//     priority-only edit therefore emits nothing.
//
// A missing, empty or undecodable issue_show on either side contributes no
// show-derived kind: it is never read as a state transition. A degraded new
// snapshot yields nothing at all. link_changed is not this classifier's.
type issueClassifier struct{}

// issueTerminalStates are the states that count as closed (lower-case).
var issueTerminalStates = map[string]struct{}{
	"closed": {}, "done": {}, "resolved": {}, "cancelled": {}, "canceled": {}, "wontfix": {},
}

// issueView is the decoded, comparable part of one snapshot.
type issueView struct {
	show     map[string]any // nil when issue_show is absent, empty or undecodable
	state    string
	assignee string
	deps     []string // sorted "id\x00type" edges from issue_show.deps
	depIDs   []string // sorted ids from issue_deps; nil when absent
	hasDeps  bool     // issue_deps present and decodable
}

func (issueClassifier) Classify(old, new Snapshot) []Record {
	if new.Degraded {
		return nil
	}
	o, n := decodeIssueFacts(old.Payload), decodeIssueFacts(new.Payload)
	var kinds []Kind

	if o.show != nil && n.show != nil {
		kinds = append(kinds, issueStateKinds(o.state, n.state)...)
		if o.assignee != n.assignee {
			kinds = append(kinds, KindAssigneeChanged)
		}
		if !reflect.DeepEqual(o.deps, n.deps) {
			kinds = append(kinds, KindDepsChanged)
		}
		if issueCommentsProxy(o.show, n.show) {
			kinds = append(kinds, KindCommentsChanged)
		}
	}
	if o.hasDeps && n.hasDeps && !reflect.DeepEqual(o.depIDs, n.depIDs) {
		kinds = append(kinds, KindDepsChanged)
	}

	out := make([]Record, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, Record{Kind: k})
	}
	return out
}

// issueStateKinds maps a state movement to kinds. An unknown (empty) state on
// either side yields nothing.
func issueStateKinds(oldState, newState string) []Kind {
	if oldState == "" || newState == "" || oldState == newState {
		return nil
	}
	kinds := []Kind{KindStatusChanged}
	oldTerm, newTerm := issueIsTerminal(oldState), issueIsTerminal(newState)
	switch {
	case !oldTerm && newTerm:
		kinds = append(kinds, KindClosed)
	case oldTerm && !newTerm:
		kinds = append(kinds, KindReopened)
	case !oldTerm && !newTerm && strings.EqualFold(newState, "open"):
		kinds = append(kinds, KindOpened)
	}
	return kinds
}

func issueIsTerminal(state string) bool {
	_, ok := issueTerminalStates[strings.ToLower(state)]
	return ok
}

// decodeIssueFacts decodes the comparable parts of a gather.IssueFacts
// payload. Anything undecodable yields a zero view (no show-derived kinds).
func decodeIssueFacts(payload json.RawMessage) issueView {
	var v issueView
	var facts struct {
		IssueShow json.RawMessage `json:"issue_show"`
		IssueDeps json.RawMessage `json:"issue_deps"`
	}
	if len(payload) == 0 || json.Unmarshal(payload, &facts) != nil {
		return v
	}

	var show map[string]any
	if len(facts.IssueShow) > 0 && json.Unmarshal(facts.IssueShow, &show) == nil && len(show) > 0 {
		v.show = show
		v.state, _ = show["state"].(string)
		v.assignee, _ = show["assignee"].(string)
		if edges, ok := show["deps"].([]any); ok {
			for _, e := range edges {
				if m, ok := e.(map[string]any); ok {
					id, _ := m["id"].(string)
					typ, _ := m["type"].(string)
					v.deps = append(v.deps, id+"\x00"+typ)
				}
			}
			sort.Strings(v.deps)
		}
	}

	if len(facts.IssueDeps) > 0 {
		var d struct {
			IDs *[]string `json:"ids"`
		}
		if json.Unmarshal(facts.IssueDeps, &d) == nil && d.IDs != nil {
			v.hasDeps = true
			v.depIDs = append([]string(nil), *d.IDs...)
			sort.Strings(v.depIDs)
		}
	}
	return v
}

// issueCommentsProxy reports whether updated_at advanced while every other
// field, ignoring the read-time as_of and stale, is unchanged.
func issueCommentsProxy(oldShow, newShow map[string]any) bool {
	oldAt, _ := oldShow["updated_at"].(string)
	newAt, _ := newShow["updated_at"].(string)
	if !issueAdvanced(oldAt, newAt) {
		return false
	}
	return reflect.DeepEqual(issueStable(oldShow), issueStable(newShow))
}

// issueStable returns show without its volatile fields.
func issueStable(show map[string]any) map[string]any {
	out := make(map[string]any, len(show))
	for k, v := range show {
		switch k {
		case "updated_at", "as_of", "stale":
			continue
		}
		out[k] = v
	}
	return out
}

// issueAdvanced reports whether newAt is later than oldAt. Both must be
// non-empty; RFC3339 values compare as instants, anything else as strings.
func issueAdvanced(oldAt, newAt string) bool {
	if oldAt == "" || newAt == "" || oldAt == newAt {
		return false
	}
	ot, oerr := time.Parse(time.RFC3339, oldAt)
	nt, nerr := time.Parse(time.RFC3339, newAt)
	if oerr == nil && nerr == nil {
		return nt.After(ot)
	}
	return newAt > oldAt
}
