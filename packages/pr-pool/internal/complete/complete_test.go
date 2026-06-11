package complete

import (
	"context"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

func TestDoneSignal(t *testing.T) {
	cases := []struct {
		name        string
		kind        roles.RoleKind
		status      string
		seenClaimed bool
		want        bool
	}{
		{"feedback closed", roles.Feedback, "closed", false, true},
		{"feedback open not done", roles.Feedback, "open", false, false},
		{"feedback in_progress not done", roles.Feedback, "in_progress", true, false},
		{"worker closed", roles.Worker, "closed", false, true},
		{"worker open after claim = handback done", roles.Worker, "open", true, true},
		{"worker open pre-claim NOT done (startup race)", roles.Worker, "open", false, false},
		{"worker in_progress not done", roles.Worker, "in_progress", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DoneSignal(tc.kind, tc.status, tc.seenClaimed); got != tc.want {
				t.Errorf("DoneSignal(%v,%q,%v) = %v, want %v", tc.kind, tc.status, tc.seenClaimed, got, tc.want)
			}
		})
	}
}

func TestOnFailure_workerAddsHumanNeverUnclaims(t *testing.T) {
	fr := &recRunner{}
	reg := roles.NewRegistry(config.Default())
	if err := OnFailure(context.Background(), fr, reg.Worker, "zr-w1"); err != nil {
		t.Fatal(err)
	}
	if !fr.has("update zr-w1 --add-label human") {
		t.Errorf("worker failure must add human; calls=%v", fr.calls)
	}
	if fr.has("--status=open") {
		t.Errorf("worker failure must NOT unclaim; calls=%v", fr.calls)
	}
}

func TestOnFailure_feedbackUnclaims(t *testing.T) {
	fr := &recRunner{}
	reg := roles.NewRegistry(config.Default())
	if err := OnFailure(context.Background(), fr, reg.Feedback, "zr-c1"); err != nil {
		t.Fatal(err)
	}
	if !fr.has("update zr-c1 --status=open --assignee=") {
		t.Errorf("feedback failure must unclaim; calls=%v", fr.calls)
	}
	if fr.has("--add-label human") {
		t.Errorf("feedback failure must NOT add human; calls=%v", fr.calls)
	}
}

type recRunner struct{ calls []string }

func (r *recRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, join(args))
	return "", nil
}
func (r *recRunner) has(sub string) bool {
	for _, c := range r.calls {
		if c == sub {
			return true
		}
	}
	return false
}
func join(a []string) string {
	s := ""
	for i, x := range a {
		if i > 0 {
			s += " "
		}
		s += x
	}
	return s
}
