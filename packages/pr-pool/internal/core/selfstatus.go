package core

import (
	"encoding/json"
	"io"
	"log/slog"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/schemas"
)

// SubcommandSelfStatus is the INTF-CLI callback target ANY registered
// participant — source, handler, monitor, or storage — uses to push its own
// status (healthy / degraded / unavailable), independent of any per-item
// outcome (interfaces.md "Self-status"). Unlike SubcommandIngestEvent, every
// participant kind is handed this callback (Service.Register / core.go), because
// self-status is part of the common manager contract, not one interface's own
// concern.
//
// This is bead pg2-zaghi's realization: before it landed, Registry.SetSelfStatus
// had zero production callers (only its own test suite called it) and no
// participant kind was handed a callback for it at all — recorded as a
// realization gap against INV-INTF-1 in docs/behavior/README.md.
const SubcommandSelfStatus = "self-status"

// The message types backing this subcommand (schemas/, checked via package
// conformance — INV-INTF-2).
const (
	SelfStatusRequestSchema = "cli.self-status"
	SelfStatusReplySchema   = "cli.self-status-reply"
)

// selfStatusRequest is the decoded cli.self-status envelope.
type selfStatusRequest struct {
	ID            string `json:"id"`
	ParticipantID string `json:"participantId"`
	Self          string `json:"self"`
}

// selfStatusReply is the cli.self-status-reply shape.
type selfStatusReply struct {
	SchemaVersion string `json:"schemaVersion"`
	ID            string `json:"id"`
	Accepted      bool   `json:"accepted"`
}

// handleSelfStatus runs the `self-status` callback: any registered participant
// pushes a report about ITSELF, and the core records it on the registry entry
// named by `participantId` — the same entry Available() and the pre-accept
// re-offer path (INV-FAIL-1 / INV-CONC-1) already read.
//
// Unlike `ingest-event`'s tracking id (a push source's own id, never required to
// be one the core issued), `participantId` here MUST name an id the core already
// holds a registration for — self-status describes an EXISTING participant, it
// does not mint one. A `participantId` the registry does not recognize (never
// registered, or already deregistered) is reported as an error rather than
// silently accepted: it is the caller's own identity claim failing to resolve,
// not a correlation to some earlier core-issued call, so INV-INTF-1's "unknown
// tracking id is acknowledged and ignored" rule does not apply here.
func (s *Service) handleSelfStatus(stdin io.Reader, stdout io.Writer) int {
	data, err := io.ReadAll(stdin)
	if err != nil {
		writeBody(stdout, errorReply("self-status: read request: "+err.Error()))
		return conformance.ExitError
	}
	if err := conformance.CheckBytes(SelfStatusRequestSchema, data); err != nil {
		writeBody(stdout, errorReply("self-status: "+err.Error()))
		return conformance.ExitError
	}
	var req selfStatusRequest
	if err := json.Unmarshal(data, &req); err != nil {
		// Unreachable once CheckBytes has passed: the schema already proved this
		// decodes as a well-typed object with these fields. Kept for the same
		// report-don't-guess discipline the rest of this package follows.
		writeBody(stdout, errorReply("self-status: malformed request: "+err.Error()))
		return conformance.ExitError
	}
	if err := s.reg.SetSelfStatus(req.ParticipantID, SelfStatus(req.Self)); err != nil {
		slog.Warn("core: self-status push rejected", "trackingId", req.ID, "participantId", req.ParticipantID, "err", err)
		writeBody(stdout, errorReply("self-status: "+err.Error()))
		return conformance.ExitError
	}
	body, err := json.Marshal(selfStatusReply{SchemaVersion: schemas.SchemaVersion, ID: req.ID, Accepted: true})
	if err != nil { // unreachable: the reply holds only strings and a bool
		writeBody(stdout, errorReply("self-status: marshal reply: "+err.Error()))
		return conformance.ExitError
	}
	writeBody(stdout, body)
	return conformance.ExitOK
}
