package discover

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// routingRunner answers bd calls based on argv, simulating a small bead store.
type routingRunner struct {
	readyFeedback string // JSON for `bd ready` (no label filter)
	readyWorker   string // JSON for `bd ready --label worker-ready ...`
	show          map[string]string
	sawWorkerArgs []string
	readyErr      error            // if set, returned from any "ready" branch
	showErr       map[string]error // keyed by parent id; if set, returned for that show call
}

func (r *routingRunner) Run(_ context.Context, args ...string) (string, error) {
	switch args[0] {
	case "ready":
		if r.readyErr != nil {
			return "", r.readyErr
		}
		if contains(args, "--label") {
			r.sawWorkerArgs = args
			return r.readyWorker, nil
		}
		return r.readyFeedback, nil
	case "show":
		id := args[1]
		if r.showErr != nil {
			if err, ok := r.showErr[id]; ok {
				return "", err
			}
		}
		return r.show[id], nil
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

func TestDiscover_feedbackOwnership(t *testing.T) {
	rr := &routingRunner{
		readyFeedback: `[
			{"id":"zr-mine","issue_type":"task","title":"process-feedback: A","parent":"zr-prA"},
			{"id":"zr-other","issue_type":"task","title":"process-feedback: B","parent":"zr-prB"},
			{"id":"zr-nottask","issue_type":"feature","title":"process-feedback: C","parent":"zr-prA"},
			{"id":"zr-nofb","issue_type":"task","title":"some other task","parent":"zr-prA"}
		]`,
		readyWorker: `[]`,
		show: map[string]string{
			"zr-prA": `{"id":"zr-prA","metadata":{"author":"phillipg"}}`,
			"zr-prB": `{"id":"zr-prB","metadata":{"author":"someoneelse"}}`,
		},
	}
	reg := roles.NewRegistry(config.Default())
	got, err := Discover(context.Background(), rr, reg, "phillipg")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].BeadID != "zr-mine" || got[0].Role.Kind != roles.Feedback {
		t.Fatalf("feedback discovery = %+v (want only zr-mine)", got)
	}
}

func TestDiscover_workerLabelFilter(t *testing.T) {
	rr := &routingRunner{
		readyFeedback: `[]`,
		readyWorker:   `[{"id":"zr-w1"},{"id":"zr-w2"}]`,
	}
	reg := roles.NewRegistry(config.Default())
	got, err := Discover(context.Background(), rr, reg, "phillipg")
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
		readyFeedback: `[{"id":"zr-mine","issue_type":"task","title":"process-feedback: A","parent":"zr-prA"}]`,
		readyWorker:   `[{"id":"zr-w1"}]`,
		show:          map[string]string{"zr-prA": `{"id":"zr-prA","metadata":{"author":"phillipg"}}`},
	}
	cfg := config.Default()
	cfg.WorkerEnabled = false // worker disabled: its ready bead must be skipped
	reg := roles.NewRegistry(cfg)
	got, err := Discover(context.Background(), rr, reg, "phillipg")
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
		readyFeedback: `[{"id":"zr-c","issue_type":"task","title":"process-feedback: x","parent":"zr-p"}]`,
		readyWorker:   `[{"id":"zr-w"}]`,
		show:          map[string]string{"zr-p": `{"id":"zr-p","metadata":{"author":"phillipg"}}`},
	}
	reg := roles.NewRegistry(config.Default())
	got, _ := Discover(context.Background(), rr, reg, "phillipg")
	if len(got) != 2 || got[0].Role.Kind != roles.Feedback || got[1].Role.Kind != roles.Worker {
		t.Fatalf("order wrong: %+v", got)
	}
}

func TestDiscover_emptySelfLoginErrors(t *testing.T) {
	rr := &routingRunner{readyFeedback: `[]`, readyWorker: `[]`}
	reg := roles.NewRegistry(config.Default())
	if _, err := Discover(context.Background(), rr, reg, ""); err == nil {
		t.Error("empty selfLogin should error (cannot resolve feedback ownership)")
	}
}

// pg2-qq9v: a bd query failure must NOT look like "no ready work". Returning
// nil,nil there made the pool silently idle on infra failure. The error must
// propagate so the drain fails loudly instead of doing nothing.
func TestDiscover_propagatesReadyError(t *testing.T) {
	sentinel := errors.New("bd: connection refused")
	rr := &routingRunner{readyErr: sentinel}
	reg := roles.NewRegistry(config.Default())
	got, err := Discover(context.Background(), rr, reg, "phillipg")
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
	if _, err := Discover(context.Background(), rr, reg, "phillipg"); !errors.Is(err, sentinel) {
		t.Fatalf("worker bd ready failure must propagate; got %v", err)
	}
}

func TestDiscover_skipsBeadOnParentShowError(t *testing.T) {
	rr := &routingRunner{
		readyFeedback: `[
			{"id":"zr-bad","issue_type":"task","title":"process-feedback: A","parent":"zr-prBad"},
			{"id":"zr-good","issue_type":"task","title":"process-feedback: B","parent":"zr-prGood"}
		]`,
		readyWorker: `[]`,
		show: map[string]string{
			"zr-prGood": `{"id":"zr-prGood","metadata":{"author":"phillipg"}}`,
		},
		showErr: map[string]error{
			"zr-prBad": errors.New("bd: not found"),
		},
	}
	reg := roles.NewRegistry(config.Default())
	got, err := Discover(context.Background(), rr, reg, "phillipg")
	if err != nil {
		t.Fatalf("parent lookup error should be skipped, not propagated; err=%v", err)
	}
	if len(got) != 1 || got[0].BeadID != "zr-good" {
		t.Fatalf("only zr-good should be returned (zr-bad's parent errored); got %v", got)
	}
}

func TestForRole_feedbackOwnershipBypassesEnabled(t *testing.T) {
	rr := &routingRunner{
		readyFeedback: `[{"id":"zr-mine","issue_type":"task","title":"process-feedback: A","parent":"zr-prA"}]`,
		readyWorker:   `[]`,
		show:          map[string]string{"zr-prA": `{"id":"zr-prA","metadata":{"author":"phillipg"}}`},
	}
	cfg := config.Default()
	cfg.FeedbackEnabled = false // ForRole must run the query anyway (Enabled is bypassed)
	reg := roles.NewRegistry(cfg)
	got, err := ForRole(context.Background(), rr, reg.Feedback, "phillipg")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].BeadID != "zr-mine" || got[0].Role.Kind != roles.Feedback {
		t.Fatalf("ForRole(feedback) = %+v (want only zr-mine)", got)
	}
}

func TestForRole_workerIgnoresSelfLogin(t *testing.T) {
	rr := &routingRunner{readyFeedback: `[]`, readyWorker: `[{"id":"zr-w1"},{"id":"zr-w2"}]`}
	reg := roles.NewRegistry(config.Default())
	got, err := ForRole(context.Background(), rr, reg.Worker, "") // empty selfLogin is fine for worker
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Role.Kind != roles.Worker {
		t.Fatalf("ForRole(worker) = %+v", got)
	}
}

func TestForRole_feedbackEmptySelfLoginErrors(t *testing.T) {
	rr := &routingRunner{readyFeedback: `[]`, readyWorker: `[]`}
	reg := roles.NewRegistry(config.Default())
	if _, err := ForRole(context.Background(), rr, reg.Feedback, ""); err == nil {
		t.Error("ForRole(feedback) with empty selfLogin must error (cannot resolve ownership)")
	}
}

func TestDiscover_feedbackDisabled_emptySelfLoginOK(t *testing.T) {
	cfg := config.Default()
	cfg.FeedbackEnabled = false
	rr := &routingRunner{readyWorker: `[{"id":"zr-w1"}]`}
	reg := roles.NewRegistry(cfg)
	got, err := Discover(context.Background(), rr, reg, "")
	if err != nil {
		t.Fatalf("feedback disabled => empty selfLogin must not error; got %v", err)
	}
	if len(got) != 1 || got[0].BeadID != "zr-w1" {
		t.Fatalf("expected only the worker dispatch; got %+v", got)
	}
}

func TestForRole_unknownKindErrors(t *testing.T) {
	rr := &routingRunner{}
	if _, err := ForRole(context.Background(), rr, roles.Role{Kind: roles.RoleKind(99)}, "x"); err == nil {
		t.Error("unknown role kind must error")
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
