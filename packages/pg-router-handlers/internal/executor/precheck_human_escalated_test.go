package executor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/beads"
	"github.com/phillipgreenii/pg-router-handlers/internal/item"
)

// recBD is a beads.Runner that records every call and fails the calls whose first
// argument is failOn.
type recBD struct {
	mu     sync.Mutex
	calls  []string
	failOn string
}

func (r *recBD) Run(_ context.Context, args ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, strings.Join(args, " "))
	if r.failOn != "" && len(args) > 0 && args[0] == r.failOn {
		return "", errors.New("bd down")
	}
	return "", nil
}

func humanEscalatedTriage(t *testing.T, bd *recBD, iss *beads.Issue) (Skip, bool) {
	t.Helper()
	cfg := fastCfg()
	deps := Deps{Cfg: cfg, BD: bd, Now: func() time.Time { return time.Unix(0, 0) }}
	return PrecheckTriage(context.Background(), DispatchContext{Role: triageRole(cfg), Item: item.Item{ID: "zr-t"}}, deps, iss)
}

// Bead pg2-n7pufu: a bead with escalated AND a human label gets a comment, then
// loses escalated, and the event completes as moot.
func TestPrecheckTriage_HumanAndEscalated_CommentsThenStrips(t *testing.T) {
	for _, l := range beads.HumanLabels {
		t.Run(l, func(t *testing.T) {
			iss := escalated()
			iss.Labels = append(iss.Labels, l)
			bd := &recBD{}
			got, skipped := humanEscalatedTriage(t, bd, &iss)
			if !skipped || got.Reason != SkipBeadHumanEscalated || !got.Moot || got.Retryable {
				t.Fatalf("got (%+v, %v), want a moot %s", got, skipped, SkipBeadHumanEscalated)
			}
			if len(bd.calls) != 2 || !strings.HasPrefix(bd.calls[0], "comment zr-t ") || bd.calls[1] != "update zr-t --remove-label escalated" {
				t.Fatalf("calls = %q, want a comment THEN the strip (LV-4 order)", bd.calls)
			}
		})
	}
}

func TestPrecheckTriage_HumanAndEscalated_WriteFailuresDeclineRetryably(t *testing.T) {
	for _, failOn := range []string{"comment", "update"} {
		t.Run(failOn, func(t *testing.T) {
			iss := escalated()
			iss.Labels = append(iss.Labels, "human")
			bd := &recBD{failOn: failOn}
			got, skipped := humanEscalatedTriage(t, bd, &iss)
			if !skipped || got.Reason != SkipBeadHumanStripFailed || got.Moot || !got.Retryable {
				t.Fatalf("got (%+v, %v), want a retryable non-moot decline", got, skipped)
			}
			if failOn == "comment" {
				for _, c := range bd.calls {
					if strings.HasPrefix(c, "update") {
						t.Errorf("must not strip escalated after a failed comment: %q", bd.calls)
					}
				}
			}
		})
	}
}

// A repeat of the event sees the stripped bead: moot through not-escalated, and
// it writes nothing.
func TestPrecheckTriage_HumanAndEscalated_RepeatIsMootNoWrites(t *testing.T) {
	iss := escalated()
	iss.Labels = []string{"agent-support", "human"}
	bd := &recBD{}
	got, skipped := humanEscalatedTriage(t, bd, &iss)
	if !skipped || got.Reason != SkipBeadNotEscalated || !got.Moot {
		t.Fatalf("got (%+v, %v), want moot %s", got, skipped, SkipBeadNotEscalated)
	}
	if len(bd.calls) != 0 {
		t.Errorf("repeat must write nothing, got %q", bd.calls)
	}
}

// The drain ("ready") precheck still declines a human bead and writes nothing.
func TestPrecheckReady_HumanStillDeclines(t *testing.T) {
	cfg := fastCfg()
	iss := groomed()
	iss.Labels = append(iss.Labels, "human")
	bd := &recBD{}
	deps := Deps{Cfg: cfg, BD: bd, Now: func() time.Time { return time.Unix(0, 0) }}
	got, skipped := PrecheckReady(DispatchContext{Role: readyRole(cfg), Item: item.Item{ID: "zr-t"}}, deps, &iss)
	if !skipped || got.Reason != SkipBeadHuman || got.Moot {
		t.Fatalf("got (%+v, %v), want a non-moot %s", got, skipped, SkipBeadHuman)
	}
	if len(bd.calls) != 0 {
		t.Errorf("PrecheckReady must write nothing, got %q", bd.calls)
	}
}
