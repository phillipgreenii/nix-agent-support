// exec.go: caller-side helpers for invoking script-out provider binaries.
//
// The exec-style provider is a binary on $PATH (or an absolute path).
// For each provider method, we:
//
//  1. Marshal the args struct to JSON wrapped in {"op": ..., "args": ...}.
//  2. exec.Cmd the binary, attach stdin/stdout pipes.
//  3. Write the request to stdin and close it.
//  4. Wait for the binary, capture stdout, decode the Response.
//  5. If Response.Error is set, surface it as a Go error.
//  6. Otherwise, unmarshal Response.Result into the caller-side type.
//
// Stderr is captured and folded into the error message if the binary
// produced any. The provider's process exit code is not consulted
// independently — the stdout JSON is the contract.

package scriptout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
)

// execCmdFactory is the function used to construct exec.Cmd. Production
// code uses exec.CommandContext. Tests can swap this to spawn the test
// binary (using the standard "TestHelperProcess" trick) so the wrappers
// can be exercised end-to-end without a real provider binary.
var execCmdFactory = exec.CommandContext

// invoke runs binary with the given Request on stdin, decodes the
// Response, and returns either Result (as json.RawMessage so the caller
// can do typed unmarshal) or an error.
func invoke(ctx context.Context, binary string, req Request) (json.RawMessage, error) {
	if binary == "" {
		return nil, errors.New("scriptout: empty provider binary name")
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

	// Decode response if any. If decode fails AND we have stderr, the
	// stderr message is the most useful thing to surface.
	out := stdout.Bytes()
	if len(out) == 0 {
		// Nothing on stdout: process likely failed before responding (e.g.
		// binary missing, panic). Use runErr + stderr.
		if runErr != nil {
			if stderr.Len() > 0 {
				return nil, fmt.Errorf("scriptout: %s: %w (stderr: %s)",
					binary, runErr, bytes.TrimSpace(stderr.Bytes()))
			}
			return nil, fmt.Errorf("scriptout: %s: %w", binary, runErr)
		}
		return nil, fmt.Errorf("scriptout: %s: no response on stdout", binary)
	}

	var resp Response
	if err := json.Unmarshal(out, &resp); err != nil {
		// Stdout was non-empty but not valid JSON. Include stderr if any.
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("scriptout: %s: invalid JSON response: %w (stdout=%q, stderr=%q)",
				binary, err, out, bytes.TrimSpace(stderr.Bytes()))
		}
		return nil, fmt.Errorf("scriptout: %s: invalid JSON response: %w (stdout=%q)",
			binary, err, out)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("scriptout: %s: %s", binary, resp.Error)
	}
	// Re-marshal Result so callers can do strongly-typed Unmarshal.
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("scriptout: re-marshal result: %w", err)
	}
	return raw, nil
}

// unmarshalInto decodes raw into v. v must be a non-nil pointer.
// A null raw is treated as a zero-value v (some ops return null/no result).
func unmarshalInto(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, v)
}
