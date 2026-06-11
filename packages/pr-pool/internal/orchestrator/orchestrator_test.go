package orchestrator

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// fakeCC records calls and serves scripted List results.
type fakeCC struct {
	ensured []string
	sent    []string
	closed  []string
	sendErr error
	listSeq [][]ccpool.Session // one entry consumed per List call (last repeats)
	listIdx int
}

func (f *fakeCC) Ensure(_ context.Context, name, _ string, _ map[string]string) error {
	f.ensured = append(f.ensured, name)
	return nil
}
func (f *fakeCC) Send(_ context.Context, name, _ string, _ ccpool.SendMode) error {
	f.sent = append(f.sent, name)
	return f.sendErr
}
func (f *fakeCC) Cancel(_ context.Context, _ string) error { return nil }
func (f *fakeCC) Close(_ context.Context, name string) error {
	f.closed = append(f.closed, name)
	return nil
}
func (f *fakeCC) List(_ context.Context) ([]ccpool.Session, error) {
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
type scriptBD struct {
	statusSeq map[string][]string
	idx       map[string]int
	updates   []string
	ready     map[string]string // keyed by "feedback"/"worker"
	show      map[string]string
}

func (s *scriptBD) Run(_ context.Context, args ...string) (string, error) {
	switch args[0] {
	case "ready":
		if contains(args, "--label") {
			return s.ready["worker"], nil
		}
		return s.ready["feedback"], nil
	case "show":
		id := args[1]
		if v, ok := s.show[id]; ok {
			return v, nil
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

func newOrch(cc ccpool.Runner, bd *scriptBD, cfg config.Config) *Orchestrator {
	o := &Orchestrator{CC: cc, BD: bd, Reg: roles.NewRegistry(cfg), Cfg: cfg}
	o.sleep = func(time.Duration) {} // instant polling in tests
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
	d := discover.Dispatch{Role: o.Reg.Worker, BeadID: "zr-w"}
	if err := o.waitDone(context.Background(), d, "pr-pool-worker-zr-w"); err != nil {
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
	d := discover.Dispatch{Role: o.Reg.Worker, BeadID: "zr-w"}
	if err := o.waitDone(context.Background(), d, "pr-pool-worker-zr-w"); err != nil {
		t.Fatalf("handback after seen_claimed should be success, got %v", err)
	}
}

func TestWaitDone_workerTimeoutAddsHumanNoUnclaim(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{{{Name: "pr-pool-worker-zr-w", Live: true, State: ccpool.StateWorking}}}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.Dispatch{Role: o.Reg.Worker, BeadID: "zr-w"}
	if err := o.waitDone(context.Background(), d, "pr-pool-worker-zr-w"); err == nil {
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
		{{Name: "pr-pool-worker-zr-w", Live: false, State: ccpool.StateDone}},
	}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.Dispatch{Role: o.Reg.Worker, BeadID: "zr-w"}
	if err := o.waitDone(context.Background(), d, "pr-pool-worker-zr-w"); err != nil {
		t.Fatalf("bead closed as pane died = success, got %v", err)
	}
	if len(bd.updates) != 0 {
		t.Errorf("must not flag on race-success; updates=%v", bd.updates)
	}
}

func TestWaitDone_paneDiesStillInProgress_failure(t *testing.T) {
	bd := &scriptBD{statusSeq: map[string][]string{"zr-w": {"in_progress"}}}
	cc := &fakeCC{listSeq: [][]ccpool.Session{
		{{Name: "pr-pool-worker-zr-w", Live: false, State: ccpool.StateFailed}},
	}}
	o := newOrch(cc, bd, fastCfg())
	d := discover.Dispatch{Role: o.Reg.Worker, BeadID: "zr-w"}
	if err := o.waitDone(context.Background(), d, "pr-pool-worker-zr-w"); err == nil {
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
	d := discover.Dispatch{Role: o.Reg.Feedback, BeadID: "zr-c"}
	if err := o.waitDone(context.Background(), d, "pr-pool-feedback-processor-zr-c"); err == nil {
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
	if err := o.DrainOnce(context.Background(), "phillipg"); err != nil {
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
	if err := o.DrainOnce(context.Background(), "phillipg"); err != nil {
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
	_ = o.DrainOnce(context.Background(), "phillipg")
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
	_ = o.DrainOnce(context.Background(), "phillipg")
	if len(cc.sent) != 2 {
		t.Errorf("one of each role should be worked, sent=%v", cc.sent)
	}
}

func TestDrainOnce_teardownReapsStrays(t *testing.T) {
	cfg := fastCfg()
	bd := &scriptBD{ready: map[string]string{"feedback": "[]", "worker": "[]"}}
	// a stray session from a prior crashed run remains in the list
	cc := &fakeCC{listSeq: [][]ccpool.Session{{
		{Name: "pr-pool-worker-zr-stray", Live: true},
		{Name: "cc-unrelated", Live: true},
	}}}
	o := newOrch(cc, bd, cfg)
	_ = o.DrainOnce(context.Background(), "phillipg")
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
	d := discover.Dispatch{Role: o.Reg.Feedback, BeadID: "zr-c"}
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
	d := discover.Dispatch{Role: o.Reg.Worker, BeadID: "zr-w"}
	_ = o.workOne(context.Background(), d)
	if hasUpdate(bd, "--status=open") {
		t.Errorf("worker send-fail must NOT unclaim; updates=%v", bd.updates)
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
