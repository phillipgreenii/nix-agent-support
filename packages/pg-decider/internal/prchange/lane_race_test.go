package prchange

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The concurrency and failure-injection scenarios of the lane suite (docket
// pg2-6mo45.3, packet P4c-2), against the same tools and tracker as the review
// scenarios (lane_review_test.go; the runs are described in lane_world_test.go).

// bgRun is the composed command started in its own process group, so a test can
// let it run on while it does something else, or end the whole group.
type bgRun struct {
	cmd         *exec.Cmd
	out, errb   bytes.Buffer
	done        chan struct{}
	exitErr     error
	killedGroup bool
}

// startChange starts the real composed command with args, in its own process
// group, and kills the group when the test ends.
func (r *rig) startChange(args ...string) *bgRun {
	r.t.Helper()
	b := &bgRun{done: make(chan struct{})}
	b.cmd = exec.Command(r.changeBin, args...)
	b.cmd.Env = r.env
	b.cmd.Dir = filepath.Join(r.dir, "work")
	b.cmd.Stdout, b.cmd.Stderr = &b.out, &b.errb
	b.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	must(r.t, b.cmd.Start())
	go func() { b.exitErr = b.cmd.Wait(); close(b.done) }()
	r.t.Cleanup(func() { _ = syscall.Kill(-b.cmd.Process.Pid, syscall.SIGKILL); <-b.done })
	return b
}

// finished reports whether the process has exited.
func (b *bgRun) finished() bool {
	select {
	case <-b.done:
		return true
	default:
		return false
	}
}

// wait returns the finished run's result, failing the test when it does not
// finish within limit.
func (b *bgRun) wait(r *rig, limit time.Duration) result {
	r.t.Helper()
	select {
	case <-b.done:
	case <-time.After(limit):
		_ = syscall.Kill(-b.cmd.Process.Pid, syscall.SIGKILL)
		r.t.Fatalf("a composed run did not finish within %s:\n%s%s", limit, b.out.String(), b.errb.String())
	}
	res := result{stdout: b.out.String(), stderr: b.errb.String(), code: b.cmd.ProcessState.ExitCode()}
	r.checkExit(res)
	return res
}

// kill ends the whole process group and waits for it.
func (b *bgRun) kill() {
	_ = syscall.Kill(-b.cmd.Process.Pid, syscall.SIGKILL)
	<-b.done
}

// until polls cond every 50 ms for at most limit.
func until(t *testing.T, what string, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", limit, what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (r *rig) ctlExists(name string) bool {
	_, err := os.Stat(filepath.Join(r.ctl, name))
	return err == nil
}

const raceLimit = 5 * time.Minute

// anchorsAfterRace runs two composed runs for one PR whose interleaving the
// forge fake's block control forces, and returns how many anchors the tracker
// holds afterwards. withLock selects the real command (one lock directory, so
// the second run waits for the first) or the test double of it that skips the
// lock (each run gets a lock directory of its own, so the two never exclude each
// other); the double is the real binary, so the only difference is the lock.
func anchorsAfterRace(t *testing.T, tools e2eTools, withLock bool) int {
	t.Helper()
	w, id := freshWorld(t, tools, sha('a'), false)
	r := w.r
	args := func(n string) []string {
		if withLock {
			// The wait is long: under load the first run can take longer than the
			// default 30 s lock wait to finish after the block is lifted.
			return []string{"apply", id, "--lock-wait", "10m"}
		}
		return []string{"apply", id, "--lock-dir", filepath.Join(r.dir, "locks-"+n)}
	}
	if !withLock {
		// The anchors are created one at a time by the real tracker, but only after
		// both runs have decided: each create waits for the other run's create.
		r.ctlWrite("barrier.create", "2\n")
	}
	r.ctlWrite("block", "")
	a := r.startChange(args("a")...)
	until(t, "the first run to reach the forge", raceLimit, func() bool { return len(r.calls("pr show")) >= 1 })
	// Let the first run settle at the block: it may make more than one forge
	// call before it waits, so the second run is measured against that count.
	time.Sleep(time.Second)
	held := len(r.calls("pr show"))
	b := r.startChange(args("b")...)
	if withLock {
		// The second run must be waiting for the lock, not reading the forge.
		time.Sleep(3 * time.Second)
		if n := len(r.calls("pr show")); n != held {
			t.Fatalf("the second run reached the forge (%d pr show calls, %d before it started) while the first held the lock:\n%s", n, held, r.ctlRead("calls.log"))
		}
		if b.finished() {
			t.Fatalf("the second run finished while the first held the lock:\n%s%s", b.out.String(), b.errb.String())
		}
	} else {
		until(t, "both runs to be past the lock and at the forge", raceLimit, func() bool { return len(r.calls("pr show")) > held })
	}
	r.ctlRemove("block")
	for name, run := range map[string]*bgRun{"first": a, "second": b} {
		if res := run.wait(r, raceLimit); res.code != 0 {
			t.Fatalf("the %s run exited %d:\n%s", name, res.code, res.stderr)
		}
	}
	return len(w.anchors())
}

// failureScenarios are the concurrency and failure-injection scenarios of the
// gate (design section 10, "Concurrency and failure injection").
func failureScenarios(l *laneRun) {
	tools, run := l.tools, l.run
	run("two-concurrent-composed-runs-for-one-pr-yield-exactly-one-anchor", func(t *testing.T) {
		if n := anchorsAfterRace(t, tools, true); n != 1 {
			t.Fatalf("two concurrent runs yielded %d anchors, want exactly one", n)
		}
	})

	run("the-same-race-against-a-double-that-skips-the-lock-yields-two-anchors", func(t *testing.T) {
		// The negative control: without the lock the same interleaving makes two
		// anchors, so the scenario above really exercises the race.
		if n := anchorsAfterRace(t, tools, false); n != 2 {
			t.Fatalf("the double that skips the lock yielded %d anchors, want 2 (the scenario does not exercise the race)", n)
		}
	})

	run("a-duplicate-pr-changed-writes-nothing", func(t *testing.T) {
		w, id := freshWorld(t, tools, sha('a'), false)
		w.apply(id, "7")
		settled := w.tr.snapshot()
		if len(settled) < 2 {
			t.Fatalf("setup: the first delivery left %d beads, want the anchor and the review item", len(settled))
		}
		start := w.callCount()
		for _, again := range [][]string{{id, "7"}, {id + "@0123456789ab", "7"}, {id}} {
			w.apply(again[0], again[1:]...)
		}
		if got := w.tr.snapshot(); sameSnapshot(settled, got) != "" {
			t.Errorf("a duplicate delivery wrote to the tracker: %s", sameSnapshot(settled, got))
		}
		if wr := w.writes(start); len(wr) != 0 {
			t.Errorf("a duplicate delivery made tracker writes: %v", wr)
		}
		if rec := w.lastRecord(); rec.Residual == nil || *rec.Residual != 0 {
			t.Errorf("a duplicate delivery still has residual actions: %+v", rec.Residual)
		}
	})

	run("a-kill-between-a-tracker-create-and-its-audit-comment-then-a-rerun-creates-no-duplicate", func(t *testing.T) {
		w, id := freshWorld(t, tools, sha('a'), false)
		w.r.ctlWrite("hold.comment", "")
		run := w.r.startChange("apply", id)
		until(t, "the run to reach its first audit comment", raceLimit, func() bool { return w.r.ctlExists("hold.comment.reached") })
		// The anchor exists, written by the create; its audit comment was never made.
		if a := w.anchors(); len(a) != 1 {
			t.Fatalf("at the first audit comment the tracker holds %d anchors, want the one just created", len(a))
		}
		run.kill()
		w.r.ctlRemove("hold.comment")
		// The rerun finds the killed run's anchor by its dedup key and creates
		// only what is missing.
		w.apply(id)
		if n := len(w.anchors()); n != 1 {
			t.Errorf("the rerun left %d anchors, want one", n)
		}
		if n := len(w.reviews()); n != 1 {
			t.Errorf("the rerun left %d review items, want one", n)
		}
		w.keysAreUnique()
		settled := w.tr.snapshot()
		start := w.callCount()
		w.apply(id)
		if got := w.tr.snapshot(); sameSnapshot(settled, got) != "" {
			t.Errorf("a further run wrote to the tracker: %s", sameSnapshot(settled, got))
		}
		if wr := w.writes(start); len(wr) != 0 {
			t.Errorf("a further run made tracker writes: %v", wr)
		}
	})

	run("a-decider-run-that-exits-2-converges-on-rerun-with-the-failure-injected-by-a-passthrough-over-bd", func(t *testing.T) {
		w, id := freshWorld(t, tools, sha('a'), true)
		// The first apply writes the anchor (create, audit comment) and then the
		// review item (create, audit comment): the third write is the review
		// item's create.
		w.failWrite(3)
		res := w.r.change("apply", id)
		if res.code != ExitDecidePartial {
			t.Fatalf("a run whose third write failed exited %d, want %d:\n%s", res.code, ExitDecidePartial, res.stderr)
		}
		contains(t, "the failure line", res.stderr, "class=decide_partial")
		log := w.bdWrites()
		if len(log) < 3 || log[2] != "3 create FAILED" {
			t.Fatalf("the injected failure was not the third write, a create: %v", log)
		}
		if rec := w.lastRecord(); rec.Decide == nil || rec.Decide.ExitCode != 2 {
			t.Fatalf("the run record does not show a decider exit of 2: %+v", rec.Decide)
		}
		if n := len(w.anchors()); n != 1 {
			t.Fatalf("the partial run left %d anchors, want the one it created", n)
		}
		if n := len(w.reviews()); n != 0 {
			t.Fatalf("the failed create left %d review items, want none", n)
		}
		// The rerun, with no failure injected, converges.
		w.failWrite(0)
		w.apply(id)
		if n := len(w.anchors()); n != 1 {
			t.Errorf("after the rerun there are %d anchors, want one", n)
		}
		if n := len(w.reviews()); n != 1 {
			t.Errorf("after the rerun there are %d review items, want one", n)
		}
		if rec := w.lastRecord(); rec.Residual == nil || *rec.Residual != 0 {
			t.Errorf("the rerun still has residual actions: %+v", rec.Residual)
		}
		w.keysAreUnique()
		settled := w.tr.snapshot()
		start := w.callCount()
		w.apply(id)
		if got := w.tr.snapshot(); sameSnapshot(settled, got) != "" {
			t.Errorf("the converged state was written to again: %s", sameSnapshot(settled, got))
		}
		if wr := w.writes(start); len(wr) != 0 {
			t.Errorf("the converged state made tracker writes: %v", wr)
		}
	})
}
