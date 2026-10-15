package pr

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

type fakeShowManyProvider struct {
	fakeProvider
	got ShowManyRequest
	res *schema.PRShowManyResult
	err error
}

func (f *fakeShowManyProvider) ShowMany(_ context.Context, req ShowManyRequest) (*schema.PRShowManyResult, error) {
	f.got = req
	return f.res, f.err
}

func TestShowMany_DispatchRoundTrip(t *testing.T) {
	p := &fakeShowManyProvider{res: &schema.PRShowManyResult{
		Entities: []schema.PR{{ID: "o/r#1", Title: "t"}},
		Missing:  []string{"o/r#2"},
	}}
	entry, ok := NewDispatchTable(p)["show_many"]
	if !ok {
		t.Fatal("show_many missing for a capable provider")
	}
	if entry.SchemaVersion != schema.PRSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", entry.SchemaVersion, schema.PRSchemaVersion)
	}
	out, err := entry.Handle(context.Background(), json.RawMessage(`{"refs":[{"id":"o/r#1","node_id":"PR_a"},{"id":"o/r#2","node_id":"PR_b"}]}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	want := ShowManyRequest{Refs: []ShowManyRef{{ID: "o/r#1", NodeID: "PR_a"}, {ID: "o/r#2", NodeID: "PR_b"}}}
	if !slices.Equal(p.got.Refs, want.Refs) {
		t.Fatalf("request = %#v, want %#v", p.got, want)
	}
	if out != p.res {
		t.Fatalf("Handle returned %#v, want the provider's result passed through", out)
	}
}

func TestShowMany_DecodeFailureIsInvalidArgument(t *testing.T) {
	_, err := NewDispatchTable(&fakeShowManyProvider{})["show_many"].Handle(context.Background(), json.RawMessage(`not json`))
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
}

func TestShowMany_ProviderErrorPassesThroughUnwrapped(t *testing.T) {
	want := scriptout.WrapError(scriptout.ErrUnavailable, "boom")
	_, err := NewDispatchTable(&fakeShowManyProvider{err: want})["show_many"].Handle(context.Background(), json.RawMessage(`{"refs":[]}`))
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want the provider's unavailable error", err)
	}
}

func TestShowMany_CapabilitiesGating(t *testing.T) {
	cases := []struct {
		name string
		p    Provider
		want bool
	}{
		{"capable", &fakeShowManyProvider{}, true},
		{"incapable", &fakeProvider{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			table := NewDispatchTable(c.p)
			_, inTable := table["show_many"]
			table = scriptout.AddCapabilities(table, 1, scriptout.CapabilitiesResponse{})
			inOps := slices.Contains(table.Ops(), "show_many")
			if inTable != c.want || inOps != c.want {
				t.Fatalf("inTable=%v inOps=%v want %v", inTable, inOps, c.want)
			}
		})
	}
}
