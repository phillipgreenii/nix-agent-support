package orchestrator

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/roles"
	"github.com/phillipgreenii/pr-pool/internal/usage"
)

// rampReader is a fake usage.Reader that serves a fixed sequence of Snapshots
// (last entry repeats once exhausted). Used to inject a usage ramp into tests.
// Mutex-guarded so it is safe for concurrent use (watchdog goroutine).
type rampReader struct {
	mu  sync.Mutex
	seq []usage.Snapshot
	i   int
}

func (r *rampReader) Read(_ context.Context, _ string) (usage.Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.seq[min(r.i, len(r.seq)-1)]
	r.i++
	return s, nil
}

// fakeCC records calls and serves scripted List results.
// mu guards listIdx so List is safe for concurrent goroutines (workerWaitWithWatchdog).
type fakeCC struct {
	mu          sync.Mutex
	ensured     []string
	sent        []string
	closed      []string
	closedPurge []bool
	sendErr     error
	ensureErr   error
	listSeq     [][]ccpool.Session // one entry consumed per List call (last repeats)
	listIdx     int
}

func (f *fakeCC) Ensure(_ context.Context, externalID, _, _ string, _ map[string]string) error {
	f.ensured = append(f.ensured, externalID)
	return f.ensureErr
}
func (f *fakeCC) Send(_ context.Context, externalID, _ string, _ ccpool.SendMode) error {
	f.sent = append(f.sent, externalID)
	return f.sendErr
}
func (f *fakeCC) Cancel(_ context.Context, _ string) error { return nil }
func (f *fakeCC) Close(_ context.Context, externalID string, purge bool) error {
	f.closed = append(f.closed, externalID)
	f.closedPurge = append(f.closedPurge, purge)
	return nil
}
func (f *fakeCC) List(_ context.Context) ([]ccpool.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.listSeq) == 0 {
		return nil, nil
	}
	i := f.listIdx
	if i >= len(f.listSeq) {
		i = len(f.listSeq) - 1
	}
	f.listIdx++
	return f.listSeq[i], nil
}

// scriptBD serves a status sequence per bead id and records update calls.
// mu guards shared state so Run is safe for concurrent goroutines
// (workerWaitWithWatchdog runs waitDone + watchdog in parallel; both call BD.Run).
type scriptBD struct {
	mu          sync.Mutex
	statusSeq   map[string][]string
	idx         map[string]int
	updates     []string
	ready       map[string]string // keyed by "feedback"/"worker"
	readyErr    error             // if set, every `bd ready` returns this error
	show        map[string]string
	showErrOnce map[string]error // returns error once per id, then clears
}

func (s *scriptBD) Run(_ context.Context, args ...string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch args[0] {
	case "ready":
		if s.readyErr != nil {
			return "", s.readyErr
		}
		if contains(args, "worker-ready") {
			return s.ready["worker"], nil
		}
		return s.ready["feedback"], nil
	case "show":
		id := args[1]
		if v, ok := s.show[id]; ok {
			return v, nil
		}
		// one-shot error injection for status reads
		if s.showErrOnce != nil {
			if err, ok := s.showErrOnce[id]; ok {
				delete(s.showErrOnce, id)
				return "", err
			}
		}
		// status sequence
		if s.idx == nil {
			s.idx = map[string]int{}
		}
		seq := s.statusSeq[id]
		i := s.idx[id]
		if i >= len(seq) {
			i = len(seq) - 1
		}
		s.idx[id]++
		return `{"id":"` + id + `","status":"` + seq[i] + `"}`, nil
	case "update":
		s.updates = append(s.updates, join(args))
	}
	return "", nil
}

func contains(a []string, x string) bool {
	for _, v := range a {
		if v == x {
			return true
		}
	}
	return false
}
func join(a []string) string {
	out := ""
	for i, x := range a {
		if i > 0 {
			out += " "
		}
		out += x
	}
	return out
}

// manualClock advances only when the test ticks it, so waitDone polling is
// deterministic and instant.
// mu guards t so it is safe for concurrent use when workerWaitWithWatchdog runs
// waitDone (which advances via tick) and the watchdog (which reads via Now)
// in parallel goroutines.
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *manualClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// tickAdvancing returns a tick func that advances the clock by d each poll, so a
// finite-deadline loop terminates without real sleeping.
func (c *manualClock) tickAdvancing() func(context.Context, time.Duration) error {
	return func(ctx context.Context, d time.Duration) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.mu.Lock()
		c.t = c.t.Add(d)
		c.mu.Unlock()
		return nil
	}
}

func newOrch(cc ccpool.Runner, bd *scriptBD, cfg config.Config) *Orchestrator {
	o := &Orchestrator{CC: cc, BD: bd, Reg: roles.NewRegistry(cfg), Cfg: cfg}
	clk := &manualClock{t: time.Unix(0, 0)}
	o.now = clk.now
	o.tick = clk.tickAdvancing()
	return o
}

func fastCfg() config.Config {
	c := config.Default()
	c.MaxWait = 50 * time.Millisecond
	c.PollInterval = time.Millisecond
	return c
}

// --- waitDone scenarios (ports bats: wait_done cases) ---

func TestWaitDone_workerCloses(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.DispatchContext{Role: o.Reg.Worker, BeadID: "zr-w"}
	if err := o.waitDone(context.Background(), nil, d, "pr-pool-worker-zr-w"); err != nil {
		t.Fatalf("expected success, got %v; updates=%v", err, bd.updates)
	}
	if len(bd.updates) != 0 {
		t.Errorf("success must not unclaim/human; updates=%v", bd.updates)
	}
}

func TestWaitDone_workerHandbackToOpen(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress", "open"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.DispatchContext{Role: o.Reg.Worker, BeadID: "zr-w"}
	if err := o.waitDone(context.Background(), nil, d, "pr-pool-worker-zr-w"); err != nil {
		t.Fatalf("handback after seen_claimed should be success, got %v", err)
	}
}

func TestWaitDone_workerTimeoutAddsHumanNoUnclaim(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.DispatchContext{Role: o.Reg.Worker, BeadID: "zr-w"}
	if err := o.waitDone(context.Background(), nil, d, "pr-pool-worker-zr-w"); err == nil {
		t.Fatal("timeout should be failure")
	}
	if !hasUpdate(bd, "update zr-w --add-label human") || hasUpdate(bd, "--status=open") {
		t.Errorf("worker timeout must add human and not unclaim; updates=%v", bd.updates)
	}
}

func TestWaitDone_paneDiesAsBeadCloses_success(t *testing.T) {
	// first poll: in_progress + live; second poll: status reads closed AND session not live.
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{
		{{Name: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}},
		{{Name: "pr-pool-worker-zr-w", Live: false, State: ccpool.StateIdle}},
	}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.DispatchContext{Role: o.Reg.Worker, BeadID: "zr-w"}
	if err := o.waitDone(context.Background(), nil, d, "pr-pool-worker-zr-w"); err != nil {
		t.Fatalf("bead closed as pane died = success, got %v", err)
	}
	if len(bd.updates) != 0 {
		t.Errorf("must not flag on race-success; updates=%v", bd.updates)
	}
}

func TestWaitDone_paneDiesStillInProgress_failure(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{
		{{Name: "pr-pool-worker-zr-w", Live: false, State: ccpool.StateErrored}},
	}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.DispatchContext{Role: o.Reg.Worker, BeadID: "zr-w"}
	if err := o.waitDone(context.Background(), nil, d, "pr-pool-worker-zr-w"); err == nil {
		t.Fatal("dead session + in_progress = failure")
	}
	if !hasUpdate(bd, "update zr-w --add-label human") {
		t.Errorf("must add human; updates=%v", bd.updates)
	}
}

func TestWaitDone_feedbackTimeoutUnclaims(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-feedback-processor-zr-c", Live: true, State: ccpool.StateWorking}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.DispatchContext{Role: o.Reg.Feedback, BeadID: "zr-c"}
	if err := o.waitDone(context.Background(), nil, d, "pr-pool-feedback-processor-zr-c"); err == nil {
		t.Fatal("timeout should fail")
	}
	if !hasUpdate(bd, "update zr-c --status=open --assignee=") {
		t.Errorf("feedback timeout must unclaim; updates=%v", bd.updates)
	}
}

// --- DrainOnce scenarios (ports bats: drain_once cases) ---

func TestDrainOnce_gatedNoTeardown(t *testing.T) {
	f, _ := writeTemp(t) // creates a sentinel file
	cfg := fastCfg()
	cfg.QuotaPaused = f
	bd := &scriptBD{ready: map[string]string{"feedback": "[]", "worker": "[]"}}
	cc := &fakeCC{}
	o := newOrch(cc, bd, cfg)
	if err := o.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(cc.ensured) != 0 || len(cc.closed) != 0 {
		t.Errorf("gated pass must not dispatch or teardown; ensured=%v closed=%v", cc.ensured, cc.closed)
	}
}

func TestDrainOnce_workerCapZeroSkips(t *testing.T) {
	cfg := fastCfg()
	cfg.MaxWorker = 0
	bd := &scriptBD{ready: map[string]string{"feedback": "[]", "worker": `[{"id":"zr-w"}]`}}
	cc := &fakeCC{}
	o := newOrch(cc, bd, cfg)
	if err := o.DrainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, n := range cc.ensured {
		if n == "pr-pool-worker-zr-w" {
			t.Errorf("cap=0 should skip worker; ensured=%v", cc.ensured)
		}
	}
}

func TestDrainOnce_capStopsAtOne(t *testing.T) {
	cfg := fastCfg()
	bd := &scriptBD{
		ready:     map[string]string{"feedback": "[]", "worker": `[{"id":"zr-w1"},{"id":"zr-w2"}]`},
		statusSeq: map[string][]string{"zr-w1": {"in_progress", "closed"}, "zr-w2": {"in_progress", "closed"}},
	}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{
		{Name: "pr-pool-worker-zr-w1", Live: true}, {Name: "pr-pool-worker-zr-w2", Live: true},
	}}}
	o := newOrch(cc, bd, cfg)
	_ = o.DrainOnce(context.Background())
	if len(cc.sent) != 1 {
		t.Errorf("MAX_WORKER=1 should dispatch one worker, sent=%v", cc.sent)
	}
}

func TestDrainOnce_noStarvation(t *testing.T) {
	cfg := fastCfg()
	bd := &scriptBD{
		ready: map[string]string{
			"feedback": `[{"id":"zr-c","issue_type":"task","title":"process-feedback: x","parent":"zr-p"}]`,
			"worker":   `[{"id":"zr-w"}]`,
		},
		show:      map[string]string{"zr-p": `{"id":"zr-p","metadata":{"author":"phillipg"}}`},
		statusSeq: map[string][]string{"zr-c": {"in_progress", "closed"}, "zr-w": {"in_progress", "closed"}},
	}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{
		{Name: "pr-pool-feedback-processor-zr-c", Live: true}, {Name: "pr-pool-worker-zr-w", Live: true},
	}}}
	o := newOrch(cc, bd, cfg)
	_ = o.DrainOnce(context.Background())
	if len(cc.sent) != 2 {
		t.Errorf("one of each role should be worked, sent=%v", cc.sent)
	}
}

func TestDrainOnce_teardownReapsStrays(t *testing.T) {
	cfg := fastCfg()
	bd := &scriptBD{ready: map[string]string{"feedback": "[]", "worker": "[]"}}
	// a stray session from a prior crashed run remains in the list
	cc := &fakeCC{listSeq: [][]ccpool.Session{{
		{ExternalID: "pr-pool-worker-zr-stray", Live: true},
		{ExternalID: "cc-unrelated", Live: true},
	}}}
	o := newOrch(cc, bd, cfg)
	_ = o.DrainOnce(context.Background())
	if !contains(cc.closed, "pr-pool-worker-zr-stray") {
		t.Errorf("teardown must reap pr-pool- strays; closed=%v", cc.closed)
	}
	if contains(cc.closed, "cc-unrelated") {
		t.Errorf("teardown must NOT close non-pr-pool sessions; closed=%v", cc.closed)
	}
}

func TestWorkOne_sendFailFeedbackUnclaims(t *testing.T) {
	cfg := fastCfg()
	bd := &scriptBD{}
	cc := &fakeCC{sendErr: errSend}
	o := newOrch(cc, bd, cfg)
	d := discover.DispatchContext{Role: o.Reg.Feedback, BeadID: "zr-c"}
	_ = o.workOne(context.Background(), d)
	if !hasUpdate(bd, "update zr-c --status=open --assignee=") {
		t.Errorf("feedback send-fail must unclaim; updates=%v", bd.updates)
	}
}

func TestWorkOne_sendFailWorkerNotUnclaimed(t *testing.T) {
	cfg := fastCfg()
	bd := &scriptBD{}
	cc := &fakeCC{sendErr: errSend}
	o := newOrch(cc, bd, cfg)
	d := discover.DispatchContext{Role: o.Reg.Worker, BeadID: "zr-w"}
	_ = o.workOne(context.Background(), d)
	if hasUpdate(bd, "--status=open") {
		t.Errorf("worker send-fail must NOT unclaim; updates=%v", bd.updates)
	}
}

// pg2-c1vp / review follow-up: a worker that completes successfully while the
// budget watchdog is armed (but never trips) must return success with NO bead
// mutation — waitDone wins the terminal claim and the watchdog is the loser via
// cancellation, touching nothing. Exercises the workerWaitWithWatchdog happy
// path (the prior watchdog test only covered the watchdog-WINS case).
func TestWorkOne_workerSuccessWithWatchdogArmed(t *testing.T) {
	cfg := fastCfg()
	cfg.BudgetTokens = 1_000_000 // armed, but usage stays far below the cap
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress", "closed"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking, TranscriptPath: "/t"}}}}
	o := newOrch(cc, bd, cfg)
	o.usageReader = &rampReader{seq: []usage.Snapshot{{OutputTokens: 10}}} // nowhere near 1,000,000
	d := discover.DispatchContext{Role: o.Reg.Worker, BeadID: "zr-w"}
	if err := o.workOne(context.Background(), d); err != nil {
		t.Fatalf("worker that closes its bead should succeed, got %v; updates=%v", err, bd.updates)
	}
	if len(bd.updates) != 0 {
		t.Errorf("success must not unclaim/human/comment; updates=%v", bd.updates)
	}
}

func hasUpdate(bd *scriptBD, sub string) bool {
	for _, u := range bd.updates {
		if u == sub {
			return true
		}
	}
	return false
}

var errSend = errors.New("send failed")

// writeTemp creates a sentinel file and returns its path (+ a no-op cleanup;
// t.TempDir() handles removal). Used by the gated-pass test.
func writeTemp(t *testing.T) (string, func()) {
	t.Helper()
	p := t.TempDir() + "/sentinel"
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p, func() {}
}

func TestWaitDone_transientStatusErrorKeepsPolling(t *testing.T) {
	// First show call for "zr-w" errors; subsequent calls return "closed".
	bd := &scriptBD{
		statusSeq:   map[string][]string{"zr-w": {"closed"}},
		showErrOnce: map[string]error{"zr-w": errors.New("bd: transient error")},
	}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.DispatchContext{Role: o.Reg.Worker, BeadID: "zr-w"}
	if err := o.waitDone(context.Background(), nil, d, "pr-pool-worker-zr-w"); err != nil {
		t.Fatalf("transient status error should not flag bead; got err=%v; updates=%v", err, bd.updates)
	}
	if len(bd.updates) != 0 {
		t.Errorf("transient error must not trigger human/unclaim; updates=%v", bd.updates)
	}
}

func TestWaitDone_ctxCancelDoesNotFail(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-worker-zr-w", Live: true}}}}
	o := newOrch(cc, bd, fastCfg())
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	d := discover.DispatchContext{Role: o.Reg.Worker, BeadID: "zr-w"}
	err := o.waitDone(ctx, nil, d, "pr-pool-worker-zr-w")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if len(bd.updates) != 0 {
		t.Errorf("cancellation must NOT run a failure action; updates=%v", bd.updates)
	}
}

// TestWaitDone_ctxCancelledBeforeDeathPathNoFail covers the structural
// single-terminal guard (Fix 2): when ctx is already cancelled AND the session
// is reported NOT live AND bead status is in_progress (the death path), waitDone
// must return ctx.Err() and run NO failure action (no bead update).
func TestWaitDone_ctxCancelledBeforeDeathPathNoFail(t *testing.T) {
	// Session is not live on the very first List call — the death path triggers.
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{
		{{Name: "pr-pool-worker-zr-w", Live: false, State: ccpool.StateErrored}},
	}}
	o := newOrch(cc, bd, fastCfg())
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before waitDone is called (watchdog already won)
	d := discover.DispatchContext{Role: o.Reg.Worker, BeadID: "zr-w"}
	err := o.waitDone(ctx, nil, d, "pr-pool-worker-zr-w")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled on cancelled-ctx death path, got %v", err)
	}
	if len(bd.updates) != 0 {
		t.Errorf("cancelled-ctx death path must NOT run o.fail; updates=%v", bd.updates)
	}
}

// pg2-c1vp: when the watchdog wins the single-terminal race, waitDone (the
// loser) must run NO failure action — otherwise the bead ends up both unclaimed
// (watchdog) AND human-labeled (waitDone). An injected claimTerminal that always
// loses drives the death path; it must mutate nothing and exit with ctx.Err().
func TestWaitDone_lostRace_deathPathNoFail(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{
		{{Name: "pr-pool-worker-zr-w", Live: false, State: ccpool.StateErrored}},
	}}
	o := newOrch(cc, bd, fastCfg())
	ctx, cancel := context.WithCancel(context.Background())
	claimed := make(chan struct{}, 1)
	claim := func() bool { claimed <- struct{}{}; return false } // always lose the claim
	d := discover.DispatchContext{Role: o.Reg.Worker, BeadID: "zr-w"}
	resCh := make(chan error, 1)
	go func() { resCh <- o.waitDone(ctx, claim, d, "pr-pool-worker-zr-w") }()
	<-claimed // waitDone reached its terminal decision and lost
	cancel()  // release the loser
	if err := <-resCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("loser should return ctx.Err(), got %v", err)
	}
	if len(bd.updates) != 0 {
		t.Errorf("loser must not mutate the bead; updates=%v", bd.updates)
	}
}

// pg2-c1vp: the watchdog's terminal unclaim sets the bead to open; waitDone must
// NOT misread that as a successful "open" hand-back when it lost the race (else a
// budget hard-stop is reported as success). A lost "open" must yield ctx.Err().
func TestWaitDone_lostRace_openNotReportedSuccess(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress", "open"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	o := newOrch(cc, bd, fastCfg())
	ctx, cancel := context.WithCancel(context.Background())
	claimed := make(chan struct{}, 1)
	claim := func() bool { claimed <- struct{}{}; return false }
	d := discover.DispatchContext{Role: o.Reg.Worker, BeadID: "zr-w"}
	resCh := make(chan error, 1)
	go func() { resCh <- o.waitDone(ctx, claim, d, "pr-pool-worker-zr-w") }()
	<-claimed
	cancel()
	if err := <-resCh; err == nil {
		t.Fatal("a lost 'open' hand-back must NOT be reported as success (nil)")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("loser should return ctx.Err(), got %v", err)
	}
	if len(bd.updates) != 0 {
		t.Errorf("loser must not mutate the bead; updates=%v", bd.updates)
	}
}

func TestDrainOnce_teardownRunsOnDiscoverError(t *testing.T) {
	cfg := fastCfg()
	// a bd ready failure makes Discover return an error
	bd := &scriptBD{readyErr: errors.New("bd ready failed")}
	// a stray pr-pool session exists from a prior run
	cc := &fakeCC{listSeq: [][]ccpool.Session{{
		{ExternalID: "pr-pool-worker-zr-stray", Live: true},
	}}}
	o := newOrch(cc, bd, cfg)
	if err := o.DrainOnce(context.Background()); err == nil {
		t.Fatal("a bd ready failure should return an error from DrainOnce")
	}
	if !contains(cc.closed, "pr-pool-worker-zr-stray") {
		t.Errorf("teardown must run even on discover error; closed=%v", cc.closed)
	}
}

// TestWorkOne_workerBudgetHardStopUnclaimsNoHuman verifies that when the budget
// watchdog fires a hard stop for a worker dispatch: workOne returns a budget
// error, the bead is left open+unclaimed, and the human label is NOT added.
func TestWorkOne_workerBudgetHardStopUnclaimsNoHuman(t *testing.T) {
	cfg := fastCfg()
	cfg.BudgetTokens = 1000                                                  // finite token cap so the ramp can trip it
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress"}}} // never completes on its own
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-worker-zr-w", Live: true, TranscriptPath: "/t", CWD: "/repo"}}}}
	o := newOrch(cc, bd, cfg)
	o.usageReader = &rampReader{seq: []usage.Snapshot{{OutputTokens: 2000}}} // immediately over 100%
	d := discover.DispatchContext{Role: o.Reg.Worker, BeadID: "zr-w"}
	err := o.workOne(context.Background(), d)
	if err == nil {
		t.Fatal("expected a budget error")
	}
	if !hasUpdate(bd, "update zr-w --status=open --assignee=") {
		t.Errorf("hard stop must unclaim; updates=%v", bd.updates)
	}
	if hasUpdate(bd, "update zr-w --add-label human") {
		t.Error("hard stop must NOT add human")
	}
}

func TestWaitDone_workerDoneStopsFast_failure(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateIdle}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.DispatchContext{Role: o.Reg.Worker, BeadID: "zr-w"}
	if err := o.waitDone(context.Background(), nil, d, "pr-pool-worker-zr-w"); err == nil {
		t.Fatal("done + not-closed should fail")
	}
	if !hasUpdate(bd, "update zr-w --add-label human") {
		t.Errorf("worker done-without-close must add human; updates=%v", bd.updates)
	}
	// active() is the ONLY List caller in waitDone, so a single List call proves
	// the loop stopped on the first check instead of polling to MaxWait.
	if cc.listIdx != 1 {
		t.Errorf("done must stop on first check (listIdx=1), got %d (looped to MaxWait?)", cc.listIdx)
	}
}

func TestWaitDone_feedbackDoneStopsFast_unclaims(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-feedback-processor-zr-c", Live: true, State: ccpool.StateIdle}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.DispatchContext{Role: o.Reg.Feedback, BeadID: "zr-c"}
	if err := o.waitDone(context.Background(), nil, d, "pr-pool-feedback-processor-zr-c"); err == nil {
		t.Fatal("done + not-closed should fail")
	}
	if !hasUpdate(bd, "update zr-c --status=open --assignee=") {
		t.Errorf("feedback done-without-close must unclaim; updates=%v", bd.updates)
	}
	if cc.listIdx != 1 {
		t.Errorf("done must stop on first check (listIdx=1), got %d", cc.listIdx)
	}
}

// Regression guard (passes before AND after the fix): a session that reaches done
// in the same instant its bead closes must still be a SUCCESS via the re-check.
func TestWaitDone_doneStopsFast_successRace(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-c": {"in_progress", "closed"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-feedback-processor-zr-c", Live: true, State: ccpool.StateIdle}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.DispatchContext{Role: o.Reg.Feedback, BeadID: "zr-c"}
	if err := o.waitDone(context.Background(), nil, d, "pr-pool-feedback-processor-zr-c"); err != nil {
		t.Fatalf("bead closed as the turn ended = success, got %v", err)
	}
	if len(bd.updates) != 0 {
		t.Errorf("success must not unclaim/flag; updates=%v", bd.updates)
	}
}

// Lock-in (passes before AND after): needs_input is NOT terminal — a human may
// attach. The loop must keep waiting to MaxWait, then time out and apply OnFailure.
func TestWaitDone_needsInputWaitsUntilMaxWait(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-feedback-processor-zr-c", Live: true, State: ccpool.StateNeedsInput}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.DispatchContext{Role: o.Reg.Feedback, BeadID: "zr-c"}
	if err := o.waitDone(context.Background(), nil, d, "pr-pool-feedback-processor-zr-c"); err == nil {
		t.Fatal("needs_input that never resolves should time out (failure)")
	}
	if !hasUpdate(bd, "update zr-c --status=open --assignee=") {
		t.Errorf("needs_input timeout must apply OnFailure (feedback unclaim); updates=%v", bd.updates)
	}
	if cc.listIdx < 10 {
		t.Errorf("needs_input must keep waiting to MaxWait; listIdx=%d (stopped early?)", cc.listIdx)
	}
}

func TestRunOne_feedbackClosesSession(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-c": {"closed"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-feedback-processor-zr-c", Live: true, State: ccpool.StateWorking}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.DispatchContext{Role: o.Reg.Feedback, BeadID: "zr-c"}
	if err := o.RunOne(context.Background(), d); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if !contains(cc.ensured, "pr-pool-feedback-processor-zr-c") {
		t.Errorf("RunOne must Ensure the session; ensured=%v", cc.ensured)
	}
	if !contains(cc.closed, "pr-pool-feedback-processor-zr-c") {
		t.Errorf("RunOne must close its one session; closed=%v", cc.closed)
	}
}

func TestRunOne_doneWithoutCloseFlagsAndCloses(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-c": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-feedback-processor-zr-c", Live: true, State: ccpool.StateIdle}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.DispatchContext{Role: o.Reg.Feedback, BeadID: "zr-c"}
	if err := o.RunOne(context.Background(), d); err == nil {
		t.Fatal("done-without-close should fail")
	}
	if !hasUpdate(bd, "update zr-c --status=open --assignee=") {
		t.Errorf("feedback failure must unclaim; updates=%v", bd.updates)
	}
	if !contains(cc.closed, "pr-pool-feedback-processor-zr-c") {
		t.Errorf("RunOne must still close its session on failure; closed=%v", cc.closed)
	}
}

func TestActive_stateMapping(t *testing.T) {
	cases := []struct {
		name string
		sess []ccpool.Session
		want bool
	}{
		{"working-live", []ccpool.Session{{Name: "s", Live: true, State: ccpool.StateWorking}}, true},
		{"needs_input-live", []ccpool.Session{{Name: "s", Live: true, State: ccpool.StateNeedsInput}}, true},
		{"done-live", []ccpool.Session{{Name: "s", Live: true, State: ccpool.StateIdle}}, false},
		{"failed-live", []ccpool.Session{{Name: "s", Live: true, State: ccpool.StateErrored}}, false},
		{"working-not-live", []ccpool.Session{{Name: "s", Live: false, State: ccpool.StateWorking}}, false},
		{"absent", []ccpool.Session{{Name: "other", Live: true, State: ccpool.StateWorking}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newOrch(&fakeCC{listSeq: [][]ccpool.Session{tc.sess}}, &scriptBD{}, fastCfg())
			if got := o.active(context.Background(), "s"); got != tc.want {
				t.Errorf("active(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
