// ops.go: the ci capability's typed op catalog. Each op is one exported
// Op[Req, Res] value; the backend dispatch table (NewDispatchTable) and the
// client's generic Call are both built from the same descriptor, so a
// renamed op or request key is a compile error on both sides. The wire
// encoding is unchanged: the catalog types the existing JSON.
package ci

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Op describes one wire op by name and request/result type. SchemaVersion is
// a descriptor-level value: it is not sent on the wire and neither Handle
// nor Call checks it.
type Op[Req, Res any] struct {
	Name          string
	SchemaVersion int
}

// ListRunsReq is the list_runs request.
type ListRunsReq struct {
	PRID string `json:"pr_id"`
}

// GetLogsReq is the get_logs request.
type GetLogsReq struct {
	RunID string `json:"run_id"`
	Repo  string `json:"repo"`
}

// RerunFailedReq is the rerun_failed request.
type RerunFailedReq struct {
	PRID string `json:"pr_id"`
}

// None is the result type of an op with no result payload; a nil *None
// encodes as the wire's "result": null.
type None struct{}

// The ci capability's ops. Initial SchemaVersion is schema.CISchemaVersion.
var (
	ListRuns    = Op[ListRunsReq, []schema.CIRun]{Name: "list_runs", SchemaVersion: schema.CISchemaVersion}
	GetLogs     = Op[GetLogsReq, []byte]{Name: "get_logs", SchemaVersion: schema.CISchemaVersion}
	RerunFailed = Op[RerunFailedReq, *None]{Name: "rerun_failed", SchemaVersion: schema.CISchemaVersion}
)

// Catalog lists every ci op name, for the catalog-versus-doc test.
func Catalog() []string {
	return []string{ListRuns.Name, GetLogs.Name, RerunFailed.Name}
}

// Handle builds op's dispatch entry: it decodes Req from the wire args
// (an undecodable request is ErrInvalidArgument) and encodes Res as the
// handler's result. fn's error passes through unwrapped.
func Handle[Req, Res any](op Op[Req, Res], fn func(ctx context.Context, req Req) (Res, error)) scriptout.OpHandler {
	return scriptout.OpHandler{
		SchemaVersion: op.SchemaVersion,
		Handle: func(ctx context.Context, args json.RawMessage) (any, error) {
			var req Req
			if err := scriptout.Decode(args, &req); err != nil {
				return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode "+op.Name+" args: "+err.Error())
			}
			res, err := fn(ctx, req)
			if none, ok := any(res).(*None); ok && none == nil {
				// no result payload: an untyped nil, as the wire always carried
				return nil, err
			}
			return res, err
		},
	}
}

// Invoker sends one op request and returns the wire response. It is the
// seam Call is built over (the umbrella's targeted dispatch satisfies it).
type Invoker func(ctx context.Context, op string, args any) (*scriptout.Response, error)

// Call sends req as op through invoke and returns the raw wire response, so
// a caller can report the envelope as-is. Decode the result with
// DecodeResult.
func Call[Req, Res any](ctx context.Context, invoke Invoker, op Op[Req, Res], req Req) (*scriptout.Response, error) {
	return invoke(ctx, op.Name, req)
}

// DecodeResult decodes op's Res from a successful response's result.
func DecodeResult[Req, Res any](op Op[Req, Res], resp *scriptout.Response) (Res, error) {
	var res Res
	if resp == nil || resp.Error != nil {
		return res, nil
	}
	if err := scriptout.Decode(resp.Result, &res); err != nil {
		return res, fmt.Errorf("decode %s result: %w", op.Name, err)
	}
	return res, nil
}
