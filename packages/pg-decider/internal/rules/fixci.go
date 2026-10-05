package rules

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/decide"
	"github.com/phillipgreenii/pg-decider/internal/workitem"
)

func init() {
	decide.Register(decide.EntityTypePR, decide.OrdinalFixCIFailingOnHead, fixciRule{})
}

// fixciRule is fixci.failing-on-head (design 7.3, S26): one fix-ci work item
// per head commit, carrying the failing build ids (run id + attempt) of that
// commit. It computes everything from the view's `ci` section and never calls
// pg-connector.
type fixciRule struct{}

func (fixciRule) ID() string          { return "fixci.failing-on-head" }
func (fixciRule) Kind() workitem.Kind { return workitem.KindFixCI }
func (fixciRule) Evaluate(in decide.Input) decide.Result {
	v := in.View
	if v == nil || (v.Decorations.Relationship != "mine" && v.Decorations.Relationship != "co-owned") {
		return fixciNotMatched(nil)
	}
	head := v.Snapshot.HeadSHA
	if head == "" {
		return fixciNotMatched(nil)
	}
	runs := ciRuns(v)
	ctx := workitem.Context{HeadSHA: head}
	item, haveItem := in.Items.Find(workitem.KindFixCI, ctx)

	var failing []ciRun
	for _, r := range runs {
		if ciRunFailing(r) {
			failing = append(failing, r)
		}
	}
	if len(failing) > 0 {
		builds, checks := fixciFailing(failing)
		if !haveItem {
			return fixciCreate(in, head, failing, builds, checks)
		}
		return fixciExisting(item, head, builds, checks)
	}

	if haveItem && item.Open() && ciGreen(runs) {
		if item.Assignee != "" {
			// Claimed: left for the worker to close.
			return fixciNotMatched(map[string]any{"item": item.ID, "claimed": true})
		}
		return decide.Result{Actions: []action.Action{{
			Op: action.OpClose, Kind: string(workitem.KindFixCI), Target: fixciStr(item.ID),
			Fields: action.Fields{Description: "CI is green on head " + head},
			Facts:  map[string]any{"head_sha": head, "item": item.ID},
		}}}
	}
	return fixciNotMatched(nil)
}

func fixciNotMatched(facts map[string]any) decide.Result {
	return decide.Result{Skip: &action.Skip{Reason: action.ReasonNotMatched, Facts: facts}}
}

func fixciStr(s string) *string { return &s }

// fixciFailing returns the sorted unique build ids ("<run id>:<attempt>") and
// the sorted unique check names of the given (failing) runs.
func fixciFailing(failing []ciRun) (builds, checks []string) {
	b, c := map[string]bool{}, map[string]bool{}
	for _, r := range failing {
		b[r.buildID()] = true
		if r.Name != "" {
			c[r.Name] = true
		}
	}
	return fixciSortedKeys(b), fixciSortedKeys(c)
}

func fixciSortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// fixciSplit parses a comma-separated metadata list, dropping blanks.
func fixciSplit(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func fixciCreate(in decide.Input, head string, failing []ciRun, builds, checks []string) decide.Result {
	v := in.View
	snap := v.Snapshot
	c := workitem.ContractFor(workitem.KindFixCI)
	parent := action.AnchorParent
	if a, ok := in.Items.Anchor(); ok {
		parent = a.ID
	}
	key := workitem.DedupKey(workitem.EntityRefFrom(v), workitem.KindFixCI, workitem.Context{HeadSHA: head})
	return decide.Result{Actions: []action.Action{{
		Op:   action.OpCreate,
		Kind: string(workitem.KindFixCI),
		Fields: action.Fields{
			Title:       fmt.Sprintf("fix-ci: %s#%d", snap.Repo, snap.Number),
			IssueType:   c.IssueType,
			Description: fixciDescription(snap.Repo, snap.Number, head, failing),
			Parent:      parent,
			Labels:      c.Labels,
			Metadata: map[string]string{
				"repo":           snap.Repo,
				"pr_number":      strconv.Itoa(snap.Number),
				"branch":         snap.Branch,
				"head_sha":       head,
				"failing_checks": strings.Join(checks, ","),
				"failing_builds": strings.Join(builds, ","),
				"dedup_key":      key,
			},
		},
		Facts: map[string]any{"head_sha": head, "failing_builds": builds, "failing_checks": checks},
	}}}
}

// fixciExisting handles failing CI when an item for this head already exists.
func fixciExisting(item workitem.Item, head string, builds, checks []string) decide.Result {
	have := map[string]bool{}
	for _, b := range fixciSplit(item.Metadata["failing_builds"]) {
		have[b] = true
	}
	var added []string
	for _, b := range builds {
		if !have[b] {
			added = append(added, b)
		}
	}
	if len(added) == 0 {
		return decide.Result{Skip: &action.Skip{
			Reason: action.ReasonAlreadyHandled,
			Facts:  map[string]any{"item": item.ID, "head_sha": head, "failing_builds": builds},
		}}
	}

	allBuilds := map[string]bool{}
	for b := range have {
		allBuilds[b] = true
	}
	for _, b := range builds {
		allBuilds[b] = true
	}
	allChecks := map[string]bool{}
	for _, c := range fixciSplit(item.Metadata["failing_checks"]) {
		allChecks[c] = true
	}
	for _, c := range checks {
		allChecks[c] = true
	}
	facts := map[string]any{"head_sha": head, "item": item.ID, "added_builds": added}

	var acts []action.Action
	reopened := !item.Open()
	if reopened {
		acts = append(acts, action.Action{
			Op: action.OpReopen, Kind: string(workitem.KindFixCI), Target: fixciStr(item.ID), Facts: facts,
		})
	}
	acts = append(acts, action.Action{
		Op: action.OpUpdate, Kind: string(workitem.KindFixCI), Target: fixciStr(item.ID),
		Fields: action.Fields{Metadata: map[string]string{
			"failing_builds": strings.Join(fixciSortedKeys(allBuilds), ","),
			"failing_checks": strings.Join(fixciSortedKeys(allChecks), ","),
		}},
		Facts:         facts,
		RequiresPrior: reopened,
	})
	return decide.Result{Actions: acts}
}

// fixciDescription renders each failing check with a link to its run.
func fixciDescription(repo string, number int, head string, failing []ciRun) string {
	runs := append([]ciRun(nil), failing...)
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].Name != runs[j].Name {
			return runs[i].Name < runs[j].Name
		}
		return runs[i].buildID() < runs[j].buildID()
	})
	var b strings.Builder
	fmt.Fprintf(&b, "CI is failing on head %s of %s#%d. Failing checks:\n", head, repo, number)
	for _, r := range runs {
		fmt.Fprintf(&b, "- %s (run %s, attempt %d): %s", r.Name, r.ID, r.Attempt, r.Conclusion)
		if r.URL != "" {
			fmt.Fprintf(&b, " - log: %s", r.URL)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
