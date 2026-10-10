package view_test

import (
	"slices"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/testutil"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/view"
)

// offerOf is the cycle the state offers, "" when it offers none, and the
// action of the offer.
func offerOf(st view.State) (event.CycleID, view.OfferAction) {
	if st.ResumeOffer == nil {
		return "", ""
	}
	return st.ResumeOffer.Cycle.Cycle.ID, st.ResumeOffer.Action
}

func assertOffer(t *testing.T, st view.State, wantID event.CycleID, wantAction view.OfferAction) {
	t.Helper()
	if id, action := offerOf(st); id != wantID || action != wantAction {
		t.Errorf("ResumeOffer = %q %q, want %q %q", id, action, wantID, wantAction)
	}
}

func TestResumeOfferAfterInterruptingCycleStopped(t *testing.T) {
	cfg := testutil.LoadConfig(t, nil)
	b := bootstrapped(t)
	b.add(at(0), startOf(cycleA, "deep-work", 50))
	b.add(at(10), interruptOf(cycleB, cycleA, "notifications", 15))

	assertOffer(t, view.Build(b.model(), cfg, at(11)), "", "")

	b.add(at(20), stopOf(cycleB))
	st := view.Build(b.model(), cfg, at(21))
	assertOffer(t, st, cycleA, view.OfferResume)
	if st.Focus != nil {
		t.Errorf("Focus = %v, want none: the app never resumes a cycle on its own", st.Focus.Cycle.ID)
	}
	if got := dimmedIDs(st); !slices.Equal(got, []event.CycleID{cycleA}) {
		t.Errorf("Dimmed = %v, want the offered cycle kept there", got)
	}
	if st.ResumeOffer != nil && st.ResumeOffer.Cycle.Elapsed != 10*time.Minute {
		t.Errorf("offered cycle Elapsed = %v, want 10m", st.ResumeOffer.Cycle.Elapsed)
	}

	t.Run("resuming the offered cycle ends the offer", func(t *testing.T) {
		b.add(at(22), resumeOf(cycleA))
		st := view.Build(b.model(), cfg, at(23))
		assertOffer(t, st, "", "")
		if st.Focus == nil || st.Focus.Cycle.ID != cycleA {
			t.Errorf("Focus = %+v, want A", st.Focus)
		}
	})
}

func TestNoResumeOfferWhileInterrupterPausedOrRunning(t *testing.T) {
	cfg := testutil.LoadConfig(t, nil)
	b := bootstrapped(t)
	b.add(at(0), startOf(cycleA, "deep-work", 50))
	b.add(at(10), interruptOf(cycleB, cycleA, "notifications", 15))

	t.Run("the interrupter runs", func(t *testing.T) {
		assertOffer(t, view.Build(b.model(), cfg, at(12)), "", "")
	})
	t.Run("the interrupter is paused", func(t *testing.T) {
		b.add(at(15), pauseOf(cycleB))
		st := view.Build(b.model(), cfg, at(16))
		assertOffer(t, st, "", "")
		if got := dimmedIDs(st); !slices.Equal(got, []event.CycleID{cycleB, cycleA}) {
			t.Errorf("Dimmed = %v, want both paused cycles, nothing hidden", got)
		}
	})
	t.Run("the interrupter stops", func(t *testing.T) {
		b.add(at(18), stopOf(cycleB))
		assertOffer(t, view.Build(b.model(), cfg, at(19)), cycleA, view.OfferResume)
	})
}

func TestResumeOfferChain(t *testing.T) {
	cfg := testutil.LoadConfig(t, nil)
	b := bootstrapped(t)
	b.add(at(0), startOf(cycleA, "deep-work", 50))
	b.add(at(10), interruptOf(cycleB, cycleA, "review", 25))
	b.add(at(20), interruptOf(cycleC, cycleB, "notifications", 15))

	st := view.Build(b.model(), cfg, at(21))
	assertOffer(t, st, "", "")
	if got, want := cycleIDs(st.InterruptStack), []event.CycleID{cycleB, cycleA}; !slices.Equal(got, want) {
		t.Errorf("InterruptStack = %v, want %v, the cycle interrupted last first", got, want)
	}

	b.add(at(30), stopOf(cycleC))
	assertOffer(t, view.Build(b.model(), cfg, at(31)), cycleB, view.OfferResume)

	b.add(at(32), resumeOf(cycleB))
	st = view.Build(b.model(), cfg, at(33))
	assertOffer(t, st, "", "")
	if got, want := cycleIDs(st.InterruptStack), []event.CycleID{cycleA}; !slices.Equal(got, want) {
		t.Errorf("InterruptStack = %v, want %v", got, want)
	}

	b.add(at(40), stopOf(cycleB))
	assertOffer(t, view.Build(b.model(), cfg, at(41)), cycleA, view.OfferResume)
}

func TestResumeOfferPersistsWhileAnotherCycleRuns(t *testing.T) {
	cfg := testutil.LoadConfig(t, nil)
	// A is interrupted by B, B stops (A is offered), then C starts and runs.
	fixture := func(t *testing.T) *logb {
		b := bootstrapped(t)
		b.add(at(0), startOf(cycleA, "deep-work", 50))
		b.add(at(10), interruptOf(cycleB, cycleA, "notifications", 15))
		b.add(at(20), stopOf(cycleB))
		b.add(at(25), startOf(cycleC, "review", 25))
		return b
	}

	t.Run("the offer stays and its action is a switch", func(t *testing.T) {
		st := view.Build(fixture(t).model(), cfg, at(26))
		assertOffer(t, st, cycleA, view.OfferSwitch)
		if got := dimmedIDs(st); !slices.Equal(got, []event.CycleID{cycleA}) {
			t.Errorf("Dimmed = %v, want the offered cycle also listed", got)
		}
		for _, d := range st.Dimmed {
			if !d.CanSwitch {
				t.Errorf("cycle %s CanSwitch = false while another cycle runs", d.Cycle.ID)
			}
		}
		if focusOf(st).Cycle.ID != cycleC {
			t.Errorf("Focus = %s, want C", focusOf(st).Cycle.ID)
		}
	})

	t.Run("switching to the offered cycle ends the offer", func(t *testing.T) {
		b := fixture(t)
		b.switchTo(at(30), cycleC, cycleA)
		st := view.Build(b.model(), cfg, at(31))
		assertOffer(t, st, "", "")
		if focusOf(st).Cycle.ID != cycleA {
			t.Errorf("Focus = %s, want A", focusOf(st).Cycle.ID)
		}
	})

	t.Run("stopping the offered cycle ends the offer", func(t *testing.T) {
		b := fixture(t)
		b.add(at(30), stopOf(cycleA))
		st := view.Build(b.model(), cfg, at(31))
		assertOffer(t, st, "", "")
		if got := dimmedIDs(st); len(got) != 0 {
			t.Errorf("Dimmed = %v, want none", got)
		}
	})

	t.Run("a pause of the running cycle turns the offer back into a resume", func(t *testing.T) {
		b := fixture(t)
		b.add(at(30), pauseOf(cycleC))
		assertOffer(t, view.Build(b.model(), cfg, at(31)), cycleA, view.OfferResume)
	})
}

func TestResumeOfferPicksTheLatestInterruption(t *testing.T) {
	// Two paused cycles are candidates, each interrupted by a cycle that is
	// now stopped; the one interrupted later is offered, whatever the cycle ids
	// and the order the interrupters stopped in. Two cycles cannot be paused at
	// the same instant: a cycle is paused when its last running segment ends,
	// one cycle runs at a time, and a segment has a length, so a tie cannot be
	// built from valid events.
	cfg := testutil.LoadConfig(t, nil)
	tests := []struct {
		name  string
		build func(b *logb)
		want  event.CycleID
		other event.CycleID
	}{
		{"A is interrupted first and C later", func(b *logb) {
			b.add(at(0), startOf(cycleA, "deep-work", 50))
			b.add(at(10), interruptOf(cycleB, cycleA, "notifications", 15))
			b.add(at(20), stopOf(cycleB))
			b.add(at(30), startOf(cycleC, "deep-work", 50))
			b.add(at(40), interruptOf(cycleD, cycleC, "notifications", 15))
			b.add(at(50), stopOf(cycleD))
		}, cycleC, cycleA},
		{"the instants swapped: C is interrupted first and A later", func(b *logb) {
			b.add(at(0), startOf(cycleC, "deep-work", 50))
			b.add(at(10), interruptOf(cycleD, cycleC, "notifications", 15))
			b.add(at(20), stopOf(cycleD))
			b.add(at(30), startOf(cycleA, "deep-work", 50))
			b.add(at(40), interruptOf(cycleB, cycleA, "notifications", 15))
			b.add(at(50), stopOf(cycleB))
		}, cycleA, cycleC},
		{"the interrupter that stopped last belongs to the earlier interruption", func(b *logb) {
			b.add(at(0), startOf(cycleA, "deep-work", 50))
			b.add(at(10), interruptOf(cycleB, cycleA, "notifications", 15))
			b.add(at(15), pauseOf(cycleB))
			b.add(at(16), startOf(cycleC, "deep-work", 50))
			b.add(at(20), interruptOf(cycleD, cycleC, "notifications", 15))
			b.add(at(25), stopOf(cycleD))
			b.add(at(30), stopOf(cycleB)) // a dimmed cycle can be stopped where it is
		}, cycleC, cycleA},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := bootstrapped(t)
			tt.build(b)
			st := view.Build(b.model(), cfg, at(100))
			assertOffer(t, st, tt.want, view.OfferResume)
			got := dimmedIDs(st)
			if !slices.Contains(got, tt.want) || !slices.Contains(got, tt.other) {
				t.Errorf("Dimmed = %v, want both candidates listed: the one not offered stays visible", got)
			}
			if stack := cycleIDs(st.InterruptStack); len(stack) != 2 || stack[0] != tt.want {
				t.Errorf("InterruptStack = %v, want both, %s first", stack, tt.want)
			}
		})
	}
}

func TestResumeOfferAfterSwitchSequences(t *testing.T) {
	cfg := testutil.LoadConfig(t, nil)

	t.Run("an interrupted cycle switched back to and then paused by hand has no link and no offer", func(t *testing.T) {
		b := bootstrapped(t)
		b.add(at(0), startOf(cycleA, "deep-work", 50))
		b.add(at(10), interruptOf(cycleB, cycleA, "notifications", 15))
		b.switchTo(at(20), cycleB, cycleA)
		b.add(at(30), stopOf(cycleB))
		assertOffer(t, view.Build(b.model(), cfg, at(31)), "", "")
		b.add(at(40), pauseOf(cycleA))
		st := view.Build(b.model(), cfg, at(41))
		assertOffer(t, st, "", "")
		if got := dimmedIDs(st); !slices.Equal(got, []event.CycleID{cycleA}) {
			t.Errorf("Dimmed = %v, want A, paused by hand", got)
		}
		if len(st.InterruptStack) != 0 {
			t.Errorf("InterruptStack = %v, want none: a pause by hand is no interruption", cycleIDs(st.InterruptStack))
		}
	})

	t.Run("a cycle switched away from is offered once the displacing cycle stops", func(t *testing.T) {
		b := bootstrapped(t)
		b.add(at(0), startOf(cycleA, "deep-work", 50))
		b.add(at(10), pauseOf(cycleA))
		b.add(at(20), startOf(cycleB, "notifications", 25))
		b.switchTo(at(30), cycleB, cycleA)
		b.switchTo(at(40), cycleA, cycleB)
		assertOffer(t, view.Build(b.model(), cfg, at(41)), "", "")
		b.add(at(50), stopOf(cycleB))
		assertOffer(t, view.Build(b.model(), cfg, at(51)), cycleA, view.OfferResume)
	})

	t.Run("the scripted day", func(t *testing.T) {
		// deep-work starts 09:10, notifications interrupts it 09:40, a switch
		// to deep-work 09:50 and back 09:55, notifications stops 09:58 and
		// deep-work resumes 09:58. The base time is 08:00 New York.
		deep, notif := cycleA, cycleB
		b := bootstrapped(t)
		b.add(at(70), startOf(deep, "deep-work", 50))
		b.add(at(100), interruptOf(notif, deep, "notifications", 15))
		b.switchTo(at(110), notif, deep)
		b.switchTo(at(115), deep, notif)

		st := view.Build(b.model(), cfg, at(116))
		assertOffer(t, st, "", "")
		if focusOf(st).Cycle.ID != notif {
			t.Errorf("Focus = %s, want notifications", focusOf(st).Cycle.ID)
		}

		b.add(at(118), stopOf(notif))
		st = view.Build(b.model(), cfg, at(118))
		assertOffer(t, st, deep, view.OfferResume)

		b.add(at(118), resumeOf(deep))
		st = view.Build(b.model(), cfg, at(119))
		assertOffer(t, st, "", "")
		if focusOf(st).Cycle.ID != deep {
			t.Errorf("Focus = %s, want deep-work", focusOf(st).Cycle.ID)
		}
		if got := focusOf(st).Elapsed; got != 36*time.Minute {
			t.Errorf("deep-work Elapsed = %v, want 36m (09:10-09:40, 09:50-09:55, 09:58-09:59)", got)
		}
	})
}

func TestInterruptStackOrder(t *testing.T) {
	cfg := testutil.LoadConfig(t, nil)
	b := bootstrapped(t)
	b.add(at(0), startOf(cycleD, "review", 25))
	b.add(at(5), pauseOf(cycleD)) // paused by hand: dimmed but not in the stack
	b.add(at(10), startOf(cycleA, "deep-work", 50))
	b.add(at(20), interruptOf(cycleB, cycleA, "review", 25))
	b.add(at(30), interruptOf(cycleC, cycleB, "notifications", 15))
	st := view.Build(b.model(), cfg, at(40))

	if got, want := dimmedIDs(st), []event.CycleID{cycleB, cycleA, cycleD}; !slices.Equal(got, want) {
		t.Errorf("Dimmed = %v, want %v", got, want)
	}
	if got, want := cycleIDs(st.InterruptStack), []event.CycleID{cycleB, cycleA}; !slices.Equal(got, want) {
		t.Errorf("InterruptStack = %v, want %v", got, want)
	}
}
