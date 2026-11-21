package conformance

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/phillipgreenii/pg-router/api/participant"
	"github.com/phillipgreenii/pg-router/schemas"
)

// RoundTrip runs one request through a Participant over the (in-memory)
// stdin/stdout transport, returning the reply bytes and exit code — the
// end-to-end boundary the conformance suite exercises live.
func RoundTrip(p participant.Participant, subcommand string, request any) (reply []byte, exitCode int, err error) {
	reqBytes, err := json.Marshal(request)
	if err != nil {
		return nil, participant.ExitError, err
	}
	var out bytes.Buffer
	code := p.Serve(subcommand, bytes.NewReader(reqBytes), &out)
	return out.Bytes(), code, nil
}

// ReferenceHandler is a reference INTF-HANDLER implementation used to prove the
// transport round-trip end to end. It accepts a dispatch only while Started
// (lifecycle boundary), declines with participant.ExitBusy when Busy, rejects a
// malformed/non-conforming dispatch with participant.ExitError, and otherwise replies with
// a sync outcome (or a deferred ack when Deferred). It tolerates a duplicate
// event id (idempotent, INV-EVT-2).
type ReferenceHandler struct {
	State    participant.Lifecycle
	Busy     bool
	Deferred bool
	seen     map[string]bool
}

// Serve implements Participant, dispatching on subcommand: `dispatch`
// (INTF-HANDLER's per-event message, the original behavior this method
// carried before it took a subcommand parameter at all) or `postStartup`/
// `preShutdown` (pg2-oju6w.15's once-per-process-lifetime lifecycle hooks,
// same transport, DEC-WIRE-1) or the optional `validate` config check. Earlier this method ignored subcommand
// entirely and always validated against "handler.dispatch" — a bug once a
// second subcommand existed to invoke it with, fixed here in the same change
// that introduces postStartup/preShutdown.
func (h *ReferenceHandler) Serve(subcommand string, stdin io.Reader, stdout io.Writer) int {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return participant.ExitError
	}
	// Lifecycle: messages accepted only when Started (INV-INTF-1).
	if h.State != participant.Started {
		writeReply(stdout, map[string]any{"schemaVersion": schemas.SchemaVersion, "error": "not accepting: participant is not started"})
		return participant.ExitError
	}
	if h.Busy {
		return participant.ExitBusy // busy: coarse exit code only, no body (INV-CONC-1)
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		writeReply(stdout, map[string]any{"schemaVersion": schemas.SchemaVersion, "error": "malformed JSON"})
		return participant.ExitError
	}
	switch subcommand {
	case "postStartup", "preShutdown":
		return h.serveLifecycleHook(subcommand, v, stdout)
	case "validate":
		return h.serveValidate(v, stdout)
	default:
		return h.serveDispatch(v, stdout)
	}
}

// serveDispatch is Serve's original `dispatch` handling, unchanged.
func (h *ReferenceHandler) serveDispatch(v any, stdout io.Writer) int {
	if err := Check("handler.dispatch", v); err != nil {
		writeReply(stdout, map[string]any{"schemaVersion": schemas.SchemaVersion, "error": err.Error()})
		return participant.ExitError
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
		return participant.ExitOK
	}
	// outcome is an opaque STRING (docket pg2-oju6w's Task 5.4, ADR 0065's
	// "Wire contract" section): the schema's own outcome property retypes
	// object -> string to match wireclient.Reply.Outcome's Go type, since
	// encoding/json cannot unmarshal an object into a string field.
	writeReply(stdout, map[string]any{"schemaVersion": schemas.SchemaVersion, "id": id, "outcome": "delivered"})
	return participant.ExitOK
}

// serveLifecycleHook is Serve's `postStartup`/`preShutdown` handling
// (pg2-oju6w.15): unlike dispatch, the reply carries no deferred branch —
// these are not queued dispatches, so ErrBusy/exit-9 does not apply once
// Serve is past the Busy check above.
func (h *ReferenceHandler) serveLifecycleHook(subcommand string, v any, stdout io.Writer) int {
	if err := Check("handler."+subcommand, v); err != nil {
		writeReply(stdout, map[string]any{"schemaVersion": schemas.SchemaVersion, "error": err.Error()})
		return participant.ExitError
	}
	obj := v.(map[string]any)
	id, _ := obj["id"].(string)
	writeReply(stdout, map[string]any{"schemaVersion": schemas.SchemaVersion, "id": id, "outcome": "ok"})
	return participant.ExitOK
}

// serveValidate is Serve's `validate` handling (docket pg2-12gr4.8): the optional
// config check. The reference handler has nothing to complain about, so it
// replies exit 0 with empty problems and warnings (valid).
func (h *ReferenceHandler) serveValidate(v any, stdout io.Writer) int {
	if err := Check("handler.validate", v); err != nil {
		writeReply(stdout, map[string]any{"schemaVersion": schemas.SchemaVersion, "error": err.Error()})
		return participant.ExitError
	}
	writeReply(stdout, map[string]any{"schemaVersion": schemas.SchemaVersion, "problems": []string{}, "warnings": []string{}})
	return participant.ExitOK
}

// Seen reports whether the handler has already received the given event id
// (used to assert idempotent duplicate absorption).
func (h *ReferenceHandler) Seen(eventID string) bool { return h.seen[eventID] }

func writeReply(w io.Writer, v any) {
	b, _ := json.Marshal(v)
	_, _ = w.Write(b)
}
