package servetest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file holds the Phase 3a fixtures of the counting fake gh: the batched
// detail document, the conversation pages, the files and commits pages, the
// pending review, the REST runs and jobs, and the write answers. Every name is
// a generic placeholder; the shapes are the ones GitHub answers (and the group
// fetchers parse).

// rateLimit is the rateLimit selection every GraphQL fixture carries; the shim
// fills @REMAINING@. The marker is quoted here so the document is valid JSON
// while it is built, then unquoted by finish.
var rateLimit = map[string]any{"cost": 1, "remaining": "@@REMAINING@@", "resetAt": "2999-01-01T00:00:00Z"}

// finish renders a fixture document, leaving @REMAINING@ for the shim to fill.
func finish(t testing.TB, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), `"@@REMAINING@@"`, "@REMAINING@")
}

// prKey is the shim's key of a per-PR fixture: owner, name and number with every
// character outside [A-Za-z0-9] written as an underscore.
func prKey(p PR) string {
	owner, name, _ := strings.Cut(p.Repo, "/")
	return sanitizeQuery(owner) + "__" + sanitizeQuery(name) + "__" + sanitizeQuery(fmt.Sprint(p.Number))
}

// repoPR wraps a pullRequest selection in the repository envelope.
func repoPR(t testing.TB, pr map[string]any) string { return repoPRDoc(t, pr) }

func repoPRDoc(t testing.TB, pr map[string]any) string {
	return finish(t, map[string]any{"data": map[string]any{
		"rateLimit":  rateLimit,
		"repository": map[string]any{"pullRequest": pr},
	}})
}

func page(total int, nodes any) map[string]any {
	return map[string]any{
		"totalCount": total,
		"pageInfo":   map[string]any{"hasNextPage": false, "endCursor": ""},
		"nodes":      nodes,
	}
}

// Remove deletes a control or fixture file.
func (s *Shim) Remove(name string) {
	s.t.Helper()
	if err := os.Remove(filepath.Join(s.Dir, name)); err != nil && !os.IsNotExist(err) {
		s.t.Fatal(err)
	}
}

// Fail makes every call of kind answer the HTTP status (and, with times > 0,
// only the next times calls).
func (s *Shim) Fail(kind string, status, times int) {
	s.t.Helper()
	if times > 0 {
		s.Set("fail."+kind, fmt.Sprintf("%d %d", status, times))
		return
	}
	s.Set("fail."+kind, fmt.Sprint(status))
}

// FailBatch makes every call of kind that carries more than one id answer the
// HTTP status.
func (s *Shim) FailBatch(kind string, status int) {
	s.t.Helper()
	s.Set("fail."+kind+".batch", fmt.Sprint(status))
}

// Block holds the next n calls of kind (the answer is decided when the call
// arrives, and sent when Release is called).
func (s *Shim) Block(kind string, n int) {
	s.t.Helper()
	s.Remove("release." + kind)
	s.Remove("held." + kind)
	s.Set("block."+kind, fmt.Sprint(n))
}

// Held counts the calls of kind that Block has caught and that are holding now
// (or were, until Release). A call that has merely been logged is not held yet:
// the fake gh classifies it, claims a Block slot and only then records it here,
// so a test that must act while a call is held waits on Held, not CountKind.
func (s *Shim) Held(kind string) int {
	s.t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.Dir, "held."+kind))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		s.t.Fatal(err)
	}
	return strings.Count(string(raw), "\n")
}

// Release lets the calls Block holds, and the ones that arrive later, go.
func (s *Shim) Release(kind string) {
	s.t.Helper()
	s.Remove("block." + kind)
	s.Set("release."+kind, "1")
}

// After makes the shim copy fixture src over dst once a call of kind has been
// answered (a write that changes what the next read sees).
func (s *Shim) After(kind, src, dst string) {
	s.t.Helper()
	path := filepath.Join(s.Dir, "after."+kind)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		s.t.Fatal(err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s %s\n", src, dst); err != nil {
		s.t.Fatal(err)
	}
}

// StdinCall is what a recorded write sent on its standard input.
type StdinCall struct {
	Kind    string
	Payload string
}

// Stdin returns the payload of every call that read its standard input, oldest
// first.
func (s *Shim) Stdin() []StdinCall {
	s.t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.Dir, "stdin.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		s.t.Fatal(err)
	}
	var out []StdinCall
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		kind, payload, _ := strings.Cut(line, "\t")
		out = append(out, StdinCall{Kind: kind, Payload: strings.ReplaceAll(payload, `\n`, "\n")})
	}
	return out
}

// CountStdin counts the recorded calls of one kind that read their standard
// input (the review write documents, whose argv does not tell them apart):
// "write_items", "update_body" or "create_review".
func (s *Shim) CountStdin(kind string) int {
	n := 0
	for _, c := range s.Stdin() {
		if c.Kind == kind {
			n++
		}
	}
	return n
}

// CountKindWith counts the recorded calls of one kind whose argv has an argument
// containing text (for example "number=21" for the per-PR GraphQL reads).
func (s *Shim) CountKindWith(kind, text string) int {
	n := 0
	for _, c := range s.Calls() {
		if c.Kind() != kind {
			continue
		}
		for _, a := range c.Args {
			if strings.Contains(a, text) {
				n++
				break
			}
		}
	}
	return n
}

// CountEndpoint counts the recorded calls whose argv has an argument containing
// the text (an endpoint, a path).
func (s *Shim) CountEndpoint(text string) int {
	n := 0
	for _, c := range s.Calls() {
		for _, a := range c.Args {
			if strings.Contains(a, text) {
				n++
				break
			}
		}
	}
	return n
}

// ---- detail ------------------------------------------------------------------

// Detail is the detail group's wire content of one PR.
type Detail struct {
	MergeStateStatus string
	Merged           bool
	Additions        int
	Deletions        int
	ChangedFiles     int
	// Reviewers are the logins of the requested reviewers.
	Reviewers []string
}

// SetDetail makes the batched detail document answer d for p.
func (s *Shim) SetDetail(p PR, d Detail) {
	s.t.Helper()
	if d.MergeStateStatus == "" {
		d.MergeStateStatus = "CLEAN"
	}
	nodes := []map[string]any{}
	for _, login := range d.Reviewers {
		nodes = append(nodes, map[string]any{"requestedReviewer": map[string]any{"login": login}})
	}
	s.Set("detail."+p.NodeID+".json", finish(s.t, map[string]any{
		"id": p.NodeID, "mergeStateStatus": d.MergeStateStatus, "merged": d.Merged,
		"additions": d.Additions, "deletions": d.Deletions, "changedFiles": d.ChangedFiles,
		"reviewRequests": map[string]any{"nodes": nodes},
	}))
}

// ---- conversation ---------------------------------------------------------------

// Comment is one issue comment; Review one review; Thread one review thread.
type Comment struct {
	ID, Author, Body string
}

// Review is one review of the conversation.
type Review struct {
	ID, Author, State, Body, Commit string
}

// Thread is one review thread with one comment.
type Thread struct {
	ID, CommentID, Author, Body, Path, ReviewID string
	Line                                        int
	Resolved                                    bool
}

// Conversation is the content the conversation group reads for one PR.
type Conversation struct {
	Comments []Comment
	Reviews  []Review
	Threads  []Thread
}

// SetConversation makes the three conversation pages answer c for p.
func (s *Shim) SetConversation(p PR, c Conversation) {
	s.t.Helper()
	key := prKey(p)
	const at = "2026-10-12T09:00:00Z"
	var threads []map[string]any
	for _, th := range c.Threads {
		comment := map[string]any{
			"id": th.CommentID, "author": map[string]any{"login": th.Author}, "authorAssociation": "MEMBER",
			"body": th.Body, "path": th.Path, "line": th.Line, "originalLine": th.Line,
			"createdAt": at, "updatedAt": at, "isMinimized": false, "minimizedReason": "",
			"originalCommit": map[string]any{"oid": "abc123"},
		}
		if th.ReviewID != "" {
			comment["pullRequestReview"] = map[string]any{"id": th.ReviewID}
		}
		threads = append(threads, map[string]any{
			"id": th.ID, "isResolved": th.Resolved, "isOutdated": false,
			"comments": page(1, []map[string]any{comment}),
		})
	}
	s.Set("threads."+key+".json", repoPR(s.t, map[string]any{"reviewThreads": page(len(threads), nonNil(threads))}))
	var comments []map[string]any
	for _, ic := range c.Comments {
		comments = append(comments, map[string]any{
			"id": ic.ID, "author": map[string]any{"login": ic.Author}, "authorAssociation": "MEMBER",
			"body": ic.Body, "createdAt": at, "updatedAt": at,
		})
	}
	s.Set("issue_comments."+key+".json", repoPR(s.t, map[string]any{"comments": page(len(comments), nonNil(comments))}))
	var reviews []map[string]any
	for _, r := range c.Reviews {
		node := map[string]any{
			"id": r.ID, "state": r.State, "body": r.Body, "createdAt": at, "submittedAt": at,
			"author": map[string]any{"login": r.Author}, "commit": nil,
		}
		if r.Commit != "" {
			node["commit"] = map[string]any{"oid": r.Commit}
		}
		reviews = append(reviews, node)
	}
	s.Set("reviews."+key+".json", repoPR(s.t, map[string]any{"reviews": page(len(reviews), nonNil(reviews))}))
}

func nonNil(v []map[string]any) []map[string]any {
	if v == nil {
		return []map[string]any{}
	}
	return v
}

// ---- files and commits ------------------------------------------------------------

// File is one changed file; CommitInfo one commit (Author may be empty: no
// linked account).
type File struct {
	Path                 string
	Additions, Deletions int
}

// CommitInfo is one commit of the PR.
type CommitInfo struct {
	SHA, Headline, Author string
}

// SetFiles makes the files page answer files for p.
func (s *Shim) SetFiles(p PR, files ...File) {
	s.t.Helper()
	nodes := []map[string]any{}
	for _, f := range files {
		nodes = append(nodes, map[string]any{"path": f.Path, "additions": f.Additions, "deletions": f.Deletions})
	}
	s.Set("files."+prKey(p)+".json", repoPR(s.t, map[string]any{"files": page(len(nodes), nodes)}))
}

// SetCommits makes the commits page answer commits for p.
func (s *Shim) SetCommits(p PR, commits ...CommitInfo) {
	s.t.Helper()
	nodes := []map[string]any{}
	for _, c := range commits {
		var author any
		if c.Author != "" {
			author = map[string]any{"login": c.Author}
		}
		nodes = append(nodes, map[string]any{"commit": map[string]any{
			"oid": c.SHA, "messageHeadline": c.Headline,
			"authors": map[string]any{"nodes": []map[string]any{{"user": author}}},
		}})
	}
	s.Set("commits."+prKey(p)+".json", repoPR(s.t, map[string]any{"commits": page(len(nodes), nodes)}))
}

// ---- pending review -------------------------------------------------------------

// PendingComment is one inline comment of a pending review.
type PendingComment struct {
	ID, Path, Body, Commit string
	Line                   int
}

// PendingReview is one pending review of the viewer.
type PendingReview struct {
	ID, URL, Commit, Body string
	DatabaseID            int64
	Comments              []PendingComment
}

// PendingState is what the pending lookup answers for a PR.
type PendingState struct {
	// Viewer is the login the lookup reports (and the author of every pending
	// review); empty means DefaultViewer.
	Viewer string
	// Head is the PR's head sha.
	Head    string
	Reviews []PendingReview
}

// PendingJSON renders the pending lookup answer, to be written under the file
// name PendingFile gives (so a write can switch the state a read sees).
func (s *Shim) PendingJSON(ps PendingState) string {
	s.t.Helper()
	viewer := ps.Viewer
	if viewer == "" {
		viewer = DefaultViewer
	}
	pending := []map[string]any{}
	threads := []map[string]any{}
	for _, r := range ps.Reviews {
		comments := []map[string]any{}
		for _, c := range r.Comments {
			comments = append(comments, map[string]any{
				"id": c.ID, "databaseId": 1, "path": c.Path, "line": c.Line, "originalLine": c.Line,
				"body": c.Body, "originalCommit": map[string]any{"oid": c.Commit},
			})
			threads = append(threads, map[string]any{
				"id":       "PRRT_" + c.ID,
				"comments": map[string]any{"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""}, "nodes": []map[string]any{{"id": c.ID}}},
			})
		}
		node := map[string]any{
			"id": r.ID, "databaseId": r.DatabaseID, "url": r.URL, "state": "PENDING",
			"author": map[string]any{"login": viewer}, "commit": nil, "body": r.Body,
			"comments": page(len(comments), comments),
		}
		if r.Commit != "" {
			node["commit"] = map[string]any{"oid": r.Commit}
		}
		pending = append(pending, node)
	}
	return finish(s.t, map[string]any{"data": map[string]any{
		"rateLimit": rateLimit,
		"viewer":    map[string]any{"login": viewer},
		"repository": map[string]any{"pullRequest": map[string]any{
			"headRefOid": ps.Head,
			"pending":    page(len(pending), pending),
			"submitted":  page(0, []map[string]any{}),
			"reviewThreads": map[string]any{
				"totalCount": len(threads),
				"pageInfo":   map[string]any{"hasNextPage": false, "endCursor": ""},
				"nodes":      threads,
			},
		}},
	}})
}

// SetPending makes the pending lookup answer ps for p.
func (s *Shim) SetPending(p PR, ps PendingState) {
	s.t.Helper()
	s.Set(PendingFile(p), s.PendingJSON(ps))
}

// PendingFile is the fixture file name of p's pending lookup.
func PendingFile(p PR) string { return "pending." + prKey(p) + ".json" }

// ---- runs and jobs ------------------------------------------------------------------

// Run is one workflow run of the REST runs answer.
type Run struct {
	ID         int
	Name       string
	Status     string
	Conclusion string
	Head       string
	Attempt    int
}

// SetRuns makes the runs list of repo's branch answer runs (a run with no
// attempt number is attempt 1).
func (s *Shim) SetRuns(repo, branch string, runs ...Run) {
	s.t.Helper()
	s.Set(RunsFile(repo, branch), RunsJSON(s.t, repo, branch, runs...))
}

// RunsFile is the fixture file name of the runs list of repo's branch.
func RunsFile(repo, branch string) string {
	owner, name, _ := strings.Cut(repo, "/")
	return "runs." + sanitizeQuery(owner) + "__" + sanitizeQuery(name) + "__" + sanitizeQuery(branch) + ".json"
}

// RunsJSON renders the REST runs answer.
func RunsJSON(t testing.TB, repo, branch string, runs ...Run) string {
	t.Helper()
	nodes := []map[string]any{}
	for _, r := range runs {
		attempt := r.Attempt
		if attempt == 0 {
			attempt = 1
		}
		var conclusion any
		if r.Conclusion != "" {
			conclusion = r.Conclusion
		}
		nodes = append(nodes, map[string]any{
			"id": r.ID, "name": r.Name, "head_branch": branch, "head_sha": r.Head,
			"status": r.Status, "conclusion": conclusion, "run_attempt": attempt,
			"html_url": fmt.Sprintf("https://%s/%s/actions/runs/%d", Host, repo, r.ID),
		})
	}
	return finish(t, map[string]any{"total_count": len(nodes), "workflow_runs": nodes})
}

// Job is one job of a run attempt.
type Job struct {
	ID         int
	Name       string
	Conclusion string
}

// SetJobs makes the jobs list of the run answer jobs.
func (s *Shim) SetJobs(runID int, jobs ...Job) {
	s.t.Helper()
	nodes := []map[string]any{}
	for _, j := range jobs {
		nodes = append(nodes, map[string]any{
			"id": j.ID, "run_id": runID, "name": j.Name, "status": "completed", "conclusion": j.Conclusion,
			"html_url": fmt.Sprintf("https://%s/jobs/%d", Host, j.ID),
		})
	}
	s.Set(fmt.Sprintf("jobs.%d.json", runID), finish(s.t, map[string]any{"total_count": len(nodes), "jobs": nodes}))
}
