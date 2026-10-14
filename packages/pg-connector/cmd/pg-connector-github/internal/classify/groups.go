package classify

import (
	"bytes"
	"fmt"
	"slices"
	"sort"
	"strconv"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/conversation"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/detail"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/prfacts"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/runs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// Group classifies one non-summary group from its stored content bytes, before
// and after.
//
// An empty or nil prev is the group's first fetch, which is that group's
// baseline: the zero Result, even when the entity's other groups have content.
// The pending group is never classified. The summary group is classified by
// Summary, which needs the persister's verdict, so it is an unknown name here,
// as is any name outside the closed set; content that does not decode is an
// error.
func Group(group string, prev, next []byte, bodyCap int) (Result, error) {
	var fn func(prev, next []byte, bodyCap int) (Result, error)
	switch group {
	case groups.Pending:
		return Result{}, nil
	case groups.Detail:
		fn = classifyDetail
	case groups.Conversation:
		fn = classifyConversation
	case groups.Files:
		fn = classifyFiles
	case groups.Commits:
		fn = classifyCommits
	case groups.Runs:
		fn = classifyRuns
	default:
		return Result{}, fmt.Errorf("classify: group %q is not classified here (use Summary for the summary group)", group)
	}
	if len(bytes.TrimSpace(prev)) == 0 {
		return Result{}, nil
	}
	res, err := fn(prev, next, bodyCap)
	if err != nil {
		return Result{}, fmt.Errorf("classify: group %s: %w", group, err)
	}
	return res, nil
}

func classifyDetail(prev, next []byte, _ int) (Result, error) {
	p, err := detail.Decode(prev)
	if err != nil {
		return Result{}, err
	}
	n, err := detail.Decode(next)
	if err != nil {
		return Result{}, err
	}
	var b builder
	if diff(&b, "merged", p.Merged, n.Merged) && n.Merged {
		b.kind(KindMerged)
	}
	diffSet(&b, "review_requests", p.ReviewRequests, n.ReviewRequests)
	diff(&b, "additions", p.Additions, n.Additions)
	diff(&b, "deletions", p.Deletions, n.Deletions)
	diff(&b, "changed_files", p.ChangedFiles, n.ChangedFiles)
	return b.result(), nil
}

// commentView is what the classifier reads of one comment, at the PR level or
// inside a review.
type commentView struct {
	body     string
	resolved bool
}

// commentsOf flattens a conversation's comments, top-level and inline, by id.
// The count is the number of comments, ids that repeat included.
func commentsOf(c conversation.Content) (map[string]commentView, int) {
	out := map[string]commentView{}
	count := 0
	add := func(cs []schema.PRComment) {
		for _, x := range cs {
			out[x.ID] = commentView{body: x.Body, resolved: x.Resolved}
			count++
		}
	}
	add(c.Comments)
	for _, r := range c.Reviews {
		add(r.Comments)
	}
	return out, count
}

type reviewView struct{ state, body string }

func reviewsOf(c conversation.Content) map[string]reviewView {
	out := map[string]reviewView{}
	for _, r := range c.Reviews {
		out[r.ID] = reviewView{state: r.State, body: r.Body}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// classifyConversation reads the feedback and review signals (a comment's
// (id, resolved) or count moving, a review's (id, state) or count moving) and
// records what changed: per changed comment its id with the capped before and
// after body and a hash of the full text, a flipped resolved flag, a review's
// state and body. It never records the whole conversation.
func classifyConversation(prev, next []byte, bodyCap int) (Result, error) {
	p, err := conversation.Decode(prev)
	if err != nil {
		return Result{}, err
	}
	n, err := conversation.Decode(next)
	if err != nil {
		return Result{}, err
	}
	var b builder

	pc, pcount := commentsOf(p)
	nc, ncount := commentsOf(n)
	feedback := pcount != ncount
	for _, id := range sortedUnion(pc, nc) {
		x, inP := pc[id]
		y, inN := nc[id]
		switch {
		case !inP || !inN:
			feedback = true
			b.field("comment:"+id, commentValue(x, inP, bodyCap), commentValue(y, inN, bodyCap))
		default:
			diffBody(&b, "comment:"+id, x.body, y.body, bodyCap)
			if diff(&b, "comment_resolved:"+id, x.resolved, y.resolved) {
				feedback = true
			}
		}
	}
	if feedback {
		b.kind(KindFeedbackChanged)
	}

	pr, nr := reviewsOf(p), reviewsOf(n)
	review := len(p.Reviews) != len(n.Reviews)
	for _, id := range sortedUnion(pr, nr) {
		x, inP := pr[id]
		y, inN := nr[id]
		if !inP || !inN || x.state != y.state {
			review = true
			b.field("review:"+id, optString(x.state, inP), optString(y.state, inN))
		}
		if inP && inN {
			diffBody(&b, "review_body:"+id, x.body, y.body, bodyCap)
		}
	}
	if review {
		b.kind(KindReviewChanged)
	}
	return b.result(), nil
}

func sortedUnion[V any](a, b map[string]V) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	return sortedKeys(seen)
}

// commentValue is the before or after of a comment field; a side where the
// comment does not exist is empty.
func commentValue(c commentView, present bool, bodyCap int) []byte {
	if !present {
		return nil
	}
	return bodyValue(c.body, bodyCap)
}

func optString(s string, present bool) []byte {
	if !present {
		return nil
	}
	return jsonOf(s)
}

// filesValue is the bounded form of a PR's changed files: counts, totals and a
// hash of the whole list, never the list. A PR with thousands of files records
// the same few numbers.
func filesValue(fs []schema.PRFile) (value []byte, hash string) {
	adds, dels := 0, 0
	for _, f := range fs {
		adds += f.Additions
		dels += f.Deletions
	}
	whole := jsonOf(fs)
	hash = textHash(string(whole))
	return jsonOf(struct {
		Count     int    `json:"count"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
		Hash      string `json:"hash"`
	}{len(fs), adds, dels, hash}), hash
}

func classifyFiles(prev, next []byte, _ int) (Result, error) {
	p, err := prfacts.DecodeFiles(prev)
	if err != nil {
		return Result{}, err
	}
	n, err := prfacts.DecodeFiles(next)
	if err != nil {
		return Result{}, err
	}
	pv, ph := filesValue(p.Files)
	nv, nh := filesValue(n.Files)
	var b builder
	if ph != nh {
		b.field("files", pv, nv)
	}
	return b.result(), nil
}

// commitsValue is the bounded form of a PR's commits: the count, the last
// commit (the head) and a hash of the whole list.
func commitsValue(cs []schema.PRCommit) (value []byte, hash string) {
	last := ""
	if len(cs) > 0 {
		last = cs[len(cs)-1].SHA
	}
	hash = textHash(string(jsonOf(cs)))
	return jsonOf(struct {
		Count   int    `json:"count"`
		LastSHA string `json:"last_sha"`
		Hash    string `json:"hash"`
	}{len(cs), last, hash}), hash
}

func classifyCommits(prev, next []byte, _ int) (Result, error) {
	p, err := prfacts.DecodeCommits(prev)
	if err != nil {
		return Result{}, err
	}
	n, err := prfacts.DecodeCommits(next)
	if err != nil {
		return Result{}, err
	}
	pv, ph := commitsValue(p.Commits)
	nv, nh := commitsValue(n.Commits)
	var b builder
	if ph != nh {
		b.field("commits", pv, nv)
	}
	return b.result(), nil
}

// runView and jobView are the fields of a run and of a job the spec's
// ci_changed rule reads (the ones runs.Fingerprint hashes).
type runView struct {
	Attempt    int    `json:"attempt"`
	Workflow   string `json:"workflow"`
	HeadSHA    string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

type jobView struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// runState is one run with its jobs, keyed by a name unique within the list.
type runState struct {
	view runView
	jobs map[string]jobView
}

// uniqueKey returns base, or base#n for its n-th repeat, so two runs that share
// an id (or two jobs that share a name) stay two fields.
func uniqueKey(seen map[string]int, base string) string {
	seen[base]++
	if seen[base] == 1 {
		return base
	}
	return base + "#" + strconv.Itoa(seen[base])
}

func runStates(c runs.Content) map[string]runState {
	out := map[string]runState{}
	ids := map[string]int{}
	for _, r := range c.Runs {
		key := uniqueKey(ids, r.ID)
		st := runState{
			view: runView{r.Attempt, r.Name, r.HeadSHA, r.Status, r.Conclusion},
			jobs: map[string]jobView{},
		}
		js := slices.Clone(r.Jobs)
		slices.SortStableFunc(js, func(a, b schema.CIJob) int {
			if a.Name != b.Name {
				return compareStrings(a.Name, b.Name)
			}
			if a.Status != b.Status {
				return compareStrings(a.Status, b.Status)
			}
			return compareStrings(a.Conclusion, b.Conclusion)
		})
		names := map[string]int{}
		for _, j := range js {
			st.jobs[uniqueKey(names, j.Name)] = jobView{j.Status, j.Conclusion}
		}
		out[key] = st
	}
	return out
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// classifyRuns emits ci_changed when a run or job field the fingerprint reads
// moved: a run field per changed, added or removed run (run:<id>) and a job
// field per changed, added or removed job (job:<run id>/<job name>). Timestamps,
// log URLs and the answer annotations are not signals, and a run list that is
// only reordered is not a change.
func classifyRuns(prev, next []byte, _ int) (Result, error) {
	p, err := runs.Decode(prev)
	if err != nil {
		return Result{}, err
	}
	n, err := runs.Decode(next)
	if err != nil {
		return Result{}, err
	}
	ps, ns := runStates(p), runStates(n)
	var b builder
	for _, id := range sortedUnion(ps, ns) {
		x, inP := ps[id]
		y, inN := ns[id]
		if !inP || !inN || x.view != y.view {
			b.field("run:"+id, runValue(x.view, inP), runValue(y.view, inN))
		}
		for _, name := range sortedUnion(x.jobs, y.jobs) {
			xj, jP := x.jobs[name]
			yj, jN := y.jobs[name]
			if !jP || !jN || xj != yj {
				b.field("job:"+id+"/"+name, jobValue(xj, jP), jobValue(yj, jN))
			}
		}
	}
	res := b.result()
	if len(res.Fields) > 0 {
		res.Kinds = []string{KindCIChanged}
	}
	return res, nil
}

func runValue(v runView, present bool) []byte {
	if !present {
		return nil
	}
	return jsonOf(v)
}

func jobValue(v jobView, present bool) []byte {
	if !present {
		return nil
	}
	return jsonOf(v)
}
