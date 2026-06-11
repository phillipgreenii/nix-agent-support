package discover

import (
	"context"
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
}

func (r *routingRunner) Run(_ context.Context, args ...string) (string, error) {
	switch args[0] {
	case "ready":
		if contains(args, "--label") {
			r.sawWorkerArgs = args
			return r.readyWorker, nil
		}
		return r.readyFeedback, nil
	case "show":
		return r.show[args[1]], nil
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
