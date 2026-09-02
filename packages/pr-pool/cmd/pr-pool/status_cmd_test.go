package main

import (
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/core"
)

// TestStatus_DeliversToTheRunningCore proves `status` against a REAL running
// core reaches it and reports its live queue depth — the assertion is on
// what the CORE holds, matching push-inject's own "against a real core"
// tests.
func TestStatus_DeliversToTheRunningCore(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir)

	code := callCore(new(strings.Builder), new(strings.Builder), svc.Ref(), core.SubcommandIngestEvent, []byte(testIngestRequest))
	if code != conformance.ExitOK {
		t.Fatalf("seed ingest exit = %d, want 0", code)
	}

	var stdout, stderr strings.Builder
	code = status(&stdout, &stderr, false, svc.Ref())
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "QUEUES:") || !strings.Contains(out, "t: depth=1") {
		t.Fatalf("stdout = %q, want the live queue depth", out)
	}
	if !strings.Contains(out, "(none)") {
		t.Fatalf("stdout = %q, want an explicit empty-section marker for at least one empty section", out)
	}
}

// --json emits the raw, schema-valid cli.status-reply body.
func TestStatus_JSONOutput(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir)

	var stdout, stderr strings.Builder
	code := status(&stdout, &stderr, true, svc.Ref())
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	if err := conformance.CheckBytes(core.StatusReplySchema, []byte(strings.TrimSpace(stdout.String()))); err != nil {
		t.Fatalf("--json output failed its own schema: %v", err)
	}
}

// No running core: exit 1 with the "no running core" diagnostic and the
// remedy — status never starts one (ADR 0036), matching push-inject.
func TestStatus_NoRunningCoreIsExit1(t *testing.T) {
	var stdout, stderr strings.Builder
	code := status(&stdout, &stderr, false, core.Ref{Socket: shortDir(t) + "/gone.sock"})
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no running core") {
		t.Fatalf("stderr = %q, want a no-running-core diagnostic", stderr.String())
	}
	if !strings.Contains(stderr.String(), "start the core's socket service") {
		t.Fatalf("stderr = %q, want the remedy", stderr.String())
	}
}

// TestStatus_DiscriminatesErrorBeforeReplySchema proves the status client
// path (register row bead pg2-o9r6a; Task 3.8 Binding decisions, Step 7)
// recognizes a protocol-level refusal (bad token) as the error envelope
// BEFORE attempting to validate the raw reply against cli.status-reply.
func TestStatus_DiscriminatesErrorBeforeReplySchema(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir)
	badRef := core.Ref{Socket: svc.Ref().Socket, Token: "wrong-token"}

	var stdout, stderr strings.Builder
	code := status(&stdout, &stderr, false, badRef)
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want %d", code, conformance.ExitError)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing rendered for a discriminated refusal", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unauthorized") {
		t.Fatalf("stderr = %q, want the discriminated protocol refusal named", stderr.String())
	}
}

// A usage error exits 2, never BUSY (ADR 0042's Decision) — matching every
// other operator subcommand.
func TestRunStatus_UsageErrorExitsUsageNotBusy(t *testing.T) {
	cases := map[string][]string{
		"unknown flag":   {"--nope"},
		"extra argument": {"extra"},
	}
	for desc, args := range cases {
		t.Run(desc, func(t *testing.T) {
			if code := runStatus(args); code != conformance.ExitUsage {
				t.Fatalf("exit = %d, want %d/usage (never %d/busy)", code, conformance.ExitUsage, conformance.ExitBusy)
			}
		})
	}
}

// -h prints the help and exits 0 rather than attempting a status call.
func TestRunStatus_HelpExitsZero(t *testing.T) {
	if code := runStatus([]string{"-h"}); code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
}

// Routing: `status` is its own route, forwards its args, and is advertised
// in helpText/usageLine — the helpText-mentions test the operator-command-
// surface rule requires (pattern: push_inject_test.go).
func TestRoute_status(t *testing.T) {
	r := route([]string{"pr-pool", "status", "--json", "--socket", "/s"})
	if r.kind != routeStatus {
		t.Fatalf("kind = %v, want routeStatus", r.kind)
	}
	if strings.Join(r.rest, " ") != "--json --socket /s" {
		t.Fatalf("rest = %v, want the flags forwarded", r.rest)
	}
	if !strings.Contains(helpText, "status") {
		t.Fatal("helpText does not mention status")
	}
	if !strings.Contains(usageLine, "status") {
		t.Fatal("usageLine does not mention status")
	}
}

// gatesAreStale: the marker fires only when a tick interval is known AND
// gatesObservedAt genuinely predates lastTickAt by more than it.
func TestGatesAreStale(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 10, 0, 0, time.UTC)
	cases := []struct {
		name string
		st   statusReply
		want bool
	}{
		{"no tick interval", statusReply{GatesObservedAt: now.Format(time.RFC3339Nano), LastTickAt: now.Format(time.RFC3339Nano)}, false},
		{"fresh", statusReply{
			GatesObservedAt: now.Format(time.RFC3339Nano),
			LastTickAt:      now.Format(time.RFC3339Nano),
			TickIntervalMs:  1000,
		}, false},
		{"stale", statusReply{
			GatesObservedAt: now.Add(-5 * time.Second).Format(time.RFC3339Nano),
			LastTickAt:      now.Format(time.RFC3339Nano),
			TickIntervalMs:  1000,
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gatesAreStale(tc.st); got != tc.want {
				t.Fatalf("gatesAreStale = %v, want %v", got, tc.want)
			}
		})
	}
}
