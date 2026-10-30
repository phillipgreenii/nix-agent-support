//go:build integration

package serve_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/posted"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The write path end to end, through the real daemon and the counting fake gh.
// Nothing here writes to a real repository: the shim answers the create, write
// and rerun requests from canned documents, and a write changes what the next
// read sees through the shim's after-hooks (see the fake gh's header).

const (
	// commentBody is the request text of the one comment the tests post.
	commentBody = "please rename this"
	commentPath = "a.go"
	commentLine = 4
)

// commentFingerprint is the fingerprint the daemon gives the comment.
func commentFingerprint() string {
	return posted.PointFingerprint(commentPath, "RIGHT", commentLine, commentBody)
}

// seedWrites is seed3a plus the target PR of the writes (prW, a member of the
// configured query so the daemon keeps it) and three canned states of its
// pending review: none, one created empty, and one holding the posted comment.
func seedWrites(d *servetest.Daemon) {
	seed3a(d)
	d.Shim.SetPRs(servetest.MineExpr, prA, prB, prC, prW)
	head := prW.Head
	review := servetest.PendingReview{
		ID: "PRR_new1", DatabaseID: 9001, URL: "https://" + servetest.Host + "/example-org/widget/pull/21#pullrequestreview-9001",
		Commit: head, Body: "*Posted by pg-connector at aaaaaaa.*",
	}
	created := review
	written := review
	written.Comments = []servetest.PendingComment{{
		ID: "PRRC_new1", Path: commentPath, Line: commentLine, Commit: head,
		Body: commentBody + "\n\n*Posted by pg-connector at aaaaaaa.*\n" + posted.Marker(commentFingerprint()),
	}}
	d.Shim.SetPending(prW, servetest.PendingState{Head: head})
	d.Shim.Set("pending.created.json", d.Shim.PendingJSON(servetest.PendingState{Head: head, Reviews: []servetest.PendingReview{created}}))
	d.Shim.Set("pending.written.json", d.Shim.PendingJSON(servetest.PendingState{Head: head, Reviews: []servetest.PendingReview{written}}))
	d.Shim.After("create_review", "pending.created.json", servetest.PendingFile(prW))
	d.Shim.After("write_items", "pending.written.json", servetest.PendingFile(prW))
	// prB's pending lookup, for a write that changes nothing.
	d.Shim.SetPending(prB, servetest.PendingState{Head: sha40("b")})
}

// submitArgs is the review_submit request for the one comment.
func submitArgs() map[string]any {
	return map[string]any{
		"id": prW.Ref(), "head_sha": prW.Head, "body": "",
		"comments": []map[string]any{{"path": commentPath, "line": commentLine, "body": commentBody}},
	}
}

// writeOpts is the daemon of a write test: the legacy home named by the daemon's
// own XDG_STATE_HOME (where the one-time import looks for the old backend's
// files; none exist here), a fast scheduler poll and a summary kept fresh for an
// hour.
func writeOpts(stateHome string) servetest.Options {
	return servetest.Options{
		Before: seedWrites,
		Hooks:  "poll_interval=50ms",
		Env:    map[string]string{"XDG_STATE_HOME": stateHome},
	}
}

// openStoreRW opens the daemon's store file for writing, alongside the running
// daemon (WAL mode, a busy timeout).
func openStoreRW(t *testing.T, d *servetest.Daemon) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+d.StorePath()+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// sidecarRecord reads the posted-review record of owner/repo#n from the
// daemon's store, as the sidecar's Load does: ok is false when there is no row.
func sidecarRecord(t *testing.T, d *servetest.Daemon, owner, repo string, n int) (st posted.State, raw string, ok bool) {
	t.Helper()
	err := d.OpenStore(t).QueryRow(`SELECT state FROM posted_review WHERE owner = ? AND repo = ? AND number = ?`, owner, repo, n).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return posted.State{}, "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatalf("record %s: %v", raw, err)
	}
	return st, raw, true
}

func TestWrites_ReviewSubmitThroughTheDaemon_RecordsTheSidecarInvalidatesPendingOnceAndReplaysAsNoChange(t *testing.T) {
	stateHome := t.TempDir()
	d := servetest.NewDaemon(t, writeOpts(stateHome))
	d.Start(t)
	settlePRs(t, d, 4)

	// The pending group is stored first (one read), so the write has something
	// to invalidate.
	before := decode[pr.PendingReviewResult](t, callOK(t, d, "pr", "review_pending", map[string]any{"id": prW.Ref()}))
	if before.Pending {
		t.Fatalf("fixture: no pending review yet: %+v", before)
	}
	d.Shim.WaitQuiet(t, 500*time.Millisecond, 30*time.Second)
	pendingBefore := d.Shim.CountKindWith("pending", "number=21")
	fetchedAtOf := func(group string) int64 {
		var at int64
		if err := d.OpenStore(t).QueryRow(`SELECT fetched_at FROM entity_group WHERE id = ? AND group_name = ?`, prW.NodeID, group).Scan(&at); err != nil {
			t.Fatalf("group %s of %s: %v", group, prW.NodeID, err)
		}
		return at
	}
	others := []string{"summary", "detail", "conversation", "runs"}
	atBefore := map[string]int64{}
	for _, g := range others {
		atBefore[g] = fetchedAtOf(g)
	}

	res := decode[pr.ReviewSubmitResult](t, callOK(t, d, "pr", "review_submit", submitArgs()))
	if res.Status != pr.StatusPosted || res.Added != 1 || res.ReviewID != "PRR_new1" || res.HeadSHA != prW.Head || res.HeadMoved {
		t.Fatalf("review_submit = %+v, want a posted review with one added comment", res)
	}
	if res.LastAppend == nil || res.LastAppend.Added != 1 {
		t.Errorf("last_append = %+v", res.LastAppend)
	}

	// The request went through the shim as the canned writes: the create with the
	// head as commit_id, and the document with the comment anchored on the line.
	if got := d.Shim.CountKind("create_review"); got != 1 {
		t.Errorf("create_review calls = %d, want 1", got)
	}
	if got := d.Shim.CountStdin("write_items"); got != 1 {
		t.Errorf("write_items calls = %d, want 1", got)
	}
	var create, write string
	for _, s := range d.Shim.Stdin() {
		switch s.Kind {
		case "create_review":
			create = s.Payload
		case "write_items":
			write = s.Payload
		}
	}
	var createReq map[string]any
	if err := json.Unmarshal([]byte(create), &createReq); err != nil || createReq["commit_id"] != prW.Head {
		t.Errorf("the create request = %q (%v), want commit_id %s", create, err, prW.Head)
	}
	for _, want := range []string{"addPullRequestReviewThread", commentPath, "pg-fp:" + commentFingerprint()} {
		if !strings.Contains(write, want) {
			t.Errorf("the write document lacks %q: %s", want, write)
		}
	}

	// The sidecar is a row of the daemon's store (posted_review) and records the
	// comment's fingerprint and the append.
	st, _, found := sidecarRecord(t, d, "example-org", "widget", 21)
	if !found {
		t.Fatal("the sidecar record is not in the store's posted_review table")
	}
	if len(st.Fingerprints) != 1 || st.Fingerprints[0] != commentFingerprint() || st.LastAppend == nil || st.LastAppend.Added != 1 {
		t.Errorf("sidecar = %+v", st)
	}

	// The write invalidated exactly the pending group, and queued it once: the
	// run's own reads (the lookup, the look-again after the create, the final
	// re-read) plus one refresh.
	// The write left the pending group stale (fresh_until = the write's moment);
	// only the queued refresh's commit makes it fresh again. The call log's
	// silence does not say that: a host under load can have the refresh in
	// flight (its lookup recorded, its row not yet stored) for longer than any
	// quiet period, and the read below would then join it and answer from
	// origin (bead pg2-8byzi).
	db := d.OpenStore(t)
	until(t, 60*time.Second, "the queued refresh of the pending group to be stored", func() bool {
		var freshUntil sql.NullInt64
		err := db.QueryRow(`SELECT fresh_until FROM entity_group WHERE id = ? AND group_name = 'pending'`, prW.NodeID).Scan(&freshUntil)
		return err == nil && freshUntil.Valid && freshUntil.Int64 > time.Now().UnixMilli()
	})
	d.Shim.WaitQuiet(t, time.Second, 30*time.Second)
	if got, want := d.Shim.CountKindWith("pending", "number=21")-pendingBefore, 3+1; got != want {
		t.Errorf("the pending lookups after the write = %d, want %d (3 in the run, 1 queued refresh)", got, want)
	}
	for _, g := range others {
		if got := fetchedAtOf(g); got != atBefore[g] {
			t.Errorf("the write refetched the %s group (fetched_at %d -> %d); it must invalidate only pending", g, atBefore[g], got)
		}
	}
	// The refresh stored what the host now holds, and review_pending serves it
	// from the store with the sidecar's last_append.
	mark := len(d.Shim.OriginCalls())
	after := decode[pr.PendingReviewResult](t, callOK(t, d, "pr", "review_pending", map[string]any{"id": prW.Ref()}))
	if !after.Pending || after.Review == nil || after.Review.ReviewID != "PRR_new1" || after.ServedFrom != "cache" {
		t.Errorf("review_pending after the write = %+v", after)
	}
	if after.Review == nil || after.Review.LastAppend == nil || after.Review.LastAppend.Added != 1 {
		t.Errorf("review_pending last_append = %+v", after.Review)
	}
	if got := originSince(d, mark); len(got) != 0 {
		t.Errorf("review_pending after the refresh made %d origin calls", len(got))
	}

	// A replay of the identical request writes nothing and invalidates nothing.
	creates, writes := d.Shim.CountKind("create_review"), d.Shim.CountStdin("write_items")
	pendingMark := d.Shim.CountKindWith("pending", "number=21")
	replay := decode[pr.ReviewSubmitResult](t, callOK(t, d, "pr", "review_submit", submitArgs()))
	if replay.Status != pr.StatusNoChange || replay.AlreadyPresent != 1 || replay.Added != 0 {
		t.Errorf("the replay = %+v, want no_change with the comment already present", replay)
	}
	d.Shim.WaitQuiet(t, time.Second, 30*time.Second)
	if d.Shim.CountKind("create_review") != creates || d.Shim.CountStdin("write_items") != writes {
		t.Error("the replay wrote to the host")
	}
	if got := d.Shim.CountKindWith("pending", "number=21") - pendingMark; got != 1 {
		t.Errorf("the replay made %d pending lookups, want just its own 1 (no invalidation, no refresh)", got)
	}
}

func TestWrites_ACorruptSidecarRefusesTheWriteAndIsNeverReplaced(t *testing.T) {
	stateHome := t.TempDir()
	d := servetest.NewDaemon(t, writeOpts(stateHome))
	d.Start(t)
	settlePRs(t, d, 4)

	const garbage = "{ this is not json"
	if _, err := openStoreRW(t, d).Exec(
		`INSERT INTO posted_review (owner, repo, number, state, updated_at) VALUES ('example-org', 'widget', 21, ?, 1)`, garbage,
	); err != nil {
		t.Fatal(err)
	}

	e := callErr(t, d, "pr", "review_submit", submitArgs(), 20*time.Second)
	if e == nil || e.Code != "unavailable" || !strings.Contains(e.Message, "posted-sidecar") {
		t.Fatalf("a corrupt sidecar must refuse the write as unavailable: %+v", e)
	}
	if got := d.Shim.CountKind("create_review") + d.Shim.CountStdin("write_items"); got != 0 {
		t.Errorf("a refused write reached the host (%d writes)", got)
	}
	var raw string
	if err := d.OpenStore(t).QueryRow(`SELECT state FROM posted_review WHERE owner = 'example-org' AND repo = 'widget' AND number = 21`).Scan(&raw); err != nil || raw != garbage {
		t.Errorf("the corrupt record was replaced or removed: %q (%v)", raw, err)
	}
	// review_pending is local bookkeeping's reader: a corrupt sidecar only hides
	// last_append, it never fails the read.
	res := decode[pr.PendingReviewResult](t, callOK(t, d, "pr", "review_pending", map[string]any{"id": prW.Ref()}))
	if res.Pending || res.HeadSHA != prW.Head {
		t.Errorf("review_pending with a corrupt sidecar = %+v", res)
	}
}

func TestWrites_ALegacyImportThatCannotRunLeavesTheDaemonRunningWithNoMarkerAndWritesWork(t *testing.T) {
	// No PG_CONNECTOR_PR_GITHUB_STATE_DIR, XDG_STATE_HOME or HOME in the daemon's
	// environment: the import has no legacy home to read.
	d := servetest.NewDaemon(t, servetest.Options{Before: seedWrites})
	d.Start(t)
	settlePRs(t, d, 4)
	var logged bool
	for _, l := range d.DaemonLog(t) {
		if l["change"] == "sidecar_import_failed" {
			logged = true
		}
	}
	if !logged {
		t.Error("a failed import must be in the structured log")
	}
	var n int
	if err := d.OpenStore(t).QueryRow(`SELECT count(*) FROM store_meta WHERE key = 'posted_import_v1'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a failed import wrote a marker: count %d (%v)", n, err)
	}
	// The daemon runs and the write path works: the record goes to the store.
	res := decode[pr.ReviewSubmitResult](t, callOK(t, d, "pr", "review_submit", submitArgs()))
	if res.Status != pr.StatusPosted {
		t.Fatalf("review_submit with no legacy home = %+v, want posted", res)
	}
	if st, _, found := sidecarRecord(t, d, "example-org", "widget", 21); !found || len(st.Fingerprints) != 1 {
		t.Errorf("sidecar = %+v found=%v", st, found)
	}
	callOK(t, d, "pr", "show", map[string]any{"id": prA.Ref()})
}

// The live check of the start-up import: a daemon started with the legacy home
// named by its own environment brings the old backend's file into the store,
// and the review-submit path reads it (a comment the legacy record holds as
// posted, which is nowhere on the host, is classified dismissed and never
// written).
func TestWrites_ALegacySidecarFileIsImportedAtStartAndReadByReviewSubmit(t *testing.T) {
	legacy := t.TempDir()
	if err := os.MkdirAll(filepath.Join(legacy, "posted"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(legacy, "posted", "example-org__widget__21.json")
	body := `{"version":1,"fingerprints":["` + commentFingerprint() + `"],"body_heads":[]}`
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	d := servetest.NewDaemon(t, servetest.Options{Before: seedWrites, Hooks: "poll_interval=50ms"})
	d.SetEnv("PG_CONNECTOR_PR_GITHUB_STATE_DIR", legacy)
	d.Start(t)
	settlePRs(t, d, 4)

	if _, raw, found := sidecarRecord(t, d, "example-org", "widget", 21); !found || raw != body {
		t.Fatalf("the legacy record was not imported as-is: %q found=%v", raw, found)
	}
	res := decode[pr.ReviewSubmitResult](t, callOK(t, d, "pr", "review_submit", submitArgs()))
	if res.Dismissed != 1 || res.Added != 0 || res.Status != pr.StatusNoChange {
		t.Errorf("review_submit = %+v, want the imported record to dismiss the comment (nothing added)", res)
	}
	if got := d.Shim.CountKind("create_review") + d.Shim.CountStdin("write_items"); got != 0 {
		t.Errorf("a dismissed comment reached the host (%d writes)", got)
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != body {
		t.Errorf("the legacy file changed: %q (%v)", b, err)
	}

	// A restart does not import again: a record deleted by hand stays deleted.
	if _, err := openStoreRW(t, d).Exec(`DELETE FROM posted_review`); err != nil {
		t.Fatal(err)
	}
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("stop exit = %d; output: %s", code, d.Stderr())
	}
	d.Start(t)
	if _, _, found := sidecarRecord(t, d, "example-org", "widget", 21); found {
		t.Error("a second start re-imported the legacy file")
	}
}

func TestWrites_SamePRWritesSerializeDifferentPRsRunInParallelAndTheWaitStaysInsideTheDeadline(t *testing.T) {
	stateHome := t.TempDir()
	d := servetest.NewDaemon(t, writeOpts(stateHome))
	d.Start(t)
	settlePRs(t, d, 4)

	type outcome struct {
		resp scriptout.Response
		err  error
	}
	start := func(args map[string]any, deadline time.Duration) chan outcome {
		ch := make(chan outcome, 1)
		go func() {
			resp, err := d.Call("pr", req("review_submit", args), deadline)
			ch <- outcome{resp, err}
		}()
		return ch
	}

	// The first write to prW is held at the create request.
	d.Shim.Block("create_review", 1)
	first := start(submitArgs(), 30*time.Second)
	until(t, 15*time.Second, "the first write to reach the create request", func() bool { return d.Shim.CountKind("create_review") == 1 })
	lookups := d.Shim.CountKindWith("pending", "number=21")

	// A second write to the SAME PR does not touch the host while the first runs.
	second := start(submitArgs(), 30*time.Second)
	// One within a short deadline gives up inside it.
	startShort := time.Now()
	e := callErr(t, d, "pr", "review_submit", submitArgs(), time.Second)
	if e == nil || e.Code != "unavailable" || !strings.Contains(e.Message, "could not start the write") {
		t.Errorf("a same-PR write past its deadline: %+v, want unavailable (could not start the write)", e)
	}
	if took := time.Since(startShort); took > 3*time.Second {
		t.Errorf("the lock wait took %s with a 1 s deadline", took)
	}

	// A write to a DIFFERENT PR runs to its end at once (no comment, no body: it
	// only looks, and writes nothing).
	other := map[string]any{"id": prB.Ref(), "head_sha": sha40("b"), "body": "", "comments": []map[string]any{}}
	t0 := time.Now()
	res := decode[pr.ReviewSubmitResult](t, callOK(t, d, "pr", "review_submit", other))
	if res.Status != pr.StatusNoChange || res.State != pr.StateNone {
		t.Errorf("the write to the other PR = %+v", res)
	}
	if took := time.Since(t0); took > 5*time.Second {
		t.Errorf("a write to another PR waited %s behind the held one", took)
	}
	if got := d.Shim.CountKindWith("pending", "number=21"); got != lookups {
		t.Errorf("the queued same-PR writes made %d pending lookups for the held PR while it was held", got-lookups)
	}
	select {
	case o := <-second:
		t.Fatalf("the second same-PR write finished while the first was held: %+v", o)
	default:
	}

	// Release the first: it posts, the second then runs and finds it present.
	d.Shim.Release("create_review")
	o := <-first
	if o.err != nil || o.resp.Error != nil || decode[pr.ReviewSubmitResult](t, o.resp.Result).Status != pr.StatusPosted {
		t.Fatalf("the first write: %+v %v", o.resp, o.err)
	}
	o = <-second
	if o.err != nil || o.resp.Error != nil {
		t.Fatalf("the second write: %+v %v", o.resp, o.err)
	}
	if got := decode[pr.ReviewSubmitResult](t, o.resp.Result); got.Status != pr.StatusNoChange || got.AlreadyPresent != 1 {
		t.Errorf("the second write = %+v, want no_change with the comment already present (it ran after the first)", got)
	}
	if got := d.Shim.CountStdin("write_items"); got != 1 {
		t.Errorf("write_items calls = %d, want one (no duplicate comment)", got)
	}
}

func TestWrites_ADaemonKilledMidWriteAndTheRequestRetriedLeavesNoDuplicateComment(t *testing.T) {
	stateHome := t.TempDir()
	d := servetest.NewDaemon(t, writeOpts(stateHome))
	d.Start(t)
	settlePRs(t, d, 4)

	// The write document is held on its way out; the daemon is killed while the
	// caller waits for its answer. The document still lands on the host (the
	// shim applies it once released): the answer was lost, the write was not.
	d.Shim.Block("write_items", 1)
	go func() { _, _ = d.Call("pr", req("review_submit", submitArgs()), 30*time.Second) }()
	until(t, 15*time.Second, "the write document to reach the host", func() bool { return d.Shim.CountStdin("write_items") == 1 })
	d.Kill(t)
	d.Shim.Release("write_items")
	until(t, 10*time.Second, "the held write to land on the host", func() bool {
		raw, err := os.ReadFile(filepath.Join(d.Shim.Dir, servetest.PendingFile(prW)))
		return err == nil && strings.Contains(string(raw), "pg-fp:"+commentFingerprint())
	})
	// The killed run recorded nothing in the sidecar.
	if st, _, found := sidecarRecord(t, d, "example-org", "widget", 21); found && len(st.Fingerprints) != 0 {
		t.Fatalf("a killed run's sidecar = %+v, want nothing recorded", st)
	}

	// The retry finds the comment by its marker and writes nothing.
	d.Start(t)
	res := decode[pr.ReviewSubmitResult](t, callOK(t, d, "pr", "review_submit", submitArgs()))
	if res.Status != pr.StatusNoChange || res.AlreadyPresent != 1 {
		t.Errorf("the retry = %+v, want no_change with the comment already present", res)
	}
	if got := d.Shim.CountStdin("write_items"); got != 1 {
		t.Errorf("write_items calls = %d, want the one the killed run made", got)
	}
	if got := d.Shim.CountKind("create_review"); got != 1 {
		t.Errorf("create_review calls = %d, want 1", got)
	}
}

func TestWrites_SIGTERMDuringAnInFlightWriteWaitsForItToFinishBeforeExit(t *testing.T) {
	stateHome := t.TempDir()
	d := servetest.NewDaemon(t, writeOpts(stateHome))
	d.Start(t)
	settlePRs(t, d, 4)

	d.Shim.Block("create_review", 1)
	type outcome struct {
		resp scriptout.Response
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, err := d.Call("pr", req("review_submit", submitArgs()), 30*time.Second)
		done <- outcome{resp, err}
	}()
	until(t, 15*time.Second, "the write to reach the create request", func() bool { return d.Shim.CountKind("create_review") == 1 })

	d.Term(t)
	// The daemon is draining, not gone: the write is still in flight.
	time.Sleep(800 * time.Millisecond)
	if ended, code := d.Exited(); ended {
		t.Fatalf("the daemon exited (%d) while a write was in flight; output: %s", code, d.Stderr())
	}
	select {
	case o := <-done:
		t.Fatalf("the write was answered before it could finish: %+v %v", o.resp, o.err)
	default:
	}

	d.Shim.Release("create_review")
	o := <-done
	if o.err != nil || o.resp.Error != nil {
		t.Fatalf("the in-flight write must finish and answer: %+v %v", o.resp.Error, o.err)
	}
	if res := decode[pr.ReviewSubmitResult](t, o.resp.Result); res.Status != pr.StatusPosted || res.Added != 1 {
		t.Errorf("the in-flight write = %+v", res)
	}
	if code := d.Wait(t, 30*time.Second); code != 0 {
		t.Errorf("exit = %d after the drain; output: %s", code, d.Stderr())
	}
	// The record of the finished write is on disk, and a second serve starts
	// cleanly on the same state directory.
	if st, _, found := sidecarRecord(t, d, "example-org", "widget", 21); !found || len(st.Fingerprints) != 1 {
		t.Errorf("sidecar after the drain = %+v found=%v", st, found)
	}
	d.Start(t)
	callOK(t, d, "pr", "review_pending", map[string]any{"id": prW.Ref()})
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Errorf("second run exit = %d; output: %s", code, d.Stderr())
	}
}

func TestWrites_ARefreshStartedBeforeAWriteNeitherOverwritesItNorSurvivesIt(t *testing.T) {
	stateHome := t.TempDir()
	d := servetest.NewDaemon(t, writeOpts(stateHome))
	d.Start(t)
	settlePRs(t, d, 4)
	callOK(t, d, "pr", "review_pending", map[string]any{"id": prW.Ref()})
	d.Shim.WaitQuiet(t, 500*time.Millisecond, 30*time.Second)

	// A refresh of the pending group starts and is held at the host: it has read
	// the state with no pending review. The test waits for the call to be
	// HOLDING, not merely logged: the write's own pending lookups use the same
	// request kind, and one that reached the fake gh first would take the held
	// slot and block the write itself.
	d.Shim.Block("pending", 1)
	callOK(t, d, "pr", "refresh", map[string]any{"id": prW.Ref(), "groups": []string{"pending"}})
	until(t, 15*time.Second, "the refresh to be held at the host", func() bool { return d.Shim.Held("pending") == 1 })

	// The write runs meanwhile (its own lookups are not held) and lands.
	res := decode[pr.ReviewSubmitResult](t, callOK(t, d, "pr", "review_submit", submitArgs()))
	if res.Status != pr.StatusPosted {
		t.Fatalf("the write = %+v", res)
	}
	// The held refresh is answered now, with the OLD state.
	d.Shim.Release("pending")
	d.Shim.WaitQuiet(t, 800*time.Millisecond, 30*time.Second)

	// What is stored is the host's state after the write: the held refresh did
	// not overwrite it, and the group is served with the review.
	got := decode[pr.PendingReviewResult](t, callOK(t, d, "pr", "review_pending", map[string]any{"id": prW.Ref()}))
	if !got.Pending || got.Review == nil || got.Review.ReviewID != "PRR_new1" || got.Stale != nil && *got.Stale {
		t.Errorf("review_pending after the write and the late refresh = %+v, want the posted review, not stale", got)
	}
	var content string
	if err := d.OpenStore(t).QueryRow(`SELECT content FROM entity_group WHERE id = ? AND group_name = 'pending'`, prW.NodeID).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "PRR_new1") {
		t.Errorf("the stored pending group lost the write: %s", content)
	}
}

// ---- rerun_failed ------------------------------------------------------------------

func TestWrites_RerunFailedSendsTheExplicitRepositoryRerunInvalidatesRunsAndIsSafeToRetry(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: func(d *servetest.Daemon) {
		seed3a(d)
		// The rerun starts attempt 2 of the failed run: the runs list afterwards
		// shows it in progress.
		d.Shim.Set("runs.after_rerun.json", servetest.RunsJSON(t, prA.Repo, "feature-3",
			servetest.Run{ID: 3001, Name: "ci", Status: "in_progress", Head: "head-3", Attempt: 2},
			servetest.Run{ID: 3002, Name: "lint", Status: "completed", Conclusion: "success", Head: "head-3"}))
		d.Shim.After("rerun", "runs.after_rerun.json", servetest.RunsFile(prA.Repo, "feature-3"))
	}, Hooks: "poll_interval=50ms"})
	d.Start(t)
	settle(t, d)

	runsFetchedAt := func() int64 {
		var at int64
		if err := d.OpenStore(t).QueryRow(`SELECT fetched_at FROM entity_group WHERE id = ? AND group_name = 'runs'`, prA.NodeID).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	detailAt := func() int64 {
		var at int64
		if err := d.OpenStore(t).QueryRow(`SELECT fetched_at FROM entity_group WHERE id = ? AND group_name = 'detail'`, prA.NodeID).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	runsBefore, detailBefore := runsFetchedAt(), detailAt()

	callOK(t, d, "ci", "rerun_failed", map[string]any{"pr_id": prA.Ref()})
	if got := d.Shim.CountKind("rerun"); got != 1 {
		t.Fatalf("rerun requests = %d, want 1", got)
	}
	var rerun servetest.Call
	for _, c := range d.Shim.Calls() {
		if c.Kind() == "rerun" {
			rerun = c
		}
	}
	joined := strings.Join(rerun.Args, " ")
	if !strings.Contains(joined, "repos/example-org/widget/actions/runs/3001/rerun-failed-jobs") || !strings.Contains(joined, "POST") {
		t.Errorf("the rerun request = %v, want a POST that names the repository and the run", rerun.Args)
	}

	// The write invalidated the runs group and queued its refresh: the next read
	// sees the rerun in progress; no other group was touched.
	until(t, 20*time.Second, "the runs group to show the rerun", func() bool {
		runs := decode[[]struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}](t, callOK(t, d, "ci", "list_runs", map[string]any{"pr_id": prA.Ref()}))
		for _, r := range runs {
			if r.ID == "3001" && r.Status == "in_progress" {
				return true
			}
		}
		return false
	})
	if runsFetchedAt() <= runsBefore {
		t.Error("the runs group was not refetched after the write")
	}
	if detailAt() != detailBefore {
		t.Error("the write refetched the detail group; it must invalidate only runs")
	}

	// A retry after an unknown outcome is safe: a run of the current head is now
	// in progress, so the call succeeds and sends nothing.
	callOK(t, d, "ci", "rerun_failed", map[string]any{"pr_id": prA.Ref()})
	if got := d.Shim.CountKind("rerun"); got != 1 {
		t.Errorf("the retry sent %d rerun requests in all, want still 1", got)
	}
}
