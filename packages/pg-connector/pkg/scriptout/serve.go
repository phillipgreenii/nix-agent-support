package scriptout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// OpHandler pairs an op's business logic with the schema version of the
// capability it belongs to. A DispatchTable a capability package builds is
// keyed by op name; ServeLoop uses the matched entry's SchemaVersion to
// populate the wire envelope's schemaVersion field — that classification
// lives with the capability (which owns its schema), never inside this
// capability-agnostic package.
type OpHandler struct {
	SchemaVersion int
	Handle        func(ctx context.Context, args json.RawMessage) (any, error)
}

// DispatchTable maps an op name to its handler. A Tier-2 backend binary's
// own capability package builds one (including, if it wants to answer
// them, entries for OpAuthStatus and OpCapabilities) and hands it to
// ServeLoop. pkg/scriptout itself never imports a per-capability package —
// it only ever sees whatever table it's handed.
type DispatchTable map[string]OpHandler

// dispatchEnv bundles stdin/stdout so tests can swap them out.
type dispatchEnv struct {
	in  io.Reader
	out io.Writer
}

// ServeLoop reads exactly one JSON request from stdin, dispatches it
// through table, and writes exactly one JSON response to stdout. It
// returns the process exit code main() should use: 0 on success, or on
// failure the taxonomy-specific code ExitCodeForError computes (2-7),
// falling back to the generic 1 only for a wire-level failure with no
// classifiable error at all (e.g. a failure to write the response itself)
// [bead pg2-7vgn5]. The JSON error body's Code field always carries the
// same classification — the exit code is an additional, parse-free signal,
// not a replacement for the JSON body as the primary source of truth.
func ServeLoop(table DispatchTable) int {
	return ServeOne(table, os.Stdin, os.Stdout)
}

// ServeOne is ServeLoop's logic against caller-supplied in/out instead of
// the real process's os.Stdin/os.Stdout — the same one-request/one-response
// round trip, exported so a caller (e.g. pkg/scriptout/conformance's
// in-process Backend double) can drive an in-process DispatchTable without
// redirecting real process stdio. ServeLoop itself is exactly
// ServeOne(table, os.Stdin, os.Stdout).
func ServeOne(table DispatchTable, in io.Reader, out io.Writer) int {
	return serveLoop(dispatchEnv{in: in, out: out}, table)
}

func serveLoop(env dispatchEnv, table DispatchTable) int {
	req, err := readRequest(env.in)
	if err != nil {
		return writeErrorResponse(env.out, 0, WrapError(ErrUnavailable, err.Error()))
	}

	entry, ok := table[req.Op]
	if !ok {
		return writeErrorResponse(env.out, 0, WrapError(ErrUnknownOp, fmt.Sprintf("unknown op %q", req.Op)))
	}

	result, err := entry.Handle(context.Background(), req.Args)
	if err != nil {
		return writeErrorResponse(env.out, entry.SchemaVersion, err)
	}

	if req.Op == OpCapabilities {
		// Bespoke top-level shape: write the handler's own
		// CapabilitiesResponse directly, no result/error wrapper.
		if err := writeJSON(env.out, result); err != nil {
			return 1
		}
		return 0
	}

	raw, err := json.Marshal(result)
	if err != nil {
		return writeErrorResponse(env.out, entry.SchemaVersion, fmt.Errorf("scriptout: marshal result for op %q: %w", req.Op, err))
	}
	resp := Response{
		ProtocolVersion: ProtocolVersion,
		SchemaVersion:   entry.SchemaVersion,
		Result:          raw,
	}
	if err := writeJSON(env.out, resp); err != nil {
		return 1
	}
	return 0
}

// readRequest reads exactly one JSON object from r.
func readRequest(r io.Reader) (*Request, error) {
	dec := json.NewDecoder(r)
	var req Request
	if err := dec.Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("scriptout: empty stdin (expected one JSON request)")
		}
		return nil, fmt.Errorf("scriptout: decode request: %w", err)
	}
	if req.Op == "" {
		return nil, errors.New("scriptout: request is missing required field \"op\"")
	}
	return &req, nil
}

// writeJSON encodes v to w as one JSON object, HTML-escaping disabled to
// match scriptout's existing convention.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// writeErrorResponse writes a Response carrying a wire-taxonomy error
// derived from err, and returns the matching backend-process exit code via
// ExitCodeForError (2-7, one per wire-taxonomy code — see errors.go;
// bead pg2-7vgn5). The returned exit code and the JSON body's error.code
// always agree, since both derive from the same codeForError
// classification.
func writeErrorResponse(w io.Writer, schemaVersion int, err error) int {
	_ = writeJSON(w, Response{
		ProtocolVersion: ProtocolVersion,
		SchemaVersion:   schemaVersion,
		Error: &Error{
			Code:    codeForError(err),
			Message: err.Error(),
		},
	})
	return ExitCodeForError(err)
}
