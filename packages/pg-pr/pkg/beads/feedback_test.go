package beads

import (
	"context"
	"testing"
)

// feedbackRecordingRunner is a Runner that returns canned output and records
// calls. Named to avoid collision with recordingRunner in processingcycle_test.go.
type feedbackRecordingRunner struct {
	output string
	err    error
	calls  [][]string
}

func (r *feedbackRecordingRunner) Run(_ context.Context, args ...string) (string, error) {
	cp := make([]string, len(args))
	copy(cp, args)
	r.calls = append(r.calls, cp)
	return r.output, r.err
}

func TestListFeedbackBeadIDs_ParsesEnvelope(t *testing.T) {
	// bd 1.0.4+ envelope with two feedback beads.
	canned := `{"data":[{"id":"f-1","title":"Feedback 1","status":"open","issue_type":"feedback"},{"id":"f-2","title":"Feedback 2","status":"open","issue_type":"feedback"}],"schema_version":1}`

	rr := &feedbackRecordingRunner{output: canned}
	c := NewClientWithRunner(rr)

	ids, err := c.ListFeedbackBeadIDs(context.Background())
	if err != nil {
		t.Fatalf("ListFeedbackBeadIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 IDs, got %d: %v", len(ids), ids)
	}
	if ids[0] != "f-1" || ids[1] != "f-2" {
		t.Fatalf("expected [f-1 f-2], got %v", ids)
	}
}

func TestListFeedbackBeadIDs_PassesCorrectArgs(t *testing.T) {
	rr := &feedbackRecordingRunner{output: `{"data":[],"schema_version":1}`}
	c := NewClientWithRunner(rr)

	if _, err := c.ListFeedbackBeadIDs(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rr.calls) != 1 {
		t.Fatalf("expected 1 bd call, got %d", len(rr.calls))
	}
	args := rr.calls[0]
	if len(args) < 3 {
		t.Fatalf("expected at least 3 args, got %v", args)
	}
	// Must include --type=feedback, --all, --json.
	found := map[string]bool{}
	for _, a := range args {
		found[a] = true
	}
	for _, want := range []string{"--type=feedback", "--all", "--json"} {
		if !found[want] {
			t.Errorf("expected arg %q in bd call, got %v", want, args)
		}
	}
}

func TestListFeedbackBeadIDs_EmptyList(t *testing.T) {
	rr := &feedbackRecordingRunner{output: `{"data":[],"schema_version":1}`}
	c := NewClientWithRunner(rr)

	ids, err := c.ListFeedbackBeadIDs(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected empty slice, got %v", ids)
	}
}
