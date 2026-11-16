package manager

import (
	"fmt"
	"sort"

	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// candidates lists the worktrees a lease may take instead of creating one,
// best first (INV-GWM-20): available worktrees by slot, then, with reuse on,
// released ones (the longest released first). Lease-expired worktrees are not
// here: they are reclaimed first (reclaimOne). The row 12 target (a released
// worktree already holding the requested branch) comes first and is chosen by
// planBranch, not here.
func (m *Manager) candidates(st *state.State) []*state.Worktree {
	var avail, released []*state.Worktree
	for i := range st.Worktrees {
		w := &st.Worktrees[i]
		switch {
		case w.Status == state.Available && w.Lease == nil:
			avail = append(avail, w)
		case m.Cfg.Reuse && w.Status == state.Released && w.Lease == nil:
			released = append(released, w)
		}
	}
	sort.SliceStable(avail, func(i, j int) bool { return avail[i].Slot < avail[j].Slot })
	sort.SliceStable(released, func(i, j int) bool {
		if !released[i].StatusSince.Equal(released[j].StatusSince) {
			return released[i].StatusSince.Before(released[j].StatusSince)
		}
		return released[i].Slot < released[j].Slot
	})
	return append(avail, released...)
}

// counts tallies every managed worktree by effective status, and lists the
// broken ones, for a max-reached refusal.
func (m *Manager) counts(st *state.State) (map[state.Status]int, []string) {
	c := map[state.Status]int{}
	var broken []string
	now := m.now()
	for _, w := range st.Worktrees {
		c[w.Effective(now)]++
		if w.Status == state.Broken {
			broken = append(broken, w.ID)
		}
	}
	return c, broken
}

// freeSlot is the lowest slot index no managed worktree holds whose rendered
// directory does not already exist (INV-GWM-22); an occupied directory moves
// the lease on to the next index.
func freeSlot(st *state.State, exists func(index int) (bool, error)) (int, error) {
	held := map[int]bool{}
	for _, w := range st.Worktrees {
		held[w.Slot] = true
	}
	for i := 0; i < maxSlots; i++ {
		if held[i] {
			continue
		}
		occupied, err := exists(i)
		if err != nil {
			return 0, err
		}
		if !occupied {
			return i, nil
		}
	}
	return 0, fmt.Errorf("no free slot among the first %d indexes: worktree-dir-template may render the same directory for every index", maxSlots)
}

const maxSlots = 10000
