// activity.go: the "list_activity" op's conformance case. Like list.go, this
// package stays capability-agnostic about everything a backend decides (what
// kinds it emits, how many items its fake holds); it checks only the generic
// activity envelope contract every implementing backend shares: item
// invariants, range handling, id stability across repeated calls, and the
// invalid_argument answers for a malformed range. A backend's own fake-backend
// test runs RunListActivityCase against its real production DispatchTable (via
// TableBackend) and asserts backend-specific content separately.
package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// ListActivityRequest builds one "list_activity" op's raw wire request bytes:
// args {"since": since, "before": before}. An empty since is omitted (an
// open-ended range); an empty before is omitted too, which exists so a caller
// can build the negative "before absent" request.
func ListActivityRequest(since, before string) []byte {
	args := map[string]any{}
	if since != "" {
		args["since"] = since
	}
	if before != "" {
		args["before"] = before
	}
	req := struct {
		Op   string         `json:"op"`
		Args map[string]any `json:"args"`
	}{Op: "list_activity", Args: args}
	b, err := json.Marshal(req)
	if err != nil {
		// args are plain strings in every caller; a failure here would be a
		// programmer error, not a runtime condition (same stance as ListRequest).
		panic(fmt.Sprintf("conformance: marshal list_activity request: %v", err))
	}
	return b
}

// ListActivityResult is the decoded outcome of one InvokeListActivity round
// trip: either a well-formed success (Result populated, ErrorCode empty) or a
// well-formed error-branch response (ErrorCode populated), plus the process's
// own exit code.
type ListActivityResult struct {
	Result    json.RawMessage
	ErrorCode string
	ExitCode  int
}

// InvokeListActivity sends a "list_activity" request (built by
// ListActivityRequest) to backend and asserts the reply is a well-formed wire
// response (CheckResponse), on either branch. The returned err is non-nil only
// for a transport/schema-conformance failure; a well-formed error-branch
// response is a normal, err == nil result with ErrorCode set.
func InvokeListActivity(ctx context.Context, backend Backend, since, before string) (*ListActivityResult, error) {
	out, exitCode, err := backend.Invoke(ctx, ListActivityRequest(since, before))
	if err != nil {
		return nil, fmt.Errorf("conformance: invoke list_activity: %w", err)
	}
	var v any
	if jsonErr := json.Unmarshal(out, &v); jsonErr != nil {
		return nil, fmt.Errorf("conformance: list_activity reply not JSON: %w (stdout=%q)", jsonErr, out)
	}
	if checkErr := CheckResponse(v); checkErr != nil {
		return nil, fmt.Errorf("conformance: list_activity reply failed response schema: %w", checkErr)
	}
	obj, _ := v.(map[string]any)
	if errObj, ok := obj["error"].(map[string]any); ok {
		code, _ := errObj["code"].(string)
		return &ListActivityResult{ErrorCode: code, ExitCode: exitCode}, nil
	}
	resultBytes, marshalErr := json.Marshal(obj["result"])
	if marshalErr != nil {
		return nil, fmt.Errorf("conformance: re-marshal list_activity result: %w", marshalErr)
	}
	return &ListActivityResult{Result: resultBytes, ExitCode: exitCode}, nil
}

// RunListActivityCase runs the generic list_activity conformance checks
// against backend for the range [since, before) and returns one Result per
// sub-case, each named with the "list_activity/" prefix. A zero since means
// the since arg is omitted (an open-ended range). The case never requires a
// backend to return at least one item: an empty fake passes the item and
// id-stability sub-cases vacuously.
func RunListActivityCase(ctx context.Context, backend Backend, since, before time.Time) []Result {
	sinceArg := ""
	if !since.IsZero() {
		sinceArg = since.Format(time.RFC3339)
	}
	beforeArg := before.Format(time.RFC3339)

	var results []Result

	first, firstResult := activityValidRange(ctx, backend, sinceArg, beforeArg)
	results = append(results, firstResult)

	results = append(results, activityItemInvariants(first, since, before))
	results = append(results, activityIDStability(ctx, backend, first, sinceArg, beforeArg))

	// The reversed range always puts since strictly after before so that it is
	// invalid whatever the caller's own since is.
	reversedSince := before.Add(time.Hour).Format(time.RFC3339)
	results = append(results, activityExpectInvalid(ctx, backend, "list_activity/reversed-range", reversedSince, beforeArg))
	results = append(results, activityExpectInvalid(ctx, backend, "list_activity/missing-before", sinceArg, ""))
	return results
}

// activityValidRange is sub-case 1: a valid range answers a well-formed
// success whose result decodes into schema.ActivityListResult. The decoded
// result is returned (nil on failure) for the sub-cases that inspect items.
func activityValidRange(ctx context.Context, backend Backend, sinceArg, beforeArg string) (*schema.ActivityListResult, Result) {
	const name = "list_activity/valid-range"
	res, err := InvokeListActivity(ctx, backend, sinceArg, beforeArg)
	if err != nil {
		return nil, Result{Name: name, Err: err}
	}
	if res.ErrorCode != "" {
		return nil, Result{Name: name, Err: fmt.Errorf("valid range answered error code %q, want a success", res.ErrorCode)}
	}
	if res.ExitCode != 0 {
		return nil, Result{Name: name, Err: fmt.Errorf("exit code = %d, want 0", res.ExitCode)}
	}
	var decoded schema.ActivityListResult
	if err := json.Unmarshal(res.Result, &decoded); err != nil {
		return nil, Result{Name: name, Err: fmt.Errorf("result does not decode into ActivityListResult: %w (result=%s)", err, res.Result)}
	}
	return &decoded, Result{Name: name}
}

// activityItemInvariants is sub-case 2: every item carries the required
// non-empty fields, RFC3339-with-offset timestamps, an object-valued fields
// member, and an occurred_at inside [since, before). A zero since means no
// lower bound.
func activityItemInvariants(got *schema.ActivityListResult, since, before time.Time) Result {
	const name = "list_activity/item-invariants"
	if got == nil {
		return Result{Name: name, Skipped: true, SkipReason: "valid-range sub-case produced no decodable result"}
	}
	for i, it := range got.Items {
		for field, val := range map[string]string{
			"id": it.ID, "kind": it.Kind, "entity_type": it.EntityType,
			"entity_id": it.EntityID, "summary": it.Summary,
		} {
			if val == "" {
				return Result{Name: name, Err: fmt.Errorf("item %d (id %q): %s is empty", i, it.ID, field)}
			}
		}
		occurred, err := time.Parse(time.RFC3339, it.OccurredAt)
		if err != nil {
			return Result{Name: name, Err: fmt.Errorf("item %d (id %q): occurred_at %q is not RFC3339 with an offset: %w", i, it.ID, it.OccurredAt, err)}
		}
		if _, err := time.Parse(time.RFC3339, it.AsOf); err != nil {
			return Result{Name: name, Err: fmt.Errorf("item %d (id %q): as_of %q is not RFC3339 with an offset: %w", i, it.ID, it.AsOf, err)}
		}
		if !isJSONObject(it.Fields) {
			return Result{Name: name, Err: fmt.Errorf("item %d (id %q): fields is not a JSON object (got %q)", i, it.ID, it.Fields)}
		}
		if !since.IsZero() && occurred.Before(since) {
			return Result{Name: name, Err: fmt.Errorf("item %d (id %q): occurred_at %s is before since %s (since is inclusive)", i, it.ID, it.OccurredAt, since.Format(time.RFC3339))}
		}
		if !occurred.Before(before) {
			return Result{Name: name, Err: fmt.Errorf("item %d (id %q): occurred_at %s is not before %s (before is exclusive)", i, it.ID, it.OccurredAt, before.Format(time.RFC3339))}
		}
	}
	return Result{Name: name}
}

// activityIDStability is sub-case 3: ids are unique within one response and a
// second identical call returns the same set of ids.
func activityIDStability(ctx context.Context, backend Backend, first *schema.ActivityListResult, sinceArg, beforeArg string) Result {
	const name = "list_activity/id-stability"
	if first == nil {
		return Result{Name: name, Skipped: true, SkipReason: "valid-range sub-case produced no decodable result"}
	}
	firstIDs, dup, hasDup := activityIDSet(first.Items)
	if hasDup {
		return Result{Name: name, Err: fmt.Errorf("duplicate item id %q within one response", dup)}
	}
	res, err := InvokeListActivity(ctx, backend, sinceArg, beforeArg)
	if err != nil {
		return Result{Name: name, Err: fmt.Errorf("second call: %w", err)}
	}
	if res.ErrorCode != "" {
		return Result{Name: name, Err: fmt.Errorf("second identical call answered error code %q", res.ErrorCode)}
	}
	var second schema.ActivityListResult
	if err := json.Unmarshal(res.Result, &second); err != nil {
		return Result{Name: name, Err: fmt.Errorf("second call result does not decode: %w", err)}
	}
	secondIDs, dup, hasDup := activityIDSet(second.Items)
	if hasDup {
		return Result{Name: name, Err: fmt.Errorf("duplicate item id %q within the second response", dup)}
	}
	if !sameStringSet(firstIDs, secondIDs) {
		return Result{Name: name, Err: fmt.Errorf("item ids differ between two identical calls: first %v, second %v", sortedKeys(firstIDs), sortedKeys(secondIDs))}
	}
	return Result{Name: name}
}

// activityExpectInvalid is a negative sub-case: the request must answer the
// error branch with code invalid_argument and the exit code that code implies.
func activityExpectInvalid(ctx context.Context, backend Backend, name, sinceArg, beforeArg string) Result {
	res, err := InvokeListActivity(ctx, backend, sinceArg, beforeArg)
	if err != nil {
		return Result{Name: name, Err: err}
	}
	if res.ErrorCode != "invalid_argument" {
		return Result{Name: name, Err: fmt.Errorf("error code = %q, want %q (result=%s)", res.ErrorCode, "invalid_argument", res.Result)}
	}
	if want := scriptout.ExitCodeForCode("invalid_argument"); res.ExitCode != want {
		return Result{Name: name, Err: fmt.Errorf("exit code = %d, want %d (invalid_argument)", res.ExitCode, want)}
	}
	return Result{Name: name}
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var m map[string]json.RawMessage
	return json.Unmarshal(trimmed, &m) == nil
}

// activityIDSet returns the set of item ids, and the first id that occurs more
// than once, if any.
func activityIDSet(items []schema.ActivityItem) (set map[string]struct{}, dup string, hasDup bool) {
	set = make(map[string]struct{}, len(items))
	for _, it := range items {
		if _, seen := set[it.ID]; seen {
			return set, it.ID, true
		}
		set[it.ID] = struct{}{}
	}
	return set, "", false
}

func sameStringSet(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
