package focus

import "fmt"

// CapLine is the plan's cap line: Cap is the period's cap, InPlan the
// UNFINISHED rows in the plan, OpenSlots what is left of Cap, and Finished
// the finished rows shown but not counted.
type CapLine struct {
	Cap, InPlan, OpenSlots, Finished int
}

// newCapLine builds a CapLine; open slots are never negative.
func newCapLine(cap, inPlan, finished int) CapLine {
	open := cap - inPlan
	if open < 0 {
		open = 0
	}
	return CapLine{Cap: cap, InPlan: inPlan, OpenSlots: open, Finished: finished}
}

// String renders the cap line, for example
// `cap 6: 4 in plan, 2 open slots; 2 finished, not counted`. The finished
// clause is omitted when no row is finished.
func (c CapLine) String() string {
	s := fmt.Sprintf("cap %d: %d in plan, %d open slots", c.Cap, c.InPlan, c.OpenSlots)
	if c.Finished > 0 {
		s += fmt.Sprintf("; %d finished, not counted", c.Finished)
	}
	return s
}
