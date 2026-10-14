package serve_test

import (
	"encoding/json"
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

// writeOpts is the daemon of a write test: the state home named by the daemon's
// own XDG_STATE_HOME (the old backend's lookup), a fast scheduler poll and a
// summary kept fresh for an hour.
func writeOpts(stateHome string) servetest.Options {
	return servetest.Options{
		Before: seedWrites,
		Hooks:  "poll_interval=50ms",
		Env:    map[string]string{"XDG_STATE_HOME": stateHome},
	}
}

// sidecarPath is where the old backend keeps the posted-review record of prW
// under XDG_STATE_HOME.
func sidecarStore(stateHome string) posted.Store {
	return posted.Store{Dir: filepath.Join(stateHome, "pg-connector-pr-github", "posted")}
}

func TestWrites_ReviewSubmitThroughTheDaemon_RecordsTheSidecarInvalidatesPendingOnceAndReplaysAsNoChange(t *testing.T) {
	stateHome := t.TempDir()
	d := servetest.NewDaemon(t, writeOpts(stateHome))
	d.Start(t)
	settle(t, d)

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

	// The sidecar is where the old backend keeps it, under its file name, and
	// records the comment's fingerprint and the append.
	store := sidecarStore(stateHome)
	path, err := store.Path("example-org", "widget", 21)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(stateHome, "pg-connector-pr-github", "posted", "example-org__widget__21.json"); path != want {
		t.Fatalf("sidecar path = %s, want %s", path, want)
	}
	st, err := store.Load("example-org", "widget", 21)
	if err != nil {
		t.Fatalf("the sidecar is not readable: %v", err)
	}
	if len(st.Fingerprints) != 1 || st.Fingerprints[0] != commentFingerprint() || st.LastAppend == nil || st.LastAppend.Added != 1 {
		t.Errorf("sidecar = %+v", st)
	}

	// The write invalidated exactly the pending group, and queued it once: the
	// run's own reads (the lookup, the look-again after the create, the final
	// re-read) plus one refresh.
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

func TestWrites_ASecondProcessHoldingTheSidecarLockDelaysTheWriteThenLetsItRun(t *testing.T) {
	stateHome := t.TempDir()
	d := servetest.NewDaemon(t, writeOpts(stateHome))
	d.Start(t)
	settle(t, d)

	locker := posted.Locker{Dir: filepath.Join(stateHome, "pg-connector-pr-github", "locks"), Wait: 5 * time.Second, Poll: 20 * time.Millisecond}
	lock, err := locker.Acquire("example-org", "widget", 21)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			_ = lock.Release()
		}
	}()

	// Within a short deadline the wait ends as unavailable, naming the lock, and
	// nothing reached the host.
	start := time.Now()
	e := callErr(t, d, "pr", "review_submit", submitArgs(), 1500*time.Millisecond)
	if e == nil || e.Code != "unavailable" || !strings.Contains(e.Message, "lock") {
		t.Fatalf("a held lock within a short deadline: %+v, want unavailable naming the lock", e)
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("the lock wait ended after %s with a 1.5 s deadline", took)
	}
	if got := d.Shim.CountKind("create_review") + d.Shim.CountStdin("write_items"); got != 0 {
		t.Errorf("a refused write reached the host (%d writes)", got)
	}

	// With time to wait, the call is delayed until the other process lets go,
	// and then runs.
	type outcome struct {
		resp scriptout.Response
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, err := d.Call("pr", req("review_submit", submitArgs()), 30*time.Second)
		done <- outcome{resp, err}
	}()
	select {
	case o := <-done:
		t.Fatalf("the write finished while the lock was held: %+v %v", o.resp, o.err)
	case <-time.After(800 * time.Millisecond):
	}
	if got := d.Shim.CountKind("create_review"); got != 0 {
		t.Fatalf("a write ran under a held lock")
	}
	released = true
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	select {
	case o := <-done:
		if o.err != nil || o.resp.Error != nil {
			t.Fatalf("after the release: %+v %v", o.resp.Error, o.err)
		}
		if res := decode[pr.ReviewSubmitResult](t, o.resp.Result); res.Status != pr.StatusPosted {
			t.Errorf("after the release: %+v", res)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the write did not run after the lock was released")
	}
}

func TestWrites_ACorruptSidecarRefusesTheWriteAndIsNeverReplaced(t *testing.T) {
	stateHome := t.TempDir()
	d := servetest.NewDaemon(t, writeOpts(stateHome))
	d.Start(t)
	settle(t, d)

	store := sidecarStore(stateHome)
	path, err := store.Path("example-org", "widget", 21)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const garbage = "{ this is not json"
	if err := os.WriteFile(path, []byte(garbage), 0o600); err != nil {
		t.Fatal(err)
	}

	e := callErr(t, d, "pr", "review_submit", submitArgs(), 20*time.Second)
	if e == nil || e.Code != "unavailable" || !strings.Contains(e.Message, "posted-sidecar") {
		t.Fatalf("a corrupt sidecar must refuse the write as unavailable: %+v", e)
	}
	if got := d.Shim.CountKind("create_review") + d.Shim.CountStdin("write_items"); got != 0 {
		t.Errorf("a refused write reached the host (%d writes)", got)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != garbage {
		t.Errorf("the corrupt sidecar was replaced or removed: %q (%v)", raw, err)
	}
	// review_pending is local bookkeeping's reader: a corrupt sidecar only hides
	// last_append, it never fails the read.
	res := decode[pr.PendingReviewResult](t, callOK(t, d, "pr", "review_pending", map[string]any{"id": prW.Ref()}))
	if res.Pending || res.HeadSHA != prW.Head {
		t.Errorf("review_pending with a corrupt sidecar = %+v", res)
	}
}

func TestWrites_ReviewSubmitWithoutAStateHomeAnswersUnavailableAndTheDaemonRuns(t *testing.T) {
	// No XDG_STATE_HOME, HOME or posted_state_dir in the daemon's environment.
	d := servetest.NewDaemon(t, servetest.Options{Before: seedWrites})
	d.Start(t)
	settle(t, d)
	e := callErr(t, d, "pr", "review_submit", submitArgs(), 20*time.Second)
	if e == nil || e.Code != "unavailable" {
		t.Fatalf("with no state home review_submit must answer unavailable: %+v", e)
	}
	if got := d.Shim.CountKind("create_review") + d.Shim.CountStdin("write_items"); got != 0 {
		t.Errorf("a refused write reached the host (%d writes)", got)
	}
	var logged bool
	for _, l := range d.DaemonLog(t) {
		if l["change"] == "sidecar_unavailable" {
			logged = true
		}
	}
	if !logged {
		t.Error("the missing state home must be in the structured log")
	}
	// The reads are unaffected.
	callOK(t, d, "pr", "review_pending", map[string]any{"id": prW.Ref()})
	callOK(t, d, "pr", "show", map[string]any{"id": prA.Ref()})
}

func TestWrites_SamePRWritesSerializeDifferentPRsRunInParallelAndTheWaitStaysInsideTheDeadline(t *testing.T) {
	stateHome := t.TempDir()
	d := servetest.NewDaemon(t, writeOpts(stateHome))
	d.Start(t)
	settle(t, d)

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
	settle(t, d)

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
	if st, err := sidecarStore(stateHome).Load("example-org", "widget", 21); err != nil || len(st.Fingerprints) != 0 {
		t.Fatalf("a killed run's sidecar = %+v (%v), want nothing recorded", st, err)
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
	settle(t, d)

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
	if st, err := sidecarStore(stateHome).Load("example-org", "widget", 21); err != nil || len(st.Fingerprints) != 1 {
		t.Errorf("sidecar after the drain = %+v (%v)", st, err)
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
	settle(t, d)
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
