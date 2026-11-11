package settle

import (
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// rollEnv is an env whose settler runs the rolling doctor with budget bytes per
// slice (it survives crash(), which reopens the settler).
func rollEnv(t *testing.T, budget int64) *env {
	t.Helper()
	return newEnvWith(t, false, func(e *env) {
		e.extra = func(c *Config) {
			c.DoctorBytesPerSlice = budget
			c.DoctorInterval = 24 * time.Hour
			c.DoctorSliceEvery = time.Minute
		}
	})
}

// many ingests n small append files (ids 1..n) of the same size and returns
// that size.
func (e *env) many(n int) int64 {
	e.t.Helper()
	var size int64
	for i := 1; i <= n; i++ {
		rel := fmt.Sprintf("projects/example-project/other-%03d.jsonl", i)
		c := line(i)
		size = int64(len(c))
		e.write(rel, c, e.clk.Now().Add(-time.Hour), false)
		if _, err := e.cp.Ingest(e.path(rel)); err != nil {
			e.t.Fatal(err)
		}
	}
	return size
}

func (e *env) tickDoctor() {
	e.t.Helper()
	if err := e.sett.DoctorTick(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) ctr(name string, labels map[string]string) float64 {
	v, _ := e.obs.Reg.Counter(name, labels)
	return v
}

func (e *env) slices(outcome string) float64 {
	return e.ctr(obs.MDoctorSlices, map[string]string{"outcome": outcome})
}

func TestRollingCycleChecksEachFileOnceAndSurvivesARestart(t *testing.T) {
	const n, perSlice = 100, 10
	e := rollEnv(t, 0)
	size := e.many(n)
	budget := size * perSlice
	e.extra = func(c *Config) {
		c.DoctorBytesPerSlice = budget
		c.DoctorInterval = 24 * time.Hour
		c.DoctorSliceEvery = time.Minute
	}
	counts := map[int64]int{}
	arm := func() { e.sett.doctorSeamRef().hashing = func(f int64) { counts[f]++ } }
	e.crash() // the settler is rebuilt with the budget
	arm()
	for i := 1; i <= perSlice; i++ {
		if i == 6 {
			e.crash() // a restart mid-cycle: the cursor file carries on
			arm()
		}
		if _, ok := LoadDoctor(e.state); ok {
			t.Fatalf("doctor.json written after %d slices, before the cycle ended", i-1)
		}
		e.tickDoctor()
	}
	got, ok := LoadDoctor(e.state)
	if !ok || got.FilesChecked != n || len(got.Mismatches) != 0 {
		t.Fatalf("after ten slices: %v %+v", ok, got)
	}
	if got.AtNS != e.clk.Now().UnixNano() {
		t.Fatalf("at_ns %d, want the cycle end %d", got.AtNS, e.clk.Now().UnixNano())
	}
	if len(counts) != n {
		t.Fatalf("%d files hashed, want %d", len(counts), n)
	}
	for f, c := range counts {
		if c != 1 {
			t.Fatalf("file %d hashed %d times", f, c)
		}
	}
	if v, _ := e.obs.Reg.Gauge(obs.MDoctorLastRun, nil); v != float64(e.clk.Now().UnixNano())/1e9 {
		t.Fatalf("doctor_last_run_timestamp %v", v)
	}
	if v, _ := e.obs.Reg.Gauge(obs.MDoctorCycleProgress, nil); v != 1 {
		t.Fatalf("progress %v", v)
	}
	if c := e.loadCursor(); c.Active {
		t.Fatalf("cycle still open: %+v", c)
	}
}

func (e *env) loadCursor() DoctorCursor { return e.sett.loadCursor() }

func TestRollingSliceReleasesTheCopierLockBetweenFiles(t *testing.T) {
	e := rollEnv(t, 1<<30)
	e.many(3)
	inHash := make(chan struct{})
	release := make(chan struct{})
	e.sett.doctorSeamRef().hashing = func(int64) {
		inHash <- struct{}{}
		<-release
	}
	done := make(chan error, 1)
	go func() { done <- e.sett.DoctorTick() }()
	<-inHash // a file is being hashed
	got := make(chan struct{})
	go func() {
		_ = e.cp.WithLock(func() error { close(got); return nil })
	}()
	select {
	case <-got: // an ingest is not blocked while the slice hashes
	case <-time.After(5 * time.Second):
		t.Fatal("the copier lock is held while a file is hashed")
	}
	go func() {
		for range inHash {
		}
	}()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// rollingFixtures build a store with one thing wrong each; the rolling run must
// report exactly what the synchronous run reports on the same fixture.
var rollingFixtures = map[string]func(*testing.T, *env){
	"corrupt copy": func(t *testing.T, e *env) {
		e.seed(5, time.Hour)
		f, _ := e.st.File(e.fileID())
		cp := e.st.CopyPath(f.ID, f.CurGen)
		b, _ := os.ReadFile(cp)
		b[10] ^= 0xff
		if err := os.WriteFile(cp, b, 0o600); err != nil {
			t.Fatal(err)
		}
	},
	"bad manifest": func(t *testing.T, e *env) {
		e.oldSettled(5, 48*time.Hour)
		if err := os.WriteFile(e.st.SegDir(sess)+"/manifest.json", []byte(`{"session":"other"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	},
	"journal differs": func(t *testing.T, e *env) {
		e.seed(3, time.Hour)
		e.skipInv = true
		if err := e.st.WithWrite("", func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE files SET last_size=last_size+1`)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	},
	"clean": func(t *testing.T, e *env) { e.seed(5, time.Hour) },
}

func TestRollingDoctorEqualsTheSynchronousOracle(t *testing.T) {
	for name, build := range rollingFixtures {
		t.Run(name, func(t *testing.T) {
			sync := newEnv(t, false)
			build(t, sync)
			want, err := sync.sett.Doctor()
			if err != nil {
				t.Fatal(err)
			}
			roll := rollEnv(t, 1<<30)
			build(t, roll)
			got, err := roll.sett.Doctor()
			if err != nil {
				t.Fatal(err)
			}
			got.AtNS, want.AtNS = 0, 0
			for i := range got.Mismatches {
				got.Mismatches[i].Path = strings.TrimPrefix(got.Mismatches[i].Path, roll.claude)
			}
			for i := range want.Mismatches {
				want.Mismatches[i].Path = strings.TrimPrefix(want.Mismatches[i].Path, sync.claude)
			}
			// The journal finding's detail quotes rows holding each env's own
			// inode numbers; compare the part that names the table.
			for _, r := range []*DoctorResult{&got, &want} {
				for i := range r.Mismatches {
					if r.Mismatches[i].Kind == MismatchJournal {
						r.Mismatches[i].Detail, _, _ = strings.Cut(r.Mismatches[i].Detail, ": files: ")
					}
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("rolling %+v\nsynchronous %+v", got, want)
			}
		})
	}
}

func TestRollingRepairsACorruptCopyWithANewGeneration(t *testing.T) {
	e := rollEnv(t, 1<<30)
	content := e.seed(5, time.Hour)
	file := e.fileID()
	f, _ := e.st.File(file)
	rollingFixtures["corrupt copy"](t, e)
	res, err := e.sett.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mismatches) != 1 || !res.Mismatches[0].Repaired {
		t.Fatalf("%+v", res)
	}
	after, _ := e.st.File(file)
	if after.CurGen != f.CurGen+1 {
		t.Fatalf("generation %d, want %d", after.CurGen, f.CurGen+1)
	}
	e.ingest(e.transcript())
	if got := e.raw(file, len(content)); got != content {
		t.Fatal("the new generation does not hold the original's bytes")
	}
}

func TestRollingDropsAFindingWhenTheGenerationChangesBeforeTheRecheck(t *testing.T) {
	e := rollEnv(t, 1<<30)
	e.seed(5, time.Hour)
	file := e.fileID()
	f, _ := e.st.File(file)
	cp := e.st.CopyPath(file, f.CurGen)
	b, _ := os.ReadFile(cp)
	b[10] ^= 0xff
	if err := os.WriteFile(cp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	first := true
	e.sett.doctorSeamRef().recheck = func(id int64) {
		if !first {
			return
		}
		first = false
		orig := e.path(e.transcript())
		if err := e.cp.WithLock(func() error { return e.cp.NewGenerationLocked(f, orig, "test") }); err != nil {
			t.Error(err)
		}
	}
	res, err := e.sett.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mismatches) != 0 {
		t.Fatalf("a finding over a changed generation was kept: %+v", res.Mismatches)
	}
	if v := e.ctr(obs.MDoctorRaceSkips, nil); v != 1 {
		t.Fatalf("doctor_race_skips_total %v, want 1", v)
	}
}

func TestRollingRequeuesOnceThenDrops(t *testing.T) {
	var c DoctorCursor
	c.requeue(7, false)
	c.requeue(7, true)  // the retry itself: dropped
	c.requeue(7, false) // already requeued once
	if !reflect.DeepEqual(c.Retry, []int64{7}) {
		t.Fatalf("retry %v", c.Retry)
	}
}

func TestRollingCopyReclaimedUnderTheSliceIsSkippedGone(t *testing.T) {
	e := rollEnv(t, 1<<30)
	e.seed(5, time.Hour)
	e.skipInv = true // the test deletes a copy on purpose
	file := e.fileID()
	f, _ := e.st.File(file)
	calls := 0
	e.sett.doctorSeamRef().hashing = func(int64) {
		calls++
		_ = os.Remove(e.st.CopyPath(file, f.CurGen))
	}
	e.tickDoctor()
	if calls != 2 {
		t.Fatalf("file checked %d times, want once plus one requeue", calls)
	}
	if v := e.slices("skipped_gone"); v != 2 {
		t.Fatalf("skipped_gone %v, want 2", v)
	}
	res, ok := LoadDoctor(e.state)
	if !ok || len(res.Mismatches) != 0 {
		t.Fatalf("a reclaimed copy is not a mismatch: %v %+v", ok, res)
	}
}

func TestRollingSliceIsDeferredWhileTheCopierIsBehind(t *testing.T) {
	e := rollEnv(t, 1<<30)
	e.seed(5, time.Hour)
	e.obs.Reg.Set(obs.MCopyLagBytes, nil, float64(defaultLagBound)+1)
	e.tickDoctor()
	if v := e.slices("deferred"); v != 1 {
		t.Fatalf("deferred %v", v)
	}
	if _, ok := LoadDoctor(e.state); ok {
		t.Fatal("a deferred slice ran")
	}
	e.obs.Reg.Set(obs.MCopyLagBytes, nil, 0)
	e.tickDoctor()
	if _, ok := LoadDoctor(e.state); !ok {
		t.Fatal("the slice did not run once the copier caught up")
	}
}

func TestRollingCursorKeepsTheFindingsAcrossARestart(t *testing.T) {
	e := rollEnv(t, 1)
	size := e.many(3)
	_ = size
	f, _ := e.st.File(1)
	cp := e.st.CopyPath(1, f.CurGen)
	b, _ := os.ReadFile(cp)
	b[10] ^= 0xff
	if err := os.WriteFile(cp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	e.tickDoctor() // file 1: a finding, repaired
	if v, _ := e.obs.Reg.Gauge(obs.MDoctorMismatchFiles, nil); v != 1 {
		t.Fatalf("doctor_mismatch_files after the first slice %v, want 1 (updated per slice)", v)
	}
	if _, ok := LoadDoctor(e.state); ok {
		t.Fatal("doctor.json written mid-cycle")
	}
	c := e.loadCursor()
	if !c.Active || len(c.Mismatches) != 1 || !c.Mismatches[0].Repaired || c.Mismatches[0].File != 1 {
		t.Fatalf("cursor %+v", c)
	}
	e.crash()
	e.tickDoctor()
	e.tickDoctor()
	res, ok := LoadDoctor(e.state)
	if !ok || len(res.Mismatches) != 1 || res.Mismatches[0].Kind != MismatchCopy || res.MismatchFiles != 1 || res.FilesChecked != 3 {
		t.Fatalf("after the restart: %v %+v", ok, res)
	}
}

func TestRollingIdleSliceRunsTheIdleStepOncePerSlice(t *testing.T) {
	e := rollEnv(t, 1<<30)
	e.seed(5, time.Hour)
	calls := 0
	e.sett.SetDoctorIdleStep(func() error { calls++; return nil })
	e.tickDoctor() // a cycle is due: it runs, no idle step
	if calls != 0 {
		t.Fatalf("idle step ran in a working slice: %d", calls)
	}
	e.clk.Advance(time.Hour)
	e.tickDoctor()
	e.tickDoctor()
	if calls != 2 {
		t.Fatalf("idle step calls %d, want one per idle slice", calls)
	}
}

func TestRollingNewCycleStartsWhenTheLastIsOlderThanIntervalMinusExpected(t *testing.T) {
	e := rollEnv(t, 1<<30)
	e.seed(5, time.Hour)
	e.tickDoctor()
	end, _ := LoadDoctor(e.state)
	// duration 0: the one-hour floor, so the next cycle starts 23 h after the end
	e.clk.Advance(23*time.Hour - time.Minute)
	e.tickDoctor()
	if got, _ := LoadDoctor(e.state); got.AtNS != end.AtNS {
		t.Fatal("a cycle started before interval minus the expected length")
	}
	e.clk.Advance(time.Minute)
	e.tickDoctor()
	if got, _ := LoadDoctor(e.state); got.AtNS != e.clk.Now().UnixNano() {
		t.Fatal("no new cycle once the last is old enough")
	}
	// a long previous cycle starts the next one earlier
	c := e.loadCursor()
	c.LastDurationNS = int64(3 * time.Hour)
	if err := e.sett.saveCursor(&c); err != nil {
		t.Fatal(err)
	}
	last, _ := LoadDoctor(e.state)
	e.clk.Advance(21*time.Hour - time.Minute)
	e.tickDoctor()
	if got, _ := LoadDoctor(e.state); got.AtNS != last.AtNS {
		t.Fatal("a cycle started early")
	}
	e.clk.Advance(time.Minute)
	e.tickDoctor()
	if got, _ := LoadDoctor(e.state); got.AtNS != e.clk.Now().UnixNano() {
		t.Fatal("the previous cycle's duration did not bring the next one forward")
	}
}

func TestDoctorRunTakesOverAnOpenCycleAndTheRollingTickStandsDown(t *testing.T) {
	e := rollEnv(t, 1)
	e.many(4)
	e.tickDoctor()
	if !e.loadCursor().Active {
		t.Fatal("no cycle open")
	}
	ans, err := e.sett.DoctorRun()
	if err != nil {
		t.Fatal(err)
	}
	if ans.TookOver == nil || ans.TookOver.TotalFiles != 4 || ans.TookOver.Processed != 1 || ans.FilesChecked != 4 {
		t.Fatalf("%+v", ans)
	}
	if e.loadCursor().Active {
		t.Fatal("the cycle is still open after the CLI run")
	}
	// a held cursor: the tick does nothing
	e.sett.doctorMuRef().Lock()
	before := e.slices("ok")
	e.clk.Advance(48 * time.Hour)
	e.tickDoctor()
	e.sett.doctorMuRef().Unlock()
	if e.slices("ok") != before {
		t.Fatal("a rolling slice ran while the CLI held the cursor")
	}
}

func TestRollingProjectionAndGauges(t *testing.T) {
	e := rollEnv(t, 0)
	size := e.many(5)
	e.sett.setDoctorBudget(size * 2)
	total, proj, err := e.sett.DoctorProjection()
	if err != nil || total != 5*size || proj != 3*time.Minute {
		t.Fatalf("%v: %d bytes, %v", err, total, proj)
	}
	if v, _ := e.obs.Reg.Gauge(obs.MDoctorProjectedCycle, nil); v != 180 {
		t.Fatalf("doctor_projected_cycle_seconds %v", v)
	}
	e.tickDoctor()
	if v, _ := e.obs.Reg.Gauge(obs.MDoctorCycleProgress, nil); v != 0.4 {
		t.Fatalf("progress %v", v)
	}
	if h := e.obs.Reg.Hist(obs.MDoctorSliceDuration); h == nil || h.Samples != 1 {
		t.Fatalf("slice duration %+v", h)
	}
	e.clk.Advance(90 * time.Second)
	if err := e.sett.DoctorTick(); err != nil {
		t.Fatal(err)
	}
	if v, _ := e.obs.Reg.Gauge(obs.MDoctorCursorAge, nil); v != 0 {
		t.Fatalf("cursor age %v right after a write", v)
	}
	for i := 0; i < 5; i++ {
		e.tickDoctor()
	}
	if v, _ := e.obs.Reg.Gauge(obs.MDoctorCycleDuration, nil); v != 90 {
		t.Fatalf("doctor_cycle_duration_seconds %v, want 90", v)
	}
}

func TestRollingArchiveHashingUsesTheSameBudget(t *testing.T) {
	e := rollEnv(t, 1)
	e.oldSettled(5, 48*time.Hour)
	e.tickDoctor()
	if _, ok := LoadDoctor(e.state); ok {
		t.Fatal("the cycle ended in the first slice although the budget was spent before the archives")
	}
	e.tickDoctor()
	res, ok := LoadDoctor(e.state)
	if !ok || res.ArchivesCheck != 1 || len(res.Mismatches) != 0 {
		t.Fatalf("%v %+v", ok, res)
	}
}

func TestBytesPerSliceZeroKeepsTheSynchronousPath(t *testing.T) {
	e := rollEnv(t, 0)
	e.seed(5, time.Hour)
	if err := e.sett.DoctorTick(); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadDoctor(e.state); ok {
		t.Fatal("the rolling tick ran with bytes_per_slice = 0")
	}
	res, err := e.sett.Doctor()
	if err != nil || res.FilesChecked != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	if _, err := os.Stat(e.state + "/" + DoctorCursorFile); err == nil {
		t.Fatal("the synchronous path wrote a cursor")
	}
}
