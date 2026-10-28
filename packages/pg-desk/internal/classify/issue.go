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
//   - closed:           the issue moves from non-terminal to terminal.
//   - reopened:         the issue moves from terminal to non-terminal.
//   - opened:           state moves from another non-terminal state to "open".
//   - status_changed:   the state string differs between two known states
//     (so opened is always accompanied by it, and closed and reopened are
//     too, except in the category rollout case below).
//
// "Terminal" is decided by the tracker's status category when issue_show
// carries one (issue_show.status_category, schema.Issue.StatusCategory): the
// category "done" is terminal, "new" and "indeterminate" are not, whatever
// the status is called ("Complete", "Released", "Won't Do"). Only when the
// category is absent (the beads backend never sends one; Jira sends none for
// a legacy "No Category" status or an older pjira) does the state NAME decide,
// through issueTerminalStates. The jira.done_statuses config is deliberately
// not consulted here: the classifier has no config import.
//
// closed, reopened and opened compare the two snapshots' terminal-ness, not
// their state strings, which has two consequences:
//
//   - Category rollout: an issue stored as "Complete" before its category was
//     known, whose next read arrives with category "done", keeps the same
//     state string yet moves from non-terminal (by name) to terminal (by
//     category). It emits closed ALONE (no status_changed: the state did not
//     change), and SourceTerminal then deactivates it. The same happens once
//     to every stored done-category Jira issue on its next hydration.
//   - Category disappearing: when the old snapshot had a category and the new
//     one does not (an older pjira on PATH, a degraded decode), the new
//     terminal-ness would be the name fallback and would misread "Complete"
//     as non-terminal. A vanished category is NOT a state movement: no
//     closed, reopened or opened is emitted (status_changed still is when the
//     state strings differ).
//   - assignee_changed: the assignee differs.
//   - deps_changed:     the set of (id, type) edges in issue_show.deps
//     differs, or the id set of issue_deps differs when BOTH snapshots carry
//     it. An absent issue_deps (switch off, or the read failed) is never read
//     as every dependency having been removed.
//   - comments_changed: schema.Issue carries NO comment list or count, so the
//     proxy is updated_at advancing while every other issue_show field
//     (except the volatile ones) is unchanged. The proxy is KEPT for beads and
//     RETIRED for an owning backend (Snapshot.OwningBackend), which supplies a
//     real comment summary (INV-ICD-6).
//   - fields_changed:   the residual (INV-ICD-5): when the issue_show payloads
//     differ in top-level fields that no emitted specific kind covers
//     (issueKindFields), ONE record names those fields, so a labels-only or
//     priority-only edit is never lost.
//
// A missing, empty or undecodable issue_show on either side contributes no
// show-derived kind: it is never read as a state transition. A degraded new
// snapshot yields nothing at all. link_changed is not this classifier's.
type issueClassifier struct{}

// issueTerminalStates are the states that count as closed (lower-case).
var issueTerminalStates = map[string]struct{}{
	"closed": {}, "done": {}, "resolved": {}, "cancelled": {}, "canceled": {}, "wontfix": {},
}

// IssueStatus is the coarse progress kind of an issue, derived from the
// tracker's status category (preferred) or the state name (fallback).
type IssueStatus string

const (
	// IssueStatusUnknown: no category, and the state name is not a known
	// terminal state. The name fallback cannot tell "not started" from "in
	// progress", so it says nothing about either.
	IssueStatusUnknown IssueStatus = ""
	// IssueStatusNotStarted is the category "new".
	IssueStatusNotStarted IssueStatus = "not_started"
	// IssueStatusInProgress is the category "indeterminate".
	IssueStatusInProgress IssueStatus = "in_progress"
	// IssueStatusDone is the category "done", or (category absent) a state
	// name in issueTerminalStates.
	IssueStatusDone IssueStatus = "done"
)

// IssueStatusKind reports the progress kind of an issue entity's stored
// facts (a marshaled gather.IssueFacts). When issue_show carries a recognized
// status category (new, indeterminate or done, case-insensitive), the kind
// follows it and categoryPresent is true. Otherwise categoryPresent is false
// and the kind is IssueStatusDone for a terminal state name and
// IssueStatusUnknown for anything else (including missing, empty or
// undecodable facts). It is pure: no clock, no config, no I/O.
func IssueStatusKind(facts json.RawMessage) (kind IssueStatus, categoryPresent bool) {
	v := decodeIssueFacts(facts)
	return v.status, v.hasCategory
}

// issueStatusFor derives the kind from a state name and a raw category.
func issueStatusFor(state, category string) (status IssueStatus, hasCategory bool) {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "new":
		return IssueStatusNotStarted, true
	case "indeterminate":
		return IssueStatusInProgress, true
	case "done":
		return IssueStatusDone, true
	}
	if issueIsTerminalName(state) {
		return IssueStatusDone, false
	}
	return IssueStatusUnknown, false
}

// issueView is the decoded, comparable part of one snapshot.
type issueView struct {
	show        map[string]any // nil when issue_show is absent, empty or undecodable
	state       string
	status      IssueStatus // category-derived when hasCategory, else the name fallback
	hasCategory bool        // issue_show carries a recognized status_category
	assignee    string
	deps        []string // sorted "id\x00type" edges from issue_show.deps
	depIDs      []string // sorted ids from issue_deps; nil when absent
	hasDeps     bool     // issue_deps present and decodable
}

// issueKindFields is the residual table: the top-level issue_show keys each
// specific kind covers. A key that differs between two snapshots and is covered
// by no EMITTED kind is named by fields_changed.
var issueKindFields = map[Kind][]string{
	KindStatusChanged:   {"state", "status_category"},
	KindClosed:          {"state", "status_category"},
	KindReopened:        {"state", "status_category"},
	KindOpened:          {"state", "status_category"},
	KindAssigneeChanged: {"assignee"},
	KindDepsChanged:     {"deps"},
	KindCommentsChanged: {"comments", "comment_count"},
}

// issueVolatileFields are the top-level issue_show keys that move without the
// issue changing. They mirror the keys pipeline.NormalizeIssuePayload drops
// (which classify cannot import): the residual never names them, and the
// comments proxy ignores them.
var issueVolatileFields = map[string]struct{}{
	"updated_at": {}, "updated": {}, "as_of": {}, "stale": {},
	"served_from": {}, "age_seconds": {}, "groups": {}, "entity_meta": {},
}

func (issueClassifier) Classify(old, new Snapshot) []Record {
	if new.Degraded {
		return nil
	}
	o, n := decodeIssueFacts(old.Payload), decodeIssueFacts(new.Payload)
	var kinds []Kind

	if o.show != nil && n.show != nil {
		kinds = append(kinds, issueStateKinds(o, n)...)
		if o.assignee != n.assignee {
			kinds = append(kinds, KindAssigneeChanged)
		}
		if !reflect.DeepEqual(o.deps, n.deps) {
			kinds = append(kinds, KindDepsChanged)
		}
		if !new.OwningBackend && issueCommentsProxy(o.show, n.show, old.Updated, new.Updated) {
			kinds = append(kinds, KindCommentsChanged)
		}
	}
	if o.hasDeps && n.hasDeps && !reflect.DeepEqual(o.depIDs, n.depIDs) {
		kinds = append(kinds, KindDepsChanged)
	}

	out := make([]Record, 0, len(kinds)+1)
	for _, k := range kinds {
		out = append(out, Record{Kind: k})
	}
	if o.show != nil && n.show != nil {
		if fields := issueResidualFields(o.show, n.show, kinds); len(fields) > 0 {
			out = append(out, Record{Kind: KindFieldsChanged, Fields: fields})
		}
	}
	return out
}

// issueResidualFields returns the sorted top-level issue_show keys that differ
// between the two shows, are not volatile and are covered by none of the
// emitted kinds.
func issueResidualFields(oldShow, newShow map[string]any, emitted []Kind) []string {
	covered := map[string]struct{}{}
	for _, k := range emitted {
		for _, f := range issueKindFields[k] {
			covered[f] = struct{}{}
		}
	}
	keys := map[string]struct{}{}
	for k := range oldShow {
		keys[k] = struct{}{}
	}
	for k := range newShow {
		keys[k] = struct{}{}
	}
	var out []string
	for k := range keys {
		if _, v := issueVolatileFields[k]; v {
			continue
		}
		if _, c := covered[k]; c {
			continue
		}
		ov, oOK := oldShow[k]
		nv, nOK := newShow[k]
		if oOK == nOK && reflect.DeepEqual(ov, nv) {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// issueStateKinds maps a state movement to kinds. An unknown (empty) state on
// either side yields nothing. closed and reopened compare the views'
// terminal-ness (see the classifier doc), so they can fire with an unchanged
// state string; opened and status_changed need the strings to differ.
func issueStateKinds(o, n issueView) []Kind {
	if o.state == "" || n.state == "" {
		return nil
	}
	differ := o.state != n.state
	var kinds []Kind
	if differ {
		kinds = append(kinds, KindStatusChanged)
	}
	if o.hasCategory && !n.hasCategory {
		return kinds // a vanished category is not a state movement
	}
	oldTerm, newTerm := o.terminal(), n.terminal()
	switch {
	case !oldTerm && newTerm:
		kinds = append(kinds, KindClosed)
	case oldTerm && !newTerm:
		kinds = append(kinds, KindReopened)
	case differ && !oldTerm && !newTerm && strings.EqualFold(n.state, "open"):
		kinds = append(kinds, KindOpened)
	}
	return kinds
}

// terminal reports whether the view's issue is finished: the category when
// present, else the state-name rule.
func (v issueView) terminal() bool { return v.status == IssueStatusDone }

// issueIsTerminalName reports whether state is one of the terminal state
// names (the fallback when no status category is available).
func issueIsTerminalName(state string) bool {
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
		category, _ := show["status_category"].(string)
		v.status, v.hasCategory = issueStatusFor(v.state, category)
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
// field, ignoring the volatile ones, is unchanged. The timestamps
// come from the snapshots' Updated (the pipeline hands over a normalised
// payload without updated_at); a payload-embedded updated_at is the fallback
// for a snapshot built without Updated.
func issueCommentsProxy(oldShow, newShow map[string]any, oldUpdated, newUpdated string) bool {
	oldAt, newAt := oldUpdated, newUpdated
	if oldAt == "" {
		oldAt, _ = oldShow["updated_at"].(string)
	}
	if newAt == "" {
		newAt, _ = newShow["updated_at"].(string)
	}
	if !issueAdvanced(oldAt, newAt) {
		return false
	}
	return reflect.DeepEqual(issueStable(oldShow), issueStable(newShow))
}

// issueStable returns show without its volatile fields.
func issueStable(show map[string]any) map[string]any {
	out := make(map[string]any, len(show))
	for k, v := range show {
		if _, volatile := issueVolatileFields[k]; volatile {
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
