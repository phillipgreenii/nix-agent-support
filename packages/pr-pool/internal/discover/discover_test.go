package discover

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// routingRunner answers bd `ready` calls based on the label in argv.
type routingRunner struct {
	readyFeedback   string // JSON for `bd ready --label mine`
	readyWorker     string // JSON for `bd ready --label worker-ready ...`
	sawFeedbackArgs []string
	sawWorkerArgs   []string
	readyErr        error // if set, returned from any "ready" branch
}

func (r *routingRunner) Run(_ context.Context, args ...string) (string, error) {
	switch args[0] {
	case "ready":
		if r.readyErr != nil {
			return "", r.readyErr
		}
		if contains(args, "worker-ready") {
			r.sawWorkerArgs = args
			return r.readyWorker, nil
		}
		r.sawFeedbackArgs = args // feedback carries --label mine
		return r.readyFeedback, nil
	}
	return "", nil
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func TestDiscover_feedbackByLabel(t *testing.T) {
	rr := &routingRunner{
		readyFeedback: `[
			{"id":"zr-c1","issue_type":"task","title":"process-feedback: A"},
			{"id":"zr-nottask","issue_type":"feature","title":"process-feedback: B"},
			{"id":"zr-nofb","issue_type":"task","title":"some other task"}
		]`,
		readyWorker: `[]`,
	}
	reg := roles.NewRegistry(config.Default())
	got, err := Discover(context.Background(), rr, reg)
	if err != nil {
		t.Fatal(err)
	}
	// Only the task whose title has the cycle prefix survives the type/title guard.
	if len(got) != 1 || got[0].BeadID != "zr-c1" || got[0].Role.Kind != roles.Feedback {
		t.Fatalf("feedback discovery = %+v (want only zr-c1)", got)
	}
	// The feedback query must be `bd ready --label mine` — no parent `bd show` —
	// AND must exclude human-labeled beads, so a feedback cycle escalated to a
	// human is not rediscovered forever (mirrors the worker exclusion).
	a := strings.Join(rr.sawFeedbackArgs, " ")
	for _, sub := range []string{"--label mine", "--exclude-label human"} {
		if !strings.Contains(a, sub) {
			t.Fatalf("feedback bd ready missing %q; got %q", sub, a)
		}
	}
}

// TestDiscover_feedbackExcludesHuman locks in IMPORTANT #4: a human-labeled
// feedback cycle must be filtered by the bd query itself (--exclude-label
// human), exactly as the worker query already does. Without it, a feedback bead
// escalated to a human is rediscovered and re-dispatched on every pass.
func TestDiscover_feedbackExcludesHuman(t *testing.T) {
	rr := &routingRunner{
		readyFeedback: `[{"id":"zr-c1","issue_type":"task","title":"process-feedback: A"}]`,
		readyWorker:   `[]`,
	}
	reg := roles.NewRegistry(config.Default())
	if _, err := Discover(context.Background(), rr, reg); err != nil {
		t.Fatal(err)
	}
	if !contains(rr.sawFeedbackArgs, "--exclude-label") || !contains(rr.sawFeedbackArgs, "human") {
		t.Errorf("feedback bd ready must carry `--exclude-label human`; got %v", rr.sawFeedbackArgs)
	}
}

func TestDiscover_workerLabelFilter(t *testing.T) {
	rr := &routingRunner{
		readyFeedback: `[]`,
		readyWorker:   `[{"id":"zr-w1"},{"id":"zr-w2"}]`,
	}
	reg := roles.NewRegistry(config.Default())
	got, err := Discover(context.Background(), rr, reg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].BeadID != "zr-w1" || got[0].Role.Kind != roles.Worker {
		t.Fatalf("worker discovery = %+v", got)
	}
	// the worker query must carry the native label filters
	a := strings.Join(rr.sawWorkerArgs, " ")
	for _, sub := range []string{"--label worker-ready", "--exclude-label human"} {
		if !strings.Contains(a, sub) {
			t.Errorf("worker bd ready missing %q; got %q", sub, a)
		}
	}
}

func TestDiscover_skipsDisabledRole(t *testing.T) {
	rr := &routingRunner{
		readyFeedback: `[{"id":"zr-mine","issue_type":"task","title":"process-feedback: A"}]`,
		readyWorker:   `[{"id":"zr-w1"}]`,
	}
	cfg := config.Default()
	cfg.WorkerEnabled = false // worker disabled: its ready bead must be skipped
	reg := roles.NewRegistry(cfg)
	got, err := Discover(context.Background(), rr, reg)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range got {
		if d.Role.Kind == roles.Worker {
			t.Errorf("disabled worker role should yield no dispatches; got %+v", got)
		}
	}
	if len(got) != 1 || got[0].BeadID != "zr-mine" {
		t.Fatalf("expected only the feedback dispatch; got %+v", got)
	}
	if rr.sawWorkerArgs != nil {
		t.Errorf("disabled worker role must not even query bd ready; saw %v", rr.sawWorkerArgs)
	}
}

func TestDiscover_orderFeedbackThenWorker(t *testing.T) {
	rr := &routingRunner{
		readyFeedback: `[{"id":"zr-c","issue_type":"task","title":"process-feedback: x"}]`,
		readyWorker:   `[{"id":"zr-w"}]`,
	}
	reg := roles.NewRegistry(config.Default())
	got, _ := Discover(context.Background(), rr, reg)
	if len(got) != 2 || got[0].Role.Kind != roles.Feedback || got[1].Role.Kind != roles.Worker {
		t.Fatalf("order wrong: %+v", got)
	}
}

// pg2-qq9v: a bd query failure must NOT look like "no ready work". Returning
// nil,nil there made the pool silently idle on infra failure. The error must
// propagate so the drain fails loudly instead of doing nothing.
func TestDiscover_propagatesReadyError(t *testing.T) {
	sentinel := errors.New("bd: connection refused")
	rr := &routingRunner{readyErr: sentinel}
	reg := roles.NewRegistry(config.Default())
	got, err := Discover(context.Background(), rr, reg)
	if err == nil {
		t.Fatal("bd ready failure must propagate, not be swallowed as 'no work'")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("propagated error should wrap the bd error; got %v", err)
	}
	if got != nil {
		t.Errorf("on error, dispatches must be nil; got %v", got)
	}
}

// The worker query failing must likewise propagate (feedback disabled so the
// worker branch is the one that errors).
func TestDiscover_propagatesWorkerReadyError(t *testing.T) {
	sentinel := errors.New("bd: store offline")
	rr := &routingRunner{readyErr: sentinel}
	cfg := config.Default()
	cfg.FeedbackEnabled = false // skip feedback so the worker branch hits the error
	reg := roles.NewRegistry(cfg)
	if _, err := Discover(context.Background(), rr, reg); !errors.Is(err, sentinel) {
		t.Fatalf("worker bd ready failure must propagate; got %v", err)
	}
}

func TestForRole_feedbackBypassesEnabled(t *testing.T) {
	rr := &routingRunner{
		readyFeedback: `[{"id":"zr-c","issue_type":"task","title":"process-feedback: x"}]`,
	}
	cfg := config.Default()
	cfg.FeedbackEnabled = false // ForRole must run the query regardless of Enabled
	reg := roles.NewRegistry(cfg)
	got, err := ForRole(context.Background(), rr, reg.Feedback)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].BeadID != "zr-c" {
		t.Fatalf("ForRole(feedback) = %+v (want zr-c even though disabled)", got)
	}
}

func TestForRole_unknownKindErrors(t *testing.T) {
	rr := &routingRunner{}
	if _, err := ForRole(context.Background(), rr, roles.Role{Name: "bogus", Kind: 999}); err == nil {
		t.Fatal("ForRole with unknown kind must error")
	}
}

func TestDispatchContext_Validate(t *testing.T) {
	reg := roles.NewRegistry(config.Default())
	cases := []struct {
		name     string
		d        DispatchContext
		wantErr  bool
		wantSubs []string
	}{
		{"valid", DispatchContext{Role: reg.Worker, BeadID: "zr-1"}, false, nil},
		{"missing-bead", DispatchContext{Role: reg.Worker}, true, []string{"bead"}},
		{"missing-role", DispatchContext{BeadID: "zr-1"}, true, []string{"role"}},
		{"missing-both", DispatchContext{}, true, []string{"role", "bead"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.d.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
			for _, sub := range tc.wantSubs {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("err %q should mention %q", err, sub)
				}
			}
		})
	}
}
