package issue

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// paramsConfig is a backend config with one parameterized query.
const paramsConfig = `{"queries":{
 "for-pr":{"expr":["list --title-contains {repo}#{pr_number}"],"params":{"repo":"[a-z]+/[a-z]+","pr_number":"[0-9]{1,9}"}},
 "ready":"ready"}}`

// paramsProvider is a fakeProvider that opts in to query params.
type paramsProvider struct {
	*fakeProvider
	support bool
}

func (p paramsProvider) SupportsQueryParams() bool { return p.support }

func newParamsProvider(support bool, got *schema.QueryParams, listed *int) paramsProvider {
	return paramsProvider{support: support, fakeProvider: &fakeProvider{
		listFn: func(ctx context.Context, q schema.QueryExpr, _ bool, _ json.RawMessage) (*schema.IssueListResult, error) {
			*listed++
			*got = schema.QueryParamsFromContext(ctx)
			return &schema.IssueListResult{}, nil
		},
	}}
}

func listCall(t *testing.T, p Provider, args string) error {
	t.Helper()
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(paramsConfig))
	_, err := NewDispatchTable(p)["list"].Handle(ctx, json.RawMessage(args))
	return err
}

func TestDispatch_List_ParamsReachTheProviderThroughContext(t *testing.T) {
	var got schema.QueryParams
	listed := 0
	err := listCall(t, newParamsProvider(true, &got, &listed), `{"query":"for-pr","params":{"repo":"zr/app","pr_number":"42"}}`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if listed != 1 || got.Values["repo"] != "zr/app" || got.Values["pr_number"] != "42" || len(got.Def.Params) != 2 {
		t.Fatalf("listed=%d params=%#v", listed, got)
	}
}

func TestDispatch_List_BadParamsAreInvalidArgumentAndNeverListed(t *testing.T) {
	for name, args := range map[string]string{
		"bad value":                           `{"query":"for-pr","params":{"repo":"zr/app","pr_number":"4 2"}}`,
		"leading dash":                        `{"query":"for-pr","params":{"repo":"-zr/app","pr_number":"42"}}`,
		"missing":                             `{"query":"for-pr","params":{"repo":"zr/app"}}`,
		"none supplied":                       `{"query":"for-pr"}`,
		"unknown":                             `{"query":"for-pr","params":{"repo":"zr/app","pr_number":"42","x":"1"}}`,
		"params for an unparameterized query": `{"query":"ready","params":{"x":"1"}}`,
	} {
		var got schema.QueryParams
		listed := 0
		err := listCall(t, newParamsProvider(true, &got, &listed), args)
		if !errors.Is(err, scriptout.ErrInvalidArgument) {
			t.Errorf("%s: err = %v, want invalid_argument", name, err)
		}
		if listed != 0 {
			t.Errorf("%s: the provider was called", name)
		}
	}
}

func TestDispatch_List_NamesTheParamAndRegex(t *testing.T) {
	var got schema.QueryParams
	listed := 0
	err := listCall(t, newParamsProvider(true, &got, &listed), `{"query":"for-pr","params":{"repo":"zr/app","pr_number":"x"}}`)
	if err == nil || !strings.Contains(err.Error(), "pr_number") || !strings.Contains(err.Error(), "[0-9]{1,9}") {
		t.Fatalf("err = %v, want it to name the param and its regex", err)
	}
}

func TestDispatch_List_ProviderWithoutSupportRefusesParameterizedQuery(t *testing.T) {
	for name, p := range map[string]Provider{
		"plain provider": &fakeProvider{listFn: func(context.Context, schema.QueryExpr, bool, json.RawMessage) (*schema.IssueListResult, error) {
			t.Fatal("listed")
			return nil, nil
		}},
		"declines params": newParamsProvider(false, new(schema.QueryParams), new(int)),
	} {
		err := listCall(t, p, `{"query":"for-pr","params":{"repo":"zr/app","pr_number":"42"}}`)
		if !errors.Is(err, scriptout.ErrInvalidArgument) || !strings.Contains(err.Error(), "does not support query params") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestDispatch_List_NoParamsLeavesContextUntouched(t *testing.T) {
	var got schema.QueryParams
	listed := 0
	if err := listCall(t, newParamsProvider(true, &got, &listed), `{"query":"ready"}`); err != nil {
		t.Fatalf("list: %v", err)
	}
	if listed != 1 || len(got.Values) != 0 || len(got.Def.Params) != 0 {
		t.Fatalf("listed=%d params=%#v", listed, got)
	}
}

// oldLenientBackend mimics the backend shipped BEFORE bead pg2-eclw3j.3 at the
// two places that matter: its list args decoder ignores unknown members (so a
// "params" member is silently dropped), and its query resolver accepts only the
// "expr" and "watched" keys of an object, so ONE parameterized query makes the
// whole queries block fail to decode and every name answer
// query_not_recognized. bdCalls records what would have been exec'd.
func oldLenientBackend(config json.RawMessage, args string, bdCalls *[]string) error {
	var a struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil { // lenient: unknown "params" ignored
		return err
	}
	var cfg struct {
		Queries map[string]json.RawMessage `json:"queries"`
	}
	if err := json.Unmarshal(config, &cfg); err != nil {
		return scriptout.WrapError(scriptout.ErrQueryNotRecognized, "no queries")
	}
	for _, raw := range cfg.Queries { // decode every definition, as the typed map does
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			continue
		}
		for k := range obj {
			if k != "expr" && k != "watched" {
				return scriptout.WrapError(scriptout.ErrQueryNotRecognized, "queries block failed to decode")
			}
		}
	}
	def, ok := cfg.Queries[a.Query]
	if !ok {
		return scriptout.WrapError(scriptout.ErrQueryNotRecognized, "not defined")
	}
	*bdCalls = append(*bdCalls, string(def))
	return nil
}

// TestWireSkew_NewConfigOldBackend_IsQueryNotRecognizedNeverALiteralBraceCall
// is the wire-skew guard: backend binary and config MUST ship together.
func TestWireSkew_NewConfigOldBackend_IsQueryNotRecognizedNeverALiteralBraceCall(t *testing.T) {
	var bdCalls []string
	for _, args := range []string{
		`{"query":"for-pr","params":{"repo":"zr/app","pr_number":"42"}}`,
		`{"query":"for-pr"}`,
		`{"query":"ready"}`, // even the unparameterized sibling is lost
	} {
		err := oldLenientBackend(json.RawMessage(paramsConfig), args, &bdCalls)
		if !errors.Is(err, scriptout.ErrQueryNotRecognized) {
			t.Errorf("%s: err = %v, want query_not_recognized", args, err)
		}
	}
	if len(bdCalls) != 0 {
		t.Fatalf("an old backend would have exec'd bd with %v", bdCalls)
	}
	// And the new decoder, same config: the query resolves (the stub differs).
	if _, ok := schema.ResolveQueryDef(json.RawMessage(paramsConfig), "for-pr"); !ok {
		t.Fatal("the new schema must resolve the parameterized query")
	}
}
