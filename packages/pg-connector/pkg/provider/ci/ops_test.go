package ci

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// TestCatalogNamesAppearInInterfacesDoc fails when a catalog op name is
// missing from the interfaces behavior doc's prose op catalog.
func TestCatalogNamesAppearInInterfacesDoc(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/behavior/interfaces.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, name := range Catalog() {
		if !strings.Contains(doc, "`"+name+"`") {
			t.Errorf("catalog op %q does not appear in docs/behavior/interfaces.md", name)
		}
	}
}

func TestCall_EncodesRequestAndDecodeResult(t *testing.T) {
	var gotOp string
	var gotArgs any
	invoke := func(_ context.Context, op string, args any) (*scriptout.Response, error) {
		gotOp, gotArgs = op, args
		return &scriptout.Response{Result: json.RawMessage(`"bG9ncw=="`)}, nil
	}
	resp, err := Call(context.Background(), invoke, GetLogs, GetLogsReq{RunID: "r1", Repo: "o/n"})
	if err != nil || resp == nil {
		t.Fatalf("err=%v resp=%v", err, resp)
	}
	logs, err := DecodeResult(GetLogs, resp)
	if err != nil || string(logs) != "logs" {
		t.Errorf("logs = %q err=%v", logs, err)
	}
	b, _ := json.Marshal(gotArgs)
	if gotOp != "get_logs" || string(b) != `{"run_id":"r1","repo":"o/n"}` {
		t.Errorf("op=%q args=%s", gotOp, b)
	}
}

func TestDecodeResult_ErrorEnvelopeAndBadResult(t *testing.T) {
	if _, err := DecodeResult(GetLogs, &scriptout.Response{Error: &scriptout.Error{Code: "not_found"}}); err != nil {
		t.Errorf("error envelope: %v", err)
	}
	if _, err := DecodeResult(GetLogs, &scriptout.Response{Result: json.RawMessage(`{"x":1}`)}); err == nil {
		t.Error("want decode error")
	}
}

func TestHandle_InvalidArgs(t *testing.T) {
	h := Handle(ListRuns, func(context.Context, ListRunsReq) ([]schema.CIRun, error) { return nil, nil })
	_, err := h.Handle(context.Background(), json.RawMessage(`[1]`))
	if err == nil {
		t.Fatal("want invalid-argument error")
	}
}
