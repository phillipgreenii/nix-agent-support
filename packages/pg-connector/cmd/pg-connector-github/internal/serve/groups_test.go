package serve_test

import (
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/explain"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// The Phase 3a reads end to end: every read op answers from the daemon, an
// identical second call makes no origin call, the answers carry the read
// annotations, and fresh: true costs one fetch per needed group.

func groupNames(infos []schema.GroupInfo) []string {
	var out []string
	for _, g := range infos {
		out = append(out, g.Group)
	}
	slices.Sort(out)
	return out
}

func TestReads_EveryOpAnswersFromTheDaemonAndASecondCallMakesNoOriginCall(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed3a})
	d.Start(t)
	settle(t, d)

	// show: the summary, detail and conversation groups were filled by the
	// daemon's own first-sighting population, so the read costs nothing.
	mark := len(d.Shim.OriginCalls())
	show := decode[schema.PR](t, callOK(t, d, "pr", "show", map[string]any{"id": prA.Ref()}))
	if got := originSince(d, mark); len(got) != 0 {
		t.Fatalf("show inside freshness made %d origin calls: %v", len(got), kinds(got))
	}
	if show.Title != "Example change 3" || show.MergeStateStatus != "BLOCKED" || show.ChangedFiles != 2 || show.Additions != 12 {
		t.Errorf("show must carry the summary and detail groups: %+v", show)
	}
	if !slices.Equal(show.ReviewRequests, []string{"teammate"}) {
		t.Errorf("review_requests = %v", show.ReviewRequests)
	}
	if len(show.Comments) < 1 || show.Comments[0].Body != "first note" || len(show.Reviews) != 1 || show.Reviews[0].ID != "PRR_1" {
		t.Errorf("show must carry the conversation group: comments %+v reviews %+v", show.Comments, show.Reviews)
	}
	if show.ServedFrom != "cache" || show.Stale || show.AgeSeconds == nil {
		t.Errorf("annotations: served_from %q stale %v age %v", show.ServedFrom, show.Stale, show.AgeSeconds)
	}
	if got, want := groupNames(show.Groups), []string{"conversation", "detail", "summary"}; !slices.Equal(got, want) {
		t.Errorf("groups[] = %v, want %v", got, want)
	}
	for _, g := range show.Groups {
		if g.FetchedAt == "" || g.FreshUntil == "" {
			t.Errorf("group %s lacks fetched_at or fresh_until: %+v", g.Group, g)
		}
	}
	// A second identical call is the same answer from the store.
	callOK(t, d, "pr", "show", map[string]any{"id": prA.Ref()})
	if got := originSince(d, mark); len(got) != 0 {
		t.Errorf("a second show made %d origin calls", len(got))
	}

	// files and commits are read-through groups: the first read costs one
	// fetch each (the summary is fresh), the second none.
	files := decode[schema.PRFilesResult](t, callOK(t, d, "pr", "files", map[string]any{"id": prA.Ref()}))
	// The files, commits and runs answers have the old backends' shape and
	// content (here the content of the shim's pages, field by field), plus the
	// read annotations.
	wantFiles := []schema.PRFile{{Path: "a.go", Additions: 10, Deletions: 2}, {Path: "docs/b.md", Additions: 2, Deletions: 1}}
	if files.ID != prA.Ref() || !slices.Equal(files.Files, wantFiles) || files.ServedFrom != "origin" {
		t.Errorf("files = %+v, want %+v", files, wantFiles)
	}
	commits := decode[schema.PRCommitsResult](t, callOK(t, d, "pr", "commits", map[string]any{"id": prA.Ref()}))
	wantCommits := []schema.PRCommit{
		{SHA: sha40("1"), Author: "phillipgreenii", Message: "Add the widget"},
		{SHA: sha40("2"), Author: "", Message: "Fix the widget"},
	}
	if commits.ID != prA.Ref() || !slices.Equal(commits.Commits, wantCommits) || commits.ServedFrom != "origin" {
		t.Errorf("commits = %+v, want %+v", commits, wantCommits)
	}
	pending := decode[pr.PendingReviewResult](t, callOK(t, d, "pr", "review_pending", map[string]any{"id": prA.Ref()}))
	if pending.Pending || pending.HeadSHA != "head-3" || pending.ServedFrom != "origin" {
		t.Errorf("review_pending = %+v", pending)
	}
	got := kinds(originSince(d, mark))
	if got["files"] != 1 || got["commits"] != 1 || got["pending"] != 1 || len(originSince(d, mark)) != 3 {
		t.Errorf("the first reads of files, commits and review_pending made calls %v, want one each", got)
	}

	mark = len(d.Shim.OriginCalls())
	files = decode[schema.PRFilesResult](t, callOK(t, d, "pr", "files", map[string]any{"id": prA.Ref()}))
	callOK(t, d, "pr", "commits", map[string]any{"id": prA.Ref()})
	callOK(t, d, "pr", "review_pending", map[string]any{"id": prA.Ref()})
	if got := originSince(d, mark); len(got) != 0 {
		t.Errorf("the second reads made %d origin calls: %v", len(got), kinds(got))
	}
	if files.ServedFrom != "cache" || files.Stale == nil || *files.Stale || files.AgeSeconds == nil {
		t.Errorf("files annotations on the second read: %+v", files)
	}
	if got, want := groupNames(files.Groups), []string{"files"}; !slices.Equal(got, want) {
		t.Errorf("files groups[] = %v, want %v", got, want)
	}

	// ci list_runs: the runs group was filled by the first-sighting population,
	// jobs included; the answer is the newest-first run list of the PR.
	runs := decode[[]schema.CIRun](t, callOK(t, d, "ci", "list_runs", map[string]any{"pr_id": prA.Ref()}))
	if got := originSince(d, mark); len(got) != 0 {
		t.Errorf("list_runs inside freshness made %d origin calls: %v", len(got), kinds(got))
	}
	if len(runs) != 2 {
		t.Fatalf("list_runs = %+v", runs)
	}
	byID := map[string]schema.CIRun{}
	for _, r := range runs {
		byID[r.ID] = r
	}
	failed := byID["3001"]
	if failed.Conclusion != "failure" || failed.HeadSHA != "head-3" || len(failed.Jobs) != 2 || failed.ServedFrom != "cache" {
		t.Errorf("the failed run must carry its jobs and the annotations: %+v", failed)
	}
	wantURL := "https://" + servetest.Host + "/example-org/widget/actions/runs/3001"
	if failed.Name != "ci" || failed.Status != "completed" || failed.URL != wantURL || failed.Provider != "github-actions" ||
		failed.Repo != prA.Repo || failed.PRID != prA.Ref() || failed.Attempt != 1 {
		t.Errorf("the failed run's fields = %+v", failed)
	}
	if failed.Jobs[0].Name != "changes" || failed.Jobs[0].Conclusion != "success" || failed.Jobs[1].Name != "build" || failed.Jobs[1].Conclusion != "failure" {
		t.Errorf("the failed run's jobs = %+v", failed.Jobs)
	}
	if len(byID["3002"].Jobs) != 0 {
		t.Errorf("a successful run has no jobs fetched: %+v", byID["3002"])
	}
}

func TestReads_FreshTrueCostsOneFetchPerNeededGroup(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed3a})
	d.Start(t)
	settle(t, d)
	// Fill files so every group the reads below need is stored and fresh.
	callOK(t, d, "pr", "files", map[string]any{"id": prA.Ref()})

	// show needs the summary, detail and conversation groups: fresh: true is one
	// summary fetch, one detail fetch and the three conversation pages.
	mark := len(d.Shim.OriginCalls())
	show := decode[schema.PR](t, callOK(t, d, "pr", "show", map[string]any{"id": prA.Ref(), "fresh": true}))
	got := kinds(originSince(d, mark))
	want := map[string]int{"nodes": 1, "detail": 1, "threads": 1, "issue_comments": 1, "reviews": 1}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("show fresh: %d %s calls, want %d (all: %v)", got[k], k, n, got)
		}
	}
	if len(originSince(d, mark)) != 5 {
		t.Errorf("show fresh made %d calls, want 5: %v", len(originSince(d, mark)), got)
	}
	if show.ServedFrom != "origin" {
		t.Errorf("served_from = %q, want origin", show.ServedFrom)
	}

	// files needs the summary and files: two fetches.
	mark = len(d.Shim.OriginCalls())
	callOK(t, d, "pr", "files", map[string]any{"id": prA.Ref(), "fresh": true})
	got = kinds(originSince(d, mark))
	if got["nodes"] != 1 || got["files"] != 1 || len(originSince(d, mark)) != 2 {
		t.Errorf("files fresh: calls %v, want one summary fetch and one files fetch", got)
	}

	// list_runs needs the summary and runs: one summary fetch and one REST runs
	// request (the failed run's jobs are cached in the job table).
	mark = len(d.Shim.OriginCalls())
	callOK(t, d, "ci", "list_runs", map[string]any{"pr_id": prA.Ref(), "fresh": true})
	got = kinds(originSince(d, mark))
	if got["nodes"] != 1 || got["runs"] != 1 || len(originSince(d, mark)) != 2 {
		t.Errorf("list_runs fresh: calls %v, want one summary fetch and one runs request", got)
	}
}

// ---- explain: the extended answer ----------------------------------------------

func TestExplain_ShowsTheNewGroupsWithStatusStampsAndTheRunsVerdicts(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed3a})
	d.Start(t)
	settle(t, d)
	callOK(t, d, "pr", "files", map[string]any{"id": prA.Ref()})

	res := decode[explain.Result](t, callOK(t, d, "pr", "explain", map[string]any{"id": prA.Ref()}))
	got := map[string]explain.Group{}
	for _, g := range res.Groups {
		got[g.Group] = g
	}
	for _, name := range []string{"summary", "detail", "conversation", "files"} {
		g, ok := got[name]
		if !ok || g.FetchedAt == nil || g.Status != "fresh" {
			t.Errorf("explain %s = %+v, want a fresh stored group", name, g)
		}
	}
	if g := got["files"]; g.StampsMatch == nil || !*g.StampsMatch || g.FetchedForHead != "head-3" {
		t.Errorf("explain files must show matching stamps: %+v", g)
	}
	if _, ok := got["runs"]; ok {
		t.Errorf("the pr view of explain has no runs group: %+v", got["runs"])
	}

	ci := decode[explain.Result](t, callOK(t, d, "ci", "explain", map[string]any{"id": prA.Ref()}))
	var runs explain.Group
	for _, g := range ci.Groups {
		if g.Group == "runs" {
			runs = g
		}
	}
	if runs.Status != "fresh" || runs.Failing == nil || !*runs.Failing || runs.Active == nil || *runs.Active || runs.CachedJobs == nil || *runs.CachedJobs != 1 {
		t.Errorf("explain runs = %+v, want fresh, failing, not active, one cached job row", runs)
	}
}

// ---- job results -----------------------------------------------------------------

// A completed attempt's jobs are fetched once; a failed job fetch keeps the last
// known jobs and records last_error; a run on an older head never makes the PR
// active; and a job-level change behind an unchanged rollup is detected inside
// ci_failed_ttl.
func TestJobResults_AreFetchedOnceAndAFailedFetchKeepsWhatIsKnown(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{
		Before: seed3a,
		// The failing PR's runs are re-read every second.
		Config: map[string]any{"ci_failed_ttl": "1s", "ci_active_ttl": "1s", "ci_max_age": "1h"},
	})
	d.Start(t)
	settle(t, d)

	jobsCalls := func() int { return d.Shim.CountKind("jobs") }
	if got := jobsCalls(); got != 1 {
		t.Fatalf("the failed run's jobs were fetched %d times, want once (the passing run has none)", got)
	}
	store := d.OpenStore(t)
	jobRows := func() int {
		var n int
		if err := store.QueryRow(`SELECT COUNT(*) FROM job`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// The jobs call is logged when it starts; the row is stored when the runs
	// fetch that made it commits, which a loaded host can delay past any quiet
	// period.
	until(t, 30*time.Second, "the failed run's jobs to be stored", func() bool { return jobRows() == 1 })
	if got := jobRows(); got != 1 {
		t.Fatalf("job rows = %d, want 1", got)
	}

	// The runs group is refreshed again and again (ci_failed_ttl 1 s) and the
	// attempt's jobs are never fetched again.
	runsBefore := d.Shim.CountKind("runs")
	until(t, 20*time.Second, "a second background runs refresh", func() bool { return d.Shim.CountKind("runs") > runsBefore })
	if got := jobsCalls(); got != 1 {
		t.Errorf("a completed attempt's jobs were fetched %d times, want once", got)
	}

	// A run on an older head never makes the PR active: an in-progress run of
	// the previous head sits beside the current head's finished runs.
	d.Shim.SetRuns(prA.Repo, "feature-3",
		servetest.Run{ID: 3001, Name: "ci", Status: "completed", Conclusion: "failure", Head: "head-3"},
		servetest.Run{ID: 3002, Name: "lint", Status: "completed", Conclusion: "success", Head: "head-3"},
		servetest.Run{ID: 2999, Name: "preview", Status: "in_progress", Head: "head-2"})
	ci := func() explain.Group {
		res := decode[explain.Result](t, callOK(t, d, "ci", "explain", map[string]any{"id": prA.Ref()}))
		for _, g := range res.Groups {
			if g.Group == "runs" {
				return g
			}
		}
		t.Fatal("no runs group")
		return explain.Group{}
	}
	until(t, 20*time.Second, "the runs group to hold the older-head run", func() bool {
		runs := decode[[]schema.CIRun](t, callOK(t, d, "ci", "list_runs", map[string]any{"pr_id": prA.Ref()}))
		return len(runs) == 3
	})
	if g := ci(); g.Active == nil || *g.Active {
		t.Errorf("a run of an older head made the PR active: %+v", g)
	}

	// A job-level change behind an unchanged rollup: the failed run is rerun
	// (attempt 2, still failing) and a different job fails. The next runs refresh
	// notices the new attempt and fetches its jobs, within ci_failed_ttl.
	d.Shim.SetJobs(3001, servetest.Job{ID: 70020, Name: "changes", Conclusion: "success"}, servetest.Job{ID: 70021, Name: "lint-job", Conclusion: "failure"})
	d.Shim.SetRuns(prA.Repo, "feature-3",
		servetest.Run{ID: 3001, Name: "ci", Status: "completed", Conclusion: "failure", Head: "head-3", Attempt: 2},
		servetest.Run{ID: 3002, Name: "lint", Status: "completed", Conclusion: "success", Head: "head-3"})
	until(t, 20*time.Second, "the new attempt's jobs to be fetched and served", func() bool {
		runs := decode[[]schema.CIRun](t, callOK(t, d, "ci", "list_runs", map[string]any{"pr_id": prA.Ref()}))
		for _, r := range runs {
			if r.ID == "3001" && r.Attempt == 2 {
				return len(r.Jobs) == 2 && r.Jobs[1].Name == "lint-job"
			}
		}
		return false
	})
	if got := jobsCalls(); got != 2 {
		t.Errorf("jobs fetched %d times, want once per attempt (2)", got)
	}
	if got := jobRows(); got != 2 {
		t.Errorf("job rows = %d, want one per attempt (2)", got)
	}

	// A failed job fetch keeps what is known and records last_error: attempt 3
	// is announced while the jobs endpoint fails.
	d.Shim.Fail("jobs", 500, 0)
	d.Shim.SetRuns(prA.Repo, "feature-3",
		servetest.Run{ID: 3001, Name: "ci", Status: "completed", Conclusion: "failure", Head: "head-3", Attempt: 3},
		servetest.Run{ID: 3002, Name: "lint", Status: "completed", Conclusion: "success", Head: "head-3"})
	until(t, 30*time.Second, "the runs group to record the failed job fetch", func() bool {
		var lastErr string
		_ = d.OpenStore(t).QueryRow(`SELECT last_error FROM entity_group WHERE id = ? AND group_name = 'runs'`, prA.NodeID).Scan(&lastErr)
		return lastErr != ""
	})
	if got := jobRows(); got != 2 {
		t.Errorf("a failed job fetch changed the job table: %d rows, want the 2 known", got)
	}
	d.Shim.Remove("fail.jobs")
}
