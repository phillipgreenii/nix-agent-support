package lifecycle

import (
	"context"
	"database/sql"
	"reflect"
	"sort"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// processesFull is the Process list derived from the complete, uncached model.
func (v *liveEnv) processesFull(alive *bool) []Process {
	v.t.Helper()
	m, err := v.e.loadWith(context.Background(), false)
	if err != nil {
		v.t.Fatal(err)
	}
	var out []Process
	for _, k := range m.processKeys() {
		if p := v.e.process(m, k, false); alive == nil || *alive == p.Alive {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pid < out[j].Pid })
	return out
}

func (v *liveEnv) processes(alive *bool) ([]Process, int64) {
	v.t.Helper()
	v.cnt.Reset()
	got, err := v.e.Processes(context.Background(), ProcessFilter{Alive: alive, Limit: 1000})
	if err != nil {
		v.t.Fatal(err)
	}
	q := v.cnt.RowsRead()
	sort.Slice(got, func(i, j int) bool { return got[i].Pid < got[j].Pid })
	return got, q
}

// A Process list reads the registry records the store gained since the last one
// and nothing else: a history entry (which moves the complete model, not the
// registry) does not make it read the registry again, the rows it reads stay
// flat while the registry history grows, and the answer always equals the one
// derived from the complete model. process.list alive=true took about 19 s on
// the live store because every list reread the whole registry.
func TestProcessListReadsOnlyNewRegistryRecords(t *testing.T) {
	v := newLiveEnv(t)
	yes := true
	check := func(step string) int64 {
		t.Helper()
		got, rows := v.processes(&yes)
		want := v.processesFull(&yes)
		// Same facts; the list sorts by FirstSeen, compared here by pid.
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: list %+v, uncached derivation %+v", step, got, want)
		}
		return rows
	}
	for i := 0; i < 60; i++ {
		v.k.AddRegistry("73291", liveRegistry(73291, liveSessA, []string{"idle", "busy"}[i%2]))
		v.k.AddRegistry("73292", liveRegistry(73292, liveSessB, "busy"))
		v.k.Parse()
		if i == 59 {
			break
		}
		check("registry write")
	}
	// Rows read by a list after one more registry write, against after a
	// history-only write (no registry row at all).
	v.k.AddRegistry("73291", liveRegistry(73291, liveSessA, "busy"))
	v.k.Parse()
	afterRegistry := check("after registry write")
	if afterRegistry == 0 {
		t.Fatalf("a list after a registry write read no rows")
	}
	if rows := check("repeat"); rows >= afterRegistry {
		t.Fatalf("a repeat list read %d rows, the first after a write %d", rows, afterRegistry)
	}
	if err := v.k.Store.WithWrite("", func(*sql.Tx) error { v.k.Store.NoteModelInput(); return nil }); err != nil {
		t.Fatal(err)
	}
	afterHistory := check("after history-like write")
	if afterHistory > afterRegistry/2 {
		t.Fatalf("a list after a history write read %d rows (after a registry write %d): it reread the registry", afterHistory, afterRegistry)
	}
	// An exit observation and a registry deletion still show.
	if err := v.k.Store.Commit(&store.Rec{T: store.TProc, Kind: "exit", Pid: 73291, ProcStart: liveProcStart, At: 9, Reason: store.ExitObserved}); err != nil {
		t.Fatal(err)
	}
	got, _ := v.processes(&yes)
	if len(got) != 1 || got[0].Pid != 73292 {
		t.Fatalf("after exit: %+v, want only pid 73292", got)
	}
	check("after exit")
}
