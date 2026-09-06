// Package scriptout implements pg-connector's generic wire protocol: the
// stdin/stdout JSON envelope every Tier-2 backend binary speaks, and the
// caller-side helper the umbrella binary (cmd/pg-connector) uses to invoke
// one. It is capability-agnostic: it imports no per-capability provider
// package (no pr, issue, ci, or scm import), so a backend binary or the
// umbrella binary can import it without being forced to pull in a
// capability it doesn't need. A capability package (built by a sibling
// packet) supplies the op-dispatch table; this package only knows how to
// drive the request/response round trip against whatever table it's
// handed.
//
// Protocol (one request, one response, one process per call):
//
//	stdin:  {"op": "<name>", "args": {...}}
//	stdout: {"protocolVersion": N, "schemaVersion": M, "result": ...}                    on success (exit 0)
//	stdout: {"protocolVersion": N, "schemaVersion": M, "error": {"code": "...", "message": "..."}}  on failure (exit 1, or 2-7 -- see below)
//
// The capabilities op is the one exception to this shape: its response is
// the bespoke CapabilitiesResponse object, not the Result/Error envelope
// above — see CapabilitiesResponse's doc comment.
//
// This wire level's own exit code is 0 on success. On failure it is
// errors.go's ExitCodeForError(err): one of 2 (not_found), 3
// (unauthenticated), 4 (unavailable), 5 (unknown_op), 6 (version_mismatch),
// or 7 (invalid_argument) — the same classification the JSON error body's
// Code field carries — or a plain 1 for the rare wire-level failure that
// never reached a classifiable error at all (e.g. a failure to write the
// response itself). Per this workspace's code-file-standards exit-code
// convention, 1 stays the generic/catch-all code and MUST NOT be given a
// specific branchable meaning; every code that does carry one here is >=2
// (bead pg2-7vgn5). This numbering is a different, lower layer than
// pg-connector's own CLI exit codes (built by cmd/pg-connector's
// outcome-reporting helper) — the two ranges happen to overlap (2-7 here
// vs. 0-4 there) but classify entirely different things, and MUST NOT be
// confused with, or built from, one another.
//
// pkg/scriptout/schemas and pkg/scriptout/conformance (bead pg2-7vgn5) are
// this wire level's schemas/goldens/conformance suite: JSON Schema
// documents for the shapes on this page, golden fixtures, and a driver
// (conformance.Run) any backend implementation — a real compiled binary or
// an in-process DispatchTable double — can be run against to prove it
// actually speaks this protocol, closing the gap where a backend's own
// unit tests could otherwise all pass against a fake shape no real
// backend implements [design: Appendix A "Wire protocol and testing"].
package scriptout

import "encoding/json"

// ProtocolVersion is the wire envelope's own version — a single global
// number covering the Request/Response/Error shapes in this file. It is
// independent of any capability's own SchemaVersion: coupling the two would
// force every unrelated backend to redeploy on any one capability's
// unrelated schema break ("why split, not one global number").
const ProtocolVersion = 1

// Common/meta op names, shared across every capability.
const (
	// OpAuthStatus is the auth preflight op every backend may answer. Its
	// AuthStatus result-shape convention carries over unchanged from pg-pr's
	// existing convention.
	OpAuthStatus = "auth_status"
	// OpCapabilities is the capability-discovery op; see CapabilitiesResponse.
	OpCapabilities = "capabilities"
)

// Request is the wire shape read from stdin.
type Request struct {
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args,omitempty"`
}

// Error is the wire shape of a failure response. Code MUST be one of the
// closed six-value taxonomy named by the Err* sentinels in errors.go.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Response is the wire shape written to stdout for every op except
// capabilities (see CapabilitiesResponse). Exactly one of Result or Error
// is populated.
//
// Every Response carries ProtocolVersion plus the single SchemaVersion
// unambiguous for the capability the op belongs to.
type Response struct {
	ProtocolVersion int             `json:"protocolVersion"`
	SchemaVersion   int             `json:"schemaVersion,omitempty"`
	Result          json.RawMessage `json:"result,omitempty"`
	Error           *Error          `json:"error,omitempty"`
}

// CapabilitiesResponse is the sole top-level shape returned by the
// capabilities op. Unlike every other op, it does NOT nest inside a
// Result/Error envelope — its own protocolVersion sits alongside a
// schemaVersions map (one entry per schema-bearing capability the backend
// implements), an ops list, and a vocabulary object whose shape is
// per-entity-type/per-backend rather than universal.
type CapabilitiesResponse struct {
	ProtocolVersion int            `json:"protocolVersion"`
	SchemaVersions  map[string]int `json:"schemaVersions"`
	Ops             []string       `json:"ops"`
	Vocabulary      map[string]any `json:"vocabulary,omitempty"`
}

// Decode decodes raw into v. A nil/empty/"null" raw is treated as a
// zero-value v (some ops have no result payload).
func Decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, v)
}
