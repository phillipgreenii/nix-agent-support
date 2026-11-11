package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/server"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/settle"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

const exSess = "0a1b2c3d-0000-4000-8000-000000000001"

// opsDaemon serves the removal operations with canned answers and records what
// each request asked.
type opsDaemon struct {
	sock    string
	removes []serve.RemoveRequest
	enabled bool
}

func newOpsDaemon(t *testing.T, enabled bool, doctor any) *opsDaemon {
	t.Helper()
	d := &opsDaemon{sock: filepath.Join(shortDir(t), "r.sock"), enabled: enabled}
	s := server.New(obs.New(nil), nil, func() any { return map[string]any{} })
	s.Register("held", server.KindQuery, func(_ context.Context, b []byte) (any, *wire.Error) {
		var req serve.HeldRequest
		_ = json.Unmarshal(b, &req)
		return serve.HeldAnswer{Retried: map[bool]int{true: 1}[req.Retry], Held: []settle.HeldRow{{
			Session: exSess, Reason: settle.ReasonCopyTail, Since: time.Date(2026, 10, 29, 1, 2, 3, 0, time.UTC), Files: 1,
			Remedy: settle.Remedies[settle.ReasonCopyTail], Detail: "x.jsonl: tail hash differs",
		}}}, nil
	})
	s.Register("where", server.KindQuery, func(_ context.Context, b []byte) (any, *wire.Error) {
		var req serve.WhereRequest
		_ = json.Unmarshal(b, &req)
		if req.Session != exSess {
			return nil, wire.Errf(wire.ErrNotFound, "no Conversation "+req.Session)
		}
		next := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
		return settle.WhereInfo{
			Session: exSess, Stage: "settled", BlockedBy: []string{"removal_disabled", "quiet_age"}, NextEligible: &next,
			Clear: "set remove.enable = true", ArchivePath: "/example/state/archives/x", ParserVersion: 2, ClaudeCodeVersion: "2.1.0",
		}, nil
	})
	s.Register("remove", server.KindQuery, func(_ context.Context, b []byte) (any, *wire.Error) {
		var req serve.RemoveRequest
		_ = json.Unmarshal(b, &req)
		d.removes = append(d.removes, req)
		rep := settle.RemoveReport{
			Enabled: d.enabled, DryRun: req.DryRun, Candidates: []settle.RemoveCandidate{{Session: exSess, Files: 2, Bytes: 3e9}},
			Removed: []string{}, Skipped: []settle.RemoveSkip{{Session: "0a1b2c3d-0000-4000-8000-000000000002", Reasons: []string{"quiet_age"}}},
			SkipCounts: map[string]int{"quiet_age": 1}, Bytes: 3e9,
		}
		if !req.DryRun {
			if !d.enabled {
				return nil, wire.Errf(wire.ErrRemovalDisabled, "removal is disabled")
			}
			rep.Removed = []string{exSess}
		}
		return rep, nil
	})
	s.Register("extract", server.KindQuery, func(_ context.Context, b []byte) (any, *wire.Error) {
		var req serve.ExtractRequest
		_ = json.Unmarshal(b, &req)
		switch req.Session {
		case "exists":
			return nil, wire.Errf(wire.ErrTargetExists, "the target already exists: /example/claude/projects/p/x.jsonl")
		case "live":
			return nil, wire.Errf(wire.ErrHeldByLive, "a live Process holds the Conversation")
		}
		res := settle.ExtractResult{Session: req.Session, Paths: []string{"/example/claude/projects/p/" + req.Session + ".jsonl"}}
		if req.To == "" {
			res.Resume = "claude --resume " + req.Session
		}
		return res, nil
	})
	s.Register("doctor", server.KindQuery, func(context.Context, []byte) (any, *wire.Error) { return doctor, nil })
	if err := s.Listen(d.sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	return d
}

func runCLI(t *testing.T, a *app, args ...string) int {
	t.Helper()
	return a.run(args)
}

func TestHeldPrintsStableColumnsAndRemedy(t *testing.T) {
	d := newOpsDaemon(t, true, settle.DoctorResult{})
	a, out, _ := testApp(nil)
	if code := runCLI(t, a, "held", "--retry", "--socket", d.sock); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"SESSION  REASON  SINCE  FILES  REMEDY", exSess + "  copy_tail_mismatch  2026-10-29T01:02:03Z  1  ", "re-checked 1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, out.String())
		}
	}
	a, out, _ = testApp(nil)
	runCLI(t, a, "held", "--json", "--socket", d.sock)
	var ans serve.HeldAnswer
	if err := json.Unmarshal(out.Bytes(), &ans); err != nil || len(ans.Held) != 1 || ans.Held[0].Reason != "copy_tail_mismatch" {
		t.Fatalf("%v %s", err, out.String())
	}
}

func TestWherePrintsTheStageLine(t *testing.T) {
	d := newOpsDaemon(t, true, settle.DoctorResult{})
	a, out, _ := testApp(nil)
	if code := runCLI(t, a, "where", exSess, "--socket", d.sock); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{exSess + ": stage settled", "blocked_by: removal_disabled, quiet_age", "next_eligible: 2026-11-20T00:00:00Z", "clear: set remove.enable = true", "archive: /example/state/archives/x", "claude_code_version: 2.1.0"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, out.String())
		}
	}
	a, _, errw := testApp(nil)
	if code := runCLI(t, a, "where", "nope", "--socket", d.sock); code != 4 || !strings.HasPrefix(errw.String(), "error: not_found") {
		t.Fatalf("unknown session: %d %q", code, errw.String())
	}
}

func TestRemoveDryRunSaysDisabledFirst(t *testing.T) {
	d := newOpsDaemon(t, false, settle.DoctorResult{})
	a, out, _ := testApp(nil)
	if code := runCLI(t, a, "remove", "--dry-run", "--socket", d.sock); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.HasPrefix(out.String(), "removal is disabled (remove.enable = false)") {
		t.Fatalf("the disabled notice is not first:\n%s", out.String())
	}
	for _, want := range []string{"would remove 1 Conversation(s), 3.00 GB", "quiet_age: 1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, out.String())
		}
	}
	for _, r := range d.removes {
		if !r.DryRun {
			t.Fatal("a dry run sent a real remove")
		}
	}
}

func TestRemoveRefusesWhileDisabledWithExit8(t *testing.T) {
	d := newOpsDaemon(t, false, settle.DoctorResult{})
	a, _, errw := testApp(nil)
	if code := runCLI(t, a, "remove", exSess, "--yes", "--socket", d.sock); code != 8 || !strings.HasPrefix(errw.String(), "error: removal_disabled") {
		t.Fatalf("%d %q", code, errw.String())
	}
	for _, r := range d.removes {
		if !r.DryRun {
			t.Fatal("a real remove was sent while disabled")
		}
	}
}

func TestRemoveWithoutYesExits2WhenNotInteractiveAndPromptsWhenInteractive(t *testing.T) {
	d := newOpsDaemon(t, true, settle.DoctorResult{})
	a, out, errw := testApp(nil)
	if code := runCLI(t, a, "remove", exSess, "--socket", d.sock); code != 2 {
		t.Fatalf("exit %d, stderr %q", code, errw.String())
	}
	if !strings.Contains(out.String(), "no undo except extract: 1 Conversation(s), 3.00 GB") {
		t.Fatalf("the warning is not printed first:\n%s", out.String())
	}
	for _, r := range d.removes {
		if !r.DryRun {
			t.Fatal("removed without --yes")
		}
	}
	// Interactive: an answer of n removes nothing, y removes.
	a, out, _ = testApp(nil)
	a.isTTY = func() bool { return true }
	a.stdin = strings.NewReader("n\n")
	if code := runCLI(t, a, "remove", exSess, "--socket", d.sock); code != 0 || !strings.Contains(out.String(), "not removed") {
		t.Fatalf("n: %d %s", code, out.String())
	}
	a, out, _ = testApp(nil)
	a.isTTY = func() bool { return true }
	a.stdin = strings.NewReader("y\n")
	if code := runCLI(t, a, "remove", exSess, "--socket", d.sock); code != 0 || !strings.Contains(out.String(), "removed 1 Conversation(s)") {
		t.Fatalf("y: %d %s", code, out.String())
	}
}

func TestRemoveWithNoSessionNeedsAllEligible(t *testing.T) {
	d := newOpsDaemon(t, true, settle.DoctorResult{})
	a, _, errw := testApp(nil)
	if code := runCLI(t, a, "remove", "--yes", "--socket", d.sock); code != 2 || !strings.Contains(errw.String(), "--all-eligible") {
		t.Fatalf("%d %q", code, errw.String())
	}
	a, out, _ := testApp(nil)
	if code := runCLI(t, a, "remove", "--all-eligible", "--yes", "--socket", d.sock); code != 0 || !strings.Contains(out.String(), "removed 1 Conversation(s)") {
		t.Fatalf("%d %s", code, out.String())
	}
}

func TestExtractPrintsPathsAndResumeLineAndMapsExit8(t *testing.T) {
	d := newOpsDaemon(t, true, settle.DoctorResult{})
	a, out, _ := testApp(nil)
	if code := runCLI(t, a, "extract", exSess, "--socket", d.sock); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "restored /example/claude/projects/p/"+exSess+".jsonl") || !strings.Contains(out.String(), "claude --resume "+exSess) {
		t.Fatalf("%s", out.String())
	}
	a, out, _ = testApp(nil)
	if code := runCLI(t, a, "extract", "--to", "/example/out", exSess, "--socket", d.sock); code != 0 || strings.Contains(out.String(), "claude --resume") {
		t.Fatalf("--to: %d %s", code, out.String())
	}
	for session, name := range map[string]string{"exists": "target_exists", "live": "held_by_live_process"} {
		a, _, errw := testApp(nil)
		if code := runCLI(t, a, "extract", session, "--json", "--socket", d.sock); code != 8 {
			t.Fatalf("%s: exit %d", name, code)
		}
		var e struct {
			Error string `json:"error"`
			Exit  int    `json:"exit"`
		}
		if err := json.Unmarshal(errw.Bytes(), &e); err != nil || e.Error != name || e.Exit != 8 {
			t.Fatalf("%s: stderr %q", name, errw.String())
		}
	}
}

func TestDoctorExit9OnAMismatchAndZeroWhenClean(t *testing.T) {
	d := newOpsDaemon(t, true, settle.DoctorResult{FilesChecked: 3})
	a, out, _ := testApp(nil)
	if code := runCLI(t, a, "doctor", "--socket", d.sock); code != 0 || !strings.Contains(out.String(), "3 file(s)") {
		t.Fatalf("%d %s", code, out.String())
	}
	d = newOpsDaemon(t, true, settle.DoctorResult{
		FilesChecked: 3, MismatchFiles: 1,
		Mismatches: []settle.DoctorMismatch{{Kind: settle.MismatchCopy, File: 1, Path: "/example/x.jsonl", Detail: "the first 9 bytes differ", Repaired: true}},
	})
	a, out, _ = testApp(nil)
	if code := runCLI(t, a, "doctor", "--socket", d.sock); code != 9 || !strings.Contains(out.String(), "copy_differs_from_original") {
		t.Fatalf("%d %s", code, out.String())
	}
}

func TestRemovalSubcommandsOfflineExit3(t *testing.T) {
	sock := filepath.Join(shortDir(t), "none.sock")
	for _, args := range [][]string{{"held"}, {"where", exSess}, {"remove", "--dry-run"}, {"extract", exSess}, {"doctor"}} {
		a, _, errw := testApp(nil)
		if code := a.run(append(args, "--socket", sock)); code != 3 || !strings.HasPrefix(errw.String(), "error: daemon_offline") {
			t.Errorf("%v: exit %d, stderr %q", args, code, errw.String())
		}
	}
}

func TestDoctorPrintsTheCycleStateWhenItTookOverARollingCycle(t *testing.T) {
	d := newOpsDaemon(t, true, settle.DoctorAnswer{
		DoctorResult: settle.DoctorResult{FilesChecked: 40},
		TookOver:     &settle.DoctorCycleState{StartedNS: 0, Phase: "files", Processed: 12, TotalFiles: 40, Findings: 1},
	})
	a, out, _ := testApp(nil)
	if code := runCLI(t, a, "doctor", "--socket", d.sock); code != 0 || !strings.Contains(out.String(), "rolling cycle was mid-way") || !strings.Contains(out.String(), "12 of 40") {
		t.Fatalf("%d %s", code, out.String())
	}
}

func TestSweepForceStartsAndWaitsUntilTheDaemonReportsCompletion(t *testing.T) {
	sock := filepath.Join(shortDir(t), "s.sock")
	s := server.New(obs.New(nil), nil, func() any { return map[string]any{} })
	var reqs []serve.SweepRequest
	polls := 0
	s.Register("sweep", server.KindQuery, func(_ context.Context, b []byte) (any, *wire.Error) {
		var req serve.SweepRequest
		_ = json.Unmarshal(b, &req)
		reqs = append(reqs, req)
		if req.Force {
			return serve.SweepAnswer{Started: true, Running: true}, nil
		}
		polls++
		return serve.SweepAnswer{Running: polls < 3, Completed: map[bool]int64{true: 1}[polls >= 3]}, nil
	})
	if err := s.Listen(sock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	a, out, _ := testApp(nil)
	a.pollEvery = time.Millisecond
	if code := runCLI(t, a, "sweep", "--force", "--wait", "--socket", sock); code != 0 || !strings.Contains(out.String(), "finished") {
		t.Fatalf("%d %s", code, out.String())
	}
	if len(reqs) != 4 || !reqs[0].Force || reqs[1].Force {
		t.Fatalf("requests %+v", reqs)
	}
	a, out, _ = testApp(nil)
	if code := runCLI(t, a, "sweep", "--force", "--socket", sock); code != 0 || !strings.Contains(out.String(), "started") {
		t.Fatalf("%d %s", code, out.String())
	}
	a, _, errw := testApp(nil)
	if code := runCLI(t, a, "sweep", "--socket", sock); code != 2 || !strings.Contains(errw.String(), "--force") {
		t.Fatalf("without --force: %d %q", code, errw.String())
	}
}
