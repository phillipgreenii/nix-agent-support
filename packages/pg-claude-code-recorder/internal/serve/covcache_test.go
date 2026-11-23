package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/wire"
)

// referenceCoverage is the full per-file pass the status loop used to run on
// every refresh (computeCoverageOver over freshly read inputs): the oracle the
// incremental pass has to equal.
func referenceCoverage(t *testing.T, d *Daemon) wire.Coverage {
	t.Helper()
	ctx := context.Background()
	var in coverageInputs
	err := d.st.ReadTxCtx(ctx, func(rd *store.Reader) error {
		var e error
		in, e = d.readCoverageInputs(ctx, rd, false, statLazy)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	cov, err := d.computeCoverageOver(ctx, in, "summary", statLazy)
	if err != nil {
		t.Fatal(err)
	}
	return cov
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// checkIncrementalEqualsFull runs the incremental pass and the full one at the
// same clock reading and requires the same figures, the same number of Lstat'ed
// exceptions included.
func checkIncrementalEqualsFull(t *testing.T, d *Daemon, step string) wire.Coverage {
	t.Helper()
	got, err := d.coverageIncremental(context.Background())
	if err != nil {
		t.Fatalf("%s: incremental pass: %v", step, err)
	}
	exGot := d.statExceptions.Load()
	want := referenceCoverage(t, d)
	if exWant := d.statExceptions.Load(); exGot != exWant {
		t.Fatalf("%s: incremental pass Lstat'ed %d exceptions, the full pass %d", step, exGot, exWant)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: incremental result differs from the full pass\nincremental %s\nfull        %s", step, mustJSON(got), mustJSON(want))
	}
	return got
}

// The incremental coverage pass gives the figures of the full pass over a
// fixture that changes in every way the figures depend on: bytes copied, bytes
// parsed, a file lagging behind its original, a file leaving status.fresh as
// the clock moves, a new file, a moved path and a removed file.
func TestIncrementalCoverageEqualsTheFullPassOnAChangingFixture(t *testing.T) {
	e := newEnv(t, false)
	old := time.Now().Add(-time.Hour)
	var paths []string
	for i := 0; i < 8; i++ {
		paths = append(paths, e.transcript(fmt.Sprintf("f%d.jsonl", i), "line one\nline two\n", old))
	}
	d := e.open(t)
	defer func() { _ = d.Close() }()
	clk := clock.NewFake(time.Now())
	d.clock = clk
	ctx := context.Background()

	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	cov := checkIncrementalEqualsFull(t, d, "after the copy")
	if cov.Files != 8 || cov.ParseLagBytesMax == 0 {
		t.Fatalf("fixture did not produce the figures under test: %s", mustJSON(cov.CoverageSummary))
	}

	if err := d.Parser().ParseAll(); err != nil {
		t.Fatal(err)
	}
	checkIncrementalEqualsFull(t, d, "after the parse")

	growOriginal(t, paths[0], "grown\n", old)
	growOriginal(t, paths[1], "grown\n", old)
	checkIncrementalEqualsFull(t, d, "originals grew, not yet copied")
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	checkIncrementalEqualsFull(t, d, "after the second copy")

	clk.Advance(d.cfg.StatusFresh + time.Minute) // every touched file leaves status.fresh
	checkIncrementalEqualsFull(t, d, "after the fresh window passed")

	if err := d.Parser().ParseAll(); err != nil {
		t.Fatal(err)
	}
	checkIncrementalEqualsFull(t, d, "after the second parse")

	paths = append(paths, e.transcript("late.jsonl", "late line\n", old))
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	if cov := checkIncrementalEqualsFull(t, d, "a new file"); cov.Files != 9 {
		t.Fatalf("new file not counted: %d files", cov.Files)
	}

	var id int64
	var oldPath string
	err := d.st.ReadTxCtx(ctx, func(rd *store.Reader) error {
		cur, e := rd.AllCurrentPaths()
		for fid, ps := range cur {
			if id == 0 || fid < id {
				id, oldPath = fid, ps[0]
			}
		}
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(filepath.Dir(oldPath), "moved.jsonl")
	for _, r := range []*store.Rec{
		{T: store.TPathClose, At: clk.Now().UnixNano(), File: id, Path: oldPath},
		{T: store.TPath, At: clk.Now().UnixNano(), File: id, Path: moved},
	} {
		if err := d.st.Commit(r); err != nil {
			t.Fatal(err)
		}
	}
	checkIncrementalEqualsFull(t, d, "a path moved")

	if err := os.Remove(paths[2]); err != nil {
		t.Fatal(err)
	}
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	checkIncrementalEqualsFull(t, d, "an original removed")

	// The whole table read again (the hourly rebuild, a rollback) rebuilds the
	// cache to the same figures.
	d.rowsOK = false
	checkIncrementalEqualsFull(t, d, "after a whole reread")
}

// A refresh over an unchanged store redoes no file: the pass reads nothing and
// redoes only the files that can change on their own (fresh, lagging or Lstat'ed
// ones). A commit that cannot change a figure (a process observation, a stage
// record) redoes none either; a commit that can (one file's bytes copied) redoes
// that file and leaves the figures equal to the full pass.
func TestIncrementalCoverageRedoesOnlyWhatChanged(t *testing.T) {
	e := newEnv(t, false)
	old := time.Now().Add(-time.Hour)
	var paths []string
	for i := 0; i < 20; i++ {
		paths = append(paths, e.transcript(fmt.Sprintf("f%d.jsonl", i), "line\n", old))
	}
	d := e.open(t)
	defer func() { _ = d.Close() }()
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	if err := d.Parser().ParseAll(); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Now().Add(time.Hour)) // every copy is long outside status.fresh
	d.clock = clk
	stats := countStat(d)

	checkIncrementalEqualsFull(t, d, "first pass")
	if d.covc.evals < 20 {
		t.Fatalf("the first pass computed %d file figures, want all 20", d.covc.evals)
	}
	if len(d.covc.volatile) != 0 {
		t.Fatalf("%d volatile files in a caught-up fixture", len(d.covc.volatile))
	}

	evals := func() int { return d.covc.evals }
	base := evals()
	stats.Store(0)
	checkIncrementalEqualsFull(t, d, "nothing changed")
	if got := evals() - base; got != 0 {
		t.Fatalf("a pass over an unchanged store redid %d files, want 0", got)
	}
	if got := stats.Load(); got != 0 {
		t.Fatalf("a pass over an unchanged store made %d Lstat calls, want 0", got)
	}

	base = evals()
	if err := d.st.Commit(&store.Rec{T: store.TProc, Kind: "seen", Pid: 1, ProcStart: "x", At: clk.Now().UnixNano()}); err != nil {
		t.Fatal(err)
	}
	if err := d.st.Commit(&store.Rec{T: store.TStage, At: clk.Now().UnixNano(), Session: "s", State: store.StageHeld}); err != nil {
		t.Fatal(err)
	}
	cov := checkIncrementalEqualsFull(t, d, "commits that touch no file")
	if got := evals() - base; got != 0 {
		t.Fatalf("a process observation and a stage record made the pass redo %d files, want 0", got)
	}
	if cov.Held != 1 {
		t.Fatalf("the stage record is not in the figures: held %d", cov.Held)
	}

	base = evals()
	growOriginal(t, paths[3], "more\n", old)
	if err := d.Watcher().Rescan(); err != nil {
		t.Fatal(err)
	}
	cov = checkIncrementalEqualsFull(t, d, "one file copied")
	if got := evals() - base; got < 1 || got > 3 {
		t.Fatalf("one file's copy made the pass redo %d files, want the one file", got)
	}
	if cov.ParseLagBytesMax == 0 {
		t.Fatalf("the copied bytes are not in the figures: %s", mustJSON(cov.CoverageSummary))
	}
}
