package eventqueue

import (
	"slices"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/levels"
)

// BlockedRequirements reports blocked listeners and emitters with their
// shortfalls, records no transition and fires no observer hook.
func TestBlockedRequirements(t *testing.T) {
	c := newClock()
	q, src, obs := newLevelQueue(t, NewMemStore(), c)
	q.Register(newListener("L", "work"))
	q.Register(newListener("free", "work"))
	q.RegisterListener("L", reqDiskOK())
	netUp := levels.Requirement{Clauses: []levels.Clause{{Index: 0, Resource: "net", Text: "net >= UP", Allowed: []string{"UP"}}}}
	q.RegisterEmitter("src", []levels.Consumer{{Req: reqDiskOK(), Enabled: true}, {Req: netUp, Enabled: true}})
	q.RegisterEmitter("off", []levels.Consumer{{Req: reqDiskOK(), Enabled: false}})

	if got := q.BlockedRequirements(); len(got) != 0 {
		t.Fatalf("everything admitted: got %+v", got)
	}
	src.set("disk", "CRITICAL") // two steps below OK
	got := q.BlockedRequirements()
	if len(got) != 1 || got[0].Participant != "L" ||
		!slices.Equal(got[0].Shortfalls, []levels.Shortfall{{Resource: "disk", Steps: 2, Sole: true}}) {
		t.Fatalf("listener L blocked by disk: got %+v", got)
	}
	// The emitter has a consumer that can still run (net), so it is not blocked.
	src.set("net", "DOWN")
	got = q.BlockedRequirements()
	if len(got) != 2 || got[1].Participant != "src" {
		t.Fatalf("want L then src, got %+v", got)
	}
	// Both consumers have one unmet resource: the smaller distance (net, 1 step) beats
	// the first-registered disk consumer (2 steps).
	if want := []levels.Shortfall{{Resource: "net", Steps: 1, Sole: true}}; !slices.Equal(got[1].Shortfalls, want) {
		t.Fatalf("emitter shortfalls = %+v, want %+v", got[1].Shortfalls, want)
	}
	if len(obs.blocked) != 0 || len(obs.paused) != 0 {
		t.Fatalf("a read-only evaluation fired hooks: blocked=%v paused=%v", obs.blocked, obs.paused)
	}
}

func TestBlockedRequirements_NoLevelSource(t *testing.T) {
	q, err := New(NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	q.RegisterListener("L", reqDiskOK())
	if got := q.BlockedRequirements(); got != nil {
		t.Fatalf("no level source blocks nothing, got %+v", got)
	}
}

// An emitter's shortfalls come from the consumer closest to admitting: fewest
// unmet resources first, then smallest total steps, then registration order.
func TestBlockedRequirements_EmitterAttribution(t *testing.T) {
	netUp := levels.Requirement{Clauses: []levels.Clause{{Index: 0, Resource: "net", Text: "net >= UP", Allowed: []string{"UP"}}}}
	both := levels.Requirement{Clauses: []levels.Clause{
		{Index: 0, Resource: "disk", Text: "disk >= OK", Allowed: []string{"PLENTY", "OK"}},
		{Index: 1, Resource: "net", Text: "net >= UP", Allowed: []string{"UP"}},
	}}
	diskLow := levels.Requirement{Clauses: []levels.Clause{{Index: 0, Resource: "disk", Text: "disk >= LOW", Allowed: []string{"PLENTY", "OK", "LOW"}}}}
	for _, tc := range []struct {
		name      string
		consumers []levels.Consumer
		want      []levels.Shortfall
		disk      string // disk level, CRITICAL when empty
	}{
		{
			"fewest unmet beats registration order",
			[]levels.Consumer{{Req: both, Enabled: true}, {Req: reqDiskOK(), Enabled: true}},
			[]levels.Shortfall{{Resource: "disk", Steps: 2, Sole: true}},
			"",
		},
		{
			"smaller total steps breaks a tie",
			[]levels.Consumer{{Req: reqDiskOK(), Enabled: true}, {Req: netUp, Enabled: true}},
			[]levels.Shortfall{{Resource: "net", Steps: 1, Sole: true}},
			"",
		},
		{
			"a nearer threshold on the same resource wins",
			[]levels.Consumer{{Req: reqDiskOK(), Enabled: true}, {Req: diskLow, Enabled: true}},
			[]levels.Shortfall{{Resource: "disk", Steps: 1, Sole: true}},
			"",
		},
		{
			"first registered wins an exact tie",
			[]levels.Consumer{{Req: netUp, Enabled: true}, {Req: reqDiskOK(), Enabled: true}},
			[]levels.Shortfall{{Resource: "net", Steps: 1, Sole: true}},
			"LOW",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, src, _ := newLevelQueue(t, NewMemStore(), newClock())
			disk := tc.disk
			if disk == "" {
				disk = "CRITICAL"
			}
			src.set("disk", disk)
			src.set("net", "DOWN")
			q.RegisterEmitter("e", tc.consumers)
			got := q.BlockedRequirements()
			if len(got) != 1 || !slices.Equal(got[0].Shortfalls, tc.want) {
				t.Fatalf("got %+v, want shortfalls %+v", got, tc.want)
			}
		})
	}
}
