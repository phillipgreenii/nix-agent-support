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
//  6. Otherwise return the decoded envelope so the caller can further
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
	return &resp, nil
}

// InvokeCapabilities runs the capabilities op against binary and decodes
// its bespoke CapabilitiesResponse shape (see CapabilitiesResponse) — the
// one op whose response Invoke's normal Response envelope cannot decode.
func InvokeCapabilities(ctx context.Context, binary string) (*CapabilitiesResponse, error) {
	out, err := runInvoke(ctx, binary, Request{Op: OpCapabilities})
	if err != nil {
		return nil, err
	}
	var resp CapabilitiesResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("scriptout: %s: invalid capabilities response: %w (stdout=%q)", binary, err, out)
	}
	return &resp, nil
}
