package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-beads/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// perPRConfig is a deployment's per-PR query block (bead pg2-eclw3j.3).
const perPRConfig = `{"queries":{"work-items-for-pr":{
 "expr":["list --all --metadata-field repo={repo} --metadata-field pr_number={pr_number}",
         "list --all --title-contains {repo}#{pr_number}"],
 "params":{"repo":"[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*","pr_number":"[0-9]{1,9}"}}}}`

func TestCapabilities_DeclaresQueryParams(t *testing.T) {
	result, err := newDispatchTable(newTestBackend())[scriptout.OpCapabilities].Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if v, ok := result.(scriptout.CapabilitiesResponse).Vocabulary["query_params"].(bool); !ok || !v {
		t.Fatalf("vocabulary.query_params = %#v, want true", result.(scriptout.CapabilitiesResponse).Vocabulary["query_params"])
	}
}

// TestList_ParamsThroughTheRealDispatchTable runs the whole backend path: wire
// args and config in, bd argv out.
func TestList_ParamsThroughTheRealDispatchTable(t *testing.T) {
	var calls [][]string
	b := internal.New(&fakeRunner{handle: func(args []string) (string, error) {
		calls = append(calls, args)
		return `{"data":[],"schema_version":1}`, nil
	}})
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(perPRConfig))
	table := newDispatchTable(b)

	if _, err := table["list"].Handle(ctx, json.RawMessage(`{"query":"work-items-for-pr","params":{"repo":"ZR-Private/ziprecruiter","pr_number":"113446"}}`)); err != nil {
		t.Fatalf("list: %v", err)
	}
	want := [][]string{
		{"list", "--all", "--metadata-field", "repo=ZR-Private/ziprecruiter", "--metadata-field", "pr_number=113446", "--json", "--limit", "0"},
		{"list", "--all", "--title-contains", "ZR-Private/ziprecruiter#113446", "--json", "--limit", "0"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("bd calls = %q, want %q", calls, want)
	}

	calls = nil
	for _, bad := range []string{
		`{"query":"work-items-for-pr"}`,
		`{"query":"work-items-for-pr","params":{"repo":"x y/z","pr_number":"1"}}`,
		`{"query":"work-items-for-pr","params":{"repo":"a/b","pr_number":"1","extra":"1"}}`,
	} {
		_, err := table["list"].Handle(ctx, json.RawMessage(bad))
		if !errors.Is(err, scriptout.ErrInvalidArgument) {
			t.Errorf("%s: err = %v, want invalid_argument", bad, err)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("bd was invoked for a rejected request: %q", calls)
	}
}
