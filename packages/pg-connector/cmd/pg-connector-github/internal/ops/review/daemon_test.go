package review

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc/groupsvctest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	pgposted "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/posted"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/sidecar"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// This file tests what the daemon adds around the copied backend: the handler's
// contract, the write rules (serialization, the sidecar's cross-process lock,
// idempotent retries), the spend class and the invalidation of the pending group.

func TestHandlerIsTheReviewSubmitOpOfThePRSchema(t *testing.T) {
	h := Handler(Deps{Engine: &fakeEngine{}, Provider: &fakeGH{host: newFakeHost(headA)}})
	if h.SchemaVersion != schema.PRSchemaVersion || h.Handle == nil {
		t.Fatalf("handler = %+v", h)
	}
	if len(Ops) != 1 || Ops[0] != "review_submit" || OpName != "review_submit" {
		t.Errorf("Ops = %v, OpName = %q", Ops, OpName)
	}
	if _, err := h.Handle(context.Background(), []byte(`{"id":`)); !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Errorf("malformed arguments: %v, want invalid_argument", err)
	}
	unwired := Handler(Deps{})
	if _, err := unwired.Handle(context.Background(), []byte(`{}`)); !errors.Is(err, scriptout.ErrUnavailable) {
		t.Errorf("a handler with no engine or provider: %v, want unavailable", err)
	}
}

// ---- Idempotence ----

// failThird fails the third item written in one run as unconfirmed (the host
// never stored it), the way a write cut short by a crash leaves the review.
func failThird() func(it github.ReviewWriteItem) github.WriteFailReason {
	n := 0
	return func(github.ReviewWriteItem) github.WriteFailReason {
		n++
		if n == 3 {
			return github.ReasonUnconfirmed
		}
		return ""
	}
}

func fiveComments() pr.ReviewSubmitRequest {
	var cs []pr.ReviewComment
	for i := 1; i <= 5; i++ {
		cs = append(cs, point("main.go", i*10, fmt.Sprintf("finding %d", i)))
	}
	return req("overall", cs...)
}

// uniqueFingerprints fails the test when one fingerprint is on the host twice.
func uniqueFingerprints(t *testing.T, f *submitFixture) int {
	t.Helper()
	seen := map[string]int{}
	n := 0
	for _, r := range f.host.pending {
		for _, c := range r.comments {
			for _, fp := range pgposted.ExtractFingerprints(c.body) {
				seen[fp]++
				n++
			}
		}
	}
	for fp, k := range seen {
		if k != 1 {
			t.Errorf("fingerprint %s is on the host %d times", fp, k)
		}
	}
	return n
}

func TestARunCutShortAndReplayedWritesOnlyWhatIsMissing(t *testing.T) {
	f := newSubmitFixture(t)
	f.host.failItem = failThird()
	_, err := f.submit(fiveComments())
	isCode(t, err, scriptout.ErrUnavailable)
	if !strings.Contains(err.Error(), "4 of 5 comments landed; failed: unconfirmed:") {
		t.Fatalf("err = %q, want the stable failure shape", err)
	}
	if n := uniqueFingerprints(t, f); n != 4 {
		t.Fatalf("%d comments on the host after the cut-short run, want 4", n)
	}

	// The identical request again, the failure gone: only the missing comment
	// is written.
	f.host.failItem = nil
	f.host.writes = nil
	res := f.mustSubmit(fiveComments())
	if res.Status != pr.StatusAppend || res.Added != 1 || res.AlreadyPresent != 4 {
		t.Fatalf("replay = %+v, want one added and four already present", res)
	}
	if len(f.host.writes) != 1 || len(f.host.writes[0].items) != 1 {
		t.Fatalf("replay wrote %+v, want one item in one call", f.host.writes)
	}
	if n := uniqueFingerprints(t, f); n != 5 {
		t.Fatalf("%d comments on the host after the replay, want 5 with no duplicate", n)
	}

	// And after full success a further replay is no_change.
	f.host.writes = nil
	again := f.mustSubmit(fiveComments())
	if again.Status != pr.StatusNoChange || again.Added != 0 || again.AlreadyPresent != 5 || len(f.host.writes) != 0 {
		t.Fatalf("replay after success = %+v writes %d", again, len(f.host.writes))
	}
}

// A daemon killed after GitHub accepted the write but before the sidecar was
// saved: the retry finds every comment by its marker on the host, writes
// nothing and creates no duplicate.
func TestADaemonKilledBeforeTheSidecarWasSavedLeavesNoDuplicateOnRetry(t *testing.T) {
	f := newSubmitFixture(t)
	f.mustSubmit(fiveComments())
	path, err := f.store.Path("foo", "bar", 42)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil { // the record the killed process never wrote
		t.Fatal(err)
	}
	f.host.creates, f.host.updates, f.host.writes = nil, nil, nil
	res := f.mustSubmit(fiveComments())
	if res.Status != pr.StatusNoChange || res.AlreadyPresent != 5 {
		t.Fatalf("retry = %+v, want no_change with every comment already present", res)
	}
	f.untouched()
	if n := uniqueFingerprints(t, f); n != 5 {
		t.Fatalf("%d comments on the host, want 5", n)
	}
}

// ---- Sidecar: path, format, corruption ----

func TestTheSidecarStaysAtTheOldPathAndTheOldLoaderReadsIt(t *testing.T) {
	home := t.TempDir()
	store, locker, err := sidecar.Open(nil, func(k string) string {
		if k == pgposted.EnvStateDir {
			return home
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	host := newFakeHost(headA)
	h := Handler(Deps{Engine: &fakeEngine{}, Provider: &fakeGH{host: host}, Store: store, Locker: locker})
	res, err := callHandler(t, h, context.Background(), req("overall", point("main.go", 12, "rename x")))
	if err != nil || res.Status != pr.StatusPosted {
		t.Fatalf("result = %+v, %v", res, err)
	}
	want := filepath.Join(home, "posted", "foo__bar__42.json")
	raw, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("the record is not at the old name: %v", err)
	}
	if !bytes.Contains(raw, []byte(pointFP("main.go", 12, "rename x"))) {
		t.Errorf("record %s does not carry the fingerprint", raw)
	}
	// A loader built the way the old binary builds its own reads the record.
	st, err := pgposted.Store{Dir: filepath.Join(home, "posted")}.Load("foo", "bar", 42)
	if err != nil || !st.Has(pointFP("main.go", 12, "rename x")) || st.LastAppend == nil || len(st.BodyHeads) != 1 {
		t.Fatalf("old-format load = %+v, %v", st, err)
	}
	// The lock lives at its old name too, and is free after the run.
	if _, err := os.Stat(filepath.Join(home, "locks", "foo__bar__42.lock")); err != nil {
		t.Errorf("lock file: %v", err)
	}
	k, err := locker.Acquire("foo", "bar", 42)
	if err != nil {
		t.Fatalf("the lock must be released after the run: %v", err)
	}
	_ = k.Release()
}

// A corrupt sidecar stays a refusal: the message names it, nothing is written to
// the host and the file is left exactly as it was.
func TestACorruptSidecarIsRefusedAndLeftUntouched(t *testing.T) {
	f := newSubmitFixture(t)
	path, err := f.store.Path("foo", "bar", 42)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	garbage := []byte("{not json")
	if err := os.WriteFile(path, garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = f.submit(req("", point("main.go", 3, "x")))
	isCode(t, err, scriptout.ErrUnavailable)
	if !strings.Contains(err.Error(), "posted-sidecar") || !strings.Contains(err.Error(), "nothing was written") ||
		!strings.Contains(err.Error(), pgposted.ErrCorrupt.Error()) {
		t.Errorf("err = %q, want the sidecar refusal naming the corruption", err)
	}
	f.untouched()
	got, rerr := os.ReadFile(path)
	if rerr != nil || !bytes.Equal(got, garbage) {
		t.Errorf("the corrupt file was changed: %q, %v", got, rerr)
	}
	if len(f.eng.invalidations()) != 0 {
		t.Errorf("a refused call invalidated %v", f.eng.invalidations())
	}
}

// ---- Serialization: in the daemon and across processes ----

// routedGH answers each PR from its own fake host and lets a test hook the
// first lookup of a run.
type routedGH struct {
	mu       sync.Mutex
	byKey    map[string]*fakeGH
	onLookup func(repo string, number int)
}

var _ Provider = (*routedGH)(nil)

func (r *routedGH) of(repo string, number int) *fakeGH {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := fmt.Sprintf("%s#%d", repo, number)
	if r.byKey[key] == nil {
		if r.byKey == nil {
			r.byKey = map[string]*fakeGH{}
		}
		g := &fakeGH{host: newFakeHost(headA)}
		g.host.nextID = int64(1000 * (len(r.byKey) + 1)) // review ids unique across the hosts
		r.byKey[key] = g
	}
	return r.byKey[key]
}

func (r *routedGH) GetPendingReview(ctx context.Context, repo string, number int) (*github.PendingReviewData, error) {
	if r.onLookup != nil {
		r.onLookup(repo, number)
	}
	return r.of(repo, number).GetPendingReview(ctx, repo, number)
}

func (r *routedGH) CreateBodyOnlyPendingReview(ctx context.Context, repo string, number int, commitID, body string) (*github.CreatedReview, error) {
	return r.of(repo, number).CreateBodyOnlyPendingReview(ctx, repo, number, commitID, body)
}

func (r *routedGH) WriteReviewItems(ctx context.Context, reviewID string, items []github.ReviewWriteItem) ([]github.ReviewWriteResult, error) {
	// The review id names its host: node ids are unique per fake host only, so
	// route by the review's owner.
	for _, g := range r.snapshot() {
		if g.host.pendingByNode(reviewID) != nil {
			return g.WriteReviewItems(ctx, reviewID, items)
		}
	}
	return nil, errors.New("no such review")
}

func (r *routedGH) UpdateReviewBody(ctx context.Context, reviewID, body string) error {
	for _, g := range r.snapshot() {
		if g.host.pendingByNode(reviewID) != nil {
			return g.UpdateReviewBody(ctx, reviewID, body)
		}
	}
	return errors.New("no such review")
}

func (r *routedGH) GetPRHistory(ctx context.Context, repo string, number int) (*github.PRHistory, error) {
	return r.of(repo, number).GetPRHistory(ctx, repo, number)
}

func (r *routedGH) GetComparedFiles(context.Context, string, string, string, []string) ([]github.ComparedFile, error) {
	return nil, errors.New("routedGH: the earlier-head path is not routed")
}

func (r *routedGH) snapshot() []*fakeGH {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*fakeGH, 0, len(r.byKey))
	for _, g := range r.byKey {
		out = append(out, g)
	}
	return out
}

func reqFor(id, body string, cs ...pr.ReviewComment) pr.ReviewSubmitRequest {
	return pr.ReviewSubmitRequest{ID: id, HeadSHA: headA, Body: body, Comments: cs}
}

// engineFixture is the handler over the REAL group refresh engine.
type engineFixture struct {
	t      *testing.T
	rig    *groupsvctest.Rig
	gh     *routedGH
	handle scriptout.OpHandler
	store  pgposted.Store
	lock   pgposted.Locker
}

func newEngineFixture(t *testing.T) *engineFixture {
	t.Helper()
	root := t.TempDir()
	e := &engineFixture{
		t:     t,
		rig:   groupsvctest.NewRig(t, groupsvctest.RigOptions{}),
		gh:    &routedGH{},
		store: pgposted.Store{Dir: filepath.Join(root, "posted")},
		lock:  pgposted.Locker{Dir: filepath.Join(root, "locks"), Wait: 2 * time.Second, Poll: 5 * time.Millisecond},
	}
	e.handle = Handler(Deps{Engine: e.rig.Svc, Provider: e.gh, Store: e.store, Locker: e.lock, Clock: e.rig.Clock})
	return e
}

func (e *engineFixture) submit(ctx context.Context, r pr.ReviewSubmitRequest) (pr.ReviewSubmitResult, error) {
	e.t.Helper()
	return callHandler(e.t, e.handle, ctx, r)
}

func TestWritesToOnePRNeverOverlapAndWritesToDifferentPRsRunInParallel(t *testing.T) {
	t.Run("same PR serialize", func(t *testing.T) {
		e := newEngineFixture(t)
		var mu sync.Mutex
		lookups := 0
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		e.gh.onLookup = func(string, int) {
			mu.Lock()
			lookups++
			first := lookups == 1
			mu.Unlock()
			if first {
				once.Do(func() { close(entered) })
				<-release
			}
		}
		done := make(chan error, 2)
		go func() {
			_, err := e.submit(context.Background(), reqFor("foo/bar#42", "", point("main.go", 3, "a")))
			done <- err
		}()
		<-entered
		go func() {
			_, err := e.submit(context.Background(), reqFor("foo/bar#42", "", point("main.go", 4, "b")))
			done <- err
		}()
		// The second call must not reach origin while the first holds the PR.
		time.Sleep(150 * time.Millisecond)
		mu.Lock()
		got := lookups
		mu.Unlock()
		if got != 1 {
			t.Fatalf("%d lookups while the first write held the PR, want 1: the second ran concurrently", got)
		}
		close(release)
		for i := 0; i < 2; i++ {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		if n := len(e.gh.of("foo/bar", 42).host.pending[0].comments); n != 2 {
			t.Errorf("%d comments, want both writes to have landed", n)
		}
	})

	t.Run("different PRs overlap", func(t *testing.T) {
		e := newEngineFixture(t)
		var mu sync.Mutex
		arrived := map[string]bool{}
		both := make(chan struct{})
		e.gh.onLookup = func(repo string, n int) {
			mu.Lock()
			arrived[fmt.Sprintf("%s#%d", repo, n)] = true
			if len(arrived) == 2 {
				select {
				case <-both:
				default:
					close(both)
				}
			}
			mu.Unlock()
			select {
			case <-both: // each waits for the other: only parallel runs get past
			case <-time.After(10 * time.Second):
			}
		}
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for _, id := range []string{"foo/bar#42", "foo/baz#7"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := e.submit(context.Background(), reqFor(id, "", point("main.go", 3, "a")))
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		select {
		case <-both:
		default:
			t.Fatal("the two PRs never ran at the same time")
		}
	})

	t.Run("the in-daemon wait honors the deadline", func(t *testing.T) {
		e := newEngineFixture(t)
		held, release := make(chan struct{}), make(chan struct{})
		go func() {
			_ = e.rig.Svc.SerializeWrite(context.Background(), "foo/bar#42", func(context.Context) error {
				close(held)
				<-release
				return nil
			})
		}()
		<-held
		defer close(release)
		short, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
		defer cancel()
		_, err := e.submit(short, reqFor("foo/bar#42", "", point("main.go", 3, "a")))
		isCode(t, err, scriptout.ErrUnavailable)
		if g := e.gh.of("foo/bar", 42); len(g.ops) != 0 {
			t.Errorf("a call that never got the PR read origin: %v", g.ops)
		}
	})
}

// The child of the cross-process tests: this test binary re-executed as a
// second process that holds the per-PR lock. flock(2) already excludes two open
// file descriptions inside one process; the lock is the one the OLD binary takes
// on the same file, so only another process shows the exclusion across them.
const (
	holderDirEnv   = "REVIEW_TEST_HOLDER_LOCK_DIR"
	holderReadyEnv = "REVIEW_TEST_HOLDER_READY"
)

func TestHelperHoldsPRLock(t *testing.T) {
	dir, ready := os.Getenv(holderDirEnv), os.Getenv(holderReadyEnv)
	if dir == "" || ready == "" {
		t.Skip("helper process only")
	}
	lock, err := pgposted.Locker{Dir: dir}.Acquire("foo", "bar", 42)
	if err != nil {
		fmt.Fprintln(os.Stderr, "holder: acquire:", err)
		os.Exit(3)
	}
	if err := os.WriteFile(ready, []byte("locked"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "holder: ready file:", err)
		os.Exit(4)
	}
	_, _ = io.Copy(io.Discard, os.Stdin) // held until the parent closes stdin
	_ = lock.Release()
	os.Exit(0)
}

// startHolder starts the child holding the lock of foo/bar#42 under dir and
// returns once it does; release lets it go.
func startHolder(t *testing.T, dir string) (release func()) {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldsPRLock$")
	cmd.Env = append(os.Environ(), holderDirEnv+"="+dir, holderReadyEnv+"="+ready)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			_ = stdin.Close()
			_ = cmd.Wait()
		})
	}
	t.Cleanup(release)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			return release
		}
		if time.Now().After(deadline) {
			t.Fatalf("the holder process never took the lock: %s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestASecondProcessHoldingTheSidecarLock(t *testing.T) {
	t.Run("waits and then runs", func(t *testing.T) {
		f := newSubmitFixture(t)
		f.handle = Handler(Deps{Engine: f.eng, Provider: f.gh, Store: f.store, Locker: pgposted.Locker{Dir: f.lock.Dir, Wait: 10 * time.Second, Poll: 5 * time.Millisecond}})
		release := startHolder(t, f.lock.Dir)
		go func() {
			time.Sleep(200 * time.Millisecond)
			release()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		start := time.Now()
		res, err := f.submitCtx(ctx, req("", point("main.go", 3, "x")))
		if err != nil || res.Status != pr.StatusPosted {
			t.Fatalf("result = %+v, %v", res, err)
		}
		if time.Since(start) < 150*time.Millisecond {
			t.Errorf("the call ran after %v: it did not wait for the other process", time.Since(start))
		}
	})

	t.Run("gives up inside the deadline", func(t *testing.T) {
		f := newSubmitFixture(t)
		// A locker whose own wait is long: only the request's deadline can end it.
		f.handle = Handler(Deps{Engine: f.eng, Provider: f.gh, Store: f.store, Locker: pgposted.Locker{Dir: f.lock.Dir, Wait: time.Minute, Poll: 5 * time.Millisecond}})
		startHolder(t, f.lock.Dir)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := f.submitCtx(ctx, req("", point("main.go", 3, "x")))
		isCode(t, err, scriptout.ErrUnavailable)
		if !strings.Contains(err.Error(), "per-PR lock") {
			t.Errorf("err = %q, want the lock refusal", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("gave up after %v, want it inside the 300ms deadline", elapsed)
		}
		if f.host.reads != 0 {
			t.Errorf("nothing may be read without the lock; reads = %d", f.host.reads)
		}
		if len(f.eng.invalidations()) != 0 {
			t.Errorf("a refused call invalidated %v", f.eng.invalidations())
		}
	})
}

// ---- Invalidation and supersession ----

func TestOnlyASuccessfulWriteInvalidatesThePendingGroup(t *testing.T) {
	cases := map[string]struct {
		setup func(f *submitFixture)
		req   pr.ReviewSubmitRequest
		want  int
	}{
		"posted": {req: req("overall", point("main.go", 3, "x")), want: 1},
		"append": {setup: func(f *submitFixture) { f.host.addPending(headA, "typed") }, req: req("", point("main.go", 3, "x")), want: 1},
		"partial run with some comments landed": {
			setup: func(f *submitFixture) { f.host.failItem = failThird() }, req: fiveComments(), want: 1,
		},
		"every write unconfirmed (it may have landed)": {
			setup: func(f *submitFixture) {
				f.host.failItem = func(github.ReviewWriteItem) github.WriteFailReason { return github.ReasonUnconfirmed }
			},
			req: req("", point("main.go", 3, "x")), want: 1,
		},
		"no_change":       {setup: func(f *submitFixture) { f.mustSubmit(req("", point("main.go", 3, "x"))); f.eng.invalidated = nil }, req: req("", point("main.go", 3, "x")), want: 0},
		"invalid request": {req: pr.ReviewSubmitRequest{ID: "foo/bar#42", HeadSHA: "abc"}, want: 0},
		"host refused the create": {
			setup: func(f *submitFixture) { f.host.createErr = errors.New("gh: Unprocessable Entity (HTTP 422)") },
			req:   req("", point("main.go", 3, "x")), want: 0,
		},
		"read failed": {
			setup: func(f *submitFixture) {
				f.host.readErr = func(int) error { return errors.New("gh: Bad Gateway (HTTP 502)") }
			},
			req: req("", point("main.go", 3, "x")), want: 0,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSubmitFixture(t)
			if tc.setup != nil {
				tc.setup(f)
			}
			_, _ = f.submit(tc.req)
			got := f.eng.invalidations()
			if len(got) != tc.want {
				t.Fatalf("invalidations = %v, want %d", got, tc.want)
			}
			if tc.want == 1 && got[0] != "foo/bar#42 pending" {
				t.Errorf("invalidated %q, want exactly the pending group of the PR", got[0])
			}
		})
	}
}

// An invalidation that fails does not fail a write that already landed.
func TestAFailedInvalidationDoesNotFailAWriteThatLanded(t *testing.T) {
	f := newSubmitFixture(t)
	f.eng.failInval = errors.New("store unavailable")
	res, err := f.submit(req("", point("main.go", 3, "x")))
	if err != nil || res.Status != pr.StatusPosted {
		t.Fatalf("result = %+v, %v", res, err)
	}
}

// engineWithPR is the real engine over a store that knows foo/bar#42 as PR_1,
// with fake fetchers for the pending and detail groups, each holding a fresh row.
type engineWithPR struct {
	*engineFixture
	pending, detail *groupsvctest.Fetcher
	hooks           *hookLog
}

type hookLog struct {
	mu    sync.Mutex
	calls int
}

func (h *hookLog) fire(*store.GroupRow, store.GroupRow, groupsvc.RefreshInfo) {
	h.mu.Lock()
	h.calls++
	h.mu.Unlock()
}

func (h *hookLog) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

func newEngineWithPR(t *testing.T) *engineWithPR {
	t.Helper()
	hooks := &hookLog{}
	root := t.TempDir()
	e := &engineFixture{
		t:     t,
		rig:   groupsvctest.NewRig(t, groupsvctest.RigOptions{OnGroupRefreshed: hooks.fire}),
		gh:    &routedGH{},
		store: pgposted.Store{Dir: filepath.Join(root, "posted")},
		lock:  pgposted.Locker{Dir: filepath.Join(root, "locks"), Wait: 2 * time.Second, Poll: 5 * time.Millisecond},
	}
	e.handle = Handler(Deps{Engine: e.rig.Svc, Provider: e.gh, Store: e.store, Locker: e.lock, Clock: e.rig.Clock})
	w := &engineWithPR{engineFixture: e, hooks: hooks}
	e.rig.Origin.Set("PR_1", groupsvctest.PR{Repo: "foo/bar", Number: 42, State: "OPEN", HeadRefOid: headA, BaseRefOid: baseTip, HeadRefName: "feature"})
	e.rig.SeedSummary("PR_1", e.rig.Clock.Now())
	w.pending = e.rig.Register(groupsvctest.NewFetcher(groups.Pending))
	w.detail = e.rig.Register(groupsvctest.NewFetcher(groups.Detail))
	return w
}

func (w *engineWithPR) seedFresh(group, content string) {
	w.t.Helper()
	sum := w.rig.Origin.Summary("PR_1")
	fresh, maxAge := w.rig.Rules.Schedule(group, false, w.rig.Clock.Now())
	w.rig.SeedGroup(store.GroupRow{
		ID: "PR_1", Group: group, Content: []byte(content), Fingerprint: groups.Fingerprint([]byte(content)),
		FetchedAt: w.rig.Clock.Now(), FreshUntil: fresh, MaxAgeAt: maxAge,
		FetchedForHead: sum.HeadRefOid, FetchedForBase: sum.BaseRefOid, Generation: 1,
	})
}

func (w *engineWithPR) class2Depth() int {
	return w.rig.Sched.State().Classes[int(governor.ClassInvalidated)-1].Depth
}

func TestASuccessfulWriteInvalidatesExactlyPendingAndQueuesOneClass2Refresh(t *testing.T) {
	w := newEngineWithPR(t)
	w.seedFresh(groups.Pending, "p")
	w.seedFresh(groups.Detail, "d")
	before, _ := w.rig.Group("PR_1", groups.Detail)

	res, err := w.submit(context.Background(), reqFor("foo/bar#42", "overall", point("main.go", 3, "x")))
	if err != nil || res.Status != pr.StatusPosted {
		t.Fatalf("result = %+v, %v", res, err)
	}
	row, _ := w.rig.Group("PR_1", groups.Pending)
	if row.Generation != 2 || string(row.Content) != "p" || !row.FreshUntil.Equal(w.rig.Clock.Now()) {
		t.Errorf("pending row = %+v, want the content kept, the generation bumped and fresh_until now", row)
	}
	if after, _ := w.rig.Group("PR_1", groups.Detail); after.Generation != before.Generation || !after.FreshUntil.Equal(before.FreshUntil) {
		t.Errorf("detail row changed: %+v -> %+v", before, after)
	}
	if c, queued, _, _ := w.rig.Svc.Inspect(context.Background(), "PR_1", groups.Pending); !queued || c != governor.ClassInvalidated {
		t.Errorf("pending task = class %v queued %v, want one class 2 task", c, queued)
	}
	if _, queued, _, _ := w.rig.Svc.Inspect(context.Background(), "PR_1", groups.Detail); queued {
		t.Error("the detail group was queued")
	}
	if d := w.class2Depth(); d != 1 {
		t.Errorf("class 2 queue depth = %d, want exactly 1", d)
	}
}

func TestANoChangeRunInvalidatesNothing(t *testing.T) {
	w := newEngineWithPR(t)
	if _, err := w.submit(context.Background(), reqFor("foo/bar#42", "", point("main.go", 3, "x"))); err != nil {
		t.Fatal(err)
	}
	w.seedFresh(groups.Pending, "p")
	for { // drop the first call's queued refresh
		if w.rig.Sched.Step(context.Background()) == 0 {
			break
		}
	}
	w.rig.Sched.Wait()
	before, _ := w.rig.Group("PR_1", groups.Pending)

	res, err := w.submit(context.Background(), reqFor("foo/bar#42", "", point("main.go", 3, "x")))
	if err != nil || res.Status != pr.StatusNoChange {
		t.Fatalf("result = %+v, %v", res, err)
	}
	after, _ := w.rig.Group("PR_1", groups.Pending)
	if after.Generation != before.Generation || !after.FreshUntil.Equal(before.FreshUntil) {
		t.Errorf("pending row changed by a no_change run: %+v -> %+v", before, after)
	}
}

// A pending refresh that began BEFORE the write and finishes after it stores
// nothing and reports no change.
func TestAPendingRefreshBegunBeforeTheWriteStoresNothingAndFiresNoHook(t *testing.T) {
	ctx := context.Background()
	w := newEngineWithPR(t)
	w.seedFresh(groups.Pending, "old")
	release := w.pending.Block()
	t.Cleanup(release)

	if err := w.rig.Svc.QueueGroup(ctx, "PR_1", groups.Pending, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	w.rig.Sched.Step(ctx)
	if !w.pending.WaitStarted(1, 10*time.Second) {
		t.Fatal("the refresh never started")
	}

	// The write lands while the fetch is in flight.
	res, err := w.submit(ctx, reqFor("foo/bar#42", "overall", point("main.go", 3, "x")))
	if err != nil || res.Status != pr.StatusPosted {
		t.Fatalf("result = %+v, %v", res, err)
	}
	release()
	w.rig.Sched.Wait()

	row, _ := w.rig.Group("PR_1", groups.Pending)
	if string(row.Content) != "old" || row.Generation != 2 {
		t.Fatalf("row = %+v, want the old content at the write's generation (the refresh's result is discarded)", row)
	}
	if n := w.hooks.count(); n != 0 {
		t.Fatalf("a change was reported %d times for a refresh that began before the write", n)
	}
	// The queued class 2 refresh the write asked for stores, and reports.
	w.rig.Sched.Drain(ctx)
	row, _ = w.rig.Group("PR_1", groups.Pending)
	if string(row.Content) == "old" || row.Generation != 3 {
		t.Fatalf("after the queued refresh the row = %+v", row)
	}
	if n := w.hooks.count(); n != 1 {
		t.Errorf("hook fired %d times, want 1 (the refresh that began after the write)", n)
	}
}

// ---- Spend ----

func TestEveryOriginCallIsMadeAsAClass2Write(t *testing.T) {
	check := func(t *testing.T, f *submitFixture, wantOps []string) {
		t.Helper()
		for _, want := range wantOps {
			if !contains(f.gh.ops, want) {
				t.Errorf("ops = %v, want %q among them", f.gh.ops, want)
			}
		}
		if len(f.gh.metas) != len(f.gh.ops) || len(f.gh.ops) == 0 {
			t.Fatalf("%d metas for %d ops", len(f.gh.metas), len(f.gh.ops))
		}
		for i, m := range f.gh.metas {
			if m.Class != governor.ClassInvalidated || m.Purpose != governor.PurposeWrite {
				t.Errorf("call %q was made as class %v purpose %q, want class 2 purpose write", f.gh.ops[i], m.Class, m.Purpose)
			}
		}
	}
	t.Run("live head", func(t *testing.T) {
		f := newSubmitFixture(t)
		f.host.addPending(headA, "typed")
		f.mustSubmit(req("overall", point("main.go", 3, "x")))
		check(t, f, []string{"lookup", "update", "write"})
	})
	t.Run("created review", func(t *testing.T) {
		f := newSubmitFixture(t)
		f.mustSubmit(req("overall", point("main.go", 3, "x")))
		check(t, f, []string{"lookup", "create", "write"})
	})
	t.Run("earlier head", func(t *testing.T) {
		f := movedFixture(t)
		f.mustSubmit(req("", point("main.go", 22, "x")))
		check(t, f, []string{"lookup", "history", "compare", "create", "write"})
	})
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// pendingReviewDoc is the GraphQL answer of the Provider's lookup for a PR with no
// review of the identity.
func pendingReviewDoc(head string) []byte {
	return []byte(`{"data":{"rateLimit":{"cost":1,"remaining":4000,"resetAt":"2026-10-13T13:00:00Z"},` +
		`"viewer":{"login":"example-user"},"repository":{"pullRequest":{"headRefOid":"` + head + `",` +
		`"pending":{"totalCount":0,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]},` +
		`"submitted":{"totalCount":0,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]},` +
		`"reviewThreads":{"totalCount":0,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}}`)
}

// fakeInner is the gh the metered runner runs.
type fakeInner struct {
	mu    sync.Mutex
	calls int
	reply func() ([]byte, error)
}

func (f *fakeInner) Run(context.Context, ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.reply()
}

func (f *fakeInner) RunStdin(context.Context, []byte, ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.reply()
}

func (f *fakeInner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// meteredFixture is the handler over the REAL metered Provider and a real
// governor, with a fake gh.
func meteredFixture(t *testing.T, inner *fakeInner) (*submitFixture, *governor.Governor, *[]governor.SpendEvent) {
	t.Helper()
	clock := governor.NewManualClock(time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC))
	var events []governor.SpendEvent
	gov, err := governor.New(ratefeed.GovernorConfig(config.Defaults().Settings), governor.Options{
		Clock: clock, OnSpend: func(e governor.SpendEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &metered.Runner{Inner: inner, Governor: gov, Feed: ratefeed.New(gov, inner, clock)}
	f := newSubmitFixture(t)
	f.handle = Handler(Deps{Engine: f.eng, Provider: metered.NewProvider(runner), Store: f.store, Locker: f.lock})
	return f, gov, &events
}

// The lookup of a real Provider is admitted as class 2 and booked as a write.
func TestTheLookupIsAdmittedAndBookedAsAClass2Write(t *testing.T) {
	inner := &fakeInner{reply: func() ([]byte, error) { return pendingReviewDoc(headA), nil }}
	f, _, events := meteredFixture(t, inner)
	res, err := f.submit(req("", point("main.go", 3, "x"))) // the lookup, then a create that gh refuses to answer
	_ = res
	if inner.count() == 0 {
		t.Fatalf("gh never ran: %v", err)
	}
	if len(*events) == 0 {
		t.Fatal("no spend was booked")
	}
	for i, e := range *events {
		if e.Class != governor.ClassInvalidated || e.Purpose != governor.PurposeWrite || e.Bucket != governor.BucketGraphQL {
			t.Errorf("spend %d = %+v, want a GraphQL point booked as class 2 purpose write", i, e)
		}
	}
}

// A governor refusal answers unavailable naming the cap, before anything is
// written.
func TestAGovernorRefusalAnswersUnavailableBeforeAnythingIsWritten(t *testing.T) {
	inner := &fakeInner{reply: func() ([]byte, error) { return pendingReviewDoc(headA), nil }}
	f, gov, _ := meteredFixture(t, inner)
	bs, ok := gov.State().Bucket(governor.BucketGraphQL)
	if !ok {
		t.Fatal("no graphql bucket")
	}
	// Spend the whole cap: even a class 2 call is now refused.
	if err := gov.Record(governor.Spend{Bucket: governor.BucketGraphQL, Class: governor.ClassInteractive, Purpose: governor.PurposeInteractive, Cost: bs.Cap}); err != nil {
		t.Fatal(err)
	}
	_, err := f.submit(req("overall", point("main.go", 3, "x")))
	isCode(t, err, scriptout.ErrUnavailable)
	var refusal *governor.Refusal
	if !errors.As(err, &refusal) || refusal.Kind != governor.RefusedCap {
		t.Fatalf("err = %v, want the cap refusal behind the unavailable answer", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "cap") {
		t.Errorf("err = %q, want it to name the cap", err)
	}
	if inner.count() != 0 {
		t.Errorf("gh ran %d times for a refused call", inner.count())
	}
	if len(f.eng.invalidations()) != 0 {
		t.Errorf("a refused call invalidated %v", f.eng.invalidations())
	}
	f.untouched()
}
