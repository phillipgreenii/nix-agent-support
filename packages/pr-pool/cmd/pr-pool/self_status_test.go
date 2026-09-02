package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/core"
)

const testSelfStatusRequest = `{"schemaVersion":"1","id":"trk-1","participantId":"h1","self":"healthy"}`

// callCore relays self-status through the running core exactly as
// TestCallCore_RelaysReplyAndExitCode does for ingest-event, proving the
// production caller Registry.SetSelfStatus was missing end to end: register a
// participant, push its self-status over the wire, and see the registry
// reflect it.
func TestCallCore_SelfStatus_RelaysReplyAndUpdatesRegistry(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir)
	if _, err := svc.Register("h1", core.KindHandler); err != nil {
		t.Fatalf("Register: %v", err)
	}

	var stdout, stderr strings.Builder
	req := `{"schemaVersion":"1","id":"trk-1","participantId":"h1","self":"degraded"}`
	code := callCore(&stdout, &stderr, svc.Ref(), core.SubcommandSelfStatus, []byte(req))
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	var reply map[string]any
	if err := json.Unmarshal([]byte(stdout.String()), &reply); err != nil {
		t.Fatalf("stdout %q is not JSON: %v", stdout.String(), err)
	}
	if err := conformance.Check(core.SelfStatusReplySchema, reply); err != nil {
		t.Fatalf("relayed reply failed the reply schema: %v", err)
	}
	if reply["accepted"] != true {
		t.Fatalf("accepted = %v, want true", reply["accepted"])
	}
	got, ok := svc.Registry().Get("h1")
	if !ok {
		t.Fatal("participant vanished from the registry")
	}
	if got.Self != core.SelfDegraded {
		t.Fatalf("registry self = %s, want degraded", got.Self)
	}
}

// An unregistered participantId reaches the caller as exit 1 with the
// diagnostic body — the callback contract's coarse code plus the rich outcome
// in the JSON, matching ingest-event's rejection relay.
func TestCallCore_SelfStatus_RelaysUnknownParticipant(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir)

	var stdout, stderr strings.Builder
	code := callCore(&stdout, &stderr, svc.Ref(), core.SubcommandSelfStatus,
		[]byte(`{"schemaVersion":"1","id":"trk-1","participantId":"nope","self":"healthy"}`))
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want 1 for an unknown participant", code)
	}
	if !strings.Contains(stdout.String(), "unknown participant") {
		t.Fatalf("stdout = %q, want the unknown-participant diagnostic", stdout.String())
	}
}

// TestCallCore_SelfStatus_DiscriminatesErrorBeforeReplySchema proves the
// self-status client path (register row bead pg2-o9r6a; Task 3.8 Binding
// decisions, Step 7) recognizes a protocol-level refusal (bad token) as the
// error envelope BEFORE attempting to validate the raw reply against
// cli.self-status-reply — matching ingest-event's identical proof, since
// both share callCore's one discrimination call site.
func TestCallCore_SelfStatus_DiscriminatesErrorBeforeReplySchema(t *testing.T) {
	dir := shortDir(t)
	svc := startCore(t, dir)
	badRef := core.Ref{Socket: svc.Ref().Socket, Token: "wrong-token"}

	var stdout, stderr strings.Builder
	code := callCore(&stdout, &stderr, badRef, core.SubcommandSelfStatus, []byte(testSelfStatusRequest))
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want %d", code, conformance.ExitError)
	}
	if !strings.Contains(stdout.String(), `"error"`) {
		t.Fatalf("stdout = %q, want the relayed error envelope (the wire contract is unchanged)", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unauthorized") {
		t.Fatalf("stderr = %q, want the discriminated protocol refusal named", stderr.String())
	}
}

// A usage error on the callback subcommand exits 2, never BUSY (ADR 0042's
// Decision) — the same guarantee ingest-event carries.
func TestRunSelfStatus_UsageErrorExitsUsageNotBusy(t *testing.T) {
	if code := runSelfStatus([]string{"--nope"}); code != conformance.ExitUsage {
		t.Fatalf("unknown flag exit = %d, want %d/usage (never %d/busy)", code, conformance.ExitUsage, conformance.ExitBusy)
	}
	if code := runSelfStatus([]string{"extra-positional"}); code != conformance.ExitUsage {
		t.Fatalf("positional exit = %d, want %d/usage", code, conformance.ExitUsage)
	}
}

// With no core running, the CLI FAILS with a "no running core" diagnostic and
// never starts one (ADR 0036) — matching ingest-event's locate failure.
func TestCallCore_SelfStatus_NoRunningCoreIsAnError(t *testing.T) {
	var stdout, stderr strings.Builder
	ref := core.Ref{Socket: filepath.Join(shortDir(t), "gone.sock")}
	code := callCore(&stdout, &stderr, ref, core.SubcommandSelfStatus, []byte(testSelfStatusRequest))
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no running core") {
		t.Fatalf("stderr = %q, want a no-running-core diagnostic", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing on stdout for a locate failure", stdout.String())
	}
}

// Routing: `self-status` is its own route and forwards its args, and the help
// text advertises it.
func TestRoute_selfStatus(t *testing.T) {
	r := route([]string{"pr-pool", "self-status", "--socket", "/s", "--token", "t"})
	if r.kind != routeSelfStatus {
		t.Fatalf("kind = %v, want routeSelfStatus", r.kind)
	}
	if strings.Join(r.rest, " ") != "--socket /s --token t" {
		t.Fatalf("rest = %v, want the flags forwarded", r.rest)
	}
	if !strings.Contains(helpText, "self-status") {
		t.Fatal("helpText does not mention self-status")
	}
}
