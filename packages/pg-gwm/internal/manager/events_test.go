package manager

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// eventsByKind reads the event log and counts its lines by kind.
func (e *env) eventsByKind() (map[string]int, []state.Event) {
	e.t.Helper()
	counts := map[string]int{}
	var all []state.Event
	for _, line := range strings.Split(strings.TrimSpace(e.events()), "\n") {
		if line == "" {
			continue
		}
		var ev state.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			e.t.Fatalf("event line %q: %v", line, err)
		}
		counts[ev.Kind]++
		all = append(all, ev)
	}
	return counts, all
}

// Every state change is logged once, in the section that commits it: one
// line per lease, renew, release, expire, preserve, reclaim, create, remove
// and broken transition, naming the worktree id, lease id, name, owner and
// outcome.
func TestEveryTransitionLogged(t *testing.T) {
	e := newFakeEnv(t)
	ttl := ptr(dur(t, "1h"))
	lease := func(name, branch string) Record {
		return e.mustLease(LeaseRequest{Name: name, Owner: "me", Branch: branch, Create: true, TTL: ttl})
	}

	a := lease("a", "br-a")
	if _, err := e.m.Renew(t.Context(), LeaseKey{LeaseID: str(a.LeaseID)}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Release(t.Context(), LeaseKey{LeaseID: str(a.LeaseID)}); err != nil {
		t.Fatal(err)
	}
	c := lease("c", "br-c")
	if _, err := e.m.Expire(t.Context(), LeaseKey{LeaseID: str(c.LeaseID)}); err != nil {
		t.Fatal(err)
	}
	d := lease("d", "br-d")
	e.write(d.WorktreePath, "unsaved.txt", "work\n")
	if _, err := e.m.Expire(t.Context(), LeaseKey{LeaseID: str(d.LeaseID)}); err != nil {
		t.Fatal(err)
	}

	// Reap reclaims a, c and d (d preserves its unsaved work first).
	e.mustReap(ReapOptions{})

	// Reap tops up to min with a fresh worktree: a creation.
	e.m.Cfg.Min = ptr(1)
	e.mustReap(ReapOptions{})
	e.m.Cfg.Min = nil

	// A person deletes the worktree directory: reap drops the record, a removal.
	for _, w := range e.state().Worktrees {
		if err := os.RemoveAll(w.Path); err != nil {
			t.Fatal(err)
		}
	}
	e.mustReap(ReapOptions{})

	// A command died while preparing: reap marks it broken.
	g := e.mustLease(LeaseRequest{Name: "g", Owner: "me", Detach: true})
	e.seed(func(st *state.State) {
		for i := range st.Worktrees {
			if st.Worktrees[i].ID == g.ID {
				st.Worktrees[i].Status = state.Preparing
			}
		}
	})
	fr := &failRemove{Provisioner: e.m.Prov}
	fr.on.Store(true)
	e.m.Prov = fr
	e.mustReap(ReapOptions{})

	counts, events := e.eventsByKind()
	want := map[string]int{
		"lease": 4, "renew": 1, "release": 1, "expire": 2, "preserve": 1,
		"reclaim": 3, "create": 1, "remove": 1,
	}
	for kind, n := range want {
		if counts[kind] != n {
			t.Errorf("%d %q events, want %d (all: %v)", counts[kind], kind, n, counts)
		}
	}
	if counts["broken"] < 1 || counts["reap"] != 4 {
		t.Errorf("broken events %d (want at least 1), reap events %d (want 4): %v", counts["broken"], counts["reap"], counts)
	}
	for _, ev := range events {
		if ev.Outcome == "" || ev.At.IsZero() {
			t.Errorf("event %+v has no outcome or time", ev)
		}
		switch ev.Kind {
		case "lease", "renew", "release", "expire", "preserve":
			if ev.WorktreeID == "" || ev.LeaseID == "" || ev.Name == "" || ev.Owner != "me" {
				t.Errorf("%s event lacks worktree id, lease id, name or owner: %+v", ev.Kind, ev)
			}
		case "create", "remove", "reclaim", "broken":
			if ev.WorktreeID == "" {
				t.Errorf("%s event lacks a worktree id: %+v", ev.Kind, ev)
			}
		}
	}
	// A refusal writes no event.
	before, _ := e.eventsByKind()
	if _, err := e.m.Release(t.Context(), LeaseKey{LeaseID: "l-nonexistent"}); err == nil {
		t.Fatal("release of an unknown lease succeeded")
	}
	after, _ := e.eventsByKind()
	if len(before) != len(after) {
		t.Errorf("a refused release changed the event kinds: %v -> %v", before, after)
	}
	for k := range before {
		if before[k] != after[k] {
			t.Errorf("a refused release wrote a %q event", k)
		}
	}
}
