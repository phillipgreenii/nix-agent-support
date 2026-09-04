// bd.go: parses `bd ... --json` output into this backend's own bdIssue
// shape and classifies bd's own error text onto pkg/scriptout's closed
// error taxonomy. New code — the pg-pr basis packages/pg-pr/pkg/beads never
// needed this because its mergerequest.go reads bd's metadata-map shape
// (bdIssue there decodes a `metadata` object into typed
// MergeRequestFields); a plain issue backend has no metadata map to
// decode, so this is a smaller, from-scratch shape rather than a port.
//
// Verified against a real `bd` (v1.2.2) invocation, not assumed from
// memory:
//
//   - `bd show <id> --json` / `bd update <id> ... --json` wrap their
//     result in `{"data": [...], "schema_version": N}` — an ARRAY even for
//     a single id.
//   - `bd create ... --json` / `bd comment <id> ... --json` wrap their
//     result in `{"data": {...}, "schema_version": N}` — a single OBJECT.
//   - On a well-formed failure (e.g. an unknown id), several bd subcommands
//     (show, comment) still exit non-zero but ALSO write
//     `{"data": {"error": "<message>"}, "schema_version": N}` to stdout;
//     others (update, and any pre-flight flag-validation failure such as a
//     malformed --priority) write NOTHING to stdout and report the failure
//     on stderr only. Both shapes are handled uniformly here.
package internal

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// bdIssue is the subset of bd's `--json` issue shape this backend needs —
// deliberately small: no metadata map, no dependencies, no comments (this
// backend has no use for them, unlike packages/pg-pr/pkg/beads.bdIssue).
type bdIssue struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	Priority  int      `json:"priority"`
	IssueType string   `json:"issue_type"`
	Labels    []string `json:"labels,omitempty"`
}

// decodeBDEnvelope peeks bd's `--json` envelope apart into either a
// successful payload (data), a well-formed bd-level error message
// (bdErrMsg), or a hard parse failure (err — the output was not the
// envelope shape at all, e.g. empty stdout from a stderr-only failure).
// Exactly one of data/bdErrMsg/err is meaningfully populated.
func decodeBDEnvelope(raw string) (data json.RawMessage, bdErrMsg string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, "", errors.New("bd: empty output (expected a --json envelope)")
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if uerr := json.Unmarshal([]byte(raw), &env); uerr != nil {
		return nil, "", fmt.Errorf("bd: parse JSON output: %w", uerr)
	}
	// A bd-level error envelope's data is always a bare {"error": "..."}
	// object. Unmarshaling env.Data (an object OR an array, depending on
	// which op succeeded) into this struct only succeeds for the object
	// case, and only sets Error when that object actually carries the key —
	// so this check is safe to run unconditionally against every shape.
	var errPayload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(env.Data, &errPayload) == nil && errPayload.Error != "" {
		return nil, errPayload.Error, nil
	}
	return env.Data, "", nil
}

// bdIssueFromObject decodes a single-object `data` payload (bd create, bd
// comment's own echo — comment's payload is a comment, not an issue, so
// this is used only by Create).
func bdIssueFromObject(data json.RawMessage) (*bdIssue, error) {
	var iss bdIssue
	if err := json.Unmarshal(data, &iss); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "bd: decode issue: "+err.Error())
	}
	return &iss, nil
}

// bdIssueFromArray decodes an array `data` payload (bd show, bd update) and
// returns its first element — the single id every one of this backend's
// calls addresses.
func bdIssueFromArray(data json.RawMessage) (*bdIssue, error) {
	var issues []bdIssue
	if err := json.Unmarshal(data, &issues); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "bd: decode issue list: "+err.Error())
	}
	if len(issues) == 0 {
		return nil, scriptout.WrapError(scriptout.ErrNotFound, "bd: no issue returned")
	}
	return &issues[0], nil
}

// classifyBDErrorMessage maps bd's own free-text error message onto
// scriptout's closed error taxonomy. bd's not_found phrasing is
// consistently "no issue(s) found ..." (verified above) — deliberately
// matched on that specific phrase rather than a bare "not found" substring,
// because a fully different failure (bd missing from PATH) surfaces as
// "executable file not found in $PATH", which also contains "not found"
// but is emphatically not a well-formed negative answer about one issue's
// existence. Anything not matching that specific phrase falls back to
// ErrUnavailable — the taxonomy's closest fit, mirroring
// pkg/scriptout.codeForError's own fallback rationale for an
// unclassifiable error.
func classifyBDErrorMessage(msg string) error {
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "no issue found") || strings.Contains(lower, "no issues found") {
		return scriptout.WrapError(scriptout.ErrNotFound, msg)
	}
	return scriptout.WrapError(scriptout.ErrUnavailable, msg)
}
