package core

import (
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/schemas"
)

// This file is the core's Gate Registry API (bead pg2-h63eu): the socket verbs
// and Service methods external systems — and the operator's `pg-router pause` /
// `resume` — use to set and clear gates. The gate mechanism itself (TYPE-keyed
// records in the event log, the projection, the per-participant blocking
// semantics) lives in internal/eventqueue/gate.go; this layer only authenticates
// nothing new (the socket token already did), validates the wire message and
// calls the queue.
//
// A gate is what PREVENTS pg-router FROM ROUTING: one active gate per TYPE
// (ALL CAPS, arbitrary), last writer wins, ANY caller may clear any gate, an
// optional description, an optional owner (debug only) and an optional TTL
// lease that the owner keeps alive by setting the gate again.

// SubcommandGateSet / SubcommandGateClear are the INTF-CLI socket verbs the
// Gate Registry adds. SubcommandPause / SubcommandResume (INV-LIFE-2) survive as
// sugar for the SYSTEM_PAUSE gate.
const (
	SubcommandGateSet   = "gate-set"
	SubcommandGateClear = "gate-clear"
	SubcommandPause     = "pause"
	SubcommandResume    = "resume"
)

// The message types backing the gate verbs (schemas/, checked via package
// conformance — INV-INTF-2).
const (
	GateSetRequestSchema    = "cli.gate-set"
	GateSetReplySchema      = "cli.gate-set-reply"
	GateClearRequestSchema  = "cli.gate-clear"
	GateClearReplySchema    = "cli.gate-clear-reply"
	PauseRequestSchema      = "cli.pause"
	PauseReplySchema        = "cli.pause-reply"
	ResumeRequestSchema     = "cli.resume"
	ResumeReplySchema       = "cli.resume-reply"
	pauseDefaultDescription = "paused by the operator"
)

// GateSystemPause is the gate TYPE pause sets and resume clears (formerly the
// file-backed operator gate; see MIGRATION.md).
const GateSystemPause = eventqueue.GateSystemPause

// SetGate sets (or renews, or overwrites) the gate of req.Type through the
// event queue's Gate Registry. A TTL lease is optional; a renewal is just
// another SetGate.
func (s *Service) SetGate(req eventqueue.GateRequest) (eventqueue.Gate, error) {
	return s.q.SetGate(req)
}

// ClearGate clears the active gate of the given TYPE on behalf of by (debug
// only); it reports whether there was one to clear.
func (s *Service) ClearGate(gateType, by string) (bool, error) {
	_, cleared, err := s.q.ClearGate(gateType, by)
	return cleared, err
}

// ActiveGates returns the gates in force right now, sorted by TYPE.
func (s *Service) ActiveGates() []eventqueue.Gate { return s.q.ActiveGates() }

// AdmitPull is the admission check for a listener's PULL request: it returns a
// *eventqueue.GateBlockedError naming the gate when an active gate blocks the
// registered participant id (rejected pulls are counted), nil otherwise. The
// participant's non-blocking TYPEs are the ones it declared at registration
// (Registration.NonBlockingGates); an id that is not registered blocks on every
// TYPE.
func (s *Service) AdmitPull(participantID string) error {
	var exempt map[string]bool
	if reg, ok := s.reg.Get(participantID); ok {
		exempt = eventqueue.ExemptSet(reg.NonBlockingGates)
	}
	return s.q.CheckPull(participantID, exempt)
}

// gateWriteReply renders a gate-set/pause reply body.
func gateSetReply(g eventqueue.Gate, renewal bool) map[string]any {
	reply := map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"set":           true,
		"setAt":         g.SetAt.UTC().Format(time.RFC3339Nano),
		"renewal":       renewal,
	}
	if !g.ExpiresAt.IsZero() {
		reply["expiresAt"] = g.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return reply
}

// decodeVerb reads and schema-checks one gate verb request into dst.
func decodeVerb(stdin io.Reader, stdout io.Writer, verb, schema string, dst any) bool {
	data, err := io.ReadAll(stdin)
	if err != nil {
		writeBody(stdout, errorReply(verb+": read request: "+err.Error()))
		return false
	}
	if err := conformance.CheckBytes(schema, data); err != nil {
		writeBody(stdout, errorReply(verb+": "+err.Error()))
		return false
	}
	if err := json.Unmarshal(data, dst); err != nil {
		// Unreachable once CheckBytes has passed — see handleStatus's identical note.
		writeBody(stdout, errorReply(verb+": malformed request: "+err.Error()))
		return false
	}
	return true
}

// writeJSONReply marshals reply to stdout.
func writeJSONReply(stdout io.Writer, verb string, reply map[string]any) int {
	body, err := json.Marshal(reply)
	if err != nil { // unreachable: reply holds only JSON-safe scalars
		writeBody(stdout, errorReply(verb+": marshal reply: "+err.Error()))
		return conformance.ExitError
	}
	writeBody(stdout, body)
	return conformance.ExitOK
}

// doSetGate is gate-set's and pause's shared write: it reports whether the
// call replaced an already-active gate (the renewal flag) alongside the gate as
// recorded.
func (s *Service) doSetGate(req eventqueue.GateRequest) (eventqueue.Gate, bool, error) {
	_, had := s.q.Gate(req.Type)
	g, err := s.q.SetGate(req)
	return g, had, err
}

// handleGateSet runs the `gate-set` socket verb.
func (s *Service) handleGateSet(stdin io.Reader, stdout io.Writer) int {
	var req struct {
		Type        string `json:"type"`
		Description string `json:"description"`
		Owner       string `json:"owner"`
		TTLMs       int64  `json:"ttlMs"`
	}
	if !decodeVerb(stdin, stdout, "gate-set", GateSetRequestSchema, &req) {
		return conformance.ExitError
	}
	g, renewal, err := s.doSetGate(eventqueue.GateRequest{
		Type: req.Type, Description: req.Description, Owner: req.Owner,
		TTL: time.Duration(req.TTLMs) * time.Millisecond,
	})
	if err != nil {
		writeBody(stdout, errorReply("gate-set: "+err.Error()))
		return conformance.ExitError
	}
	reply := gateSetReply(g, renewal)
	reply["type"] = g.Type
	return writeJSONReply(stdout, "gate-set", reply)
}

// handleGateClear runs the `gate-clear` socket verb: ANY caller may clear any
// gate, and clearing a TYPE with no active gate is a no-op success.
func (s *Service) handleGateClear(stdin io.Reader, stdout io.Writer) int {
	var req struct {
		Type string `json:"type"`
		All  bool   `json:"all"`
		By   string `json:"by"`
	}
	if !decodeVerb(stdin, stdout, "gate-clear", GateClearRequestSchema, &req) {
		return conformance.ExitError
	}
	if req.All == (req.Type != "") {
		writeBody(stdout, errorReply("gate-clear: exactly one of \"type\" or \"all\" is required"))
		return conformance.ExitError
	}
	cleared, err := s.clearGates(req.Type, req.All, req.By)
	if err != nil {
		writeBody(stdout, errorReply("gate-clear: "+err.Error()))
		return conformance.ExitError
	}
	return writeJSONReply(stdout, "gate-clear", map[string]any{"schemaVersion": schemas.SchemaVersion, "cleared": cleared})
}

// clearGates clears one TYPE, or every active gate when all is set, returning
// the TYPEs actually cleared (never nil, so the reply array is always present).
func (s *Service) clearGates(gateType string, all bool, by string) ([]string, error) {
	cleared := []string{}
	types := []string{gateType}
	if all {
		types = types[:0]
		for _, g := range s.q.ActiveGates() {
			types = append(types, g.Type)
		}
	}
	var errs []error
	for _, t := range types {
		ok, err := s.ClearGate(t, by)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if ok {
			cleared = append(cleared, t)
		}
	}
	return cleared, errors.Join(errs...)
}

// handlePause runs the `pause` socket verb (INV-LIFE-2): sugar for gate-set of
// SYSTEM_PAUSE with the caller recorded as owner. A re-pause is another
// gate-set (last writer wins), exactly like any other gate.
func (s *Service) handlePause(stdin io.Reader, stdout io.Writer) int {
	var req struct {
		Owner       string `json:"owner"`
		Description string `json:"description"`
	}
	if !decodeVerb(stdin, stdout, "pause", PauseRequestSchema, &req) {
		return conformance.ExitError
	}
	if req.Description == "" {
		req.Description = pauseDefaultDescription
	}
	g, renewal, err := s.doSetGate(eventqueue.GateRequest{Type: GateSystemPause, Description: req.Description, Owner: req.Owner})
	if err != nil {
		writeBody(stdout, errorReply("pause: "+err.Error()))
		return conformance.ExitError
	}
	reply := gateSetReply(g, renewal)
	reply["gate"] = g.Type
	return writeJSONReply(stdout, "pause", reply)
}

// handleResume runs the `resume` socket verb: sugar for gate-clear of
// SYSTEM_PAUSE — or, with all, of every active gate. A bare resume clears ONLY
// SYSTEM_PAUSE, so a gate another system owns is never cleared by accident.
func (s *Service) handleResume(stdin io.Reader, stdout io.Writer) int {
	var req struct {
		All bool   `json:"all"`
		By  string `json:"by"`
	}
	if !decodeVerb(stdin, stdout, "resume", ResumeRequestSchema, &req) {
		return conformance.ExitError
	}
	cleared, err := s.clearGates(GateSystemPause, req.All, req.By)
	if err != nil {
		writeBody(stdout, errorReply("resume: "+err.Error()))
		return conformance.ExitError
	}
	return writeJSONReply(stdout, "resume", map[string]any{"schemaVersion": schemas.SchemaVersion, "cleared": cleared})
}
