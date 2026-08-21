package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/schemas"
)

// serveSelfStatus runs the self-status subcommand IN PROCESS through the
// participant boundary (the same entry point the socket transport funnels
// into) and returns the decoded reply plus the exit code.
func serveSelfStatus(t *testing.T, svc *Service, request string) (map[string]any, int) {
	t.Helper()
	var out strings.Builder
	code := svc.Serve(SubcommandSelfStatus, strings.NewReader(request), &out)
	var reply map[string]any
	if err := json.Unmarshal([]byte(out.String()), &reply); err != nil {
		t.Fatalf("reply %q is not JSON: %v", out.String(), err)
	}
	return reply, code
}

// The happy path: a registered participant pushes a self-status and it lands on
// the registry entry, and the reply conforms to cli.self-status-reply.
func TestSelfStatus_AcceptsAndRecords(t *testing.T) {
	svc := startedService(t)
	if _, err := svc.Register("h1", KindHandler); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := svc.Registry().SetLifecycle("h1", conformance.Started); err != nil {
		t.Fatalf("SetLifecycle: %v", err)
	}

	reply, code := serveSelfStatus(t, svc, `{"schemaVersion":"1","id":"trk-1","participantId":"h1","self":"degraded"}`)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want 0; reply=%v", code, reply)
	}
	if err := conformance.Check(SelfStatusReplySchema, reply); err != nil {
		t.Fatalf("reply failed its own schema (INV-INTF-2): %v", err)
	}
	if reply["id"] != "trk-1" {
		t.Fatalf("id = %v, want the tracking id echoed back", reply["id"])
	}
	if reply["accepted"] != true {
		t.Fatalf("accepted = %v, want true", reply["accepted"])
	}
	got, ok := svc.Registry().Get("h1")
	if !ok {
		t.Fatal("participant vanished from the registry")
	}
	if got.Self != SelfDegraded {
		t.Fatalf("registry self = %s, want degraded", got.Self)
	}
	// `degraded` is still routable — only `unavailable` is a pre-accept decline.
	if !svc.Registry().Available("h1") {
		t.Fatal("Available = false for a degraded participant; degraded is still routable")
	}
}

// `unavailable` is the pre-accept decline INV-FAIL-1/INV-CONC-1 describe: once a
// participant pushes it over THIS subcommand, Available() must reflect it, which
// is the whole reason Registry.SetSelfStatus needed a real caller.
func TestSelfStatus_UnavailableDrivesPreAcceptDecline(t *testing.T) {
	svc := startedService(t)
	if _, err := svc.Register("src-1", KindSource); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := svc.Registry().SetLifecycle("src-1", conformance.Started); err != nil {
		t.Fatalf("SetLifecycle: %v", err)
	}
	if !svc.Registry().Available("src-1") {
		t.Fatal("expected available before any self-status push")
	}

	reply, code := serveSelfStatus(t, svc, `{"schemaVersion":"1","id":"trk-1","participantId":"src-1","self":"unavailable"}`)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want 0; reply=%v", code, reply)
	}
	if svc.Registry().Available("src-1") {
		t.Fatal("Available = true after an unavailable self-report; want a pre-accept decline")
	}
}

// A self-status push naming a participant id the registry does not hold (never
// registered, or already deregistered) is an error, not a silent no-op — the
// caller's identity claim did not resolve.
func TestSelfStatus_UnknownParticipantIsAnError(t *testing.T) {
	svc := startedService(t)
	reply, code := serveSelfStatus(t, svc, `{"schemaVersion":"1","id":"trk-1","participantId":"nope","self":"healthy"}`)
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want 1 for an unknown participant id", code)
	}
	msg, _ := reply["error"].(string)
	if !strings.Contains(msg, "unknown participant") {
		t.Fatalf("error = %q, want it to name the unknown participant", msg)
	}
}

// Every kind may push self-status — not just sources. This is the crux of
// pg2-zaghi: interfaces.md says "any participant", and callbackFor previously
// handed a callback to KindSource alone.
func TestSelfStatus_EveryKindMayPush(t *testing.T) {
	for _, kind := range []Kind{KindSource, KindHandler, KindMonitor, KindStorage} {
		t.Run(string(kind), func(t *testing.T) {
			svc := startedService(t)
			id := "p-" + string(kind)
			if _, err := svc.Register(id, kind); err != nil {
				t.Fatalf("Register: %v", err)
			}
			reply, code := serveSelfStatus(t, svc, `{"schemaVersion":"1","id":"trk-1","participantId":"`+id+`","self":"healthy"}`)
			if code != conformance.ExitOK {
				t.Fatalf("exit = %d, want 0 for kind %s; reply=%v", code, kind, reply)
			}
		})
	}
}

// Envelope faults (malformed JSON, a missing/invalid field, an out-of-enum
// self value, an additional property) produce the protocol error envelope and
// exit 1 — nothing about the registry changes.
func TestSelfStatus_EnvelopeFaults(t *testing.T) {
	cases := []struct {
		desc, req string
	}{
		{"malformed JSON", `{"schemaVersion":`},
		{"not an object", `["a"]`},
		{"missing schemaVersion", `{"id":"t","participantId":"p","self":"healthy"}`},
		{"unknown schemaVersion", `{"schemaVersion":"9","id":"t","participantId":"p","self":"healthy"}`},
		{"missing id", `{"schemaVersion":"1","participantId":"p","self":"healthy"}`},
		{"missing participantId", `{"schemaVersion":"1","id":"t","self":"healthy"}`},
		{"missing self", `{"schemaVersion":"1","id":"t","participantId":"p"}`},
		{"self out of enum", `{"schemaVersion":"1","id":"t","participantId":"p","self":"mostly-fine"}`},
		{"additional property", `{"schemaVersion":"1","id":"t","participantId":"p","self":"healthy","extra":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			svc := startedService(t)
			reply, code := serveSelfStatus(t, svc, tc.req)
			if code != conformance.ExitError {
				t.Fatalf("exit = %d, want 1", code)
			}
			if _, has := reply["error"]; !has {
				t.Fatalf("reply = %v, want an error field", reply)
			}
		})
	}
}

// The reply's schemaVersion is the one the core declares, not a literal.
func TestSelfStatus_ReplyCarriesTheCoreSchemaVersion(t *testing.T) {
	svc := startedService(t)
	if _, err := svc.Register("h1", KindHandler); err != nil {
		t.Fatalf("Register: %v", err)
	}
	reply, _ := serveSelfStatus(t, svc, `{"schemaVersion":"1","id":"trk-1","participantId":"h1","self":"healthy"}`)
	if reply["schemaVersion"] != schemas.SchemaVersion {
		t.Fatalf("schemaVersion = %v, want %q", reply["schemaVersion"], schemas.SchemaVersion)
	}
}
