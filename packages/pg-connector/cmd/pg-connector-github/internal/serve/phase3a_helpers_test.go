package serve_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The Phase 3a tests drive the real daemon, as the Phase 2 ones do, with the
// counting fake gh answering the group documents (internal/servetest/fixtures.go).

// sha40 is a full 40-character commit sha made of one hex digit.
func sha40(c string) string { return strings.Repeat(c, 40) }

// Fixture pull requests of the Phase 3a tests. prW has full-length shas (the
// review write demands them) and a branch of its own.
var prW = servetest.PR{NodeID: "PR_node_w", Repo: "example-org/widget", Number: 21, Head: sha40("a"), Branch: "feature-w"}

func req(op string, args map[string]any) scriptout.Request {
	r := scriptout.Request{Op: op}
	if args != nil {
		raw, err := json.Marshal(args)
		if err != nil {
			panic(err)
		}
		r.Args = raw
	}
	return r
}

// callOK sends the request and requires a successful answer.
func callOK(t *testing.T, d *servetest.Daemon, instance, op string, args map[string]any) json.RawMessage {
	t.Helper()
	resp := d.MustCall(t, instance, req(op, args), 30*time.Second)
	if resp.Error != nil {
		t.Fatalf("%s %s: %s: %s", instance, op, resp.Error.Code, resp.Error.Message)
	}
	return resp.Result
}

// callErr sends the request and returns its error answer (nil on success).
func callErr(t *testing.T, d *servetest.Daemon, instance, op string, args map[string]any, deadline time.Duration) *scriptout.Error {
	t.Helper()
	return d.MustCall(t, instance, req(op, args), deadline).Error
}

// decode unmarshals a result into T.
func decode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

// calls counts the origin calls made since mark.
func originSince(d *servetest.Daemon, mark int) []servetest.Call {
	all := d.Shim.OriginCalls()
	if mark > len(all) {
		return nil
	}
	return all[mark:]
}

// kinds counts the calls by kind.
func kinds(calls []servetest.Call) map[string]int {
	out := map[string]int{}
	for _, c := range calls {
		out[c.Kind()]++
	}
	return out
}

// workKinds are the origin calls that are the daemon's work: every kind but the
// viewer lookup and the token request, which a daemon with a short
// viewer_interval repeats forever.
var workKinds = []string{
	"search", "nodes", "detail", "threads", "issue_comments", "reviews", "files", "commits", "pending",
	"runs", "jobs", "rerun", "create_review", "search_prs", "run_view", "other",
}

// quiet waits until the daemon has made no call of its work for a second, and
// returns how many origin calls the shim has recorded in all (the index a later
// originSince starts from). Silence alone does not say the daemon is done (see
// awaitFirstSightingFetches); a caller that needs the first-sighting fetches
// stored waits for them first, as settle does.
func quiet(t *testing.T, d *servetest.Daemon) int {
	t.Helper()
	d.Shim.WaitQuiet(t, time.Second, 30*time.Second, workKinds...)
	return len(d.Shim.OriginCalls())
}

// settle lists the two configured queries (which fills the summary group of the
// three fixture PRs and books the first-sighting detail, conversation and runs
// fetches), waits until every first-sighting group of the three fixture PRs is
// stored and then until the daemon has made every call of its work it is going
// to make. It returns the number of origin calls recorded.
func settle(t *testing.T, d *servetest.Daemon) int {
	t.Helper()
	for _, q := range []string{servetest.QueryMine, servetest.QueryTeam} {
		mustList(t, d, q, nil)
	}
	awaitFirstSightingFetches(t, d, 3)
	return quiet(t, d)
}

// seed3a is seed plus Phase 3a fixtures for prA: a PR with conversation, files,
// commits, an empty pending review and two runs, one failed.
func seed3a(d *servetest.Daemon) {
	seed(d)
	d.Shim.SetDetail(prA, servetest.Detail{MergeStateStatus: "BLOCKED", Additions: 12, Deletions: 3, ChangedFiles: 2, Reviewers: []string{"teammate"}})
	d.Shim.SetConversation(prA, servetest.Conversation{
		Comments: []servetest.Comment{{ID: "IC_1", Author: "teammate", Body: "first note"}},
		Reviews:  []servetest.Review{{ID: "PRR_1", Author: "review-bot", State: "APPROVED", Body: "looks fine", Commit: "head-3"}},
		Threads: []servetest.Thread{{
			ID: "PRRT_1", CommentID: "PRRC_1", Author: "teammate", Body: "please rename", Path: "a.go", Line: 4, ReviewID: "PRR_1",
		}},
	})
	d.Shim.SetFiles(prA,
		servetest.File{Path: "a.go", Additions: 10, Deletions: 2},
		servetest.File{Path: "docs/b.md", Additions: 2, Deletions: 1})
	d.Shim.SetCommits(prA,
		servetest.CommitInfo{SHA: sha40("1"), Headline: "Add the widget", Author: "phillipgreenii"},
		servetest.CommitInfo{SHA: sha40("2"), Headline: "Fix the widget"})
	d.Shim.SetPending(prA, servetest.PendingState{Head: "head-3"})
	d.Shim.SetRuns(prA.Repo, "feature-3",
		servetest.Run{ID: 3001, Name: "ci", Status: "completed", Conclusion: "failure", Head: "head-3"},
		servetest.Run{ID: 3002, Name: "lint", Status: "completed", Conclusion: "success", Head: "head-3"})
	d.Shim.SetJobs(3001,
		servetest.Job{ID: 70010, Name: "changes", Conclusion: "success"},
		servetest.Job{ID: 70011, Name: "build", Conclusion: "failure"})
}

// newDaemon3a is a daemon with the Phase 3a fixtures of prA, not yet started.
func newDaemon3a(t *testing.T) *servetest.Daemon {
	t.Helper()
	return servetest.NewDaemon(t, servetest.Options{Before: seed3a})
}

// stopCode stops the daemon with SIGTERM and returns its exit code.
func stopCode(t *testing.T, d *servetest.Daemon) int {
	t.Helper()
	return servetest.StopDaemon(t, d)
}
