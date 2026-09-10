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
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// bdIssue is the subset of bd's `--json` issue shape this backend needs —
// deliberately small: no comments (this backend has no use for them,
// unlike packages/pg-pr/pkg/beads.bdIssue). Description,
// Assignee, Parent, and Dependencies were added by bead pg2-akfw5 (review finding A-33: Show previously
// dropped all four), verified live against a real `bd show --json` on a
// child issue with a parent and description/assignee set, not assumed from
// memory. UpdatedAt, DueAt, ExternalRef, and Metadata were added by bead
// pg2-2j5ac.28.3 — field names (`updated_at`, `due_at`, `external_ref`,
// `metadata`) verified live against a real bd v1.2.2 `bd create --due ...
// --external-ref ... --metadata ... --json` / `bd show --json` round trip
// in a disposable embedded-dolt workspace, not assumed from `--help` text
// alone (bd's own flags are named `--due`/`--external-ref` but the WIRE
// field names differ: `due_at`/`external_ref`).
type bdIssue struct {
	ID           string                     `json:"id"`
	Title        string                     `json:"title"`
	Description  string                     `json:"description,omitempty"`
	Status       string                     `json:"status"`
	Priority     int                        `json:"priority"`
	IssueType    string                     `json:"issue_type"`
	Labels       []string                   `json:"labels,omitempty"`
	Assignee     string                     `json:"assignee,omitempty"`
	Parent       string                     `json:"parent,omitempty"`
	Dependencies []bdDependency             `json:"dependencies,omitempty"`
	UpdatedAt    string                     `json:"updated_at,omitempty"`
	DueAt        string                     `json:"due_at,omitempty"`
	ExternalRef  string                     `json:"external_ref,omitempty"`
	Metadata     map[string]json.RawMessage `json:"metadata,omitempty"`
}

// bdDepRecord is one record of `bd dep list <id> --direction=down --json`'s
// flat per-id dependency listing (used only by Deps' recursive
// blocked-by walk, bead pg2-2j5ac.28.3) — the same full-issue field shape
// bdIssue already decodes, plus its own dependency_type (this listing's
// edge label), verified live against a real bd v1.2.2 invocation.
type bdDepRecord struct {
	bdIssue
	DependencyType string `json:"dependency_type"`
}

// bdMetadataToStrings coerces bd's own metadata map onto
// schema.Issue.Metadata's map[string]string wire shape (binding decision:
// "metadata is string-to-string on the wire; a backend whose native field
// is typed ... coerces to its decimal string on the way out"). bd's own
// JSON values are NOT always strings: `bd update --set-metadata num=42`
// round-trips as a JSON number, `--set-metadata flag=true` as a JSON
// boolean, and `--set-metadata foo=bar` as a JSON string — verified live
// against a real bd v1.2.2 in a disposable embedded-dolt workspace. A
// nested object/array value (bd's own `--metadata` flag accepts an
// arbitrary JSON blob, not just flat scalars) is coerced to its compact
// JSON text rather than dropped, so no value is ever silently lost.
func bdMetadataToStrings(raw map[string]json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			out[k] = s
			continue
		}
		var f float64
		if err := json.Unmarshal(v, &f); err == nil {
			out[k] = strconv.FormatFloat(f, 'f', -1, 64)
			continue
		}
		var b bool
		if err := json.Unmarshal(v, &b); err == nil {
			out[k] = strconv.FormatBool(b)
			continue
		}
		// object/array/null: fall back to the raw compact JSON text.
		out[k] = strings.TrimSpace(string(v))
	}
	return out
}

// joinBDSetMetadataArgs renders md as repeated `--set-metadata key=value`
// flag values — bd's own merge-into-existing-metadata flag (Update's own
// binding decision: "Metadata here is a MERGE ... mirroring bd's own
// --set-metadata semantics"), distinct from Create's own `--metadata`
// flag below (a single JSON-blob REPLACE, bd's only metadata-setting flag
// on `bd create`). Keys are sorted for deterministic argv ordering (tests,
// and reproducible audit logs), never because bd itself requires it.
func joinBDSetMetadataArgs(md map[string]string) []string {
	if len(md) == 0 {
		return nil
	}
	keys := make([]string, 0, len(md))
	for k := range md {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		args = append(args, "--set-metadata", k+"="+md[k])
	}
	return args
}

// bdDependency is one entry of bd's `dependencies` array on a `show`
// response — a nested issue summary in real bd output, of which this
// backend keeps only the two fields toSchemaIssue maps onto
// schema.IssueDependency (id, dependency_type); the rest (title, status,
// ...) is redundant with what a caller would get from Show-ing that id
// directly, so json.Unmarshal simply ignores those extra keys.
type bdDependency struct {
	ID             string `json:"id"`
	DependencyType string `json:"dependency_type"`
}

// joinBDLabels renders labels as the ONE `--labels` flag value bd's own
// pflag StringSlice flag decodes correctly — that flag type parses its
// value via encoding/csv (pflag's stringSlice.go readAsCSV/writeAsCSV), so
// a label containing a literal comma or quote needs CSV quoting, not the
// bare "," separator this used to join with [review finding A-33]. Verified live
// against a real bd v1.2.2: `bd create --labels '"foo,bar",baz' --json`
// decodes back to exactly two labels, "foo,bar" and "baz" — the same
// encoding this function produces — confirming this matches bd's actual
// flag-parsing behavior rather than an assumption about pflag internals.
func joinBDLabels(labels []string) (string, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(labels); err != nil {
		return "", fmt.Errorf("bd: encode --labels value: %w", err)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", fmt.Errorf("bd: encode --labels value: %w", err)
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
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

// bdIssuesFromArray decodes an array `data` payload (bd ready, bd list)
// and returns EVERY element — unlike bdIssueFromArray, List's own
// multi-match use for the "list" op (bead pg2-2j5ac.28.1). An empty
// array is a well-formed "no matches," not an error — bd ready/list
// return a genuinely empty match set for a query with no hits, which is
// not the same failure bdIssueFromArray's own not_found handles (that
// path answers a single-id lookup where "nothing returned" specifically
// means "this id doesn't exist").
func bdIssuesFromArray(data json.RawMessage) ([]bdIssue, error) {
	var issues []bdIssue
	if err := json.Unmarshal(data, &issues); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "bd: decode issue list: "+err.Error())
	}
	return issues, nil
}

// bdDepRecordsFromArray decodes an array `data` payload from
// `bd dep list <id> --direction=down --json` (used only by Deps' recursive
// blocked-by walk, bead pg2-2j5ac.28.3). An empty array is a well-formed
// "no dependencies," never an error, mirroring bdIssuesFromArray's
// identical reasoning.
func bdDepRecordsFromArray(data json.RawMessage) ([]bdDepRecord, error) {
	var recs []bdDepRecord
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "bd: decode dependency list: "+err.Error())
	}
	return recs, nil
}

// bdListFirstTokens is the closed set of bd subcommands a "list"
// query-expression MAY start with (design's own binding decision:
// "a query expression here is the full bd argument vector after the
// binary ... permits only ready/list as the first token"). Any other
// first token — including a destructive verb like "close" or "update" —
// is rejected before ever exec'ing bd.
var bdListFirstTokens = map[string]bool{"ready": true, "list": true}

// parseBDListExpr splits expr (one QueryExpr element — a full bd argument
// vector, bead pg2-2j5ac.28.1's own binding decision) into argv tokens and validates its first token
// against bdListFirstTokens. Splitting is a plain strings.Fields
// whitespace split [freedom boundary: a query expression needing a
// literal space inside one quoted flag value — e.g. --label-any with a
// multi-word value — is not representable this simply; a config author
// needing that is a future extension, not something this packet's own
// config.queries convention supports today].
func parseBDListExpr(expr string) ([]string, error) {
	argv := strings.Fields(expr)
	if len(argv) == 0 {
		return nil, fmt.Errorf("bd: empty query expression")
	}
	if !bdListFirstTokens[argv[0]] {
		return nil, fmt.Errorf("bd: query expression %q must start with \"ready\" or \"list\", got %q", expr, argv[0])
	}
	return argv, nil
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
