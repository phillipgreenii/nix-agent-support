package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/view"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

// The bounded work-item lookup of a pr run (design 6.2 "Work-item visibility";
// docs/behavior/pg-decider/plan-and-apply.md, "The work-item lookup").
//
// A pr plan or apply reads ONE all-status listing of the six PR kinds through
// the named query config.work_items_query, once per run. A listing that
// reports itself truncated and one that is unavailable FAIL THE RUN CLOSED
// (nothing is written); there is no size ceiling (retired, pg2-eclw3j.9), the
// size is reported as lookup_items so an abnormal count is visible without
// blocking a run. The listing is then used twice, identically by plan and
// by apply: it adds the PR's items the view does not link (a closed fix-ci item
// the gather's work-beads query never lists) to the view the rules decide from,
// and it is the dedup lookup of every create.

// The counted failure reasons of a lookup that cannot be trusted. They are the
// `failures` keys of the run counters line the failed run prints.
const (
	LookupReasonQueryAbsent = "lookup-query-absent"
	LookupReasonUnavailable = "lookup-unavailable"
	LookupReasonTruncated   = "lookup-truncated"
	// LookupReasonNoPRIdentity: work_items_query_params is set but the view
	// names no repo and PR number to send as the parameters.
	LookupReasonNoPRIdentity = "lookup-pr-identity-absent"
	// LookupReasonInconsistent: the per-PR answer lacks a work item the view's
	// own index owns or adopts, so it cannot be trusted.
	LookupReasonInconsistent = "lookup-inconsistent"
)

// LookupError is a lookup that cannot be trusted. Items carries the size known
// at the point of failure (nil when not known), so the failed run can still
// report it.
type LookupError struct {
	Reason string
	Items  *int
	Err    error
}

func (e *LookupError) Error() string { return e.Err.Error() }

func (e *LookupError) Unwrap() error { return e.Err }

// ListedItem is one work item of the listing, reduced to what the lookup reads.
type ListedItem struct {
	ID, Title, State, Assignee string
	Labels                     []string
	Metadata                   map[string]string
}

// Lookup is one run's all-status listing.
type Lookup struct {
	items []ListedItem
	// elapsed is the wall time the listing took, set by the caller that timed
	// it (SetElapsed); zero when not measured.
	elapsed time.Duration
}

// NewLookup builds a Lookup from items already read (tests and the reader).
func NewLookup(items []ListedItem) *Lookup {
	return &Lookup{items: items}
}

// SetElapsed records the wall time the listing took.
func (l *Lookup) SetElapsed(d time.Duration) { l.elapsed = d }

// Elapsed is the wall time the listing took (zero when not recorded).
func (l *Lookup) Elapsed() time.Duration { return l.elapsed }

// Items is the size of the whole listing, before any filtering to a PR.
func (l *Lookup) Items() int { return len(l.items) }

// listedEntity is one entity of an `issue list` answer; metadata values of any
// JSON type are read (scalars as their text), so one odd value cannot make the
// whole listing unreadable.
type listedEntity struct {
	ID       string                     `json:"id"`
	Title    string                     `json:"title"`
	State    string                     `json:"state"`
	Assignee string                     `json:"assignee"`
	Labels   []string                   `json:"labels"`
	Metadata map[string]json.RawMessage `json:"metadata"`
}

func (e listedEntity) item() ListedItem {
	it := ListedItem{ID: e.ID, Title: e.Title, State: e.State, Assignee: e.Assignee, Labels: e.Labels}
	if len(e.Metadata) > 0 {
		it.Metadata = make(map[string]string, len(e.Metadata))
		for k, raw := range e.Metadata {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				it.Metadata[k] = s
				continue
			}
			it.Metadata[k] = strings.TrimSpace(string(raw))
		}
	}
	return it
}

// listAnswer is the fan-out answer of `issue list` the lookup reads: the
// entities, the truncated flag and one row per queried backend.
type listAnswer struct {
	Entities  []listedEntity `json:"entities"`
	Truncated bool           `json:"truncated"`
	Sources   []struct {
		Source string `json:"source"`
		Status string `json:"status"`
		Reason string `json:"reason"`
	} `json:"sources"`
	Result json.RawMessage `json:"result"`
}

// decodeListing decodes the answer the way decodeList does (the fan-out's own
// shape, or the same members under a wire envelope's result) and also keeps the
// truncated flag and the per-backend source rows.
func decodeListing(stdout []byte) (listAnswer, error) {
	var top listAnswer
	if err := json.Unmarshal(stdout, &top); err != nil {
		return listAnswer{}, err
	}
	if top.Entities != nil || len(top.Result) == 0 {
		return top, nil
	}
	var inner listAnswer
	if err := json.Unmarshal(top.Result, &inner); err == nil && inner.Entities != nil {
		return inner, nil
	}
	var arr []listedEntity
	if err := json.Unmarshal(top.Result, &arr); err != nil {
		return listAnswer{}, err
	}
	top.Entities = arr
	return top, nil
}

func intp(n int) *int { return &n }

// prParamRepo and prParamNumber are the parameter names work_items_query_params
// may hold: the PR's repository (owner/name) and its number.
const (
	PRParamRepo   = "repo"
	PRParamNumber = "pr_number"
)

// PRIdentity is the repository and number of the viewed PR: the snapshot's
// when it carries both, else parsed from the view id ("<owner/name>#<n>").
func PRIdentity(v *view.View) (repo, number string, ok bool) {
	if v == nil {
		return "", "", false
	}
	if s := v.Snapshot; s.Repo != "" && s.Number > 0 {
		return s.Repo, strconv.Itoa(s.Number), true
	}
	i := strings.LastIndex(v.ID, "#")
	if i <= 0 || i == len(v.ID)-1 {
		return "", "", false
	}
	n, err := strconv.Atoi(v.ID[i+1:])
	if err != nil || n <= 0 {
		return "", "", false
	}
	return v.ID[:i], strconv.Itoa(n), true
}

// paramArgs renders one --param flag per configured parameter name, in the
// configured order.
func paramArgs(names []string, repo, number string) ([]string, error) {
	var out []string
	for _, n := range names {
		switch n {
		case PRParamRepo:
			out = append(out, "--param", PRParamRepo+"="+repo)
		case PRParamNumber:
			out = append(out, "--param", PRParamNumber+"="+number)
		default:
			return nil, fmt.Errorf("work_items_query_params names %q, which is not a parameter the decider can supply (%s, %s)", n, PRParamRepo, PRParamNumber)
		}
	}
	return out, nil
}

// ReadLookup reads the all-status listing through config.work_items_query. When config.work_items_query_params
// is set the query is the per-PR one: each named parameter is sent as --param
// with the viewed PR's identity (PRIdentity), and the answer is then checked
// against v's own index (checkConsistency). It returns a *LookupError (never a
// partial Lookup) for an unset query, a failed or unreadable call, a
// source that did not succeed, a truncated listing, a PR with no identity to send, and a per-PR answer that lacks a work
// item the view's index owns or adopts.
func ReadLookup(ctx context.Context, env Env, v *view.View) (*Lookup, error) {
	cfg := env.Config
	if cfg == nil || cfg.WorkItemsQuery == "" {
		return nil, &LookupError{
			Reason: LookupReasonQueryAbsent,
			Err:    fmt.Errorf("work-item lookup: work_items_query is not configured, so the work items of a PR cannot be listed in every status; nothing was written"),
		}
	}
	var params []string
	if len(cfg.WorkItemsQueryParams) > 0 {
		repo, number, ok := PRIdentity(v)
		if !ok {
			return nil, &LookupError{
				Reason: LookupReasonNoPRIdentity,
				Err:    fmt.Errorf("work-item lookup: the view names no repository and PR number to send as work_items_query_params; nothing was written"),
			}
		}
		var err error
		if params, err = paramArgs(cfg.WorkItemsQueryParams, repo, number); err != nil {
			return nil, &LookupError{Reason: LookupReasonQueryAbsent, Err: fmt.Errorf("work-item lookup: %w; nothing was written", err)}
		}
	}
	args := append([]string{"issue", "list", "--query", cfg.WorkItemsQuery}, params...)
	args = append(args, backendFlag(env)...)
	out, err := call(ctx, env, args)
	if err != nil {
		return nil, &LookupError{Reason: LookupReasonUnavailable, Err: fmt.Errorf("work-item lookup is unavailable: %w", err)}
	}
	ans, err := decodeListing(out)
	if err != nil {
		return nil, &LookupError{Reason: LookupReasonUnavailable, Err: fmt.Errorf("work-item lookup: decode pg-connector %s stdout: %w", strings.Join(args, " "), err)}
	}
	for _, s := range ans.Sources {
		if s.Status != "succeeded" {
			return nil, &LookupError{
				Reason: LookupReasonUnavailable,
				Err:    fmt.Errorf("work-item lookup is unavailable: source %q of pg-connector %s is %s (%s)", s.Source, strings.Join(args, " "), s.Status, s.Reason),
			}
		}
	}
	n := len(ans.Entities)
	if ans.Truncated {
		return nil, &LookupError{
			Reason: LookupReasonTruncated, Items: intp(n),
			Err: fmt.Errorf("work-item lookup: pg-connector %s was truncated, so the listing cannot be trusted; nothing was written", strings.Join(args, " ")),
		}
	}
	items := make([]ListedItem, 0, n)
	for _, e := range ans.Entities {
		items = append(items, e.item())
	}
	if len(params) > 0 {
		if missing := missingOwned(v, items); len(missing) > 0 {
			return nil, &LookupError{
				Reason: LookupReasonInconsistent, Items: intp(n),
				Err: fmt.Errorf("work-item lookup: the per-PR answer of pg-connector %s lacks %d work item(s) the view's own index owns or adopts (%s), so it cannot be trusted; nothing was written", strings.Join(args, " "), len(missing), strings.Join(missing, ", ")),
			}
		}
	}
	return &Lookup{items: items}, nil
}

// missingOwned lists the ids of the work items the view's index owns or adopts
// (workitem.BuildIndex: keyed items and adoptable keyless ones) that the
// per-PR answer does not hold. A per-PR query is a narrower read than the whole
// listing, so an item it fails to return would be minted again; any such item
// makes the answer untrusted.
func missingOwned(v *view.View, items []ListedItem) []string {
	have := make(map[string]bool, len(items))
	for _, it := range items {
		have[it.ID] = true
	}
	var missing []string
	for _, it := range workitem.BuildIndex(v).Items() {
		if !have[it.ID] {
			missing = append(missing, it.ID)
		}
	}
	return missing
}

// belongs reports whether a listed item is one of the viewed PR's own work
// items. A keyed item belongs when its key names this PR in either key form.
// A keyless one (a legacy anchor or child) belongs when its repo and pr_number
// metadata name the PR, its node_id is the PR's, or its title has one of the
// shapes the work-item behavior doc adopts by: the kind prefix plus the PR
// reference, or the anchor prefix "<repo>#<n>: ". A metadata-only filter would
// hide the keyless ones and mint duplicates.
func belongs(it ListedItem, ref workitem.EntityRef, v *view.View) bool {
	if k := it.Metadata["dedup_key"]; k != "" {
		if _, _, _, _, ok := workitem.ParseKey(k); ok {
			return ref.Owns(k)
		}
	}
	s := v.Snapshot
	if s.Repo != "" && s.Number > 0 && it.Metadata["repo"] == s.Repo && it.Metadata["pr_number"] == fmt.Sprint(s.Number) {
		return true
	}
	if ref.NodeID != "" && it.Metadata["node_id"] == ref.NodeID {
		return true
	}
	return workitem.TitleNames(it.Title, v)
}

// Augment returns a copy of v whose links also hold the PR's own work items
// that the listing shows and the view does not link (matched by id). A listed
// item is added as an issue link, so the rules see it exactly as they see a
// linked one: a closed fix-ci item is found instead of minted again, and a
// keyless legacy anchor is adopted instead of duplicated. A focus bead is
// added with the "source" relation, as the focus watch links it. v itself is
// not modified; with nothing to add, v is returned.
func (l *Lookup) Augment(v *view.View) *view.View {
	if l == nil || v == nil {
		return v
	}
	ref := workitem.EntityRefFrom(v)
	linked := map[string]bool{}
	for _, lk := range v.Links {
		if lk.Type == "issue" {
			linked[lk.ID] = true
		}
	}
	var add []view.Link
	for _, it := range l.items {
		if linked[it.ID] || !belongs(it, ref, v) {
			continue
		}
		link := view.Link{
			Type: "issue", ID: it.ID, Title: it.Title, State: it.State, Assignee: it.Assignee,
			Labels: append([]string(nil), it.Labels...), Metadata: cloneMD(it.Metadata),
		}
		if _, _, k, _, ok := workitem.ParseKey(it.Metadata["dedup_key"]); ok && k == workitem.KindFocusItem {
			link.Relation = workitem.RelationSource
		}
		add = append(add, link)
	}
	if len(add) == 0 {
		return v
	}
	out := *v
	out.Links = append(append([]view.Link(nil), v.Links...), add...)
	return &out
}

func cloneMD(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Find returns the id of a listed work item whose dedup_key is the same
// identity as key (equal, or equal except for the id and node_id forms).
func (l *Lookup) Find(ref workitem.EntityRef, key string) (string, bool) {
	if l == nil {
		return "", false
	}
	for _, it := range l.items {
		if k := it.Metadata["dedup_key"]; k != "" && sameKey(ref, key, k) {
			return it.ID, true
		}
	}
	return "", false
}

// DedupeFacts are the facts of the skip a deduped create becomes in a plan.
const (
	factDedupKey  = "dedup_key"
	factWorkItem  = "work_item"
	factDedupedOp = "op"
	factKind      = "kind"
)

// DedupePlan removes from a plan every create that apply would dedup against
// the listing (a create whose dedup_key the listing already holds), and lists
// each as a skip of its rule with the reason `already handled`, so that what
// plan shows is what apply then does on the same state: apply runs the very
// same function before it executes anything. A child parented at the removed
// anchor create is re-parented at the id the listing holds. The result's
// actions and skips are never nil.
func (l *Lookup) DedupePlan(ref workitem.EntityRef, res action.PlanResult) action.PlanResult {
	if l == nil {
		return res
	}
	out := action.PlanResult{Actions: make([]action.Action, 0, len(res.Actions)), Skipped: append([]action.Skip{}, res.Skipped...)}
	anchorID := ""
	for _, a := range res.Actions {
		if a.Op == action.OpCreate {
			if key := a.Fields.Metadata["dedup_key"]; key != "" {
				if id, ok := l.Find(ref, key); ok {
					out.Skipped = append(out.Skipped, action.Skip{
						Rule: a.Rule, Reason: action.ReasonAlreadyHandled,
						Facts: map[string]any{factDedupedOp: string(a.Op), factKind: a.Kind, factDedupKey: key, factWorkItem: id},
					})
					if a.Kind == string(workitem.KindAnchor) {
						anchorID = id
					}
					continue
				}
			}
		}
		out.Actions = append(out.Actions, a)
	}
	if anchorID != "" {
		for i := range out.Actions {
			if out.Actions[i].Fields.Parent == action.AnchorParent {
				out.Actions[i].Fields.Parent = anchorID
			}
		}
	}
	return out
}
