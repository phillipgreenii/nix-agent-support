package collect

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/bd"
	"github.com/phillipgreenii/beads-exporter/internal/metrics"
	"github.com/phillipgreenii/beads-exporter/internal/queue"
)

// recordedRunner answers bd invocations from the fixtures the contract suite
// recorded, selecting the file by subcommand.
type recordedRunner struct {
	t *testing.T
}

func (r recordedRunner) Run(_ context.Context, c bd.Cmd) (bd.Result, error) {
	name := ""
	switch c.Args[0] {
	case "list":
		name = "list.json"
		for _, a := range c.Args {
			if a == "--all" {
				name = "list-all.json"
			}
		}
	case "ready":
		name = "ready.json"
	case "blocked":
		name = "blocked.json"
	case "count":
		name = "count.json"
	case "statuses":
		name = "statuses.json"
	default:
		r.t.Fatalf("unexpected bd subcommand %v", c.Args)
	}
	raw, err := os.ReadFile(filepath.Join(testdataDir, "bd", name))
	if err != nil {
		r.t.Fatal(err)
	}
	return bd.Result{Stdout: raw}, nil
}

func recordedDB(t *testing.T) DB {
	t.Helper()
	return DB{Name: "alpha", Adapter: bd.NewClient(bd.ClientConfig{
		BDPath: "/opt/test/bd", BeadsDir: t.TempDir(), Home: "/opt/test/home", ChildPath: "/opt/test/bin",
		Timeout: time.Minute, Runner: recordedRunner{t},
	})}
}

// TestRecordedFixturesDecodeAndCategorise runs the real adapter and main pass
// over the recorded bd output and checks the derived state counts.
func TestRecordedFixturesDecodeAndCategorise(t *testing.T) {
	clock := newClock()
	db := recordedDB(t)
	drain, _ := queue.New("drain-claim", []string{"--exclude-label", "human", "--exclude-type", "epic"})
	mp := NewMainPass([]queue.Queue{drain}, 5)
	c := New(clock, []DB{db}, []Pass{mp, ThroughputPass{}}, quietLogger())
	mp.Init(context.Background(), []DB{db})
	c.RunPass(context.Background(), PassMain)
	c.RunPass(context.Background(), PassThroughput)

	fams := samplesByFamily(c)
	wantStates := map[string]float64{"in_progress": 1, "deferred": 1, "blocked": 1, "ready": 3, "tracking": 1, "other": 0}
	total := 0.0
	for state, want := range wantStates {
		got, ok := valueOf(t, fams[metrics.FamIssues], "db", "alpha", "state", state)
		if !ok || got != want {
			t.Fatalf("state %s = %v (present %v), want %v", state, got, ok, want)
		}
		total += got
	}
	if total != 7 {
		t.Fatalf("states sum to %v, want the 7 not-closed beads of the fixture", total)
	}
	wantStored := map[string]float64{"open": 4, "in_progress": 1, "deferred": 1, "review": 1, "closed": 1, "archived": 0, "blocked": 0}
	for status, want := range wantStored {
		got, ok := valueOf(t, fams[metrics.FamIssuesStored], "db", "alpha", "status", status)
		if !ok || got != want {
			t.Fatalf("stored %s = %v (present %v), want %v", status, got, ok, want)
		}
	}
	// The template in the ready fixture is dropped; the epic is excluded by the
	// queue; the human-labelled bead is excluded: only the plain ready beads remain.
	if got, _ := valueOf(t, fams[metrics.FamQueueCandidates], "db", "alpha", "queue", "drain-claim"); got != 1 {
		t.Fatalf("drain-claim candidates = %v, want 1 (the review-status task)", got)
	}
	if got, _ := valueOf(t, fams[metrics.FamCreated24h], "db", "alpha"); got != 8 {
		t.Fatalf("created = %v", got)
	}
	if _, ok := valueOf(t, fams[metrics.FamPassLastSuccess], "db", "alpha", "pass", "main"); !ok {
		t.Fatal("main pass did not succeed over the recorded fixtures")
	}
}
