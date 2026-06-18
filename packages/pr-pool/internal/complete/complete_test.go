package complete

import (
	"context"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/roles"
)

func TestDoneSignal(t *testing.T) {
	cases := []struct {
		name        string
		completion  roles.Completion
		status      string
		seenClaimed bool
		want        bool
	}{
		{"close-only closed", roles.CloseOnly, "closed", false, true},
		{"close-only open not done", roles.CloseOnly, "open", false, false},
		{"close-only in_progress not done", roles.CloseOnly, "in_progress", true, false},
		{"handback closed", roles.CloseOrHandback, "closed", false, true},
		{"handback open after claim = done", roles.CloseOrHandback, "open", true, true},
		{"handback open pre-claim NOT done (startup race)", roles.CloseOrHandback, "open", false, false},
		{"handback in_progress not done", roles.CloseOrHandback, "in_progress", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DoneSignal(tc.completion, tc.status, tc.seenClaimed); got != tc.want {
				t.Errorf("DoneSignal(%q,%q,%v) = %v, want %v", tc.completion, tc.status, tc.seenClaimed, got, tc.want)
			}
		})
	}
}

func TestOnFailure_addHumanNeverUnclaims(t *testing.T) {
	fr := &recRunner{}
	if err := OnFailure(context.Background(), fr, roles.AddHuman, "zr-w1"); err != nil {
		t.Fatal(err)
	}
	if !fr.has("update zr-w1 --add-label human") {
		t.Errorf("add-human failure must add human; calls=%v", fr.calls)
	}
	if fr.has("--status=open") {
		t.Errorf("add-human failure must NOT unclaim; calls=%v", fr.calls)
	}
}

func TestOnFailure_unclaimNeverAddsHuman(t *testing.T) {
	fr := &recRunner{}
	if err := OnFailure(context.Background(), fr, roles.Unclaim, "zr-c1"); err != nil {
		t.Fatal(err)
	}
	if !fr.has("update zr-c1 --status=open --assignee=") {
		t.Errorf("unclaim failure must unclaim; calls=%v", fr.calls)
	}
	if fr.has("--add-label human") {
		t.Errorf("unclaim failure must NOT add human; calls=%v", fr.calls)
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
