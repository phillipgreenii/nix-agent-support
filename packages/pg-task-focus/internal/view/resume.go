package view

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
)

// OfferAction is what a client does when the operator takes a resume offer.
type OfferAction string

// The actions of a resume offer.
const (
	// OfferResume resumes the offered cycle: nothing else is running.
	OfferResume OfferAction = "resume"
	// OfferSwitch makes the offered cycle the focus, pausing the running one:
	// a plain resume would be refused while another cycle runs.
	OfferSwitch OfferAction = "switch"
)

// ResumeOffer names the cycle the operator is asked to return to.
type ResumeOffer struct {
	Cycle  CycleView
	Action OfferAction
}

// interruptStack lists the paused cycles that another cycle displaced, by a
// start that interrupted them or by a switch away from them, the cycle
// interrupted last first. A cycle paused by hand was not displaced and is not
// listed. It is read from the dimmed cycles, which are already ordered by the
// instant they were paused, and the link a cycle carries is the one the
// projection keeps: a resume or a stop of the cycle clears it, a stop of the
// displacing cycle does not.
func interruptStack(dimmed []projection.Cycle) []projection.Cycle {
	var out []projection.Cycle
	for _, c := range dimmed {
		if c.InterruptedBy != "" {
			out = append(out, c)
		}
	}
	return out
}

// offerOf picks the cycle to offer: of the interrupted cycles whose
// interrupting cycle is stopped, the one interrupted last. A cycle whose
// interrupter is still running or paused is not offered yet, and the offer
// does not depend on whether another cycle runs now: the cycle stays offered
// while it does, and its action is then a switch. It ends when the cycle is
// resumed, switched to or stopped, because then it no longer is paused with a
// link.
func offerOf(m *projection.Model, stack []projection.Cycle, running bool, view func(projection.Cycle) CycleView) *ResumeOffer {
	for _, c := range stack {
		by, ok := m.Cycle(c.InterruptedBy)
		if !ok || by.Status != projection.Stopped {
			continue
		}
		action := OfferResume
		if running {
			action = OfferSwitch
		}
		return &ResumeOffer{Cycle: view(c), Action: action}
	}
	return nil
}
