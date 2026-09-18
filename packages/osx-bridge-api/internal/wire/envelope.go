// Package wire implements osx-bridge-api's socket transport envelope: a
// small, versioned JSON request/response, one per connection, mirroring
// pg-connector's own "small, versioned JSON envelope, closed error
// taxonomy" wire-protocol convention (this repo's ADR 0062) — just carried
// over a local Unix domain socket instead of stdin/stdout, and carrying an
// extra Service field (design: "Architecture" → "Transport") so one
// daemon/one transport can host multiple OS integrations (calendar today,
// mail/contacts/reminders later) without a new transport per service.
//
// Protocol (one request, one response, one connection per call):
//
//	client -> daemon: {"protocolVersion": N, "service": "<name>", "op": "<name>", "args": {...}}
//	daemon -> client: {"protocolVersion": N, "schemaVersion": M, "result": ...}                       on success
//	daemon -> client: {"protocolVersion": N, "schemaVersion": M, "error": {"code": "...", "message": "..."}}  on failure
//
// ProtocolVersion in the request is optional (a caller built before this
// field existed sends none); when present it MUST equal wire.ProtocolVersion
// or the daemon reports version_mismatch. SchemaVersion in the response is
// the responding SERVICE's own schema version (osx-bridge-api's socketserver
// package fills it in from whichever registered service handled the
// request) — independent of ProtocolVersion, so a breaking change to one
// service's own result shape never forces every other service to bump in
// lockstep, exactly as ADR 0062 principle 3 states for pg-connector's own
// per-capability schemaVersion.
package wire

import "encoding/json"

// ProtocolVersion is this wire envelope's own version, covering the
// Request/Response/Error shapes in this file. It is independent of any
// service's own SchemaVersion.
const ProtocolVersion = 1

// Request is the shape read from the client's connection.
type Request struct {
	ProtocolVersion int             `json:"protocolVersion,omitempty"`
	Service         string          `json:"service"`
	Op              string          `json:"op"`
	Args            json.RawMessage `json:"args,omitempty"`
}

// ErrorBody is the wire shape of a failure response. Code MUST be one of
// the closed six-value taxonomy named by the Err* sentinels in errors.go —
// the same taxonomy ADR 0062 principle 3 defines for pg-connector's own
// stdio envelope (not_found, unauthenticated, unavailable, unknown_op,
// version_mismatch, invalid_argument), reused here unchanged rather than
// invented anew for the socket transport.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Response is the shape written back to the client. Exactly one of Result
// or Error is populated.
type Response struct {
	ProtocolVersion int             `json:"protocolVersion"`
	SchemaVersion   int             `json:"schemaVersion,omitempty"`
	Result          json.RawMessage `json:"result,omitempty"`
	Error           *ErrorBody      `json:"error,omitempty"`
}

// Decode decodes raw into v. A nil/empty/"null" raw is treated as a
// zero-value v (some ops take no arguments).
func Decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, v)
}
