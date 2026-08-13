package core

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/phillipgreenii/pr-pool/conformance"
)

// End to end over the REAL socket: a caller with the core's Ref delivers events
// and they land in the durable queue.
func TestSocketRoundTrip_IngestEvent(t *testing.T) {
	dir := shortDir(t)
	svc, ref := startService(t, dir)

	client, err := Dial(ref)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	reply, code, err := client.Call(SubcommandIngestEvent, []byte(oneEventRequest))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want 0; reply=%s", code, reply)
	}
	var decoded map[string]any
	if err := json.Unmarshal(reply, &decoded); err != nil {
		t.Fatalf("reply %s is not JSON: %v", reply, err)
	}
	if err := conformance.Check(IngestReplySchema, decoded); err != nil {
		t.Fatalf("socket reply failed the ingest-event reply schema: %v", err)
	}
	if decoded["accepted"] != float64(1) {
		t.Fatalf("accepted = %v, want 1", decoded["accepted"])
	}
	if depth := svc.Queue().DepthByType()["review-requested"]; depth != 1 {
		t.Fatalf("queue depth = %d, want 1 after a socket ingest", depth)
	}
}

// The SOCKET transport and the IN-PROCESS participant boundary must produce
// byte-identical replies and the same exit code: the socket is a carrier for
// conformance.Participant, not a second implementation with its own semantics.
func TestSocketAndInProcessTransportsAgree(t *testing.T) {
	requests := []string{
		oneEventRequest,
		`{"schemaVersion":"1","id":"trk-2","events":[{"id":"bad","type":"t"}]}`,
		`{"schemaVersion":"9","id":"trk-3","events":[` + oneEvent + `]}`,
	}
	for _, req := range requests {
		t.Run(req, func(t *testing.T) {
			// In-process, through the participant boundary the conformance suite uses.
			direct := startedService(t)
			wantReply, wantCode, err := conformance.RoundTrip(direct, SubcommandIngestEvent, json.RawMessage(req))
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}

			// Over the socket, against a fresh core with the same empty queue.
			_, ref := startService(t, shortDir(t))
			client, err := Dial(ref)
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			defer func() { _ = client.Close() }()
			gotReply, gotCode, err := client.Call(SubcommandIngestEvent, []byte(req))
			if err != nil {
				t.Fatalf("Call: %v", err)
			}
			if gotCode != wantCode {
				t.Fatalf("exit over socket = %d, in process = %d", gotCode, wantCode)
			}
			if string(gotReply) != string(wantReply) {
				t.Fatalf("reply over socket = %s, in process = %s", gotReply, wantReply)
			}
		})
	}
}

// Service must satisfy the EXISTING conformance.Participant transport so the
// declared suite can drive it directly (INV-INTF-2) — no bespoke harness.
func TestService_IsAConformanceParticipant(t *testing.T) {
	var p conformance.Participant = startedService(t)
	reply, code, err := conformance.RoundTrip(p, SubcommandIngestEvent, json.RawMessage(oneEventRequest))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want 0; reply=%s", code, reply)
	}
	var decoded map[string]any
	if err := json.Unmarshal(reply, &decoded); err != nil {
		t.Fatalf("reply is not JSON: %v", err)
	}
	if err := conformance.Check(IngestReplySchema, decoded); err != nil {
		t.Fatalf("reply failed its schema: %v", err)
	}
}

// Concurrent callers must be served without a race and without losing events.
func TestSocket_ConcurrentCallers(t *testing.T) {
	dir := shortDir(t)
	svc, ref := startService(t, dir)

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			client, err := Dial(ref)
			if err != nil {
				t.Errorf("Dial: %v", err)
				return
			}
			defer func() { _ = client.Close() }()
			req := `{"schemaVersion":"1","id":"trk-` + string(rune('a'+i)) + `","events":[{"id":"e` + string(rune('a'+i)) + `","type":"t"}]}`
			_, code, err := client.Call(SubcommandIngestEvent, []byte(req))
			if err != nil {
				t.Errorf("Call: %v", err)
				return
			}
			if code != conformance.ExitOK {
				t.Errorf("exit = %d, want 0", code)
			}
		}(i)
	}
	wg.Wait()
	if depth := svc.Queue().DepthByType()["t"]; depth != n {
		t.Fatalf("queue depth = %d, want %d (no event lost across concurrent callers)", depth, n)
	}
}

// A body-less reply — the legal busy shape (exit 2, no body) — must cross the
// transport as an explicit null, since an empty json.RawMessage is not valid JSON
// and would corrupt the frame.
func TestRespond_BodylessReplyIsNull(t *testing.T) {
	svc := startedService(t)
	var out strings.Builder
	svc.respond(&out, conformance.ExitBusy, nil)

	var resp wireResponse
	if err := json.Unmarshal([]byte(out.String()), &resp); err != nil {
		t.Fatalf("frame %q is not JSON: %v", out.String(), err)
	}
	if resp.ExitCode != conformance.ExitBusy {
		t.Fatalf("exit = %d, want %d", resp.ExitCode, conformance.ExitBusy)
	}
	if string(resp.Reply) != "null" {
		t.Fatalf("reply = %s, want null for a body-less response", resp.Reply)
	}
}
