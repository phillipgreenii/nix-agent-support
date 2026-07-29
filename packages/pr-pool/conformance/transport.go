package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/phillipgreenii/pr-pool/schemas"
)

// Lifecycle is a participant's lifecycle state (interfaces.md). Messages are
// accepted ONLY in Started — after started, before stopping (INV-INTF-1).
type Lifecycle int

const (
	Starting Lifecycle = iota
	Started
	Stopping
	Stopped
	Crashing
)

// String returns the lifecycle state's DOC-declared name (interfaces.md
// "Lifecycle": starting / started / stopping / stopped / crashing), so a
// diagnostic or a registry view reports the same names the behavior docs use
// rather than an opaque integer.
func (l Lifecycle) String() string {
	switch l {
	case Starting:
		return "starting"
	case Started:
		return "started"
	case Stopping:
		return "stopping"
	case Stopped:
		return "stopped"
	case Crashing:
		return "crashing"
	}
	return fmt.Sprintf("Lifecycle(%d)", int(l))
}

// Coarse exit codes for the CLI transport (interfaces.md common contract).
const (
	ExitOK    = 0 // ok; rich outcome in the JSON reply
	ExitError = 1 // unexpected / usage / malformed error
	ExitBusy  = 2 // at capacity — pre-accept busy decline (no body required)
)

// Participant speaks the default CLI transport: it reads a JSON request from
// stdin and writes a JSON reply to stdout, returning a coarse exit code. This
// is INV-INTF-1's default transport contract; an implementation MAY instead
// speak gRPC/in-code and still conform so long as the message SCHEMA holds.
type Participant interface {
	Serve(subcommand string, stdin io.Reader, stdout io.Writer) (exitCode int)
}

// RoundTrip runs one request through a Participant over the (in-memory)
// stdin/stdout transport, returning the reply bytes and exit code — the
// end-to-end boundary the conformance suite exercises live.
func RoundTrip(p Participant, subcommand string, request any) (reply []byte, exitCode int, err error) {
	reqBytes, err := json.Marshal(request)
	if err != nil {
		return nil, ExitError, err
	}
	var out bytes.Buffer
	code := p.Serve(subcommand, bytes.NewReader(reqBytes), &out)
	return out.Bytes(), code, nil
}

// ReferenceHandler is a reference INTF-HANDLER implementation used to prove the
// transport round-trip end to end. It accepts a dispatch only while Started
// (lifecycle boundary), declines with ExitBusy when Busy, rejects a
// malformed/non-conforming dispatch with ExitError, and otherwise replies with
// a sync outcome (or a deferred ack when Deferred). It tolerates a duplicate
// event id (idempotent, INV-EVT-2).
type ReferenceHandler struct {
	State    Lifecycle
	Busy     bool
	Deferred bool
	seen     map[string]bool
}

// Serve implements Participant for the `dispatch` subcommand.
func (h *ReferenceHandler) Serve(subcommand string, stdin io.Reader, stdout io.Writer) int {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return ExitError
	}
	// Lifecycle: messages accepted only when Started (INV-INTF-1).
	if h.State != Started {
		writeReply(stdout, map[string]any{"schemaVersion": schemas.SchemaVersion, "error": "not accepting: participant is not started"})
		return ExitError
	}
	if h.Busy {
		return ExitBusy // busy: coarse exit code only, no body (INV-CONC-1)
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		writeReply(stdout, map[string]any{"schemaVersion": schemas.SchemaVersion, "error": "malformed JSON"})
		return ExitError
	}
	if err := Check("handler.dispatch", v); err != nil {
		writeReply(stdout, map[string]any{"schemaVersion": schemas.SchemaVersion, "error": err.Error()})
		return ExitError
	}
	obj := v.(map[string]any)
	id, _ := obj["id"].(string)
	if ev, ok := obj["event"].(map[string]any); ok {
		if evid, ok := ev["id"].(string); ok {
			if h.seen == nil {
				h.seen = map[string]bool{}
			}
			h.seen[evid] = true // idempotency: duplicate absorbed
		}
	}
	if h.Deferred {
		writeReply(stdout, map[string]any{"schemaVersion": schemas.SchemaVersion, "id": id, "deferred": true})
		return ExitOK
	}
	writeReply(stdout, map[string]any{"schemaVersion": schemas.SchemaVersion, "id": id, "outcome": map[string]any{"ok": true}})
	return ExitOK
}

// Seen reports whether the handler has already received the given event id
// (used to assert idempotent duplicate absorption).
func (h *ReferenceHandler) Seen(eventID string) bool { return h.seen[eventID] }

func writeReply(w io.Writer, v any) {
	b, _ := json.Marshal(v)
	_, _ = w.Write(b)
}
