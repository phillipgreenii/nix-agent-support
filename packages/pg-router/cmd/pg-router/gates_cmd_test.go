package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/core"
)

// The gate CLI is a socket client of the running core's Gate Registry (bead
// pg2-h63eu). Every test below drives the real command bodies against a REAL
// core (startCore), asserting on what the CORE holds.

func activeGateTypes(svc *core.Service) []string {
	var out []string
	for _, g := range svc.ActiveGates() {
		out = append(out, g.Type)
	}
	return out
}

func TestPauseCore_SetsSystemPauseRecordingTheOperator(t *testing.T) {
	svc := startCore(t, shortDir(t))
	var stdout, stderr strings.Builder
	if code := pauseCore(&stdout, &stderr, svc.Ref(), "operator", "maintenance window"); code != exitOK {
		t.Fatalf("exit = %d; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "paused (SYSTEM_PAUSE since ") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	gates := svc.ActiveGates()
	if len(gates) != 1 || gates[0].Type != "SYSTEM_PAUSE" || gates[0].Owner != "operator" || gates[0].Description != "maintenance window" {
		t.Fatalf("gates = %+v, want SYSTEM_PAUSE owned by operator with the description", gates)
	}

	stdout.Reset()
	if code := pauseCore(&stdout, &stderr, svc.Ref(), "operator", ""); code != exitOK {
		t.Fatalf("re-pause exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "pause re-asserted") {
		t.Fatalf("a re-pause is another set (last writer wins) and is reported as such; stdout = %q", stdout.String())
	}
}

func TestResumeCore_ClearsSystemPauseOnlyUnlessAll(t *testing.T) {
	svc := startCore(t, shortDir(t))
	var stdout, stderr strings.Builder
	for _, g := range []string{"SYSTEM_PAUSE", "LOW_DISK_USAGE"} {
		if code := gateSetCore(&stdout, &stderr, svc.Ref(), g, "", "tester", 0); code != exitOK {
			t.Fatalf("set %s exit = %d; stderr=%s", g, code, stderr.String())
		}
	}
	stdout.Reset()
	if code := resumeCore(&stdout, &stderr, svc.Ref(), false, "operator"); code != exitOK {
		t.Fatalf("resume exit = %d; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "resumed (cleared SYSTEM_PAUSE)") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if got := activeGateTypes(svc); len(got) != 1 || got[0] != "LOW_DISK_USAGE" {
		t.Fatalf("after a bare resume active = %v: another system's gate must survive", got)
	}

	stdout.Reset()
	if code := resumeCore(&stdout, &stderr, svc.Ref(), false, "operator"); code != exitOK {
		t.Fatalf("idempotent resume exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "already resumed (SYSTEM_PAUSE was not set)") {
		t.Fatalf("stdout = %q", stdout.String())
	}

	stdout.Reset()
	if code := resumeCore(&stdout, &stderr, svc.Ref(), true, "operator"); code != exitOK {
		t.Fatalf("resume --all exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "cleared LOW_DISK_USAGE") || len(activeGateTypes(svc)) != 0 {
		t.Fatalf("resume --all stdout = %q active = %v", stdout.String(), activeGateTypes(svc))
	}
	stdout.Reset()
	_ = resumeCore(&stdout, &stderr, svc.Ref(), true, "operator")
	if !strings.Contains(stdout.String(), "already resumed (no gate was set)") {
		t.Fatalf("resume --all with nothing set: stdout = %q", stdout.String())
	}
}

func TestGateSetCore_DescriptionOwnerTTLAndRenewal(t *testing.T) {
	svc := startCore(t, shortDir(t))
	var stdout, stderr strings.Builder
	if code := gateSetCore(&stdout, &stderr, svc.Ref(), "LOW_DISK_USAGE", "3GiB free", "disk-watchdog", 6*time.Minute); code != exitOK {
		t.Fatalf("exit = %d; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "gate LOW_DISK_USAGE set") || !strings.Contains(stdout.String(), "lease until") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	g := svc.ActiveGates()[0]
	if g.Description != "3GiB free" || g.Owner != "disk-watchdog" || g.ExpiresAt.IsZero() {
		t.Fatalf("gate = %+v", g)
	}
	ttl := g.ExpiresAt.Sub(g.SetAt)
	if ttl != 6*time.Minute {
		t.Fatalf("lease = %v, want 6m", ttl)
	}

	stdout.Reset()
	if code := gateSetCore(&stdout, &stderr, svc.Ref(), "LOW_DISK_USAGE", "", "", 0); code != exitOK {
		t.Fatalf("renew exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "renewed") {
		t.Fatalf("stdout = %q, want a renewal", stdout.String())
	}
	g = svc.ActiveGates()[0]
	if g.Description != "" || !g.ExpiresAt.IsZero() {
		t.Fatalf("last writer wins: the overwrite carried no description or lease; gate = %+v", g)
	}
}

func TestGateClearCore_OneAllAndIdempotent(t *testing.T) {
	svc := startCore(t, shortDir(t))
	var stdout, stderr strings.Builder
	for _, ty := range []string{"A", "B", "C"} {
		_ = gateSetCore(&stdout, &stderr, svc.Ref(), ty, "", "x", 0)
	}
	stdout.Reset()
	if code := gateClearCore(&stdout, &stderr, svc.Ref(), "B", false, "anyone"); code != exitOK {
		t.Fatalf("exit = %d; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "gate cleared (B)") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	stdout.Reset()
	_ = gateClearCore(&stdout, &stderr, svc.Ref(), "B", false, "anyone")
	if !strings.Contains(stdout.String(), "gate B was not set") {
		t.Fatalf("clearing an unset gate is a no-op success; stdout = %q", stdout.String())
	}
	stdout.Reset()
	_ = gateClearCore(&stdout, &stderr, svc.Ref(), "", true, "anyone")
	if !strings.Contains(stdout.String(), "gate cleared (A, C)") || len(activeGateTypes(svc)) != 0 {
		t.Fatalf("clear --all: stdout = %q active = %v", stdout.String(), activeGateTypes(svc))
	}
}

func TestGateListCore_TextAndJSON(t *testing.T) {
	svc := startCore(t, shortDir(t))
	var stdout, stderr strings.Builder
	if code := gateListCore(&stdout, &stderr, svc.Ref(), false); code != exitOK || !strings.Contains(stdout.String(), "(none)") {
		t.Fatalf("empty list: exit=%d stdout=%q", code, stdout.String())
	}
	stdout.Reset()
	if code := gateListCore(&stdout, &stderr, svc.Ref(), true); code != exitOK || strings.TrimSpace(stdout.String()) != "[]" {
		t.Fatalf("empty --json list: exit=%d stdout=%q, want []", code, stdout.String())
	}

	_ = gateSetCore(&stdout, &stderr, svc.Ref(), "LOW_DISK_USAGE", "3GiB free", "disk-watchdog", time.Minute)
	_ = gateSetCore(&stdout, &stderr, svc.Ref(), "SYSTEM_PAUSE", "", "operator", 0)
	stdout.Reset()
	if code := gateListCore(&stdout, &stderr, svc.Ref(), false); code != exitOK {
		t.Fatalf("exit = %d; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"LOW_DISK_USAGE:", "owner=disk-watchdog", "ttlRemaining=", `description="3GiB free"`, "SYSTEM_PAUSE:"} {
		if !strings.Contains(out, want) {
			t.Errorf("gate list missing %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, "LOW_DISK_USAGE") > strings.Index(out, "SYSTEM_PAUSE") {
		t.Errorf("gates must be sorted by TYPE:\n%s", out)
	}
	stdout.Reset()
	_ = gateListCore(&stdout, &stderr, svc.Ref(), true)
	var gates []statusGate
	if err := json.Unmarshal([]byte(stdout.String()), &gates); err != nil || len(gates) != 2 || gates[0].Type != "LOW_DISK_USAGE" {
		t.Fatalf("--json list = %q (%v), want 2 gates sorted by TYPE", stdout.String(), err)
	}
}

// Gates live in the running core's log, so every gate command is a socket
// client: with no core running it fails with the "no running core" diagnostic
// and never starts one (ADR 0036).
func TestGateCommands_NoRunningCoreIsExit1(t *testing.T) {
	gone := core.Ref{Socket: shortDir(t) + "/gone.sock"}
	for name, run := range map[string]func(out, errw *strings.Builder) int{
		"pause":  func(o, e *strings.Builder) int { return pauseCore(o, e, gone, "op", "") },
		"resume": func(o, e *strings.Builder) int { return resumeCore(o, e, gone, false, "op") },
		"set":    func(o, e *strings.Builder) int { return gateSetCore(o, e, gone, "X", "", "", 0) },
		"clear":  func(o, e *strings.Builder) int { return gateClearCore(o, e, gone, "X", false, "") },
		"list":   func(o, e *strings.Builder) int { return gateListCore(o, e, gone, false) },
	} {
		var stdout, stderr strings.Builder
		if code := run(&stdout, &stderr); code != conformance.ExitError {
			t.Errorf("%s: exit = %d, want 1", name, code)
		}
		if !strings.Contains(stderr.String(), "no running core") {
			t.Errorf("%s: stderr = %q, want the no-running-core diagnostic", name, stderr.String())
		}
	}
}

func TestRunGate_UsageErrors(t *testing.T) {
	for name, args := range map[string][]string{
		"no subcommand":         {},
		"unknown subcommand":    {"frobnicate"},
		"set without TYPE":      {"set"},
		"set two TYPEs":         {"set", "A", "B"},
		"set lower-case TYPE":   {"set", "low_disk"},
		"set negative ttl":      {"set", "A", "--ttl", "-5m"},
		"set bad ttl":           {"set", "A", "--ttl", "soon"},
		"clear without target":  {"clear"},
		"clear TYPE and --all":  {"clear", "A", "--all"},
		"clear lower-case TYPE": {"clear", "a"},
		"list stray argument":   {"list", "extra"},
	} {
		if code := runGate(args); code != conformance.ExitUsage {
			t.Errorf("%s: exit = %d, want %d (usage)", name, code, conformance.ExitUsage)
		}
	}
}

// pause/resume no longer take a gate name: the old positional form is a usage
// error pointing at `gate set|clear`, never silently reinterpreted.
func TestRunPauseResume_PositionalGateNameIsUsageError(t *testing.T) {
	if code := runPause([]string{"operator-paused"}); code != conformance.ExitUsage {
		t.Errorf("pause <gate> exit = %d, want usage", code)
	}
	if code := runResume([]string{"cicd-down"}); code != conformance.ExitUsage {
		t.Errorf("resume <gate> exit = %d, want usage", code)
	}
}

func TestShortTime(t *testing.T) {
	if got := shortTime(""); got != "-" {
		t.Errorf("shortTime(\"\") = %q", got)
	}
	if got := shortTime("not-a-time"); got != "not-a-time" {
		t.Errorf("shortTime(garbage) = %q, want the raw text", got)
	}
	if got := shortTime("2026-10-01T12:34:56Z"); len(got) != len("15:04:05") {
		t.Errorf("shortTime(valid) = %q, want HH:MM:SS", got)
	}
}
