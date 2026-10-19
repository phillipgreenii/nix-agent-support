package serve_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/explain"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The error table rows that became testable with Phase 3a, each driven through
// the real daemon and the counting fake gh.

// idsOf is the number of node ids an argv carries (`-f ids[]=...`).
func idsOf(c servetest.Call) int {
	n := 0
	for _, a := range c.Args {
		if strings.HasPrefix(a, "ids[]=") {
			n++
		}
	}
	return n
}

func TestErrorTable_MergeStateStatus502Or504InABatch_FallsBackToPerPRFetchesAndTheAnswersAreCorrect(t *testing.T) {
	for _, status := range []int{502, 504} {
		t.Run(httpName(status), func(t *testing.T) { perPRFallback(t, status) })
	}
}

func httpName(status int) string {
	if status == 504 {
		return "504"
	}
	return "502"
}

func perPRFallback(t *testing.T, status int) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed3a})
	d.Start(t)
	settle(t, d)
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("exit = %d; output: %s", code, d.Stderr())
	}

	// Every stored detail group is overdue, so the restart queues the three of
	// them at once and they go out as one batch; the batch answers 502, a
	// single-PR request does not. The host now says prA is behind its base.
	past := time.Now().Add(-time.Hour).UnixMilli()
	db := d.WritableStore(t)
	if _, err := db.Exec(`UPDATE entity_group SET fresh_until = ? WHERE group_name = 'detail'`, past); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	d.Shim.SetDetail(prA, servetest.Detail{MergeStateStatus: "BEHIND", Additions: 12, Deletions: 3, ChangedFiles: 2, Reviewers: []string{"teammate"}})
	d.Shim.FailBatch("detail", status)

	mark := len(d.Shim.OriginCalls())
	d.Start(t)
	until(t, 30*time.Second, "the batch and the per-PR fallback calls", func() bool {
		n := 0
		for _, c := range originSince(d, mark) {
			if c.Kind() == "detail" {
				n++
			}
		}
		return n >= 4
	})
	d.Shim.WaitQuiet(t, time.Second, 30*time.Second, workKinds...)

	var detail []servetest.Call
	for _, c := range originSince(d, mark) {
		if c.Kind() == "detail" {
			detail = append(detail, c)
		}
	}
	// One batch call (three ids, answered 502) and then one call per PR.
	if len(detail) != 4 || idsOf(detail[0]) != 3 {
		var sizes []int
		for _, c := range detail {
			sizes = append(sizes, idsOf(c))
		}
		t.Fatalf("detail calls carried %v ids, want one batch of 3 and then 3 single-PR calls", sizes)
	}
	seen := map[string]bool{}
	for _, c := range detail[1:] {
		if idsOf(c) != 1 {
			t.Errorf("a fallback call carried %d ids, want 1", idsOf(c))
		}
		for _, a := range c.Args {
			if strings.HasPrefix(a, "ids[]=") {
				seen[strings.TrimPrefix(a, "ids[]=")] = true
			}
		}
	}
	if len(seen) != 3 {
		t.Errorf("the per-PR calls covered %v, want each of the three PRs once", seen)
	}

	// The answers are correct: the stored detail is the host's current state, and
	// nothing more is fetched to serve it.
	mark = len(d.Shim.OriginCalls())
	show := decode[schema.PR](t, callOK(t, d, "pr", "show", map[string]any{"id": prA.Ref()}))
	if show.MergeStateStatus != "BEHIND" || show.ChangedFiles != 2 {
		t.Errorf("show after the fallback = %+v, want the BEHIND detail", show)
	}
	if got := originSince(d, mark); len(got) != 0 {
		t.Errorf("serving the fallback's answers made %d origin calls", len(got))
	}
}

func TestErrorTable_PRDeleted_IsTombstonedItsQueuedTasksAreGoneAndAReadAnswersNotFound(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed3a, Hooks: "poll_interval=50ms"})
	d.Start(t)
	settle(t, d)
	callOK(t, d, "pr", "show", map[string]any{"id": prC.Ref()}) // prC is served before it goes

	// GitHub stops answering for prC's node.
	d.Shim.Remove("detail." + prC.NodeID + ".json")
	callOK(t, d, "pr", "refresh", map[string]any{"id": prC.Ref(), "groups": []string{"detail"}})
	until(t, 20*time.Second, "the entity to be tombstoned", func() bool {
		var at *int64
		if err := d.OpenStore(t).QueryRow(`SELECT tombstoned_at FROM entity WHERE id = ?`, prC.NodeID).Scan(&at); err != nil {
			return false
		}
		return at != nil && *at != 0
	})
	d.Shim.WaitQuiet(t, time.Second, 30*time.Second)

	// Its queued tasks are gone: explain shows the tombstone and no group of it is
	// queued, and the daemon makes no further call for it.
	res := decode[explain.Result](t, callOK(t, d, "pr", "explain", map[string]any{"id": prC.Ref()}))
	if res.TombstonedAt == nil {
		t.Errorf("explain shows no tombstone for a deleted PR: %+v", res)
	}
	for _, g := range res.Groups {
		if g.QueuePosition != nil {
			t.Errorf("group %s is still queued (position %d)", g.Group, *g.QueuePosition)
		}
	}
	// A read answers not_found, and costs nothing: the tombstone answers it.
	mark := len(d.Shim.OriginCalls())
	for _, op := range []string{"show", "files", "commits", "review_pending"} {
		e := callErr(t, d, "pr", op, map[string]any{"id": prC.Ref()}, 20*time.Second)
		if e == nil || e.Code != "not_found" {
			t.Errorf("%s of a deleted PR = %+v, want not_found", op, e)
		}
	}
	if e := callErr(t, d, "ci", "list_runs", map[string]any{"pr_id": prC.Ref()}, 20*time.Second); e == nil || e.Code != "not_found" {
		t.Errorf("list_runs of a deleted PR = %+v, want not_found", e)
	}
	if got := originSince(d, mark); len(got) != 0 {
		t.Errorf("reads of a tombstoned PR made %d origin calls: %v", len(got), kinds(got))
	}
	// The others are untouched.
	callOK(t, d, "pr", "show", map[string]any{"id": prA.Ref()})
}

func TestErrorTable_AuthFailure_CachedReadsAreServedWithTheirAgeAndAFetchAnswersUnauthenticated(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Hooks: "viewer_interval=300ms", Before: seed3a})
	d.Start(t)
	settle(t, d)
	if a := authStatus(t, d); a.State != scriptout.AuthOK {
		t.Fatalf("fixture: authenticated before the failure: %+v", a)
	}

	d.Shim.Mode("401")
	until(t, 15*time.Second, "auth_status to report the failure", func() bool {
		return authStatus(t, d).State == scriptout.AuthMissing
	})

	// A cached read is served, with its age.
	show := decode[schema.PR](t, callOK(t, d, "pr", "show", map[string]any{"id": prA.Ref()}))
	if show.Title == "" || show.AgeSeconds == nil || show.ServedFrom != "cache" {
		t.Errorf("a cached read while unauthenticated = %+v", show)
	}
	// Asked to refresh, it is still served (stale, with the age), not refused.
	show = decode[schema.PR](t, callOK(t, d, "pr", "show", map[string]any{"id": prA.Ref(), "fresh": true}))
	if show.Title == "" || !show.Stale || show.AgeSeconds == nil {
		t.Errorf("a refreshed read while unauthenticated = stale %v age %v title %q, want the stored content stale with its age", show.Stale, show.AgeSeconds, show.Title)
	}
	// A read that needs a fetch (prB's files were never stored) is unauthenticated.
	e := callErr(t, d, "pr", "files", map[string]any{"id": prB.Ref()}, 20*time.Second)
	if e == nil || e.Code != "unauthenticated" {
		t.Errorf("a fetch while unauthenticated = %+v, want unauthenticated", e)
	}

	// The credential works again.
	d.Shim.Mode("ok")
	until(t, 15*time.Second, "auth_status to recover", func() bool { return authStatus(t, d).State == scriptout.AuthOK })
	callOK(t, d, "pr", "files", map[string]any{"id": prB.Ref()})
}

func TestErrorTable_GraphQLRateLimitText_PausesTheBackgroundInteractiveReadsAreServedStaleAndItIsNotUnauthenticated(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed3a})
	d.Start(t)
	settle(t, d)
	callOK(t, d, "pr", "files", map[string]any{"id": prA.Ref()})

	// The host answers every call with the rate-limit text (no headers on the
	// provider's GraphQL calls, only the words).
	d.Shim.Mode("primary")
	e := callErr(t, d, "pr", "files", map[string]any{"id": prB.Ref()}, 20*time.Second)
	if e == nil || e.Code != "unavailable" || !strings.Contains(strings.ToLower(e.Message), "rate limit") {
		t.Fatalf("a read that hit the rate limit = %+v, want unavailable naming the rate limit", e)
	}
	// The limit paused the background classes, and is not an auth failure.
	b := graphqlBucket(t, statusReport(t, d))
	if !b.Paused {
		t.Errorf("the rate-limit text must pause the background: %+v", b)
	}
	if !servetest.HasChange(d.DaemonLog(t), "budget_paused") {
		t.Error("the pause must be logged as budget_paused")
	}
	if a := authStatus(t, d); a.State != scriptout.AuthOK {
		t.Errorf("a rate limit is not an authentication failure: %+v", a)
	}
	// Interactive reads with stored content are served stale.
	show := decode[schema.PR](t, callOK(t, d, "pr", "show", map[string]any{"id": prA.Ref(), "fresh": true}))
	if show.Title == "" || !show.Stale {
		t.Errorf("an interactive read under the limit = stale %v title %q, want the stored content, stale", show.Stale, show.Title)
	}
	files := decode[schema.PRFilesResult](t, callOK(t, d, "pr", "files", map[string]any{"id": prA.Ref(), "fresh": true}))
	if len(files.Files) != 2 || files.Stale == nil || !*files.Stale {
		t.Errorf("files under the limit = %+v, want the stored files, stale", files)
	}
	// Background work holds: nothing more reaches the origin on its own.
	held := len(d.Shim.OriginCalls())
	time.Sleep(2 * time.Second)
	if got := len(d.Shim.OriginCalls()); got != held {
		t.Errorf("the background made %d origin calls while paused", got-held)
	}
}

func TestErrorTable_ViewerChanged_FlushesIdentityScopedDataAndHidesOlderAppends(t *testing.T) {
	stateHome := t.TempDir()
	opts := writeOpts(stateHome)
	opts.Hooks = "viewer_interval=300ms,poll_interval=50ms"
	d := servetest.NewDaemon(t, opts)
	d.Start(t)
	settlePRs(t, d, 4)

	// An append recorded by the first viewer, and a stored pending group.
	res := decode[pr.ReviewSubmitResult](t, callOK(t, d, "pr", "review_submit", submitArgs()))
	if res.Status != pr.StatusPosted {
		t.Fatalf("fixture: the first viewer's write = %+v", res)
	}
	d.Shim.WaitQuiet(t, time.Second, 30*time.Second, "search", "nodes", "pending", "write_items", "create_review")
	pendingRows := func() int {
		var n int
		if err := d.OpenStore(t).QueryRow(`SELECT COUNT(*) FROM entity_group WHERE group_name = 'pending'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if pendingRows() == 0 {
		t.Fatal("fixture: no pending group stored")
	}
	before := decode[pr.PendingReviewResult](t, callOK(t, d, "pr", "review_pending", map[string]any{"id": prW.Ref()}))
	if before.Review == nil || before.Review.LastAppend == nil {
		t.Fatalf("fixture: review_pending shows the last append: %+v", before)
	}
	sidecar, err := sidecarStore(stateHome).Path("example-org", "widget", 21)
	if err != nil {
		t.Fatal(err)
	}
	sidecarBefore, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	mineFetched := func() int64 {
		var at int64
		if err := d.OpenStore(t).QueryRow(`SELECT COALESCE(fetched_at, 0) FROM query WHERE key = ?`, servetest.QueryMine).Scan(&at); err != nil {
			return 0
		}
		return at
	}
	mineBefore := mineFetched()

	// The viewer changes: the stored pending rows are gone at once, the earlier
	// append is hidden, the @me query is re-run for the new identity, and the
	// structured log says so.
	d.Shim.Viewer("another-viewer")
	until(t, 20*time.Second, "the viewer_changed log line", func() bool { return servetest.HasChange(d.DaemonLog(t), "viewer_changed") })
	for _, l := range d.DaemonLog(t) {
		if l["change"] == "viewer_changed" && (l["old_login"] != servetest.DefaultViewer || l["new_login"] != "another-viewer") {
			t.Errorf("viewer_changed line = %v", l)
		}
	}
	until(t, 20*time.Second, "the viewer-scoped query to be re-run", func() bool { return mineFetched() > mineBefore })
	if got := pendingRows(); got != 0 {
		t.Errorf("%d pending rows survive the viewer change, want none", got)
	}
	after := decode[pr.PendingReviewResult](t, callOK(t, d, "pr", "review_pending", map[string]any{"id": prW.Ref()}))
	if after.Review != nil && after.Review.LastAppend != nil {
		t.Errorf("the previous viewer's last_append is still reported: %+v", after.Review.LastAppend)
	}
	// The sidecar files are never touched by the flush: they are the source of
	// truth the old binary shares.
	if raw, err := os.ReadFile(sidecar); err != nil || string(raw) != string(sidecarBefore) {
		t.Errorf("the sidecar changed on a viewer change: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateHome, "pg-connector-pr-github", "posted")); err != nil {
		t.Errorf("the sidecar directory is gone: %v", err)
	}
}
