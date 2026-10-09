package engine_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/store"
)

// verifyUnchanged runs Verify on path and checks that the log file is the
// same afterwards.
func verifyUnchanged(t *testing.T, path, file string) engine.VerifyReport {
	t.Helper()
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := engine.Verify(path)
	if err != nil {
		t.Fatalf("Verify(%s): %v", path, err)
	}
	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("Verify modified the log")
	}
	return rep
}

// writeLog replaces the log file with b while the engine has it open.
func (h *harness) writeLog(b []byte) {
	h.t.Helper()
	if err := os.WriteFile(h.path(), b, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func TestVerifyOffline(t *testing.T) {
	t.Run("clean, while the engine holds the lock", func(t *testing.T) {
		h := newHarness(t, nil)
		h.bootstrap()
		h.at(local(9, 5), command.CompleteTask{TaskID: taskOf(7, "plan-day")})
		for _, path := range []string{h.dir, h.path()} {
			rep := verifyUnchanged(t, path, h.path())
			if !rep.OK() || rep.Check.Problem != nil || rep.Invalid != nil || !rep.Replayed {
				t.Errorf("Verify(%s) = %+v, want a clean report", path, rep)
			}
			if rep.Check.Lines != h.lines() || rep.Check.Batches != 1 || rep.Check.Recovery != (store.Recovery{}) {
				t.Errorf("Verify(%s): %d lines, %d batches, recovery %+v", path, rep.Check.Lines, rep.Check.Batches, rep.Check.Recovery)
			}
		}
	})
	t.Run("a torn tail", func(t *testing.T) {
		h := newHarness(t, nil)
		h.bootstrap()
		lines := h.lines()
		h.writeLog(append(h.logBytes(), `{"v":1,"id":"01J`...))
		rep := verifyUnchanged(t, h.dir, h.path())
		if !rep.OK() || !rep.Check.Recovery.TornTail || rep.Check.Lines != lines || !rep.Replayed {
			t.Errorf("Verify = %+v, want a torn tail the next start recovers, and the %d committed lines replayed", rep, lines)
		}
	})
	t.Run("corruption", func(t *testing.T) {
		h := newHarness(t, nil)
		h.bootstrap()
		b := h.logBytes()
		first := bytes.IndexByte(b, '\n') + 1
		corrupt := append(append(append([]byte{}, b[:first]...), "not an event\n"...), b[first:]...)
		h.writeLog(corrupt)
		rep := verifyUnchanged(t, h.dir, h.path())
		var ce *store.CorruptError
		if !errors.As(rep.Check.Problem, &ce) || ce.Line != 2 {
			t.Fatalf("Problem %v, want a *store.CorruptError at line 2", rep.Check.Problem)
		}
		if rep.OK() || rep.Replayed || rep.Invalid != nil {
			t.Errorf("Verify = %+v, want a refusal with no replay", rep)
		}
	})
	t.Run("an impossible timeline", func(t *testing.T) {
		golden, err := os.ReadFile("../../testdata/logs/tasks/two-resolutions.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		file := filepath.Join(dir, logName)
		if err := os.WriteFile(file, golden, 0o600); err != nil {
			t.Fatal(err)
		}
		rep := verifyUnchanged(t, dir, file)
		if rep.Check.Problem != nil || rep.Replayed || rep.OK() {
			t.Fatalf("Verify = %+v, want a log that reads but does not replay", rep)
		}
		if rep.Invalid == nil || string(rep.Invalid.Code) != string(command.ReasonTaskAlreadyResolved) ||
			!strings.Contains(rep.Invalid.Message, "day:2026-10-07:post-plan") {
			t.Errorf("Invalid = %+v, want task_already_resolved naming the task", rep.Invalid)
		}
		if _, err := os.Stat(filepath.Join(dir, "events.jsonl.lock")); !os.IsNotExist(err) {
			t.Errorf("Verify created the lock file (%v)", err)
		}
	})
	t.Run("a missing log", func(t *testing.T) {
		if _, err := engine.Verify(filepath.Join(t.TempDir(), "absent")); err == nil {
			t.Error("Verify of a missing path succeeded")
		}
	})
}

func TestVerifyReportOKAgreesWithReplayed(t *testing.T) {
	// A replay error that is not a finding leaves this report: no problem,
	// no finding and nothing replayed. It is not OK.
	if (engine.VerifyReport{}).OK() {
		t.Error("a report whose log was not replayed is OK")
	}
	if !(engine.VerifyReport{Replayed: true}).OK() {
		t.Error("a report whose log replayed is not OK")
	}
	h := newHarness(t, nil)
	h.bootstrap()
	rep, err := engine.Verify(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Replayed || !rep.OK() {
		t.Errorf("a clean log: Replayed %v, OK %v", rep.Replayed, rep.OK())
	}
}
