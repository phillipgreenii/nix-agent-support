package main

import (
	"encoding/json"
	"net"
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

// TestStatus_RefusedBySaturatedReadSemaphore proves the CLI relays the
// core's own admission-control refusal (Task 3.10) as a human-readable
// stderr message AND preserves exit 9 -- never swallowed into a generic
// exit 1. Saturating the REAL semaphore is an internal/core-package-only
// concern with no seam exposed to this package, so this fakes the core's
// wire reply directly, mirroring internal/core's own
// TestCall_NullReplyIsNoBody.
func TestStatus_RefusedBySaturatedReadSemaphore(t *testing.T) {
	dir := shortDir(t)
	sock := dir + "/busy.sock"
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var req json.RawMessage
		_ = json.NewDecoder(conn).Decode(&req)
		_ = json.NewEncoder(conn).Encode(map[string]any{
			"exitCode": conformance.ExitBusy,
			"reply": map[string]any{
				"schemaVersion": "1",
				"error":         "too many concurrent status/mon.read calls in flight; retry",
			},
		})
	}()

	var stdout, stderr strings.Builder
	code := status(&stdout, &stderr, false, core.Ref{Socket: sock, Token: "t"})
	if code != conformance.ExitBusy {
		t.Fatalf("exit = %d, want %d", code, conformance.ExitBusy)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing rendered for a busy refusal", stdout.String())
	}
	if !strings.Contains(stderr.String(), "too many concurrent status/mon.read calls in flight; retry") {
		t.Fatalf("stderr = %q, want the human-readable refusal message, not a bare exit code", stderr.String())
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

// renderStatusText's ACTIVITY header carries a dropped-entries note iff the
// reply's activityDropped is true — mirroring the GATES staleSuffix pattern,
// tested directly against a hand-built statusReply the way TestGatesAreStale
// does (bead pg2-vtuou).
func TestRenderStatusText_ActivityDroppedNote(t *testing.T) {
	base := statusReply{
		Activity: []struct {
			Seq       uint64 `json:"seq"`
			StartedAt string `json:"startedAt"`
			Type      string `json:"type"`
			Outcome   string `json:"outcome"`
		}{{Seq: 4, Type: "review-requested", Outcome: "delivered"}},
	}
	cases := []struct {
		name    string
		dropped bool
		want    string
	}{
		{"not dropped", false, "ACTIVITY (last 10):\n"},
		{"dropped", true, "ACTIVITY (last 10) (dropped: entries evicted since your last read):\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := base
			st.ActivityDropped = tc.dropped
			var out strings.Builder
			renderStatusText(&out, "/s", st)
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("stdout = %q, want it to contain %q", out.String(), tc.want)
			}
		})
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

// TestStatusCmd_RendersWidenedListenersSources proves the plain-text
// LISTENERS/SOURCES sections render the Task 4.1 widened fields
// (role/binds/enabled/excluded/delivered/declined/backoff and
// name/type/enabled/excluded/mode/lastTick/failure) rather than the prior
// {id,kind,state,self}/{name,rejected} shapes — closing the "no test would
// catch this" risk the gap this packet found against the live repo (Task
// 4.1 Files: status_cmd.go's own decode/render was not in the original
// Files list; left unmodified, `pr-pool status`'s text output would
// silently render blank/wrong LISTENERS and SOURCES sections after this
// packet's wire-shape change ships).
func TestStatusCmd_RendersWidenedListenersSources(t *testing.T) {
	st := statusReply{
		Listeners: []listenerView{
			{Role: "review", Binds: []string{"review-requested"}, Enabled: true, Excluded: false, Delivered: 3, Declined: 1},
		},
		Sources: []sourceView{
			{Name: "feedback-ready", Type: "pull", Enabled: true, Excluded: false, Mode: "pull", LastTick: "2026-09-01T00:05:00Z"},
			{
				Name: "urgent-ready", Type: "pull", Enabled: true, Excluded: true, Mode: "pull",
				Failure: &failureView{Count: 2, NextEligible: "2026-09-01T00:06:00Z"},
			},
		},
	}
	var out strings.Builder
	renderStatusText(&out, "/tmp/core.sock", st)
	text := out.String()

	wantListenerRow := "  review: binds=[review-requested] enabled=true excluded=false delivered=3 declined=1 backoff=-\n"
	if !strings.Contains(text, wantListenerRow) {
		t.Fatalf("stdout = %q, want the widened LISTENERS row %q", text, wantListenerRow)
	}
	wantSourceRow1 := "  feedback-ready: type=pull enabled=true excluded=false mode=pull lastTick=2026-09-01T00:05:00Z failure=-\n"
	if !strings.Contains(text, wantSourceRow1) {
		t.Fatalf("stdout = %q, want the widened SOURCES row %q", text, wantSourceRow1)
	}
	wantSourceRow2 := "  urgent-ready: type=pull enabled=true excluded=true mode=pull lastTick=- failure=count=2 nextEligible=2026-09-01T00:06:00Z\n"
	if !strings.Contains(text, wantSourceRow2) {
		t.Fatalf("stdout = %q, want the excluded/failing SOURCES row %q", text, wantSourceRow2)
	}
	if strings.Contains(text, "rejected=") {
		t.Fatalf("stdout = %q, want the removed `rejected` field never rendered", text)
	}
}
