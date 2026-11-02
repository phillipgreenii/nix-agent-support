package copier

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/faults"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/fsx"
)

func countOps(evs []fsx.Event, op, contains string) int {
	n := 0
	for _, e := range evs {
		if e.Op == op && (contains == "" || filepath.Base(e.Path) == contains || containsStr(e.Path, contains)) {
			n++
		}
	}
	return n
}

func containsStr(s, sub string) bool {
	return len(sub) > 0 && filepath.Base(s) != "" && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func batchPaths(e *env, n int, prefix string) []string {
	var ps []string
	for i := 0; i < n; i++ {
		ps = append(ps, e.write(fmt.Sprintf("projects/example-project/%s-%03d.jsonl", prefix, i), fmt.Sprintf("{\"n\":%d}\n", i)))
	}
	return ps
}

func TestFirstCopyIsOneJournalCommitAndOneDirSync(t *testing.T) {
	e := newEnv(t, func(e *env) { e.clone.ok = true })
	p := e.write(tx, "x\n")
	e.rec.Reset()
	e.ingest(p)
	evs := e.rec.Events()
	if got := countOps(evs, "mark", "commit"); got != 1 {
		t.Fatalf("journal commits = %d, want 1: %v", got, evs)
	}
	if got := countOps(evs, "syncdir", ""); got != 1 {
		t.Fatalf("dir syncs = %d, want 1: %v", got, evs)
	}
}

func TestIngestBatchSharesOneCommitAndOneDirSync(t *testing.T) {
	e := newEnv(t, func(e *env) { e.clone.ok = true })
	ps := batchPaths(e, 20, "s")
	e.rec.Reset()
	res := e.cp.IngestBatch(ps)
	evs := e.rec.Events()
	if got := countOps(evs, "mark", "commit"); got != 1 {
		t.Fatalf("journal commits = %d, want 1", got)
	}
	if got := countOps(evs, "syncdir", ""); got != 1 {
		t.Fatalf("dir syncs = %d, want 1", got)
	}
	// Order per the single-file contract: clone fsync, rename, dir sync, commit.
	if err := fsx.AssertOrder(evs,
		fsx.Step{Op: "sync", PathContains: ".tmp"}, fsx.Step{Op: "rename", PathContains: ".tmp"},
		fsx.Step{Op: "syncdir", PathContains: "copies"}, fsx.Step{Op: "mark", PathContains: "commit"}); err != nil {
		t.Fatal(err)
	}
	for i, r := range res {
		if r.Err != nil || r.Result.Action != "new-generation" || r.Result.File == 0 {
			t.Fatalf("result %d = %+v", i, r)
		}
		e.mustEqual(r.Result.File, fmt.Sprintf("{\"n\":%d}\n", i))
	}
}

func TestIngestBatchFallsBackForKnownAndOtherFiles(t *testing.T) {
	e := newEnv(t, func(e *env) { e.clone.ok = true })
	known := e.write(tx, "a\n")
	r := e.ingest(known)
	e.appendTo(tx, "b\n")
	snap := e.write("sessions/9.json", `{"pid":9}`)
	fresh := e.write("projects/example-project/new.jsonl", "c\n")
	res := e.cp.IngestBatch([]string{known, snap, fresh, e.abs("projects/example-project/gone.jsonl")})
	for i, br := range res {
		if br.Err != nil {
			t.Fatalf("result %d: %v", i, br.Err)
		}
	}
	if res[0].Result.File != r.File || res[0].Result.Action != "copied" {
		t.Fatalf("known = %+v", res[0].Result)
	}
	e.mustEqual(r.File, "a\nb\n")
	if res[1].Result.Action != "snapshot" || res[2].Result.Action != "new-generation" || res[3].Result.Action != "skipped" {
		t.Fatalf("results %+v", res)
	}
	e.mustEqual(res[2].Result.File, "c\n")
}

func TestIngestBatchHardLinkedPathsStayOneFile(t *testing.T) {
	e := newEnv(t, func(e *env) { e.clone.ok = true })
	a := e.write("projects/example-project/a.jsonl", "x\n")
	b := e.abs("projects/example-project/b.jsonl")
	if err := os.Link(a, b); err != nil {
		t.Fatal(err)
	}
	res := e.cp.IngestBatch([]string{a, b, a})
	for _, br := range res {
		if br.Err != nil {
			t.Fatal(br.Err)
		}
	}
	files, _ := e.st.AllFiles()
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1 (a hard link is one file with two paths)", len(files))
	}
}

// A crash between batches loses no committed file and leaves no half-recorded
// file: the first batch is recorded in full, the interrupted one not at all (its
// clones are unreferenced), and the interrupted batch is copied again after a
// restart.
func TestCrashBetweenBatchesLosesNothingCommittedAndNothingHalfRecorded(t *testing.T) {
	for _, point := range []string{"copy.batch-staged", "store.after-journal"} {
		t.Run(point, func(t *testing.T) {
			e := newEnv(t, func(e *env) { e.clone.ok = true })
			first := batchPaths(e, 5, "a")
			second := batchPaths(e, 5, "b")
			done := e.cp.IngestBatch(first)
			disarm := faults.ArmPanic(point)
			crashed := faults.Recover(func() { e.cp.IngestBatch(second) })
			disarm()
			if !crashed {
				t.Fatalf("%s never reached", point)
			}
			e.reopen()
			if err := e.cp.Recover(); err != nil {
				t.Fatal(err)
			}
			files, _ := e.st.AllFiles()
			wantAfterCrash := 5
			if point == "store.after-journal" {
				wantAfterCrash = 10 // the journal record is durable and replays whole
			}
			if len(files) != wantAfterCrash {
				t.Fatalf("files after crash = %d, want %d", len(files), wantAfterCrash)
			}
			for i, d := range done {
				e.mustEqual(d.Result.File, fmt.Sprintf("{\"n\":%d}\n", i))
			}
			for _, f := range files {
				// Every recorded file is whole: a generation whose offset matches its copy.
				g := e.gen(f.ID, f.CurGen)
				if g.CopyOffset == 0 {
					t.Fatalf("file %d recorded without its copy", f.ID)
				}
			}
			for _, br := range e.cp.IngestBatch(second) {
				if br.Err != nil {
					t.Fatal(br.Err)
				}
			}
			files, _ = e.st.AllFiles()
			if len(files) != 10 {
				t.Fatalf("files after recopy = %d, want 10 (no duplicates)", len(files))
			}
		})
	}
}

// Live (watcher) ingest of an already-recorded file keeps its durability: copy
// fsync, then one journal commit, and no directory sync.
func TestLiveAppendDurabilityUnchanged(t *testing.T) {
	e := newEnv(t, func(e *env) { e.clone.ok = true })
	p := e.write(tx, "a\n")
	e.ingest(p)
	e.appendTo(tx, "b\n")
	e.rec.Reset()
	e.ingest(p)
	evs := e.rec.Events()
	if err := fsx.AssertOrder(evs, fsx.Step{Op: "write", PathContains: "copies"}, fsx.Step{Op: "sync", PathContains: "copies"}, fsx.Step{Op: "mark", PathContains: "commit"}); err != nil {
		t.Fatal(err)
	}
	if got := countOps(evs, "mark", "commit"); got != 1 {
		t.Fatalf("commits = %d, want 1", got)
	}
}
