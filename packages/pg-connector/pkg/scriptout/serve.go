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

func defaultEnv() dispatchEnv {
	return dispatchEnv{in: os.Stdin, out: os.Stdout}
}

// ServeLoop reads exactly one JSON request from stdin, dispatches it
// through table, and writes exactly one JSON response to stdout. It
// returns the process exit code main() should use — a plain 0/1 at this
// wire level; classification of what went wrong lives in the JSON error
// body's Code field only.
func ServeLoop(table DispatchTable) int {
	return serveLoop(defaultEnv(), table)
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
// derived from err, and returns exit code 1 (the plain wire-level failure
// code; classification lives in the JSON body only).
func writeErrorResponse(w io.Writer, schemaVersion int, err error) int {
	_ = writeJSON(w, Response{
		ProtocolVersion: ProtocolVersion,
		SchemaVersion:   schemaVersion,
		Error: &Error{
			Code:    codeForError(err),
			Message: err.Error(),
		},
	})
	return 1
}
