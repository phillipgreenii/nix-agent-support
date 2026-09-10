// list.go: the "list" op's own conformance helper (bead pg2-2j5ac.28.1's
// Files section names this package for "the new list conformance
// golden"). This package stays capability-agnostic — it decodes no
// pr/issue-specific Entities/PresentIDs shape of its own (see
// conformance.go's package doc comment) — so this exposes only a request
// builder (ListRequest) and a round-trip runner (InvokeList) that checks
// the GENERIC wire-envelope contract every op already gets from
// CheckResponse/driver.go, leaving a caller's own capability-specific
// decode of the success payload to the caller (a pr or issue backend's
// own test file, which already imports pkg/schema for PRListResult/
// IssueListResult).
//
// Every pr/issue backend's own test suite is expected to run three cases
// through InvokeList against its real production DispatchTable (via
// TableBackend, no subprocess needed): a recognized query name with
// cursor: null (a success), an unrecognized query name (query_not_
// recognized, exit 8), and a query name whose config.queries value is a
// LIST of expressions (design's "union results deduplicated by id"
// rule) — design's acceptance-criteria block, first bullet.
package conformance

import (
	"context"
	"encoding/json"
	"fmt"
)

// ListRequest builds one "list" op's raw wire request bytes: args
// {"query": query, "cursor": null, "ids_only": idsOnly} — cursor is
// ALWAYS null (design's own binding decision: "list NEVER passes a
// cursor in this packet") — plus config, copied verbatim as the
// request's own opaque config member (the same shape
// pkg/scriptout.Request.Config carries).
func ListRequest(query string, idsOnly bool, config json.RawMessage) []byte {
	req := struct {
		Op     string          `json:"op"`
		Args   any             `json:"args"`
		Config json.RawMessage `json:"config,omitempty"`
	}{
		Op:     "list",
		Args:   map[string]any{"query": query, "cursor": nil, "ids_only": idsOnly},
		Config: config,
	}
	b, err := json.Marshal(req)
	if err != nil {
		// args/config are always plain, marshalable data in every caller of
		// this helper (a query string, a bool, and a json.RawMessage) — a
		// failure here would be a programmer error in the caller, not a
		// runtime condition worth returning as an error.
		panic(fmt.Sprintf("conformance: marshal list request: %v", err))
	}
	return b
}

// ListResult is the decoded outcome of one InvokeList round trip: either a
// well-formed success (Result populated, ErrorCode empty) or a
// well-formed error-branch response (ErrorCode populated), plus the
// process's own exit code.
type ListResult struct {
	Result    json.RawMessage
	ErrorCode string
	ExitCode  int
}

// InvokeList sends a "list" request (built by ListRequest) to backend and
// asserts the reply is a well-formed wire response (CheckResponse) —
// covering EITHER branch, so a caller checking the query_not_recognized
// negative case and a caller checking a successful match both go through
// the exact same assertion this package already applies to every other
// op (driver.go's invokingUnknownOp/invokingMalformedStdin/
// invokingCapabilities). The returned err is non-nil ONLY for a
// transport/schema-conformance failure (backend.Invoke itself, malformed
// JSON, or a reply that fails CheckResponse) — a well-formed
// query_not_recognized error-branch response is a normal, err == nil
// ListResult with ErrorCode set, exactly like a well-formed success.
func InvokeList(ctx context.Context, backend Backend, query string, idsOnly bool, config json.RawMessage) (*ListResult, error) {
	out, exitCode, err := backend.Invoke(ctx, ListRequest(query, idsOnly, config))
	if err != nil {
		return nil, fmt.Errorf("conformance: invoke list: %w", err)
	}
	var v any
	if jsonErr := json.Unmarshal(out, &v); jsonErr != nil {
		return nil, fmt.Errorf("conformance: list reply not JSON: %w (stdout=%q)", jsonErr, out)
	}
	if checkErr := CheckResponse(v); checkErr != nil {
		return nil, fmt.Errorf("conformance: list reply failed response schema: %w", checkErr)
	}
	obj, _ := v.(map[string]any)
	if errObj, ok := obj["error"].(map[string]any); ok {
		code, _ := errObj["code"].(string)
		return &ListResult{ErrorCode: code, ExitCode: exitCode}, nil
	}
	resultBytes, marshalErr := json.Marshal(obj["result"])
	if marshalErr != nil {
		return nil, fmt.Errorf("conformance: re-marshal list result: %w", marshalErr)
	}
	return &ListResult{Result: resultBytes, ExitCode: exitCode}, nil
}
