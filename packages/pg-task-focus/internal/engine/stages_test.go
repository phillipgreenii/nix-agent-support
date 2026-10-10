package engine_test

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/engine"
)

// The stages of a handled request are reported for a trace, and the observer
// is given the fsync's share of an append on the first event of a batch only.
func TestResultStagesAndTheObserversSyncDuration(t *testing.T) {
	h := newHarness(t, nil)
	boot := h.bootstrap()

	names := func(stages []engine.Stage) []string {
		var out []string
		for _, s := range stages {
			out = append(out, s.Name)
		}
		return out
	}
	got := names(boot.Stages)
	want := []string{"validate", "append", "fsync", "project"}
	if len(got) != len(want) {
		t.Fatalf("stages of a commit = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stages of a commit = %v, want %v", got, want)
		}
	}
	for _, s := range boot.Stages {
		if s.End.Before(s.Start) {
			t.Errorf("stage %s ends before it starts", s.Name)
		}
	}
	// fsync lies inside append.
	app, sync := boot.Stages[1], boot.Stages[2]
	if sync.Start.Before(app.Start) || sync.End.After(app.End) {
		t.Errorf("fsync %v..%v is not inside append %v..%v", sync.Start, sync.End, app.Start, app.End)
	}

	// Only the first event of the batch carries the durations.
	if len(h.obs.stats) < 3 {
		t.Fatalf("the bootstrap batch appended %d events", len(h.obs.stats))
	}
	if h.obs.stats[0].Duration <= 0 || h.obs.stats[0].SyncDuration <= 0 || h.obs.stats[0].SyncDuration > h.obs.stats[0].Duration {
		t.Errorf("first event stats = %+v, want a positive duration holding a positive sync duration", h.obs.stats[0])
	}
	for i, s := range h.obs.stats[1:] {
		if s.Duration != 0 || s.SyncDuration != 0 {
			t.Errorf("event %d of the batch carries durations %+v", i+1, s)
		}
	}

	// A no-op reports only its validation.
	c := h.start(local(9, 0), deepWork)
	h.at(local(9, 10), command.PauseCycle{ID: clientID(), CycleID: c})
	res := h.at(local(9, 10), command.PauseCycle{ID: clientID(), CycleID: c})
	if res.Changed {
		t.Fatal("the second pause appended")
	}
	if g := names(res.Stages); len(g) != 1 || g[0] != "validate" {
		t.Errorf("stages of a no-op = %v, want [validate]", g)
	}
}
