//go:build crashsweep

package settle

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/invariants"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// TestCrashSweep stops a scenario after each of its N file-system operations,
// reopens the store, runs the recovery a daemon start runs, checks the
// invariants and finishes the scenario. Each scenario runs twice: a plain crash
// (written data survives) and a power loss (data no sync made durable is
// discarded). Run it with `go test -tags crashsweep -run TestCrashSweep` or
// through checks.<system>.pg-claude-code-recorder-crashsweep-tests.

type sweepState struct {
	content string
	file    int64
	// holder is set by the restore scenario: while true the holder listing
	// reports a process, so removal restores the file.
	holder bool
}

type sweepScenario struct {
	name   string
	clone  bool
	remove bool
	// setup builds the state the operation starts from (not counted).
	setup func(e *env, s *sweepState)
	// run is the operation under sweep.
	run func(e *env, s *sweepState)
	// beforeRecover may change the world while the process is down.
	beforeRecover func(e *env, s *sweepState)
	// skipImmediate skips the invariant check right after recovery: the setup
	// planted damage (a corrupt copy) that only the operation under sweep repairs.
	skipImmediate bool
	// finish completes the scenario after recovery and asserts the outcome.
	finish func(e *env, s *sweepState)
}

func sweepScenarios() []sweepScenario {
	settleSetup := func(e *env, s *sweepState) { s.content = e.seed(6, 48*time.Hour); s.file = e.fileID() }
	settleFinish := func(e *env, s *sweepState) {
		for i := 0; i < 3 && e.stage() != StageSettled; i++ {
			e.tick()
		}
		if got := e.stage(); got != StageSettled {
			e.t.Fatalf("stage %s after recovery and ticks, want settled", got)
		}
		if got := e.raw(s.file, len(s.content)); got != s.content {
			e.t.Fatal("bytes lost")
		}
	}
	removalSetup := func(e *env, s *sweepState) {
		s.content = e.oldSettled(6, removeAge+time.Hour)
		s.file = e.fileID()
	}
	return []sweepScenario{
		{
			name: "settle-clone", clone: true,
			setup:  settleSetup,
			run:    func(e *env, _ *sweepState) { _, _ = e.sett.Tick() },
			finish: settleFinish,
		},
		{
			name:   "settle-byte",
			setup:  settleSetup,
			run:    func(e *env, _ *sweepState) { _, _ = e.sett.Tick() },
			finish: settleFinish,
		},
		{
			name: "removal", remove: true,
			setup: removalSetup,
			run: func(e *env, _ *sweepState) {
				_, _ = e.sett.Remove(RemoveOptions{Sessions: []string{sess}})
			},
			finish: func(e *env, s *sweepState) {
				// Recovery either completed the removal or put the file back.
				switch e.stage() {
				case store.StageRemoved:
				case StageSettled:
					if e.readOrig(e.transcript()) != s.content {
						e.t.Fatal("the restored file differs")
					}
				default:
					e.t.Fatalf("stage %s after recovery", e.stage())
				}
				if rep := e.removeIfNeeded(); rep != nil && len(rep.Removed) > 1 {
					e.t.Fatalf("%+v", rep)
				}
				if got := e.stage(); got != store.StageRemoved {
					e.t.Fatalf("stage %s after removing again, want removed", got)
				}
				if e.exists(e.transcript()) {
					e.t.Fatal("the file is still there")
				}
				if got := e.raw(s.file, len(s.content)); got != s.content {
					e.t.Fatal("bytes lost")
				}
			},
		},
		{
			// A holder is found: removal aborts and restores the file.
			name: "removal-abort-restore", remove: true,
			setup: func(e *env, s *sweepState) { removalSetup(e, s); s.holder = true },
			run: func(e *env, _ *sweepState) {
				_, _ = e.sett.Remove(RemoveOptions{Sessions: []string{sess}})
			},
			finish: func(e *env, s *sweepState) {
				s.holder = false
				if e.stage() == StageSettled && e.readOrig(e.transcript()) != s.content {
					e.t.Fatal("the restored file differs")
				}
				if rep := e.removeIfNeeded(); rep != nil && len(rep.Removed) > 1 {
					e.t.Fatalf("%+v", rep)
				}
				if got := e.stage(); got != store.StageRemoved {
					e.t.Fatalf("stage %s after removing again, want removed", got)
				}
				if got := e.raw(s.file, len(s.content)); got != s.content {
					e.t.Fatal("bytes lost")
				}
			},
		},
		{
			// A process comes back while the recorder is down: recovery must
			// restore a quarantined file instead of finishing the removal.
			name: "removal-restore-live", remove: true,
			setup: removalSetup,
			run: func(e *env, _ *sweepState) {
				_, _ = e.sett.Remove(RemoveOptions{Sessions: []string{sess}})
			},
			beforeRecover: func(e *env, _ *sweepState) { e.live[sess] = true },
			finish: func(e *env, s *sweepState) {
				switch e.stage() {
				case store.StageRemoved:
					if e.exists(e.transcript()) {
						e.t.Fatal("removed, but the file is back")
					}
				case StageSettled, StageQuiet:
					if e.readOrig(e.transcript()) != s.content {
						e.t.Fatal("the restored file differs")
					}
				default:
					e.t.Fatalf("stage %s after recovery", e.stage())
				}
				if got := e.raw(s.file, len(s.content)); got != s.content {
					e.t.Fatal("bytes lost")
				}
			},
		},
		{
			name: "extract", remove: true,
			setup: func(e *env, s *sweepState) {
				removalSetup(e, s)
				e.remove(sess)
			},
			run: func(e *env, _ *sweepState) { _, _ = e.sett.Extract(sess, "") },
			finish: func(e *env, s *sweepState) {
				if st := e.stage(); st == store.StageRemoved || st == store.StageExtracting {
					if _, err := e.sett.Extract(sess, ""); err != nil {
						e.t.Fatalf("running the extract again: %v", err)
					}
				}
				if got := e.readOrig(e.transcript()); got != s.content {
					e.t.Fatal("bytes differ after the retried extract")
				}
				if e.stage() != StageQuiet {
					e.t.Fatalf("stage %s", e.stage())
				}
				if m, _ := filepath.Glob(e.path("projects/example-project/*.tmp.pgrec")); len(m) != 0 {
					e.t.Fatalf("a temporary file is left: %v", m)
				}
			},
		},
		{
			name: "doctor-repair", skipImmediate: true,
			setup: func(e *env, s *sweepState) {
				s.content = e.seed(5, time.Hour)
				s.file = e.fileID()
				f, _ := e.st.File(s.file)
				cp := e.st.CopyPath(s.file, f.CurGen)
				b, _ := os.ReadFile(cp)
				b[10] ^= 0xff
				if err := os.WriteFile(cp, b, 0o600); err != nil {
					e.t.Fatal(err)
				}
			},
			run: func(e *env, _ *sweepState) { _, _ = e.sett.Doctor() },
			finish: func(e *env, s *sweepState) {
				for i := 0; i < 3; i++ {
					res, err := e.sett.Doctor()
					if err != nil {
						e.t.Fatal(err)
					}
					e.ingest(e.transcript()) // the copier fills a new generation
					if len(res.Mismatches) == 0 {
						break
					}
				}
				res, err := e.sett.Doctor()
				if err != nil || len(res.Mismatches) != 0 {
					e.t.Fatalf("the doctor still finds a mismatch: %v %+v", err, res)
				}
				if got := e.raw(s.file, len(s.content)); got != s.content {
					e.t.Fatal("the current generation does not hold the original's bytes")
				}
			},
		},
	}
}

// removeIfNeeded runs removal again when the session is not yet removed.
func (e *env) removeIfNeeded() *RemoveReport {
	if e.stage() == store.StageRemoved {
		return nil
	}
	// An aborted removal puts the session back to quiet; it settles again first.
	for i := 0; i < 3 && e.stage() != StageSettled; i++ {
		e.tickSettleOnly()
	}
	rep := e.remove(sess)
	return &rep
}

// sweepEnv builds an env for the scenario whose file system is sw.
func sweepEnv(t *testing.T, sc sweepScenario, sw *sweepFS, st *sweepState) *env {
	return newEnvWith(t, sc.clone, func(e *env) {
		e.removeOn = sc.remove
		e.innerFS = sw
		e.commitHook = sw.commit
		e.holders = func(string) ([]int, error) {
			if st.holder {
				return []int{os.Getpid() + 1}, nil
			}
			return nil, nil
		}
		e.policy = &sweepClone{fakeClone: e.clone, sw: sw}
	})
}

// sweepOnce runs the scenario with a crash after op `limit` (0 only counts) and
// returns the number of operations it performed and their log.
func sweepOnce(t *testing.T, sc sweepScenario, power bool, limit int) (int, []string) {
	t.Helper()
	sw := newSweepFS()
	st := &sweepState{}
	e := sweepEnv(t, sc, sw, st)
	sc.setup(e, st)
	sw.arm(limit)
	crashed, sig := runUntilCrash(func() { sc.run(e, st) })
	if limit > 0 && !crashed {
		t.Fatalf("the scenario finished before operation %d (it performed %d)", limit, sw.opCount())
	}
	if limit == 0 && crashed {
		t.Fatalf("crashed while only counting: %+v", sig)
	}
	n, ops := sw.opCount(), sw.opLog()
	if !crashed {
		return n, ops
	}
	t.Logf("crashed after operation %d: %s", sig.After, sig.Op)
	// The process is gone: close the store, drop what a power loss drops, and
	// start again on a clean file system.
	_ = e.st.Close()
	if power {
		t.Logf("power loss discarded %d unsynced files", sw.dropUnsynced())
	}
	e.innerFS, e.commitHook, e.policy = nil, nil, nil
	if sc.beforeRecover != nil {
		sc.beforeRecover(e, st)
	}
	e.crash()
	if v := invariants.Violations(e.st, invariants.Options{
		ClaudeDir: e.claude, AllowClaudeChange: e.removeOn, ArchiveCompare: invariants.RecordedBytes(e.st),
	}); len(v) != 0 && !sc.skipImmediate {
		t.Fatalf("invariants violated right after recovery: %v", v)
	}
	sc.finish(e, st)
	return n, ops
}

func TestCrashSweep(t *testing.T) {
	for _, sc := range sweepScenarios() {
		for _, power := range []bool{false, true} {
			mode := "crash"
			if power {
				mode = "power"
			}
			t.Run(sc.name+"/"+mode, func(t *testing.T) {
				var n int
				var ops []string
				t.Run("count", func(t *testing.T) { n, ops = sweepOnce(t, sc, power, 0) })
				if n == 0 {
					t.Fatal("the scenario performed no file-system operation")
				}
				t.Logf("scenario %s/%s: %d operations", sc.name, mode, n)
				for i, o := range ops {
					t.Logf("  op %03d %s", i+1, o)
				}
				for i := 1; i <= n; i++ {
					t.Run(fmt.Sprintf("after-op-%03d", i), func(t *testing.T) { sweepOnce(t, sc, power, i) })
				}
			})
		}
	}
}
