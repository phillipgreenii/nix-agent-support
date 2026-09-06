// exec.go: caller-side helpers for invoking script-out backend binaries.
//
// The exec-style backend is a binary on $PATH (or an absolute path). For
// each call we:
//
//  1. Marshal op+args to JSON wrapped in {"op": ..., "args": ...}.
//  2. exec.Cmd the binary, attach stdin/stdout pipes.
//  3. Write the request to stdin and close it.
//  4. Wait for the binary, capture stdout, decode the response.
//  5. If the response's error is set, surface it as a sentinel-wrapped Go error.
//  6. If neither error nor result is set, the envelope is a protocol
//     violation (not a success) — a deliberate no-payload success MUST
//     send "result":null explicitly, never omit result.
//  7. Otherwise return the decoded envelope so the caller can further
//     unmarshal Result into its own typed shape via Decode.
//
// Stderr is captured and folded into the error message if the binary
// produced any. The backend process's own exit code is not consulted
// independently — the stdout JSON is the contract. This matches (in shape,
// not capability-specific typing) pg-pr's existing
// pkg/plugin/scriptout/exec.go invoke() helper, implementing the same
// one-shot exec-per-call round trip.
package scriptout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
)

// execCmdFactory constructs exec.Cmd. Production code uses
// exec.CommandContext; tests swap this to spawn a test helper binary.
var execCmdFactory = exec.CommandContext

// runInvoke marshals req, execs binary with it on stdin, and returns the
// raw stdout bytes (or an error folding in stderr/exit status). Shared by
// Invoke and InvokeCapabilities.
func runInvoke(ctx context.Context, binary string, req Request) ([]byte, error) {
	if binary == "" {
		return nil, errors.New("scriptout: empty backend binary name")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("scriptout: marshal request: %w", err)
	}

	cmd := execCmdFactory(ctx, binary)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	out := stdout.Bytes()
	if len(out) == 0 {
		if runErr != nil {
			if stderr.Len() > 0 {
				return nil, fmt.Errorf("scriptout: %s: %w (stderr: %s)",
					binary, runErr, bytes.TrimSpace(stderr.Bytes()))
			}
			return nil, fmt.Errorf("scriptout: %s: %w", binary, runErr)
		}
		return nil, fmt.Errorf("scriptout: %s: no response on stdout", binary)
	}
	return out, nil
}

// Invoke runs binary with a request for op+args on stdin, decodes the
// response envelope, and returns it. On a wire-level error response, the
// returned error wraps the matching Err* sentinel via errors.Is so callers
// don't need to substring-match. Opaque to which capability is being
// called — matches the shape of pg-pr's existing invoke() helper.
func Invoke(ctx context.Context, binary, op string, args any) (*Response, error) {
	var rawArgs json.RawMessage
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("scriptout: marshal args for op %q: %w", op, err)
		}
		rawArgs = b
	}

	out, err := runInvoke(ctx, binary, Request{Op: op, Args: rawArgs})
	if err != nil {
		return nil, err
	}

	var resp Response
	if err := json.Unmarshal(out, &resp); err != nil {
		if len(out) > 0 {
			return nil, fmt.Errorf("scriptout: %s: invalid JSON response: %w (stdout=%q)", binary, err, out)
		}
		return nil, fmt.Errorf("scriptout: %s: invalid JSON response: %w", binary, err)
	}
	if resp.Error != nil {
		return &resp, WrapError(sentinelForCode(resp.Error.Code), fmt.Sprintf("%s: %s", binary, resp.Error.Message))
	}
	if len(resp.Result) == 0 {
		// Neither result nor error is set: the backend produced a
		// well-formed-looking envelope that answers nothing at all. This
		// is a protocol violation, not success — an explicit no-payload
		// success MUST send "result":null (a present-but-null field,
		// which decodes to a non-empty RawMessage), not omit result
		// entirely [bug A7]. Returning a nil *Response here matches this
		// package's existing convention (see the invalid-JSON branches
		// above): a nil resp is a CLI-level failure before any
		// well-formed wire response was produced.
		return nil, fmt.Errorf("scriptout: %s: protocol violation: response has neither result nor error", binary)
	}
	return &resp, nil
}

// capabilitiesWireShape is InvokeCapabilities' own unmarshal target: the
// bespoke CapabilitiesResponse fields plus an Error field, so a single
// decode can tell an error envelope apart from a real capabilities
// response. A backend that fails the capabilities op (unknown_op, or any
// handler error) still speaks the ordinary Response{Error: ...} envelope
// via serve.go's writeErrorResponse — CapabilitiesResponse's bespoke
// top-level shape is written only on success. Without this check, an
// error envelope has no "protocolVersion"/"schemaVersions"/"ops" fields
// CapabilitiesResponse recognizes, so it silently decoded as a
// zero-value success [bug A6].
type capabilitiesWireShape struct {
	CapabilitiesResponse
	Error *Error `json:"error,omitempty"`
}

// InvokeCapabilities runs the capabilities op against binary and decodes
// its bespoke CapabilitiesResponse shape (see CapabilitiesResponse) — the
// one op whose response Invoke's normal Response envelope cannot decode.
// It checks for an error envelope first (see capabilitiesWireShape) so a
// failed capabilities call surfaces as an error instead of a zero-value
// success [bug A6].
func InvokeCapabilities(ctx context.Context, binary string) (*CapabilitiesResponse, error) {
	out, err := runInvoke(ctx, binary, Request{Op: OpCapabilities})
	if err != nil {
		return nil, err
	}
	var wire capabilitiesWireShape
	if err := json.Unmarshal(out, &wire); err != nil {
		return nil, fmt.Errorf("scriptout: %s: invalid capabilities response: %w (stdout=%q)", binary, err, out)
	}
	if wire.Error != nil {
		return nil, WrapError(sentinelForCode(wire.Error.Code), fmt.Sprintf("%s: %s", binary, wire.Error.Message))
	}
	resp := wire.CapabilitiesResponse
	return &resp, nil
}
