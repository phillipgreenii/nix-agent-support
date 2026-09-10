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
//
// ctx is wrapped in a DefaultExecTimeout deadline (via the swappable
// execTimeout var) and the *exec.Cmd's WaitDelay is set to
// DefaultWaitDelay, so a hung backend binary — or one whose own grandchild
// holds its stdout pipe open — is killed and reaped within a bounded time
// rather than hanging this call (and, since pg-connector's own fan-outs
// dispatch serially, every backend queued behind it) forever [bead #13].
// Folded stderr is capped via TruncateForFold, so a runaway or unexpectedly
// verbose backend binary cannot inflate the returned error without bound
// [bead #26].
func runInvoke(ctx context.Context, binary string, req Request) ([]byte, error) {
	if binary == "" {
		return nil, errors.New("scriptout: empty backend binary name")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("scriptout: marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	cmd := execCmdFactory(ctx, binary)
	cmd.WaitDelay = DefaultWaitDelay
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
					binary, runErr, TruncateForFold(stderr.Bytes()))
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
//
// config is copied verbatim onto the outgoing Request's own Config member
// (bead pg2-2j5ac.28.1, design: "the umbrella copies a registered
// backend's backends.<binary> config block VERBATIM into every request to
// that backend") — nil for a backend with no registered config block,
// which every existing caller built before this parameter existed passes
// unchanged, so this widening is source-compatible in spirit (every call
// site was updated to pass its own resolved config; none silently changed
// meaning by omission).
func Invoke(ctx context.Context, binary, op string, args any, config json.RawMessage) (*Response, error) {
	var rawArgs json.RawMessage
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("scriptout: marshal args for op %q: %w", op, err)
		}
		rawArgs = b
	}

	out, err := runInvoke(ctx, binary, Request{Op: op, Args: rawArgs, Config: config})
	if err != nil {
		return nil, err
	}

	var resp Response
	if err := json.Unmarshal(out, &resp); err != nil {
		if len(out) > 0 {
			return nil, fmt.Errorf("scriptout: %s: invalid JSON response: %w (stdout=%q)", binary, err, TruncateForFold(out))
		}
		return nil, fmt.Errorf("scriptout: %s: invalid JSON response: %w", binary, err)
	}
	if resp.Error != nil {
		return &resp, WrapError(sentinelForCode(resp.Error.Code), fmt.Sprintf("%s: %s", binary, resp.Error.Message))
	}
	// A protocolVersion mismatch is checked before anything else on the
	// success path: the umbrella (this process) and binary are each built
	// from a separate, independently-versioned/deployed nix derivation
	// (INV-VER-1), so skew between them is the ordinary case, not an
	// edge case. Every well-formed response carries the wire envelope's
	// own protocolVersion; a binary built from a commit whose envelope
	// shape has since changed reports that here even though its Result
	// otherwise looks well-formed — trusting Result under a mismatched
	// envelope version would be trusting a shape this process cannot
	// actually verify.
	if resp.ProtocolVersion != ProtocolVersion {
		return nil, WrapError(ErrVersionMismatch, fmt.Sprintf(
			"%s: protocolVersion %d != %d", binary, resp.ProtocolVersion, ProtocolVersion,
		))
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
		return nil, fmt.Errorf("scriptout: %s: invalid capabilities response: %w (stdout=%q)", binary, err, TruncateForFold(out))
	}
	if wire.Error != nil {
		return nil, WrapError(sentinelForCode(wire.Error.Code), fmt.Sprintf("%s: %s", binary, wire.Error.Message))
	}
	resp := wire.CapabilitiesResponse
	// Same protocolVersion check Invoke applies to every other op's
	// success path (see its own comment) — capabilities is not exempt
	// just because its shape is bespoke. Per-capability schemaVersion
	// skew (the other half of INV-VER-1's mechanism) is deliberately NOT
	// checked here: this package is capability-agnostic by design (see
	// this file's package doc comment) and has no notion of which
	// schema-bearing capabilities exist or what their current versions
	// are — that comparison is cmd/pg-connector's job against resp's own
	// SchemaVersions map (see config_validate.go), the one caller that
	// actually knows both sides.
	if resp.ProtocolVersion != ProtocolVersion {
		return nil, WrapError(ErrVersionMismatch, fmt.Sprintf(
			"%s: protocolVersion %d != %d", binary, resp.ProtocolVersion, ProtocolVersion,
		))
	}
	return &resp, nil
}
