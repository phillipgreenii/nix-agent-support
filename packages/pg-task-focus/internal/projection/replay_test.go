package projection_test

import (
	"reflect"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/testgen"
)

// commitBoundaries are the lengths of the prefixes of log that end between
// writes: after an event outside a batch or after a batch.committed.
func commitBoundaries(log []event.Event) []int {
	out := []int{0}
	for i, e := range log {
		if e.Payload.EventType() == event.TypeBatchCommitted || e.Payload.BatchID() == "" {
			out = append(out, i+1)
		}
	}
	return out
}

func TestReplayDeterministic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		log := testgen.Log(60).Draw(t, "log")
		a, err := projection.Replay(log)
		if err != nil {
			t.Fatalf("Replay of a generated log: %v", err)
		}
		b, err := projection.Replay(log)
		if err != nil {
			t.Fatalf("second Replay: %v", err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatal("two replays of one log differ")
		}

		last := log[len(log)-1].EffectiveAt.Time()
		for _, e := range log {
			last = maxTime(last, e.EffectiveAt.Time())
		}
		readAt := last.Add(time.Hour)
		running := 0
		for _, c := range a.Cycles() {
			var sum time.Duration
			for _, s := range c.Segments {
				end := readAt
				if s.End != nil {
					end = *s.End
				}
				if !end.After(s.Start) {
					t.Fatalf("cycle %s has a segment of no length: %v", c.ID, s)
				}
				sum += end.Sub(s.Start)
			}
			if got := c.Elapsed(readAt); got < 0 || got != sum {
				t.Fatalf("cycle %s Elapsed = %v, want the sum of its segments %v", c.ID, got, sum)
			}
			if c.Status == projection.Running {
				running++
			}
		}
		if running > 1 {
			t.Fatalf("%d cycles running at the end of the log", running)
		}

		for _, n := range commitBoundaries(log) {
			if _, err := projection.Replay(log[:n]); err != nil {
				t.Fatalf("the prefix of %d events, which ends at a commit boundary, does not replay: %v", n, err)
			}
		}
	})
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func TestDaysIsAKnownSizeAndReplays(t *testing.T) {
	log := testgen.Days(3)
	if len(log) != 150 {
		t.Fatalf("Days(3) has %d events, want 50 a day", len(log))
	}
	m, err := projection.Replay(log)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	// Two blocks of two cycles and two shorter cycles a day.
	if got := len(m.Cycles()); got != 18 {
		t.Errorf("%d cycles, want six a day", got)
	}
	// Each block's deep-work runs 30, 5, 132 and 30 minutes.
	for _, c := range m.Cycles() {
		if c.Type == "deep-work" && c.Elapsed(c.StoppedAt.Add(time.Hour)) != 197*time.Minute {
			t.Errorf("deep-work cycle %s elapsed %v, want 197m", c.ID, c.Elapsed(*c.StoppedAt))
		}
	}
	if _, ok := m.Running(); ok {
		t.Error("a cycle is still running at the end of the scripted days")
	}
	if !reflect.DeepEqual(testgen.Days(3), log) {
		t.Error("Days is not deterministic")
	}
}

func BenchmarkReplay50k(b *testing.B) {
	log := testgen.Days(1000)
	if len(log) != 50000 {
		b.Fatalf("Days(1000) has %d events, want 50000", len(log))
	}
	for b.Loop() {
		if _, err := projection.Replay(log); err != nil {
			b.Fatal(err)
		}
	}
}
