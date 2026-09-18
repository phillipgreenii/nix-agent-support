package agentsession

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

type fakeProvider struct {
	showID  string
	show    *schema.AgentSession
	showErr error
	list    *schema.AgentSessionListResult
}

func (f *fakeProvider) Show(_ context.Context, id string) (*schema.AgentSession, error) {
	f.showID = id
	return f.show, f.showErr
}

func (f *fakeProvider) List(_ context.Context, _ schema.QueryExpr, _ bool) (*schema.AgentSessionListResult, error) {
	return f.list, nil
}

func TestDispatchTable_Show(t *testing.T) {
	fp := &fakeProvider{show: &schema.AgentSession{SessionID: "s1"}}
	table := NewDispatchTable(fp)
	entry, ok := table["show"]
	if !ok {
		t.Fatal(`"show" not in dispatch table`)
	}
	args, _ := json.Marshal(map[string]string{"id": "s1"})
	result, err := entry.Handle(context.Background(), args)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.(*schema.AgentSession)
	if !ok || got.SessionID != "s1" {
		t.Errorf("got %+v", result)
	}
	if fp.showID != "s1" {
		t.Errorf("Provider.Show called with id %q, want s1", fp.showID)
	}
}

func TestDispatchTable_ShowInvalidArgs(t *testing.T) {
	table := NewDispatchTable(&fakeProvider{})
	_, err := table["show"].Handle(context.Background(), json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected an error for malformed args")
	}
}

func TestDispatchTable_List(t *testing.T) {
	fp := &fakeProvider{list: &schema.AgentSessionListResult{PresentIDs: []string{"s1"}}}
	table := NewDispatchTable(fp)
	args, _ := json.Marshal(map[string]bool{"ids_only": true})
	result, err := table["list"].Handle(context.Background(), args)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got, ok := result.(*schema.AgentSessionListResult)
	if !ok || len(got.PresentIDs) != 1 {
		t.Errorf("got %+v", result)
	}
}

func TestDispatchTable_HasNoWriteOps(t *testing.T) {
	table := NewDispatchTable(&fakeProvider{})
	for _, op := range []string{"create", "update", "close", "delete", "transition"} {
		if _, ok := table[op]; ok {
			t.Errorf("agentsession is read-only; unexpected write op %q registered", op)
		}
	}
}
