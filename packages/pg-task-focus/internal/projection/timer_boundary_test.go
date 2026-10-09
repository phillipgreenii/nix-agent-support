package projection

import "testing"

// TestOvertimeSinceAtExactZero pins the two boundaries of the overtime
// stretch: a boost that leaves the remaining time at exactly zero keeps the
// stretch (it does not lift the time above zero), and a remaining time that
// reaches zero at the very instant of a pause starts the stretch there.
func TestOvertimeSinceAtExactZero(t *testing.T) {
	t.Run("a boost to exactly zero keeps the stretch", func(t *testing.T) {
		// Planned 10: time is up at 10; the boost of 5 at 15 leaves exactly
		// zero, so at 20 the stretch still began at 10.
		c := cycleFrom(t, 0, startOf(cycleA, 10), 15, boostOf(cycleA, 5))
		since, ok := c.OvertimeSince(at(20))
		if !ok || !since.Equal(at(10)) {
			t.Errorf("OvertimeSince(20) = %v, %v; want the instant at 10", since, ok)
		}
	})
	t.Run("zero reached at a pause starts the stretch at the pause", func(t *testing.T) {
		// Planned 10, paused at exactly 10 and resumed at 20: at 25 the
		// stretch began at 10, when the time ran out, not at the resume.
		c := cycleFrom(t, 0, startOf(cycleA, 10), 10, pauseOf(cycleA), 20, resumeOf(cycleA))
		since, ok := c.OvertimeSince(at(25))
		if !ok || !since.Equal(at(10)) {
			t.Errorf("OvertimeSince(25) = %v, %v; want the instant at 10", since, ok)
		}
	})
}
