package conformance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeListTable builds a minimal DispatchTable exercising exactly the
// query-name resolution semantics design pin: "team" and
// "multi" (list-valued) resolve against config.queries, anything else is
// query_not_recognized.
func fakeListTable() scriptout.DispatchTable {
	return scriptout.DispatchTable{
		"list": {
			SchemaVersion: 1,
			Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
				var a struct {
					Query   string `json:"query"`
					IDsOnly bool   `json:"ids_only"`
				}
				if err := scriptout.Decode(args, &a); err != nil {
					return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, err.Error())
				}
				cfg := scriptout.ConfigFromContext(ctx)
				var queries struct {
					Queries map[string]json.RawMessage `json:"queries"`
				}
				if len(cfg) > 0 {
					_ = json.Unmarshal(cfg, &queries)
				}
				if _, ok := queries.Queries[a.Query]; !ok {
					return nil, scriptout.WrapError(scriptout.ErrQueryNotRecognized, "query %q not defined")
				}
				if a.IDsOnly {
					return map[string]any{"entities": []any{}, "present_ids": []string{"1", "2"}, "cursor": nil, "truncated": false}, nil
				}
				return map[string]any{"entities": []any{map[string]any{"id": "1"}, map[string]any{"id": "2"}}, "present_ids": []string{"1", "2"}, "cursor": nil, "truncated": false}, nil
			},
		},
	}
}

func TestInvokeList_Success(t *testing.T) {
	backend := TableBackend{Table: fakeListTable()}
	config := json.RawMessage(`{"queries":{"team":"is:open"}}`)
	res, err := InvokeList(context.Background(), backend, "team", false, config)
	if err != nil {
		t.Fatalf("InvokeList: %v", err)
	}
	if res.ErrorCode != "" {
		t.Fatalf("ErrorCode = %q, want empty (success)", res.ErrorCode)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	var decoded struct {
		Entities   []map[string]any `json:"entities"`
		PresentIDs []string         `json:"present_ids"`
		Cursor     *string          `json:"cursor"`
		Truncated  bool             `json:"truncated"`
	}
	if err := json.Unmarshal(res.Result, &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if decoded.Cursor != nil {
		t.Fatalf("cursor = %v, want null", decoded.Cursor)
	}
	if len(decoded.PresentIDs) != 2 {
		t.Fatalf("present_ids = %v", decoded.PresentIDs)
	}
}

func TestInvokeList_UnrecognizedQuery(t *testing.T) {
	backend := TableBackend{Table: fakeListTable()}
	config := json.RawMessage(`{"queries":{"team":"is:open"}}`)
	res, err := InvokeList(context.Background(), backend, "nonexistent-name", false, config)
	if err != nil {
		t.Fatalf("InvokeList: %v", err)
	}
	if res.ErrorCode != "query_not_recognized" {
		t.Fatalf("ErrorCode = %q, want query_not_recognized", res.ErrorCode)
	}
	if want := scriptout.ExitCodeForCode("query_not_recognized"); res.ExitCode != want {
		t.Fatalf("ExitCode = %d, want %d", res.ExitCode, want)
	}
}

func TestInvokeList_ListValuedQuery(t *testing.T) {
	backend := TableBackend{Table: fakeListTable()}
	config := json.RawMessage(`{"queries":{"multi":["is:open author:@me","is:open review-requested:@me"]}}`)
	res, err := InvokeList(context.Background(), backend, "multi", true, config)
	if err != nil {
		t.Fatalf("InvokeList: %v", err)
	}
	if res.ErrorCode != "" {
		t.Fatalf("ErrorCode = %q, want empty (success)", res.ErrorCode)
	}
	var decoded struct {
		PresentIDs []string `json:"present_ids"`
	}
	if err := json.Unmarshal(res.Result, &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(decoded.PresentIDs) != 2 {
		t.Fatalf("present_ids = %v", decoded.PresentIDs)
	}
}

func TestListRequest_CursorAlwaysNull(t *testing.T) {
	raw := ListRequest("team", false, nil)
	var decoded struct {
		Op   string `json:"op"`
		Args struct {
			Query  string  `json:"query"`
			Cursor *string `json:"cursor"`
		} `json:"args"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Op != "list" {
		t.Fatalf("op = %q", decoded.Op)
	}
	if decoded.Args.Cursor != nil {
		t.Fatalf("cursor = %v, want null", decoded.Args.Cursor)
	}
}
