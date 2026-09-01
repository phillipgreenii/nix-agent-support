package core

import (
	"encoding/json"
	"io"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/schemas"
)

// SubcommandRegister is the INTF-CLI callback target ANY participant uses to
// join the registry (interfaces.md "Registry & lifecycle"): the participant
// names its own chosen id and kind, and MAY carry an initial self-status
// value. Unlike SubcommandSelfStatus (which describes an EXISTING
// registration), this is what MINTS one.
const SubcommandRegister = "register"

// The message types backing this subcommand (schemas/, checked via package
// conformance — INV-INTF-2). Task 0.7's authored register pair.
const (
	RegisterRequestSchema = "cli.register"
	RegisterReplySchema   = "cli.register-reply"
)

// registerRequest is the decoded cli.register envelope. Self is OPTIONAL
// (interfaces.md: registering "MAY carry an initial self-status value") —
// applied through the existing SetSelfStatus path once registration itself
// succeeds, rather than a field Register gives special meaning to inline.
type registerRequest struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Self string `json:"self"`
}

// registerReply is the cli.register-reply shape: registration mints TWO
// callbacks (Callback — empty for a kind with no event-delivery target,
// SelfStatusCallback — always minted, every kind gets one).
type registerReply struct {
	SchemaVersion      string `json:"schemaVersion"`
	Accepted           bool   `json:"accepted"`
	Callback           string `json:"callback"`
	SelfStatusCallback string `json:"selfStatusCallback"`
}

// handleRegister runs the `register` callback: any participant joins the
// registry and gets back the callback command(s) the core hands its kind
// (Service.Register — the socket-baked strings a REAL out-of-process
// participant dials back into, distinct from bootCore's in-process
// registration path, which never goes through this handler at all).
func (s *Service) handleRegister(stdin io.Reader, stdout io.Writer) int {
	data, err := io.ReadAll(stdin)
	if err != nil {
		writeBody(stdout, errorReply("register: read request: "+err.Error()))
		return conformance.ExitError
	}
	if err := conformance.CheckBytes(RegisterRequestSchema, data); err != nil {
		writeBody(stdout, errorReply("register: "+err.Error()))
		return conformance.ExitError
	}
	var req registerRequest
	if err := json.Unmarshal(data, &req); err != nil {
		// Unreachable once CheckBytes has passed — kept for the same
		// report-don't-guess discipline the rest of this package follows.
		writeBody(stdout, errorReply("register: malformed request: "+err.Error()))
		return conformance.ExitError
	}
	kind, err := ParseKind(req.Kind)
	if err != nil {
		writeBody(stdout, errorReply("register: "+err.Error()))
		return conformance.ExitError
	}
	reg, err := s.Register(req.ID, kind)
	if err != nil {
		writeBody(stdout, errorReply("register: "+err.Error()))
		return conformance.ExitError
	}
	if req.Self != "" {
		if err := s.reg.SetSelfStatus(req.ID, SelfStatus(req.Self)); err != nil {
			writeBody(stdout, errorReply("register: "+err.Error()))
			return conformance.ExitError
		}
	}
	body, err := json.Marshal(registerReply{
		SchemaVersion:      schemas.SchemaVersion,
		Accepted:           true,
		Callback:           reg.Callback,
		SelfStatusCallback: reg.SelfStatusCallback,
	})
	if err != nil { // unreachable: the reply holds only strings and a bool
		writeBody(stdout, errorReply("register: marshal reply: "+err.Error()))
		return conformance.ExitError
	}
	writeBody(stdout, body)
	return conformance.ExitOK
}
